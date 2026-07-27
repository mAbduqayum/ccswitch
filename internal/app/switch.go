package app

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// ErrUnsavedLogin reports that switching would discard credentials of a
// login that isn't registered in the store.
var ErrUnsavedLogin = errors.New("the current login is not a managed account")

type SwitchResult struct {
	From           store.Account // zero value when nothing live was identified
	To             store.Account
	ProfilePatched bool
	Warnings       []string
}

// warn records a non-fatal problem; the empty string records nothing, so
// callers can hand over an optional warning unconditionally.
func (r *SwitchResult) warn(msg string) {
	if msg != "" {
		r.Warnings = append(r.Warnings, msg)
	}
}

// Switch makes target's snapshot the live credentials. The order below is
// critical and must not be rearranged: bankLive runs FIRST, so token
// refreshes Claude Code performed since the last switch are never lost.
func (a *App) Switch(target store.Account, force bool) (SwitchResult, error) {
	res := SwitchResult{To: target}
	unlock, err := a.Store.Lock()
	if err != nil {
		return res, err
	}
	defer unlock()

	st, err := a.Store.LoadState()
	if err != nil {
		return res, err
	}
	// The caller resolved target before the lock; a concurrent remove may
	// have deregistered it since.
	if st.IndexByUUID(target.UUID) == -1 {
		return res, fmt.Errorf("account %s is no longer registered — see `ccswitch list`", target.Email)
	}

	// Read the target snapshot before writing anything, so a missing
	// snapshot aborts with zero side effects.
	snap, err := a.Store.ReadSnapshot(target.UUID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("no credentials snapshot for %s — log in as it once (`claude /login`) and rerun ccswitch", target.Email)
		}
		return res, err
	}

	banked, err := a.bankLive(st, target, force)
	if err != nil {
		return res, err
	}
	res.From = banked.from
	res.warn(banked.warning)
	if banked.restore != nil {
		snap = banked.restore
	}

	if err := a.Creds.Write(snap); err != nil {
		return res, err
	}

	patched, warning := a.patchLiveProfile(target.UUID)
	res.ProfilePatched = patched
	res.warn(warning)

	st.Active = target.UUID
	if err := a.Store.SaveState(st); err != nil {
		return res, err
	}

	return res, nil
}

// bankedLive is what banking the live credentials settled about the login
// being switched away from.
type bankedLive struct {
	// from is the account the live credentials belonged to; the zero value
	// when none could be identified.
	from store.Account
	// restore holds the live bytes when they belong to the switch target
	// itself: they are fresher than its stored snapshot, so they are what
	// goes back live. nil in every other case.
	restore []byte
	// warning is empty unless something non-fatal happened.
	warning string
}

// bankLive writes the live credentials into their owner's slot, preserving
// every token refresh Claude Code performed since the last switch. Switch
// calls it BEFORE restoring the target: reversing that order would overwrite
// the live file while the current account's newest refresh token exists
// nowhere else.
func (a *App) bankLive(st store.State, target store.Account, force bool) (bankedLive, error) {
	liveRaw, err := a.Creds.Read()
	if errors.Is(err, claude.ErrNotLoggedIn) {
		return bankedLive{}, nil // nothing live to preserve
	}
	if err != nil {
		return bankedLive{}, err
	}
	liveMeta, err := claude.ParseCredentials(liveRaw)
	if err != nil {
		return bankedLive{}, fmt.Errorf("refusing to switch: live credentials at %s are malformed (%w) — run `claude /login` to repair them first", a.Creds.Location(), err)
	}

	cur, identified := a.identifyLive(st)
	switch {
	case !identified && force:
		return bankedLive{warning: "discarded the credentials of an unregistered login"}, nil
	case !identified:
		return bankedLive{}, fmt.Errorf("%w — rerun `ccswitch` and accept the add prompt, or pass --force to discard its credentials", ErrUnsavedLogin)
	case cur.UUID == target.UUID:
		// Switching to the already-live account: the live tokens are the
		// freshest copy, so restore those, not the older snapshot.
		return bankedLive{from: cur, restore: liveRaw}, nil
	}

	// identifyLive's active-marker fallback can misattribute a foreign login,
	// so only overwrite the slot when the live tokens are strictly newer than
	// the stored snapshot.
	refresh, err := a.snapshotNeedsRefresh(cur.UUID, liveMeta)
	if err != nil {
		return bankedLive{}, err
	}
	if refresh {
		if err := a.Store.WriteSnapshot(cur.UUID, liveRaw); err != nil {
			return bankedLive{}, err
		}
	}
	return bankedLive{from: cur}, nil
}

// patchLiveProfile copies the account's stored profile into the claude config
// so its UI shows the right identity immediately. Best-effort: Claude Code
// refetches profiles on its own, so a failure only earns a warning, and a
// never-captured profile earns nothing at all.
func (a *App) patchLiveProfile(uuid string) (patched bool, warning string) {
	profile, err := a.Store.ReadProfile(uuid)
	if err != nil || profile == nil {
		return false, ""
	}
	if err := claude.PatchOAuthAccount(a.Env.ConfigPath(), profile); err != nil {
		return false, fmt.Sprintf("could not update the profile in the claude config: %v (Claude Code will refetch it)", err)
	}
	return true, ""
}

// identifyLive determines which registered account the live credentials
// belong to: by the config profile when present, falling back to the active
// marker. ok is false when the live login matches no registered account.
//
// The marker fallback is a heuristic — credentials carry no identity. A
// foreign login paired with an unreadable config gets attributed to the
// marked account, and when its tokens are newer they replace that account's
// snapshot. Accepted residual: `claude /login` always rewrites the profile,
// so this needs a separately corrupted config; refusing to snapshot here
// would instead lose legitimate refreshes whenever the config is missing.
func (a *App) identifyLive(st store.State) (store.Account, bool) {
	_, profile, err := claude.ReadOAuthAccount(a.Env.ConfigPath())
	if err == nil && profile.AccountUUID != "" {
		if idx := st.IndexByUUID(profile.AccountUUID); idx != -1 {
			return st.Accounts[idx], true
		}
		if idx := st.IndexByEmail(profile.EmailAddress); idx != -1 {
			return st.Accounts[idx], true
		}
		return store.Account{}, false // the profile names an unregistered account
	}
	// No usable profile — trust the active marker.
	if idx := st.IndexByUUID(st.Active); idx != -1 {
		return st.Accounts[idx], true
	}
	return store.Account{}, false
}
