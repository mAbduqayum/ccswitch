package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

type DiscoveryStatus int

const (
	// NotLoggedIn means no live credentials exist.
	NotLoggedIn DiscoveryStatus = iota
	// NoProfile means credentials exist, but Claude Code hasn't written an
	// oauthAccount profile yet, so the account can't be identified.
	NoProfile
	// Unknown means the live account is not registered in the store.
	Unknown
	// Known means the live account is registered.
	Known
)

// Discovery captures everything learned about the live login. It is
// produced by Discover (read-only) and consumed by AddCurrent/SyncKnown.
// The raw fields hold live credential bytes and are excluded from JSON so
// no caller can marshal them by accident.
type Discovery struct {
	Status     DiscoveryStatus
	RawCreds   []byte `json:"-"`
	Meta       claude.CredentialMeta
	RawProfile json.RawMessage `json:"-"`
	Profile    claude.Profile
	Account    store.Account // set when Status == Known
}

// ErrLiveCredsMalformed reports that live credentials exist but cannot be
// parsed. Read-only callers may degrade it to a warning; switching hard-fails
// on it independently.
var ErrLiveCredsMalformed = errors.New("live credentials are malformed")

// Discover inspects the live login without mutating anything.
func (a *App) Discover() (Discovery, error) {
	raw, err := a.Creds.Read()
	if errors.Is(err, claude.ErrNotLoggedIn) {
		return Discovery{Status: NotLoggedIn}, nil
	}
	if err != nil {
		return Discovery{}, err
	}
	meta, err := claude.ParseCredentials(raw)
	if err != nil {
		return Discovery{}, fmt.Errorf("%w: %s: %w", ErrLiveCredsMalformed, a.Creds.Location(), err)
	}
	d := Discovery{RawCreds: raw, Meta: meta}

	// The profile is best-effort — a missing or unreadable one only means
	// the account can't be identified; that is a notice, not a failure.
	rawProfile, profile, err := claude.ReadOAuthAccount(a.Env.ConfigPath())
	if err != nil || rawProfile == nil || profile.AccountUUID == "" {
		d.Status = NoProfile
		return d, nil //nolint:nilerr // a broken config only hides the identity
	}
	d.RawProfile, d.Profile = rawProfile, profile

	st, err := a.Store.LoadState()
	if err != nil {
		return Discovery{}, err
	}
	idx := st.IndexByUUID(profile.AccountUUID)
	if idx == -1 {
		idx = st.IndexByEmail(profile.EmailAddress)
	}
	if idx == -1 {
		d.Status = Unknown
		return d, nil
	}
	d.Status = Known
	d.Account = st.Accounts[idx]
	return d, nil
}

// AddCurrent registers the live login as a managed account and marks it
// active.
func (a *App) AddCurrent(d Discovery) (store.Account, error) {
	if d.Status != Unknown {
		return store.Account{}, errors.New("no unregistered login to add")
	}
	unlock, err := a.Store.Lock()
	if err != nil {
		return store.Account{}, err
	}
	defer unlock()
	st, err := a.Store.LoadState()
	if err != nil {
		return store.Account{}, err
	}
	// Another process may have added it between Discover and Lock — match
	// the same way Discover does, by uuid then by email.
	if idx := st.IndexByUUID(d.Profile.AccountUUID); idx != -1 {
		return st.Accounts[idx], nil
	}
	if idx := st.IndexByEmail(d.Profile.EmailAddress); idx != -1 {
		return st.Accounts[idx], nil
	}
	acct := store.Account{
		UUID:    d.Profile.AccountUUID,
		Email:   d.Profile.EmailAddress,
		AddedAt: a.Now().UTC(),
	}
	if err := a.Store.WriteSnapshot(acct.UUID, d.RawCreds); err != nil {
		return store.Account{}, err
	}
	if err := a.Store.WriteProfile(acct.UUID, d.RawProfile); err != nil {
		return store.Account{}, err
	}
	st.Accounts = append(st.Accounts, acct)
	st.Active = acct.UUID
	if err := a.Store.SaveState(st); err != nil {
		return store.Account{}, err
	}
	return acct, nil
}

// snapshotNeedsRefresh reports whether live tokens should replace the
// stored snapshot for uuid: yes when the snapshot is missing or corrupt, or
// when the live tokens are strictly newer — an older live file must never
// clobber a fresher snapshot, whose refresh token may be the only valid one.
func (a *App) snapshotNeedsRefresh(uuid string, live claude.CredentialMeta) (bool, error) {
	snap, err := a.Store.ReadSnapshot(uuid)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil // registered but snapshotless — heal
	}
	if err != nil {
		return false, err
	}
	snapMeta, perr := claude.ParseCredentials(snap)
	return perr != nil || live.ExpiresAt > snapMeta.ExpiresAt, nil //nolint:nilerr // a corrupt snapshot is simply replaced
}

// SyncResult reports what SyncKnown wrote, so callers can tell the user that
// their stored state moved underneath them. The zero value means the store
// already agreed with the live login.
type SyncResult struct {
	// Account is the account as it stood before the sync — NewEmail below is
	// what replaced its address.
	Account store.Account
	// Creds covers both the routine refresh Claude Code performs while an
	// account is live and the healing of a missing or corrupt snapshot.
	Creds bool
	// Profile means the stored profile snapshot followed config drift.
	Profile bool
	// Active means the marker moved onto this account, which is how a login
	// made outside ccswitch is adopted.
	Active bool
	// NewEmail is the profile's email when it no longer matches the one on
	// record; empty when the stored email was already right.
	NewEmail string
}

// Changed reports whether SyncKnown wrote anything.
func (r SyncResult) Changed() bool { return r.Creds || r.Profile || r.Active || r.NewEmail != "" }

// changesState reports whether the sync touched state.json — the snapshots
// live in their own files and are written before it.
func (r SyncResult) changesState() bool { return r.Active || r.NewEmail != "" }

// Notes renders what the user needs to know about the sync. Profile drift
// alone stays silent: Claude Code rewrites its config constantly and a
// snapshot following it changes nothing the user could act on.
func (r SyncResult) Notes() []string {
	label := r.Account.Email
	if label == "" {
		label = r.Account.UUID
	}
	var notes []string
	if r.Creds {
		notes = append(notes, fmt.Sprintf("stored refreshed credentials for %s", label))
	}
	if r.Active {
		notes = append(notes, fmt.Sprintf("%s became the active account outside ccswitch", label))
	}
	if r.NewEmail != "" {
		notes = append(notes, fmt.Sprintf("%s is now on record as %s", label, r.NewEmail))
	}
	return notes
}

// computeSync decides what SyncKnown must write, judged against the current
// on-disk state. An account removed since discovery yields nothing to do at
// all — sync must never resurrect it.
func (a *App) computeSync(d Discovery) (SyncResult, store.State, error) {
	r := SyncResult{Account: d.Account}
	st, err := a.Store.LoadState()
	if err != nil {
		return r, st, err
	}
	idx := st.IndexByUUID(d.Account.UUID)
	if idx == -1 {
		return r, st, nil
	}
	if r.Creds, err = a.snapshotNeedsRefresh(d.Account.UUID, d.Meta); err != nil {
		return r, st, err
	}
	stored, err := a.Store.ReadProfile(d.Account.UUID)
	if err != nil {
		return r, st, err
	}
	r.Profile = d.RawProfile != nil && !bytes.Equal(d.RawProfile, stored)
	r.Active = st.Active != d.Account.UUID
	if d.Profile.EmailAddress != "" && st.Accounts[idx].Email != d.Profile.EmailAddress {
		r.NewEmail = d.Profile.EmailAddress
	}
	return r, st, nil
}

// SyncKnown reconciles the store with a known live login: strictly newer
// live tokens replace the stored snapshot (so refresh tokens never rot),
// profile drift is captured, the stored email follows the profile, and the
// active marker heals after logins done outside ccswitch. The result says
// what was written so the caller can report it; a zero result means the
// store already agreed with the live login.
func (a *App) SyncKnown(d Discovery) (SyncResult, error) {
	if d.Status != Known {
		return SyncResult{}, nil
	}
	// Unlocked fast path: the common nothing-drifted case takes no lock.
	res, _, err := a.computeSync(d)
	if err != nil || !res.Changed() {
		return SyncResult{}, err
	}

	unlock, err := a.Store.Lock()
	if err != nil {
		return SyncResult{}, err
	}
	defer unlock()
	// Recompute under the lock: since the unlocked look, another process may
	// have written a fresher snapshot (invalidating our decision to refresh
	// it) or removed the account entirely.
	res, st, err := a.computeSync(d)
	if err != nil || !res.Changed() {
		return SyncResult{}, err
	}
	uuid := d.Account.UUID
	if res.Creds {
		if err := a.Store.WriteSnapshot(uuid, d.RawCreds); err != nil {
			return SyncResult{}, err
		}
	}
	if res.Profile {
		if err := a.Store.WriteProfile(uuid, d.RawProfile); err != nil {
			return SyncResult{}, err
		}
	}
	if res.NewEmail != "" {
		st.Accounts[st.IndexByUUID(uuid)].Email = res.NewEmail
	}
	if res.Active {
		st.Active = uuid
	}
	if res.changesState() {
		return res, a.Store.SaveState(st)
	}
	return res, nil
}
