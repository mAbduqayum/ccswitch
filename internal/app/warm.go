package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/store"
)

// WarmResult is one account's outcome. Err nil means claude ran successfully
// as that account.
type WarmResult struct {
	Account store.Account
	Err     error
}

// WarmReport is the outcome of a full warm cycle. Warnings carries
// non-fatal trouble (mirroring SwitchResult.Warnings) so a late failure never
// costs the caller the per-account results it already earned.
type WarmReport struct {
	Results  []WarmResult
	Warnings []string
}

// Failed counts the accounts that did not warm.
func (r WarmReport) Failed() int {
	n := 0
	for _, res := range r.Results {
		if res.Err != nil {
			n++
		}
	}
	return n
}

// Warm exercises every registered account so its refresh token never expires
// through disuse: each account is switched to in turn and claude is run once
// as it, which makes Claude Code refresh the credentials on disk.
//
// This leans on Switch's ordering guarantee — the live credentials are
// snapshotted into the *current* account's slot before the target is restored
// — so the refresh performed while an account was live is banked by the
// following switch. The last account has no following switch, so its refresh
// is folded in explicitly before the original account is restored.
//
// One account failing (offline, needs re-login) never stops the rest, and the
// originally active account is always restored.
// The results are named so the deferred restore below can still append to
// Warnings: an unnamed result would be copied before the defer ran, silently
// dropping anything it reported.
func (a *App) Warm(ctx context.Context, model, prompt string, timeout time.Duration) (report WarmReport, err error) {
	st, err := a.Store.LoadState()
	if err != nil {
		return report, err
	}
	if len(st.Accounts) == 0 {
		return report, errors.New("no accounts registered — log in with `claude /login` and run `ccswitch` to add one")
	}

	// Warming rewrites the live credentials repeatedly, so unlike the
	// read-only paths it cannot degrade an unreadable live login to a warning.
	d, err := a.Discover()
	if err != nil {
		return report, err
	}
	switch d.Status {
	case Unknown:
		return report, fmt.Errorf("%w: %s — rerun `ccswitch` and accept the add prompt, or switch to a managed account before warming",
			ErrUnsavedLogin, d.Profile.EmailAddress)
	case NoProfile:
		// The live credentials cannot be attributed, and identifyLive's
		// active-marker fallback could file them under the wrong account.
		return report, errors.New("the live login cannot be identified (no oauthAccount profile) — run `claude /login` before warming")
	}

	// Where to end up: the account that was live, so warming is invisible.
	original := restoreTarget(st, d)
	defer func() {
		// Switch banks a refresh when it switches *away* from an account, so
		// the last account warmed still has its refresh only in the live file
		// — switching to an already-live account restores those tokens
		// without storing them. Fold it in before restoring. (With a single
		// registered account that is the only thing that captures anything.)
		if err := a.captureLive(); err != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("could not store the last refresh: %v", err))
		}
		res, err := a.Switch(original, false)
		report.Warnings = append(report.Warnings, prefixWarnings(original.Email, res.Warnings)...)
		if err != nil {
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("could not restore %s: %v", original.Email, err))
		}
	}()

	report.Results = make([]WarmResult, 0, len(st.Accounts))
	for _, acct := range st.Accounts {
		warnings, warmErr := a.warmOne(ctx, acct, model, prompt, timeout)
		report.Warnings = append(report.Warnings, warnings...)
		report.Results = append(report.Results, WarmResult{Account: acct, Err: warmErr})
	}
	return report, nil
}

// captureLive stores the live credentials in their owner's slot when they are
// newer than what is on record — the same reconciliation discovery does.
func (a *App) captureLive() error {
	d, err := a.Discover()
	if err != nil {
		return err
	}
	_, err = a.SyncKnown(d)
	return err
}

// warmOne switches to acct and runs claude as it. A failed switch leaves the
// live credentials untouched — Switch reads the target snapshot before writing
// anything — so the next account is unaffected. Switch warnings (e.g. a failed
// profile patch, which can misattribute the *next* account's refresh) are
// returned so the caller surfaces them, as the switch command does.
func (a *App) warmOne(ctx context.Context, acct store.Account, model, prompt string, timeout time.Duration) ([]string, error) {
	res, err := a.Switch(acct, false)
	warnings := prefixWarnings(acct.Email, res.Warnings)
	if err != nil {
		return warnings, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return warnings, a.Warmer(ctx, model, prompt)
}

// prefixWarnings labels each switch warning with the account it came from, so
// a report cycling many accounts stays attributable.
func prefixWarnings(email string, warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, len(warnings))
	for i, w := range warnings {
		out[i] = fmt.Sprintf("%s: %s", email, w)
	}
	return out
}

// restoreTarget picks the account warm should end on: the one that was live,
// falling back to the active marker and then to the first account when there
// was no usable live login.
func restoreTarget(st store.State, d Discovery) store.Account {
	if d.Status == Known {
		return d.Account
	}
	if idx := st.IndexByUUID(st.Active); idx != -1 {
		return st.Accounts[idx]
	}
	return st.Accounts[0]
}
