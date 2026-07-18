package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
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
	var checks []Check
	add := func(name string, status CheckStatus, format string, args ...any) {
		checks = append(checks, Check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
	}

	// Store directory and permissions.
	if info, err := os.Stat(a.Store.Dir()); errors.Is(err, fs.ErrNotExist) {
		add("store", Warn, "%s does not exist yet (fine on first run)", a.Store.Dir())
	} else if err != nil {
		add("store", Fail, "cannot stat %s: %v", a.Store.Dir(), err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		add("store", Fail, "%s has mode %o, want 700", a.Store.Dir(), perm)
	} else {
		add("store", OK, "%s (mode 700)", a.Store.Dir())
	}

	// State file.
	st, err := a.Store.LoadState()
	if err != nil {
		add("state", Fail, "%v", err)
		return checks // everything below needs the state
	}
	add("state", OK, "%d account(s) registered", len(st.Accounts))

	// Active marker consistency.
	if st.Active != "" && st.IndexByUUID(st.Active) == -1 {
		add("active marker", Warn, "points at unregistered uuid %s — it heals on the next switch, or when a managed login is discovered", st.Active)
	}

	// Duplicates.
	seenUUID := map[string]bool{}
	seenEmail := map[string]bool{}
	seenAlias := map[string]bool{}
	for _, acc := range st.Accounts {
		switch {
		case seenUUID[acc.UUID]:
			add("duplicates", Fail, "uuid %s appears more than once", acc.UUID)
		case acc.Email != "" && seenEmail[acc.Email]:
			add("duplicates", Fail, "email %s appears more than once", acc.Email)
		case acc.Alias != "" && seenAlias[acc.Alias]:
			add("duplicates", Fail, "alias %q appears more than once", acc.Alias)
		}
		seenUUID[acc.UUID] = true
		seenEmail[acc.Email] = true
		seenAlias[acc.Alias] = true
	}

	// Per-account snapshots.
	now := a.Now()
	for _, acc := range st.Accounts {
		name := "account " + acc.Email
		raw, err := a.Store.ReadSnapshot(acc.UUID)
		if errors.Is(err, fs.ErrNotExist) {
			add(name, Fail, "no credentials snapshot — log in as it once and rerun ccswitch")
			continue
		}
		if err != nil {
			add(name, Fail, "%v", err)
			continue
		}
		meta, err := claude.ParseCredentials(raw)
		if err != nil {
			add(name, Fail, "snapshot is malformed: %v", err)
			continue
		}
		access, refresh := claude.HasTokens(raw)
		if !access || !refresh {
			add(name, Fail, "snapshot is missing token values")
			continue
		}
		switch left := meta.RefreshExpiry().Sub(now); {
		case meta.RefreshTokenExpiresAt == 0:
			add(name, Warn, "snapshot has no refresh-token expiry — health unknown")
		case left <= 0:
			add(name, Fail, "refresh token expired %s ago — log in as it again", (-left).Round(time.Minute))
		case left < renewSoonWindow:
			add(name, Warn, "refresh token expires in %s — switch to it soon so it renews", left.Round(time.Hour))
		default:
			add(name, OK, "snapshot healthy, refresh token valid for %dd", int(left.Hours()/24))
		}
	}

	// Orphaned snapshot directories.
	if orphans, err := a.Store.OrphanDirs(st); err != nil {
		add("orphans", Warn, "%v", err)
	} else if len(orphans) > 0 {
		add("orphans", Warn, "unreferenced snapshot dir(s): %v — remove them under %s/accounts", orphans, a.Store.Dir())
	} else {
		add("orphans", OK, "no orphaned snapshots")
	}

	// Live credentials.
	liveRaw, err := a.Creds.Read()
	switch {
	case errors.Is(err, claude.ErrNotLoggedIn):
		add("live credentials", Warn, "%v", err)
	case err != nil:
		add("live credentials", Fail, "%v", err)
	default:
		if _, err := claude.ParseCredentials(liveRaw); err != nil {
			add("live credentials", Fail, "%s: %v", a.Creds.Location(), err)
		} else {
			add("live credentials", OK, "%s", a.Creds.Location())
		}
		if info, err := os.Stat(a.Env.CredentialsPath()); err == nil {
			if perm := info.Mode().Perm(); perm != 0o600 {
				add("credentials permissions", Warn, "%s has mode %o, want 600", a.Env.CredentialsPath(), perm)
			}
		}
	}

	// Claude config and live-account registration.
	cfgPath := a.Env.ConfigPath()
	rawProfile, profile, err := claude.ReadOAuthAccount(cfgPath)
	switch {
	case err != nil:
		add("claude config", Fail, "%v", err)
	case rawProfile == nil:
		add("claude config", Warn, "%s has no oauthAccount yet — run claude once", cfgPath)
	default:
		add("claude config", OK, "%s (account %s)", cfgPath, profile.EmailAddress)
		if st.IndexByUUID(profile.AccountUUID) == -1 && st.IndexByEmail(profile.EmailAddress) == -1 {
			add("registration", Warn, "live account %s is not managed — run `ccswitch` to add it", profile.EmailAddress)
		}
	}

	return checks
}
