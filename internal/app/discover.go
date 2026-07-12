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
type Discovery struct {
	Status     DiscoveryStatus
	RawCreds   []byte
	Meta       claude.CredentialMeta
	RawProfile json.RawMessage
	Profile    claude.Profile
	Account    store.Account // set when Status == Known
}

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
		return Discovery{}, fmt.Errorf("%s: %w", a.Creds.Location(), err)
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
	// Another process may have added it between Discover and Lock.
	if idx := st.IndexByUUID(d.Profile.AccountUUID); idx != -1 {
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

// SyncKnown reconciles the store with a known live login: strictly newer
// live tokens replace the stored snapshot (so refresh tokens never rot),
// profile drift is captured, the stored email follows the profile, and the
// active marker heals after logins done outside ccswitch. Returns whether
// anything was written.
func (a *App) SyncKnown(d Discovery) (bool, error) {
	if d.Status != Known {
		return false, nil
	}
	uuid := d.Account.UUID

	needCreds := false
	snap, err := a.Store.ReadSnapshot(uuid)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		needCreds = true // registered but snapshotless — heal
	case err != nil:
		return false, err
	default:
		snapMeta, perr := claude.ParseCredentials(snap)
		// A corrupt snapshot is replaced; otherwise only strictly newer
		// live tokens win, so an older live file never clobbers a fresher
		// snapshot.
		needCreds = perr != nil || d.Meta.ExpiresAt > snapMeta.ExpiresAt
	}

	storedProfile, err := a.Store.ReadProfile(uuid)
	if err != nil {
		return false, err
	}
	needProfile := d.RawProfile != nil && !bytes.Equal(d.RawProfile, storedProfile)

	st, err := a.Store.LoadState()
	if err != nil {
		return false, err
	}
	needActive := st.Active != uuid
	needEmail := d.Profile.EmailAddress != "" && d.Account.Email != d.Profile.EmailAddress

	if !needCreds && !needProfile && !needActive && !needEmail {
		return false, nil
	}

	unlock, err := a.Store.Lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	st, err = a.Store.LoadState() // reload under the lock
	if err != nil {
		return false, err
	}
	if needCreds {
		if err := a.Store.WriteSnapshot(uuid, d.RawCreds); err != nil {
			return false, err
		}
	}
	if needProfile {
		if err := a.Store.WriteProfile(uuid, d.RawProfile); err != nil {
			return false, err
		}
	}
	if idx := st.IndexByUUID(uuid); idx != -1 && needEmail {
		st.Accounts[idx].Email = d.Profile.EmailAddress
	}
	st.Active = uuid
	return true, a.Store.SaveState(st)
}
