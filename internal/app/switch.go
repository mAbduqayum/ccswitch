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
	ClaudeRunning  bool
	Warnings       []string
}

// Switch makes target's snapshot the live credentials. The order is
// critical: the live credentials are snapshotted into the current account's
// slot FIRST, so token refreshes Claude Code performed since the last
// switch are never lost.
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

	// (1) Snapshot the live credentials into the current account's slot.
	liveRaw, err := a.Creds.Read()
	switch {
	case errors.Is(err, claude.ErrNotLoggedIn):
		// Nothing live to preserve.
	case err != nil:
		return res, err
	default:
		liveMeta, perr := claude.ParseCredentials(liveRaw)
		if perr != nil {
			return res, fmt.Errorf("refusing to switch: live credentials at %s are malformed (%w) — run `claude /login` to repair them first", a.Creds.Location(), perr)
		}
		cur, ok := a.identifyLive(st)
		switch {
		case ok && cur.UUID == target.UUID:
			// Switching to the already-live account: the live tokens are
			// the freshest copy, so restore those, not the older snapshot.
			snap = liveRaw
			res.From = cur
		case ok:
			// identifyLive's active-marker fallback can misattribute a
			// foreign login, so only overwrite the slot when the live
			// tokens are strictly newer than the stored snapshot.
			refresh, err := a.snapshotNeedsRefresh(cur.UUID, liveMeta)
			if err != nil {
				return res, err
			}
			if refresh {
				if err := a.Store.WriteSnapshot(cur.UUID, liveRaw); err != nil {
					return res, err
				}
			}
			res.From = cur
		case force:
			res.Warnings = append(res.Warnings, "discarded the credentials of an unregistered login")
		default:
			return res, fmt.Errorf("%w — rerun `ccswitch` and accept the add prompt, or pass --force to discard its credentials", ErrUnsavedLogin)
		}
	}

	// (2) Restore the target's snapshot as the live credentials.
	if err := a.Creds.Write(snap); err != nil {
		return res, err
	}

	// (3) Best-effort: patch the profile into the claude config so the UI
	// shows the right identity immediately. Claude Code refetches profiles
	// itself, so failure is only a warning.
	if p, perr := a.Store.ReadProfile(target.UUID); perr == nil && p != nil {
		if cfgErr := claude.PatchOAuthAccount(a.Env.ConfigPath(), p); cfgErr != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("could not update the profile in the claude config: %v (Claude Code will refetch it)", cfgErr))
		} else {
			res.ProfilePatched = true
		}
	}

	// (4) Update the active marker.
	st.Active = target.UUID
	if err := a.Store.SaveState(st); err != nil {
		return res, err
	}

	// (5) A running `claude` holds the old tokens in memory.
	if a.Pgrep != nil && a.Pgrep() {
		res.ClaudeRunning = true
	}
	return res, nil
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
