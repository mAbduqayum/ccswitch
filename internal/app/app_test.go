package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/claude"
	"github.com/mAbduqayum/ccswitch/internal/store"
)

var (
	testNow        = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	olderExpiry    = testNow.Add(30 * time.Minute)
	staleExpiry    = testNow.Add(1 * time.Hour)
	freshExpiry    = testNow.Add(4 * time.Hour)
	refreshOK      = testNow.Add(30 * 24 * time.Hour)
	refreshSoon    = testNow.Add(3 * 24 * time.Hour)
	refreshExpired = testNow.Add(-2 * time.Hour)
)

// newTestApp wires an App against a throwaway HOME with a real file-backed
// credential store, so tests exercise the same IO paths as production.
func newTestApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	env := claude.Env{Home: home, XDGDataHome: filepath.Join(home, "xdg"), GOOS: "linux"}
	return &App{
		Creds: claude.NewCredentialStore(env, nil),
		Env:   env,
		Store: store.New(env.StoreDir()),
		Now:   func() time.Time { return testNow },
	}
}

// fakeCreds injects read/write failures that the file store cannot produce.
type fakeCreds struct {
	raw      []byte
	readErr  error
	writeErr error
}

func (f *fakeCreds) Read() ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.raw == nil {
		return nil, claude.ErrNotLoggedIn
	}
	return f.raw, nil
}

func (f *fakeCreds) Write(raw []byte) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.raw = raw
	return nil
}

func (f *fakeCreds) Location() string { return "fake credentials" }

func credsJSON(tag string, expires, refreshExpires time.Time) []byte {
	return fmt.Appendf(nil,
		`{"claudeAiOauth":{"accessToken":"sk-test-%s","refreshToken":"rt-test-%s","expiresAt":%d,"refreshTokenExpiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max"}}`,
		tag, tag, expires.UnixMilli(), refreshExpires.UnixMilli())
}

func profileJSON(uuid, email string) []byte {
	return fmt.Appendf(nil, `{"accountUuid":%q,"emailAddress":%q}`, uuid, email)
}

func writeLiveCreds(t *testing.T, a *App, raw []byte) {
	t.Helper()
	path := a.Env.CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLiveConfig writes a claude config holding the given oauthAccount plus
// unrelated keys that switching must leave intact.
func writeLiveConfig(t *testing.T, a *App, rawProfile []byte) {
	t.Helper()
	cfg := fmt.Appendf(nil, `{"numStartups": 42, "oauthAccount": %s, "theme": "dark-daltonized"}`, rawProfile)
	if err := os.WriteFile(a.Env.ConfigPath(), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

func saveState(t *testing.T, a *App, st store.State) {
	t.Helper()
	if err := a.Store.SaveState(st); err != nil {
		t.Fatal(err)
	}
}

func loadState(t *testing.T, a *App) store.State {
	t.Helper()
	st, err := a.Store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func writeSnapshot(t *testing.T, a *App, uuid string, raw []byte) {
	t.Helper()
	if err := a.Store.WriteSnapshot(uuid, raw); err != nil {
		t.Fatal(err)
	}
}

func writeProfile(t *testing.T, a *App, uuid string, raw []byte) {
	t.Helper()
	if err := a.Store.WriteProfile(uuid, raw); err != nil {
		t.Fatal(err)
	}
}

func readSnapshot(t *testing.T, a *App, uuid string) []byte {
	t.Helper()
	raw, err := a.Store.ReadSnapshot(uuid)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readLiveCreds(t *testing.T, a *App) []byte {
	t.Helper()
	raw, err := a.Creds.Read()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func twoAccountState() store.State {
	return store.State{
		Active: "uuid-a",
		Accounts: []store.Account{
			{UUID: "uuid-a", Email: "a@x.com", Alias: "work", AddedAt: testNow},
			{UUID: "uuid-b", Email: "b@x.com", AddedAt: testNow},
		},
	}
}

func TestRotateTarget(t *testing.T) {
	accounts := twoAccountState().Accounts
	tests := []struct {
		name     string
		st       store.State
		want     string // target uuid; empty means an error is expected
		errNeeds string
	}{
		{"no accounts", store.State{}, "", "no accounts"},
		{"single account", store.State{Accounts: accounts[:1]}, "", "only one account"},
		{"next after active", store.State{Active: "uuid-a", Accounts: accounts}, "uuid-b", ""},
		{"wraps around", store.State{Active: "uuid-b", Accounts: accounts}, "uuid-a", ""},
		{"active unset starts at first", store.State{Accounts: accounts}, "uuid-a", ""},
		{"active gone starts at first", store.State{Active: "uuid-ghost", Accounts: accounts}, "uuid-a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RotateTarget(tt.st)
			if tt.errNeeds != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errNeeds) {
					t.Errorf("error = %v, want mention of %q", err, tt.errNeeds)
				}
				return
			}
			if err != nil {
				t.Fatalf("RotateTarget: %v", err)
			}
			if got.UUID != tt.want {
				t.Errorf("target = %s, want %s", got.UUID, tt.want)
			}
		})
	}
}

func TestResolveAccount(t *testing.T) {
	st := twoAccountState()
	tests := []struct {
		arg  string
		want string // uuid; empty means an error is expected
	}{
		{"1", "uuid-a"},
		{"2", "uuid-b"},
		{"0", ""},
		{"3", ""},
		{"-1", ""},
		{"a@x.com", "uuid-a"},
		{"work", "uuid-a"},
		{"uuid-b", "uuid-b"},
		{"nope", ""},
	}
	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			got, err := ResolveAccount(st, tt.arg)
			if tt.want == "" {
				if err == nil {
					t.Errorf("ResolveAccount(%q) = %+v, want error", tt.arg, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAccount(%q): %v", tt.arg, err)
			}
			if got.UUID != tt.want {
				t.Errorf("ResolveAccount(%q) = %s, want %s", tt.arg, got.UUID, tt.want)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	a := newTestApp(t)
	saveState(t, a, twoAccountState())
	writeSnapshot(t, a, "uuid-a", credsJSON("a", freshExpiry, refreshOK))

	if err := a.Remove("uuid-a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	st := loadState(t, a)
	if st.IndexByUUID("uuid-a") != -1 || len(st.Accounts) != 1 {
		t.Errorf("account still present: %+v", st.Accounts)
	}
	if st.Active != "" {
		t.Errorf("Active = %q, want cleared after removing the active account", st.Active)
	}
	if _, err := a.Store.ReadSnapshot("uuid-a"); err == nil {
		t.Error("snapshot survived Remove")
	}

	if err := a.Remove("uuid-ghost"); err == nil {
		t.Error("removing an unknown uuid must fail")
	}
}

func TestSetAlias(t *testing.T) {
	a := newTestApp(t)
	saveState(t, a, twoAccountState())

	if err := a.SetAlias("uuid-b", "personal"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}
	if st := loadState(t, a); st.Accounts[1].Alias != "personal" {
		t.Errorf("alias = %q, want personal", st.Accounts[1].Alias)
	}

	// Re-setting an account's own alias is not a duplicate.
	if err := a.SetAlias("uuid-b", "personal"); err != nil {
		t.Errorf("re-setting own alias: %v", err)
	}
	if err := a.SetAlias("uuid-b", "work"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("duplicate alias error = %v", err)
	}
	if err := a.SetAlias("uuid-b", "42"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("numeric alias error = %v", err)
	}
	if err := a.SetAlias("uuid-b", "two words"); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("whitespace alias error = %v", err)
	}
	if err := a.SetAlias("uuid-ghost", "x"); err == nil {
		t.Error("aliasing an unknown uuid must fail")
	}

	if err := a.SetAlias("uuid-b", ""); err != nil {
		t.Fatalf("clearing alias: %v", err)
	}
	if st := loadState(t, a); st.Accounts[1].Alias != "" {
		t.Errorf("alias = %q, want cleared", st.Accounts[1].Alias)
	}
}

func TestErrUnsavedLoginIsSentinel(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", ErrUnsavedLogin)
	if !errors.Is(wrapped, ErrUnsavedLogin) {
		t.Error("ErrUnsavedLogin must survive wrapping")
	}
}
