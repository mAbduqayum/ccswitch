package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

func seedUnknownLogin(t *testing.T, a *app.App) {
	t.Helper()
	path := a.Env.CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, credsJSON("n", freshExpiry, refreshOK), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Appendf(nil, `{"oauthAccount": %s}`, profileJSON("uuid-n", "n@x.com"))
	if err := os.WriteFile(a.Env.ConfigPath(), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

// drive feeds msg into the model and synchronously executes every command
// it produces (including batches), feeding the results back until the model
// settles. Update-level testing without a running program.
func drive(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		if batch, ok := next.(tea.BatchMsg); ok {
			for _, cmd := range batch {
				if cmd != nil {
					queue = append(queue, cmd())
				}
			}
			continue
		}
		updated, cmd := m.Update(next)
		m = updated.(Model)
		if cmd != nil {
			queue = append(queue, cmd())
		}
	}
	return m
}

func loaded(t *testing.T, a *app.App) Model {
	t.Helper()
	m := New(a)
	return drive(t, m, m.loadCmd()())
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestLoadBuildsRows(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	m := loaded(t, a)
	if len(m.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(m.rows))
	}
	view := m.View()
	for _, want := range []string{"a@x.com", "b@x.com", "work", "▶", "max", "ok"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestDiscoverUnknownOpensConfirmAdd(t *testing.T) {
	a := newTestApp(t)
	seedUnknownLogin(t, a)
	m := New(a)
	m = drive(t, m, m.discoverCmd()())
	if m.mode != modeConfirmAdd {
		t.Fatalf("mode = %v, want confirmAdd", m.mode)
	}
	if view := m.View(); !strings.Contains(view, "n@x.com") || !strings.Contains(view, "[y/N]") {
		t.Errorf("confirm dialog missing:\n%s", view)
	}
}

func TestConfirmAdd(t *testing.T) {
	registered := func(t *testing.T, a *app.App) bool {
		t.Helper()
		st, err := a.Store.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st.IndexByUUID("uuid-n") != -1
	}

	t.Run("accepted", func(t *testing.T) {
		a := newTestApp(t)
		seedUnknownLogin(t, a)
		m := New(a)
		m = drive(t, m, m.discoverCmd()())
		m = drive(t, m, keyMsg("y"))
		if m.mode != modeList || !strings.Contains(m.status, "added n@x.com") {
			t.Errorf("mode = %v, status = %q", m.mode, m.status)
		}
		if !registered(t, a) {
			t.Error("account was not registered")
		}
		if view := m.View(); !strings.Contains(view, "n@x.com") {
			t.Errorf("added account missing from the list:\n%s", view)
		}
	})
	t.Run("declined", func(t *testing.T) {
		a := newTestApp(t)
		seedUnknownLogin(t, a)
		m := New(a)
		m = drive(t, m, m.discoverCmd()())
		m = drive(t, m, keyMsg("n"))
		if m.mode != modeList {
			t.Errorf("mode = %v, want list", m.mode)
		}
		if registered(t, a) {
			t.Error("account registered despite declining")
		}
	})
}

func TestEnterSwitchesToSelected(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	m := loaded(t, a)
	m.table.SetCursor(1)
	m = drive(t, m, keyMsg("enter"))
	if !strings.Contains(m.status, "switched to b@x.com") {
		t.Errorf("status = %q", m.status)
	}
	st, err := a.Store.LoadState()
	if err != nil || st.Active != "uuid-b" {
		t.Errorf("Active = %q, %v", st.Active, err)
	}
	live, err := a.Creds.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(live, credsJSON("b", staleExpiry, refreshOK)) {
		t.Error("live credentials are not b's snapshot")
	}
	if row, ok := m.selected(); !ok || !m.rows[1].active || row.account.UUID != "uuid-b" {
		t.Error("table does not show b as active after the reload")
	}
}

func TestSwitchWithUnsavedLoginExplains(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	seedUnknownLogin(t, a)
	m := loaded(t, a)
	m.table.SetCursor(1)
	m = drive(t, m, keyMsg("enter"))
	if !strings.Contains(m.status, "--force") {
		t.Errorf("status = %q, want pointer to --force", m.status)
	}
	st, err := a.Store.LoadState()
	if err != nil || st.Active != "uuid-a" {
		t.Errorf("Active = %q, want untouched uuid-a", st.Active)
	}
}

func TestRemoveFlow(t *testing.T) {
	present := func(t *testing.T, a *app.App) bool {
		t.Helper()
		st, err := a.Store.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st.IndexByUUID("uuid-b") != -1
	}

	t.Run("confirmed", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		m.table.SetCursor(1)
		m = drive(t, m, keyMsg("d"))
		if m.mode != modeConfirmRemove {
			t.Fatalf("mode = %v, want confirmRemove", m.mode)
		}
		if view := m.View(); !strings.Contains(view, "Remove b@x.com") {
			t.Errorf("dialog missing:\n%s", view)
		}
		m = drive(t, m, keyMsg("y"))
		if !strings.Contains(m.status, "removed b@x.com") || present(t, a) {
			t.Errorf("status = %q, present = %v", m.status, present(t, a))
		}
		if len(m.rows) != 1 {
			t.Errorf("rows = %d after removal", len(m.rows))
		}
	})
	t.Run("declined", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		m.table.SetCursor(1)
		m = drive(t, m, keyMsg("d"))
		m = drive(t, m, keyMsg("n"))
		if m.mode != modeList || !present(t, a) {
			t.Errorf("mode = %v, present = %v", m.mode, present(t, a))
		}
	})
}

func TestRenameFlow(t *testing.T) {
	alias := func(t *testing.T, a *app.App) string {
		t.Helper()
		st, err := a.Store.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		return st.Accounts[1].Alias
	}

	t.Run("commit", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		m.table.SetCursor(1)
		m = drive(t, m, keyMsg("r"))
		if m.mode != modeRename {
			t.Fatalf("mode = %v, want rename", m.mode)
		}
		m = drive(t, m, keyMsg("personal"))
		m = drive(t, m, keyMsg("enter"))
		if got := alias(t, a); got != "personal" {
			t.Errorf("alias = %q, want personal", got)
		}
		if !strings.Contains(m.status, "personal") {
			t.Errorf("status = %q", m.status)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		m.table.SetCursor(1)
		m = drive(t, m, keyMsg("r"))
		m = drive(t, m, keyMsg("x"))
		m = drive(t, m, keyMsg("esc"))
		if m.mode != modeList {
			t.Errorf("mode = %v, want list", m.mode)
		}
		if got := alias(t, a); got != "" {
			t.Errorf("alias = %q, want unchanged", got)
		}
	})
	t.Run("r types into the input instead of reopening", func(t *testing.T) {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		m.table.SetCursor(1)
		m = drive(t, m, keyMsg("r"))
		m = drive(t, m, keyMsg("r"))
		m = drive(t, m, keyMsg("enter"))
		if got := alias(t, a); got != "r" {
			t.Errorf("alias = %q, want the typed r", got)
		}
	})
}

func TestCredsChangedTriggersRediscovery(t *testing.T) {
	a := newTestApp(t)
	seedUnknownLogin(t, a)
	m := New(a)
	m = drive(t, m, credsChangedMsg{})
	if m.mode != modeConfirmAdd {
		t.Errorf("mode = %v, want confirmAdd after rediscovery", m.mode)
	}
}

func TestWatcherFailureIsANote(t *testing.T) {
	a := newTestApp(t)
	m := New(a)
	m = drive(t, m, watchStartedMsg{err: errors.New("inotify limit")})
	if !strings.Contains(m.watchNote, "watch off") {
		t.Errorf("watchNote = %q", m.watchNote)
	}
	if !strings.Contains(m.View(), "watch off") {
		t.Error("watch note not shown")
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		a := newTestApp(t)
		seedTwoAccounts(t, a)
		m := loaded(t, a)
		_, cmd := m.Update(keyMsg(k))
		if cmd == nil {
			t.Fatalf("%s produced no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%s did not quit", k)
		}
	}
}

// TestViewNeverLeaksTokens renders every mode against a fully populated
// world and sweeps for the sentinel token prefixes.
func TestViewNeverLeaksTokens(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	seedUnknownLogin(t, a)
	m := New(a)
	m = drive(t, m, m.discoverCmd()()) // confirmAdd over a loaded table
	views := []string{m.View()}
	m2 := drive(t, m, keyMsg("y")) // list with status
	views = append(views, m2.View())
	m3 := drive(t, m2, keyMsg("d"))
	views = append(views, m3.View())
	for _, view := range views {
		for _, needle := range []string{"sk-test-", "rt-test-"} {
			if strings.Contains(view, needle) {
				t.Errorf("view leaks a token value (%s):\n%s", needle, view)
			}
		}
	}
}
