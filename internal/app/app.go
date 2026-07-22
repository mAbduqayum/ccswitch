// Package app orchestrates account discovery, switching, and health checks.
// It is the shared core behind both the CLI and the TUI.
package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

type App struct {
	Creds claude.CredentialStore
	Env   claude.Env
	Store *store.Store
	Now   func() time.Time
}

// New wires an App against the real environment.
func New(env claude.Env) *App {
	return &App{
		Creds: claude.NewCredentialStore(env, claude.RealExecRunner),
		Env:   env,
		Store: store.New(env.StoreDir()),
		Now:   time.Now,
	}
}

// RotateTarget picks the account after Active in rotation order, wrapping
// around. With Active unset (or gone) it starts at the first account.
func RotateTarget(st store.State) (store.Account, error) {
	if len(st.Accounts) == 0 {
		return store.Account{}, errors.New("no accounts registered — log in with `claude /login` and run `ccswitch` to add one")
	}
	if len(st.Accounts) == 1 {
		return store.Account{}, errors.New("only one account registered — nothing to switch to")
	}
	idx := st.IndexByUUID(st.Active)
	return st.Accounts[(idx+1)%len(st.Accounts)], nil
}

// ResolveAccount interprets arg as a 1-based list number, then an email,
// then an alias, then a UUID.
func ResolveAccount(st store.State, arg string) (store.Account, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(st.Accounts) {
			return store.Account{}, fmt.Errorf("account number %d out of range 1–%d", n, len(st.Accounts))
		}
		return st.Accounts[n-1], nil
	}
	if idx := st.IndexByEmail(arg); idx != -1 {
		return st.Accounts[idx], nil
	}
	for _, a := range st.Accounts {
		if a.Alias != "" && a.Alias == arg {
			return a, nil
		}
	}
	if idx := st.IndexByUUID(arg); idx != -1 {
		return st.Accounts[idx], nil
	}
	return store.Account{}, fmt.Errorf("no account matching %q — see `ccswitch list`", arg)
}

// Remove forgets the account and deletes its snapshots.
func (a *App) Remove(uuid string) error {
	unlock, err := a.Store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := a.Store.LoadState()
	if err != nil {
		return err
	}
	idx := st.IndexByUUID(uuid)
	if idx == -1 {
		return fmt.Errorf("no account with uuid %s", uuid)
	}
	st.Accounts = append(st.Accounts[:idx], st.Accounts[idx+1:]...)
	if st.Active == uuid {
		st.Active = ""
	}
	if err := a.Store.SaveState(st); err != nil {
		return err
	}
	return a.Store.RemoveAccount(uuid)
}

// SetAlias names the account; an empty alias clears it.
func (a *App) SetAlias(uuid, alias string) error {
	if alias != "" {
		if _, err := strconv.Atoi(alias); err == nil {
			return fmt.Errorf("alias %q would be ambiguous with account numbers", alias)
		}
		if strings.ContainsAny(alias, " \t") {
			return fmt.Errorf("alias %q must not contain whitespace", alias)
		}
	}
	unlock, err := a.Store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	st, err := a.Store.LoadState()
	if err != nil {
		return err
	}
	idx := st.IndexByUUID(uuid)
	if idx == -1 {
		return fmt.Errorf("no account with uuid %s", uuid)
	}
	if alias != "" {
		for i, acc := range st.Accounts {
			if i != idx && acc.Alias == alias {
				return fmt.Errorf("alias %q is already used by %s", alias, acc.Email)
			}
		}
	}
	st.Accounts[idx].Alias = alias
	return a.Store.SaveState(st)
}
