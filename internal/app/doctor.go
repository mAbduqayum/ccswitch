package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// The permissions doctor insists on: nothing under the store, and nothing
// Claude Code writes, may be readable by other users.
const (
	wantStoreDirPerm  = 0o700
	wantCredsFilePerm = 0o600
)

type CheckStatus int

const (
	OK CheckStatus = iota
	Warn
	Fail
)

func (s CheckStatus) String() string {
	switch s {
	case Warn:
		return "warn"
	case Fail:
		return "fail"
	default:
		return "ok"
	}
}

func (s CheckStatus) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

type Check struct {
	Name   string      `json:"name"`
	Status CheckStatus `json:"status"`
	Detail string      `json:"detail"`
}

func newCheck(name string, status CheckStatus, format string, args ...any) Check {
	return Check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)}
}

// Healthy reports whether no check failed (warnings are acceptable).
func Healthy(checks []Check) bool {
	for _, c := range checks {
		if c.Status == Fail {
			return false
		}
	}
	return true
}

// Doctor runs read-only health checks over the store, the live credentials,
// and the claude config. It never mutates anything and never surfaces token
// values.
func (a *App) Doctor() []Check {
	checks := []Check{a.checkStoreDir()}

	st, err := a.Store.LoadState()
	if err != nil {
		// Every check below reads the account list, so a corrupt state ends
		// the run here rather than producing a page of confusing failures.
		return append(checks, newCheck("state", Fail, "%v", err))
	}
	checks = append(checks, newCheck("state", OK, "%d account(s) registered", len(st.Accounts)))
	checks = append(checks, checkActiveMarker(st)...)
	checks = append(checks, checkDuplicates(st)...)
	checks = append(checks, a.checkSnapshots(st)...)
	checks = append(checks, a.checkOrphans(st))
	checks = append(checks, a.checkLiveCredentials()...)
	checks = append(checks, a.checkClaudeConfig(st)...)
	return checks
}

func (a *App) checkStoreDir() Check {
	dir := a.Store.Dir()
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return newCheck("store", Warn, "%s does not exist yet (fine on first run)", dir)
	case err != nil:
		return newCheck("store", Fail, "cannot stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != wantStoreDirPerm {
		return newCheck("store", Fail, "%s has mode %o, want %o", dir, perm, wantStoreDirPerm)
	}
	return newCheck("store", OK, "%s (mode %o)", dir, wantStoreDirPerm)
}

func checkActiveMarker(st store.State) []Check {
	if st.Active == "" || st.IndexByUUID(st.Active) != -1 {
		return nil
	}
	return []Check{newCheck("active marker", Warn,
		"points at unregistered uuid %s — it heals on the next switch, or when a managed login is discovered", st.Active)}
}

func checkDuplicates(st store.State) []Check {
	var checks []Check
	seenUUID := map[string]bool{}
	seenEmail := map[string]bool{}
	seenAlias := map[string]bool{}
	for _, acc := range st.Accounts {
		switch {
		case seenUUID[acc.UUID]:
			checks = append(checks, newCheck("duplicates", Fail, "uuid %s appears more than once", acc.UUID))
		case acc.Email != "" && seenEmail[acc.Email]:
			checks = append(checks, newCheck("duplicates", Fail, "email %s appears more than once", acc.Email))
		case acc.Alias != "" && seenAlias[acc.Alias]:
			checks = append(checks, newCheck("duplicates", Fail, "alias %q appears more than once", acc.Alias))
		}
		seenUUID[acc.UUID] = true
		seenEmail[acc.Email] = true
		seenAlias[acc.Alias] = true
	}
	return checks
}

func (a *App) checkSnapshots(st store.State) []Check {
	now := a.Now()
	checks := make([]Check, 0, len(st.Accounts))
	for _, acc := range st.Accounts {
		checks = append(checks, a.checkSnapshot(acc, now))
	}
	return checks
}

func (a *App) checkSnapshot(acc store.Account, now time.Time) Check {
	name := "account " + acc.Email
	raw, err := a.Store.ReadSnapshot(acc.UUID)
	if errors.Is(err, fs.ErrNotExist) {
		return newCheck(name, Fail, "no credentials snapshot — log in as it once and rerun ccswitch")
	}
	if err != nil {
		return newCheck(name, Fail, "%v", err)
	}
	meta, err := claude.ParseCredentials(raw)
	if err != nil {
		return newCheck(name, Fail, "snapshot is malformed: %v", err)
	}
	if access, refresh := claude.HasTokens(raw); !access || !refresh {
		return newCheck(name, Fail, "snapshot is missing token values")
	}
	left := meta.RefreshExpiry().Sub(now)
	switch refreshHealth(meta, now) {
	case tokenUnknown:
		return newCheck(name, Warn, "snapshot has no refresh-token expiry — health unknown")
	case tokenExpired:
		return newCheck(name, Fail, "refresh token expired %s ago — log in as it again", (-left).Round(time.Minute))
	case tokenRenewSoon:
		return newCheck(name, Warn, "refresh token expires in %s — switch to it soon so it renews", left.Round(time.Hour))
	default:
		return newCheck(name, OK, "snapshot healthy, refresh token valid for %dd", int(left.Hours()/24))
	}
}

func (a *App) checkOrphans(st store.State) Check {
	orphans, err := a.Store.OrphanDirs(st)
	switch {
	case err != nil:
		return newCheck("orphans", Warn, "%v", err)
	case len(orphans) > 0:
		return newCheck("orphans", Warn, "unreferenced snapshot dir(s): %v — remove them under %s/accounts", orphans, a.Store.Dir())
	default:
		return newCheck("orphans", OK, "no orphaned snapshots")
	}
}

func (a *App) checkLiveCredentials() []Check {
	raw, err := a.Creds.Read()
	if errors.Is(err, claude.ErrNotLoggedIn) {
		return []Check{newCheck("live credentials", Warn, "%v", err)}
	}
	if err != nil {
		return []Check{newCheck("live credentials", Fail, "%v", err)}
	}
	var checks []Check
	if _, err := claude.ParseCredentials(raw); err != nil {
		checks = append(checks, newCheck("live credentials", Fail, "%s: %v", a.Creds.Location(), err))
	} else {
		checks = append(checks, newCheck("live credentials", OK, "%s", a.Creds.Location()))
	}
	path := a.Env.CredentialsPath()
	if info, err := os.Stat(path); err == nil {
		if perm := info.Mode().Perm(); perm != wantCredsFilePerm {
			checks = append(checks, newCheck("credentials permissions", Warn,
				"%s has mode %o, want %o", path, perm, wantCredsFilePerm))
		}
	}
	return checks
}

func (a *App) checkClaudeConfig(st store.State) []Check {
	path := a.Env.ConfigPath()
	rawProfile, profile, err := claude.ReadOAuthAccount(path)
	switch {
	case err != nil:
		return []Check{newCheck("claude config", Fail, "%v", err)}
	case rawProfile == nil:
		return []Check{newCheck("claude config", Warn, "%s has no oauthAccount yet — run claude once", path)}
	}
	checks := []Check{newCheck("claude config", OK, "%s (account %s)", path, profile.EmailAddress)}
	if st.IndexByUUID(profile.AccountUUID) == -1 && st.IndexByEmail(profile.EmailAddress) == -1 {
		checks = append(checks, newCheck("registration", Warn,
			"live account %s is not managed — run `ccswitch` to add it", profile.EmailAddress))
	}
	return checks
}
