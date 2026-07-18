package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/app"
	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

var (
	testNow     = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	staleExpiry = testNow.Add(1 * time.Hour)
	freshExpiry = testNow.Add(4 * time.Hour)
	refreshOK   = testNow.Add(30 * 24 * time.Hour)
)

func newTestApp(t *testing.T) *app.App {
	t.Helper()
	home := t.TempDir()
	env := claude.Env{Home: home, XDGDataHome: filepath.Join(home, "xdg"), GOOS: "linux"}
	return &app.App{
		Creds: claude.NewCredentialStore(env, nil),
		Env:   env,
		Store: store.New(env.StoreDir()),
		Now:   func() time.Time { return testNow },
		Pgrep: func() bool { return false },
	}
}

func credsJSON(tag string, expires, refreshExpires time.Time) []byte {
	return fmt.Appendf(nil,
		`{"claudeAiOauth":{"accessToken":"sk-test-%s","refreshToken":"rt-test-%s","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max"}}`,
		tag, tag, expires.UnixMilli(), refreshExpires.UnixMilli())
}

func profileJSON(uuid, email string) []byte {
	return fmt.Appendf(nil, `{"accountUuid":%q,"emailAddress":%q}`, uuid, email)
}

// seedTwoAccounts registers a@x.com (active, alias work) and b@x.com with
// healthy snapshots and no live login, so discovery is a no-op.
func seedTwoAccounts(t *testing.T, a *app.App) {
	t.Helper()
	st := store.State{
		Active: "uuid-a",
		Accounts: []store.Account{
			{UUID: "uuid-a", Email: "a@x.com", Alias: "work", AddedAt: testNow},
			{UUID: "uuid-b", Email: "b@x.com", AddedAt: testNow},
		},
	}
	if err := a.Store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	for _, acc := range st.Accounts {
		tag := strings.TrimPrefix(acc.UUID, "uuid-")
		if err := a.Store.WriteSnapshot(acc.UUID, credsJSON(tag, staleExpiry, refreshOK)); err != nil {
			t.Fatal(err)
		}
		if err := a.Store.WriteProfile(acc.UUID, profileJSON(acc.UUID, acc.Email)); err != nil {
			t.Fatal(err)
		}
	}
}

func writeLiveCreds(t *testing.T, a *app.App, raw []byte) {
	t.Helper()
	path := a.Env.CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLiveConfig(t *testing.T, a *app.App, rawProfile []byte) {
	t.Helper()
	cfg := fmt.Appendf(nil, `{"numStartups": 42, "oauthAccount": %s, "theme": "dark-daltonized"}`, rawProfile)
	if err := os.WriteFile(a.Env.ConfigPath(), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

// run executes the CLI against a and returns the exit code plus captured
// stdout/stderr.
func run(t *testing.T, a *app.App, tty bool, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = Execute(Options{
		Version: "test",
		App:     a,
		IO:      IO{In: strings.NewReader(stdin), Out: &out, Err: &errBuf, IsTTY: tty},
	}, args)
	return code, out.String(), errBuf.String()
}

func TestListShowsAccountsWithActiveMarker(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, stderr := run(t, a, false, "", "list")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "▶") || !strings.Contains(lines[1], "a@x.com") ||
		!strings.Contains(lines[1], "work") || !strings.Contains(lines[1], "ok") {
		t.Errorf("active row = %q, want marker, email, alias, token status", lines[1])
	}
	if strings.Contains(lines[2], "▶") || !strings.Contains(lines[2], "b@x.com") {
		t.Errorf("inactive row = %q, want b@x.com without marker", lines[2])
	}
}

func TestListEmpty(t *testing.T) {
	a := newTestApp(t)
	code, out, _ := run(t, a, false, "", "list")
	if code != 0 || !strings.Contains(out, "no accounts registered") {
		t.Errorf("exit = %d, out = %q", code, out)
	}
}

func TestListJSON(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, _ := run(t, a, false, "", "ls", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var views []map[string]any
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(views) != 2 {
		t.Fatalf("got %d accounts", len(views))
	}
	first := views[0]
	if first["email"] != "a@x.com" || first["active"] != true ||
		first["alias"] != "work" || first["tokenStatus"] != "ok" || first["plan"] != "max" {
		t.Errorf("first account = %+v", first)
	}
}

func TestStatusText(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, _ := run(t, a, false, "", "status")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"a@x.com (work)", "max, token ok", "2 managed", a.Store.Dir()} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, _ := run(t, a, false, "", "status", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var view struct {
		Active   *struct{ Email string } `json:"active"`
		Accounts int                     `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if view.Accounts != 2 || view.Active == nil || view.Active.Email != "a@x.com" {
		t.Errorf("status = %s", out)
	}
}

func TestSwitchRotatesWithoutArgument(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, stderr := run(t, a, false, "", "switch")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(out, "switched to b@x.com") {
		t.Errorf("out = %q", out)
	}
	live, err := a.Creds.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live, credsJSON("b", staleExpiry, refreshOK)) {
		t.Error("live credentials are not b's snapshot")
	}
	if st, err := a.Store.LoadState(); err != nil || st.Active != "uuid-b" {
		t.Errorf("Active = %q, %v", st.Active, err)
	}
}

func TestSwitchResolvesArguments(t *testing.T) {
	for _, arg := range []string{"2", "b@x.com", "uuid-b"} {
		t.Run(arg, func(t *testing.T) {
			a := newTestApp(t)
			seedTwoAccounts(t, a)
			code, out, stderr := run(t, a, false, "", "switch", arg)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr)
			}
			if !strings.Contains(out, "switched to b@x.com") {
				t.Errorf("out = %q", out)
			}
		})
	}
	t.Run("alias", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		if code, out, _ := run(t, a, false, "", "switch", "work"); code != 0 || !strings.Contains(out, "a@x.com (work)") {
			t.Errorf("exit = %d, out = %q", code, out)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		code, _, stderr := run(t, a, false, "", "switch", "nope")
		if code != 1 || !strings.Contains(stderr, "no account matching") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
	})
}

func TestRemove(t *testing.T) {
	present := func(t *testing.T, a *app.App, uuid string) bool {
		t.Helper()
		st, err := a.Store.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st.IndexByUUID(uuid) != -1
	}

	t.Run("confirmed on a tty", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		code, out, stderr := run(t, a, true, "y\n", "remove", "b@x.com")
		if code != 0 || !strings.Contains(out, "removed b@x.com") {
			t.Errorf("exit = %d, out = %q, stderr = %q", code, out, stderr)
		}
		if !strings.Contains(stderr, "[y/N]") {
			t.Errorf("no prompt on stderr: %q", stderr)
		}
		if present(t, a, "uuid-b") {
			t.Error("account still present")
		}
	})
	t.Run("declined on a tty", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		code, _, stderr := run(t, a, true, "n\n", "rm", "b@x.com")
		if code != 0 || !strings.Contains(stderr, "aborted") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
		if !present(t, a, "uuid-b") {
			t.Error("account removed despite declining")
		}
	})
	t.Run("no tty requires --yes", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		code, _, stderr := run(t, a, false, "", "remove", "b@x.com")
		if code != 1 || !strings.Contains(stderr, "--yes") {
			t.Errorf("exit = %d, stderr = %q", code, stderr)
		}
		if !present(t, a, "uuid-b") {
			t.Error("account removed without confirmation")
		}
	})
	t.Run("yes flag skips the prompt", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		code, out, stderr := run(t, a, false, "", "remove", "--yes", "b@x.com")
		if code != 0 || !strings.Contains(out, "removed b@x.com") {
			t.Errorf("exit = %d, out = %q, stderr = %q", code, out, stderr)
		}
		if present(t, a, "uuid-b") {
			t.Error("account still present")
		}
	})
}

func TestAliasSetAndClear(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	if code, out, stderr := run(t, a, false, "", "alias", "b@x.com", "personal"); code != 0 || !strings.Contains(out, "personal") {
		t.Fatalf("exit = %d, out = %q, stderr = %q", code, out, stderr)
	}
	st, err := a.Store.LoadState()
	if err != nil || st.Accounts[1].Alias != "personal" {
		t.Errorf("alias = %q, %v", st.Accounts[1].Alias, err)
	}

	if code, out, _ := run(t, a, false, "", "alias", "b@x.com", ""); code != 0 || !strings.Contains(out, "cleared") {
		t.Fatalf("exit = %d, out = %q", code, out)
	}
	st, err = a.Store.LoadState()
	if err != nil || st.Accounts[1].Alias != "" {
		t.Errorf("alias = %q, %v", st.Accounts[1].Alias, err)
	}
}

func TestDoctorHealthyExitsZero(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, stderr := run(t, a, false, "", "doctor")
	if code != 0 {
		t.Fatalf("exit = %d, out = %q, stderr = %q", code, out, stderr)
	}
	if !strings.Contains(out, "OK") || !strings.Contains(out, "state") {
		t.Errorf("doctor output = %q", out)
	}
}

func TestDoctorFailureExitsOneSilently(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(a.Store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Store.Dir(), "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := run(t, a, false, "", "doctor")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("report missing FAIL: %q", out)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want the report alone to explain the failure", stderr)
	}
}

func TestDoctorJSON(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, _ := run(t, a, false, "", "doctor", "--json")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var checks []struct{ Name, Status, Detail string }
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(checks) == 0 || checks[0].Status == "" {
		t.Errorf("checks = %+v", checks)
	}
}

func TestCompletionsAreShellPure(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			a := newTestApp(t)
			// An unknown live login would make discovery prompt or print a
			// notice — completions must skip it entirely.
			writeLiveCreds(t, a, credsJSON("n", freshExpiry, refreshOK))
			writeLiveConfig(t, a, profileJSON("uuid-n", "n@x.com"))
			code, out, stderr := run(t, a, true, "", "completions", shell)
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr)
			}
			if !strings.Contains(out, "ccswitch") {
				t.Errorf("%s completions do not mention ccswitch", shell)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want pure output for shell eval", stderr)
			}
		})
	}
}

func TestDiscoveryPrompt(t *testing.T) {
	seedUnknown := func(t *testing.T) *app.App {
		t.Helper()
		a := newTestApp(t)
		writeLiveCreds(t, a, credsJSON("n", freshExpiry, refreshOK))
		writeLiveConfig(t, a, profileJSON("uuid-n", "n@x.com"))
		return a
	}
	registered := func(t *testing.T, a *app.App) bool {
		t.Helper()
		st, err := a.Store.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st.IndexByUUID("uuid-n") != -1
	}

	t.Run("accepted adds the account", func(t *testing.T) {
		a := seedUnknown(t)
		code, out, stderr := run(t, a, true, "y\n", "list")
		if code != 0 {
			t.Fatalf("exit = %d, stderr = %q", code, stderr)
		}
		if !strings.Contains(stderr, "Add it?") || !strings.Contains(stderr, "added n@x.com") {
			t.Errorf("stderr = %q", stderr)
		}
		if !registered(t, a) {
			t.Error("account was not added")
		}
		if !strings.Contains(out, "n@x.com") {
			t.Errorf("list after add misses the account: %q", out)
		}
	})
	t.Run("declined leaves the store alone", func(t *testing.T) {
		a := seedUnknown(t)
		code, _, _ := run(t, a, true, "n\n", "list")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if registered(t, a) {
			t.Error("account added despite declining")
		}
	})
	t.Run("no tty prints a notice only", func(t *testing.T) {
		a := seedUnknown(t)
		code, _, stderr := run(t, a, false, "", "list")
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(stderr, "not managed") {
			t.Errorf("stderr = %q, want an unmanaged-login notice", stderr)
		}
		if registered(t, a) {
			t.Error("account added without a prompt")
		}
	})
}

func TestRootWithoutTTYListsAccounts(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	code, out, _ := run(t, a, false, "")
	if code != 0 || !strings.Contains(out, "a@x.com") {
		t.Errorf("exit = %d, out = %q", code, out)
	}
}

func TestRootWithTTYLaunchesTUI(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	launched := false
	var out, errBuf bytes.Buffer
	code := Execute(Options{
		Version: "test",
		App:     a,
		RunTUI:  func(*app.App) error { launched = true; return nil },
		IO:      IO{In: strings.NewReader(""), Out: &out, Err: &errBuf, IsTTY: true},
	}, nil)
	if code != 0 || !launched {
		t.Errorf("exit = %d, launched = %v", code, launched)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want the TUI to own the terminal", out.String())
	}
}

func TestVersionFlagSkipsDiscovery(t *testing.T) {
	a := newTestApp(t)
	// An unknown login must not prompt during --version.
	writeLiveCreds(t, a, credsJSON("n", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-n", "n@x.com"))
	code, out, stderr := run(t, a, true, "", "--version")
	if code != 0 || out != "ccswitch test\n" {
		t.Errorf("exit = %d, out = %q", code, out)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want no discovery chatter", stderr)
	}
}

// TestNoOutputLeaksTokens runs every reading command against a fully
// populated world and sweeps all output for the sentinel token prefixes.
func TestNoOutputLeaksTokens(t *testing.T) {
	invocations := [][]string{
		{"list"},
		{"list", "--json"},
		{"status"},
		{"status", "--json"},
		{"doctor"},
		{"doctor", "--json"},
		{"switch"},
		{"switch", "work"},
	}
	for _, args := range invocations {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a := newTestApp(t)
			seedTwoAccounts(t, a)
			writeLiveCreds(t, a, credsJSON("a-live", freshExpiry, refreshOK))
			writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
			_, out, stderr := run(t, a, false, "", args...)
			blob := out + stderr
			for _, needle := range []string{"sk-test-", "rt-test-"} {
				if strings.Contains(blob, needle) {
					t.Errorf("output of %v leaks a token value (%s)", args, needle)
				}
			}
		})
	}
}
