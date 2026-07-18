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

// syncNeeds lists what SyncKnown has to write.
type syncNeeds struct{ creds, profile, active, email bool }

func (n syncNeeds) any() bool { return n.creds || n.profile || n.active || n.email }

// computeSyncNeeds decides what SyncKnown must write, judged against the
// current on-disk state. An account removed since discovery yields no needs
// at all — sync must never resurrect it.
func (a *App) computeSyncNeeds(d Discovery) (syncNeeds, store.State, error) {
	var n syncNeeds
	st, err := a.Store.LoadState()
	if err != nil {
		return n, st, err
	}
	idx := st.IndexByUUID(d.Account.UUID)
	if idx == -1 {
		return n, st, nil
	}
	if n.creds, err = a.snapshotNeedsRefresh(d.Account.UUID, d.Meta); err != nil {
		return n, st, err
	}
	stored, err := a.Store.ReadProfile(d.Account.UUID)
	if err != nil {
		return n, st, err
	}
	n.profile = d.RawProfile != nil && !bytes.Equal(d.RawProfile, stored)
	n.active = st.Active != d.Account.UUID
	n.email = d.Profile.EmailAddress != "" && st.Accounts[idx].Email != d.Profile.EmailAddress
	return n, st, nil
}

// SyncKnown reconciles the store with a known live login: strictly newer
// live tokens replace the stored snapshot (so refresh tokens never rot),
// profile drift is captured, the stored email follows the profile, and the
// active marker heals after logins done outside ccswitch. Returns whether
// anything was written.
func (a *App) SyncKnown(d Discovery) (bool, error) {
	if d.Status != Known {
		return false, nil
	}
	// Unlocked fast path: the common nothing-drifted case takes no lock.
	need, _, err := a.computeSyncNeeds(d)
	if err != nil || !need.any() {
		return false, err
	}

	unlock, err := a.Store.Lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	// Recompute under the lock: since the unlocked look, another process may
	// have written a fresher snapshot (invalidating our decision to refresh
	// it) or removed the account entirely.
	need, st, err := a.computeSyncNeeds(d)
	if err != nil || !need.any() {
		return false, err
	}
	uuid := d.Account.UUID
	if need.creds {
		if err := a.Store.WriteSnapshot(uuid, d.RawCreds); err != nil {
			return false, err
		}
	}
	if need.profile {
		if err := a.Store.WriteProfile(uuid, d.RawProfile); err != nil {
			return false, err
		}
	}
	if need.email {
		st.Accounts[st.IndexByUUID(uuid)].Email = d.Profile.EmailAddress
	}
	if need.active {
		st.Active = uuid
	}
	if need.email || need.active {
		return true, a.Store.SaveState(st)
	}
	return true, nil
}
