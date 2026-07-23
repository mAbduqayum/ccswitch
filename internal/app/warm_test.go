package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

// warmedExpiry is later than every snapshot expiry in the fixtures, so a
// simulated refresh always counts as strictly newer.
var warmedExpiry = testNow.Add(8 * time.Hour)

// warmWorld is a three-account setup with A live, wired to a fake Warmer that
// stands in for the claude binary: it records who was live at each call and
// rewrites the live credentials the way a real refresh would.
type warmWorld struct {
	a        *App
	accounts []store.Account
	// visited is the uuid live at each Warmer call, in call order.
	visited []string
	models  []string
	prompts []string
	// fail forces a Warmer error for the given uuid.
	fail map[string]error
	// refreshes turns off the simulated credential refresh when false.
	refreshes bool
	deadlines []bool
}

func newWarmWorld(t *testing.T) *warmWorld {
	t.Helper()
	w := &warmWorld{
		a: newTestApp(t),
		accounts: []store.Account{
			{UUID: "uuid-a", Email: "a@x.com", Alias: "work", AddedAt: testNow},
			{UUID: "uuid-b", Email: "b@x.com", AddedAt: testNow},
			{UUID: "uuid-c", Email: "c@x.com", AddedAt: testNow},
		},
		fail:      map[string]error{},
		refreshes: true,
	}
	saveState(t, w.a, store.State{Active: "uuid-a", Accounts: w.accounts})
	for _, acct := range w.accounts {
		writeSnapshot(t, w.a, acct.UUID, credsJSON(acct.UUID, staleExpiry, refreshOK))
		writeProfile(t, w.a, acct.UUID, profileJSON(acct.UUID, acct.Email))
	}
	writeLiveCreds(t, w.a, credsJSON("uuid-a", staleExpiry, refreshOK))
	writeLiveConfig(t, w.a, profileJSON("uuid-a", "a@x.com"))

	w.a.Warmer = func(ctx context.Context, model, prompt string) error {
		// Switch patches the config with the target's profile, so it names
		// whichever account is live right now.
		_, profile, err := claude.ReadOAuthAccount(w.a.Env.ConfigPath())
		if err != nil {
			return err
		}
		uuid := profile.AccountUUID
		_, hasDeadline := ctx.Deadline()
		w.visited = append(w.visited, uuid)
		w.models = append(w.models, model)
		w.prompts = append(w.prompts, prompt)
		w.deadlines = append(w.deadlines, hasDeadline)
		if err := w.fail[uuid]; err != nil {
			return err
		}
		if w.refreshes {
			writeLiveCreds(t, w.a, w.warmed(uuid))
		}
		return nil
	}
	return w
}

// warmed is the credential blob the fake claude leaves behind for uuid.
func (w *warmWorld) warmed(uuid string) []byte {
	return credsJSON(uuid+"-warmed", warmedExpiry, refreshOK)
}

func (w *warmWorld) run(t *testing.T) WarmReport {
	t.Helper()
	report, err := w.a.Warm(t.Context(), "haiku", "hi", time.Minute)
	if err != nil {
		t.Fatalf("Warm: %v", err)
	}
	return report
}

// TestWarmCapturesEveryRefresh is the load-bearing test: each account's
// refresh happens while it is live, and must land in that account's slot —
// the earlier ones via the following switch, the last one via the restore.
func TestWarmCapturesEveryRefresh(t *testing.T) {
	w := newWarmWorld(t)
	report := w.run(t)

	if got, want := w.visited, []string{"uuid-a", "uuid-b", "uuid-c"}; !slices.Equal(got, want) {
		t.Errorf("warmed %v, want %v (every account, in rotation order)", got, want)
	}
	if n := report.Failed(); n != 0 {
		t.Errorf("Failed() = %d, want 0; results = %+v", n, report.Results)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", report.Warnings)
	}
	for _, acct := range w.accounts {
		if got := readSnapshot(t, w.a, acct.UUID); !bytes.Equal(got, w.warmed(acct.UUID)) {
			t.Errorf("%s snapshot does not hold the warmed tokens — the refresh was lost", acct.Email)
		}
	}
}

func TestWarmRestoresOriginalAccount(t *testing.T) {
	w := newWarmWorld(t)
	w.run(t)

	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a — warm must end where it started", st.Active)
	}
	if got := readLiveCreds(t, w.a); !bytes.Equal(got, w.warmed("uuid-a")) {
		t.Error("live credentials are not A's warmed tokens")
	}
	_, profile, err := claude.ReadOAuthAccount(w.a.Env.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if profile.AccountUUID != "uuid-a" {
		t.Errorf("config profile = %q, want uuid-a", profile.AccountUUID)
	}
}

// TestWarmContinuesPastFailure covers the issue's requirement that one
// account being offline or needing a re-login never stops the rest.
func TestWarmContinuesPastFailure(t *testing.T) {
	w := newWarmWorld(t)
	w.fail["uuid-b"] = errors.New("claude: credentials expired")
	report := w.run(t)

	if got, want := w.visited, []string{"uuid-a", "uuid-b", "uuid-c"}; !slices.Equal(got, want) {
		t.Errorf("warmed %v, want %v — a failure must not stop the cycle", got, want)
	}
	if len(report.Results) != 3 {
		t.Fatalf("got %d results, want 3", len(report.Results))
	}
	for _, res := range report.Results {
		switch res.Account.UUID {
		case "uuid-b":
			if res.Err == nil {
				t.Error("b@x.com reported success despite the warmer failing")
			}
		default:
			if res.Err != nil {
				t.Errorf("%s failed unexpectedly: %v", res.Account.Email, res.Err)
			}
		}
	}
	if n := report.Failed(); n != 1 {
		t.Errorf("Failed() = %d, want 1", n)
	}
	// The neighbours still got their refreshes captured.
	for _, uuid := range []string{"uuid-a", "uuid-c"} {
		if got := readSnapshot(t, w.a, uuid); !bytes.Equal(got, w.warmed(uuid)) {
			t.Errorf("%s snapshot missing its warmed tokens", uuid)
		}
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a", st.Active)
	}
}

// TestWarmReportsUnswitchableAccount covers a per-account switch failure —
// here a missing snapshot — which must be reported without derailing the run.
func TestWarmReportsUnswitchableAccount(t *testing.T) {
	w := newWarmWorld(t)
	if err := w.a.Store.RemoveAccount("uuid-c"); err != nil {
		t.Fatal(err)
	}
	report := w.run(t)

	if got, want := w.visited, []string{"uuid-a", "uuid-b"}; !slices.Equal(got, want) {
		t.Errorf("warmed %v, want %v — C has no snapshot to switch to", got, want)
	}
	var cErr error
	for _, res := range report.Results {
		if res.Account.UUID == "uuid-c" {
			cErr = res.Err
		}
	}
	if cErr == nil {
		t.Fatal("c@x.com reported success despite having no snapshot")
	}
	if !strings.Contains(cErr.Error(), "snapshot") {
		t.Errorf("error = %q, want it to mention the missing snapshot", cErr)
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a", st.Active)
	}
}

func TestWarmThreadsModelPromptAndTimeout(t *testing.T) {
	w := newWarmWorld(t)
	if _, err := w.a.Warm(t.Context(), "sonnet", "ping", time.Minute); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	for i := range w.visited {
		if w.models[i] != "sonnet" || w.prompts[i] != "ping" {
			t.Errorf("call %d ran model %q prompt %q, want sonnet/ping", i, w.models[i], w.prompts[i])
		}
		if !w.deadlines[i] {
			t.Errorf("call %d had no deadline — --timeout is not being applied", i)
		}
	}
}

// TestWarmTimesOutPerAccount proves the timeout bounds each account
// separately rather than the run as a whole.
func TestWarmTimesOutPerAccount(t *testing.T) {
	w := newWarmWorld(t)
	w.a.Warmer = func(ctx context.Context, _, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	report, err := w.a.Warm(t.Context(), "haiku", "hi", time.Millisecond)
	if err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if len(report.Results) != 3 {
		t.Fatalf("got %d results, want 3 — every account must be attempted", len(report.Results))
	}
	for _, res := range report.Results {
		if !errors.Is(res.Err, context.DeadlineExceeded) {
			t.Errorf("%s err = %v, want DeadlineExceeded", res.Account.Email, res.Err)
		}
	}
	if st := loadState(t, w.a); st.Active != "uuid-a" {
		t.Errorf("Active = %q, want uuid-a", st.Active)
	}
}

func TestWarmRefusesUnregisteredLogin(t *testing.T) {
	w := newWarmWorld(t)
	writeLiveConfig(t, w.a, profileJSON("uuid-stranger", "stranger@x.com"))

	_, err := w.a.Warm(t.Context(), "haiku", "hi", time.Minute)
	if !errors.Is(err, ErrUnsavedLogin) {
		t.Fatalf("err = %v, want ErrUnsavedLogin", err)
	}
	if len(w.visited) != 0 {
		t.Errorf("warmed %v — nothing may run before the refusal", w.visited)
	}
}

func TestWarmRefusesUnidentifiableLogin(t *testing.T) {
	w := newWarmWorld(t)
	// Credentials with no oauthAccount to attribute them to.
	if err := os.WriteFile(w.a.Env.ConfigPath(), []byte(`{"numStartups": 42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := w.a.Warm(t.Context(), "haiku", "hi", time.Minute)
	if err == nil {
		t.Fatal("Warm accepted a login it cannot identify")
	}
	if !strings.Contains(err.Error(), "identif") {
		t.Errorf("err = %q, want it to explain the login cannot be identified", err)
	}
	if len(w.visited) != 0 {
		t.Errorf("warmed %v — nothing may run before the refusal", w.visited)
	}
}

func TestWarmWithNoAccounts(t *testing.T) {
	a := newTestApp(t)
	_, err := a.Warm(t.Context(), "haiku", "hi", time.Minute)
	if err == nil {
		t.Fatal("Warm succeeded with an empty store")
	}
	if !strings.Contains(err.Error(), "no accounts registered") {
		t.Errorf("err = %q, want it to say no accounts are registered", err)
	}
}

// TestWarmSingleAccount: the rotation guard that stops `switch` at one account
// must not apply — warming a lone account is the whole point of the command.
func TestWarmSingleAccount(t *testing.T) {
	w := newWarmWorld(t)
	only := w.accounts[0]
	saveState(t, w.a, store.State{Active: only.UUID, Accounts: []store.Account{only}})

	report := w.run(t)
	if got, want := w.visited, []string{"uuid-a"}; !slices.Equal(got, want) {
		t.Errorf("warmed %v, want %v", got, want)
	}
	if n := report.Failed(); n != 0 {
		t.Errorf("Failed() = %d, want 0", n)
	}
	if got := readSnapshot(t, w.a, "uuid-a"); !bytes.Equal(got, w.warmed("uuid-a")) {
		t.Error("the lone account's refresh was not captured")
	}
}

// TestWarmWarnsWhenRestoreFails guards the deferred-warning path: the restore
// runs in a defer, so Warm's results must be named returns or anything it
// reports is dropped on the floor.
func TestWarmWarnsWhenRestoreFails(t *testing.T) {
	w := newWarmWorld(t)
	base := w.a.Warmer
	w.a.Warmer = func(ctx context.Context, model, prompt string) error {
		err := base(ctx, model, prompt)
		// While the last account is live, strand the account warm has to
		// return to, so the deferred restore cannot succeed.
		if len(w.visited) == len(w.accounts) {
			if rerr := w.a.Store.RemoveAccount("uuid-a"); rerr != nil {
				t.Error(rerr)
			}
		}
		return err
	}

	report := w.run(t)
	if n := report.Failed(); n != 0 {
		t.Errorf("Failed() = %d, want 0 — every account warmed fine", n)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("no warning reported for the failed restore — the defer's warning was dropped")
	}
	if !strings.Contains(report.Warnings[len(report.Warnings)-1], "a@x.com") {
		t.Errorf("warnings = %v, want one naming a@x.com", report.Warnings)
	}
}
