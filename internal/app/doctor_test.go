package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mAbduqayum/ccswitch/internal/store"
)

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, checks)
	return Check{}
}

func hasCheck(checks []Check, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDoctorFirstRunIsHealthy(t *testing.T) {
	a := newTestApp(t)
	checks := a.Doctor()
	if !Healthy(checks) {
		t.Errorf("first run must be healthy (warnings only): %+v", checks)
	}
	if c := findCheck(t, checks, "store"); c.Status != Warn {
		t.Errorf("store = %+v, want Warn about a missing dir", c)
	}
	if c := findCheck(t, checks, "live credentials"); c.Status != Warn {
		t.Errorf("live credentials = %+v, want not-logged-in Warn", c)
	}
	if c := findCheck(t, checks, "claude config"); c.Status != Warn {
		t.Errorf("claude config = %+v, want no-profile Warn", c)
	}
}

func TestDoctorAllHealthy(t *testing.T) {
	w := newSwitchWorld(t)
	checks := w.a.Doctor()
	for _, c := range checks {
		if c.Status != OK {
			t.Errorf("%s: %s (%s)", c.Name, c.Status, c.Detail)
		}
	}
	if !Healthy(checks) {
		t.Error("Healthy = false on a fully healthy setup")
	}
	if hasCheck(checks, "registration") {
		t.Error("registration warning present although the live account is managed")
	}
}

func TestDoctorAccountChecks(t *testing.T) {
	tests := []struct {
		name   string
		snap   []byte // nil = no snapshot at all
		want   CheckStatus
		detail string
	}{
		{"missing snapshot", nil, Fail, "no credentials snapshot"},
		{"malformed snapshot", []byte("junk"), Fail, "malformed"},
		{"missing tokens", []byte(`{"claudeAiOauth":{"expiresAt":1}}`), Fail, "missing token"},
		{"expired refresh token", credsJSON("a", staleExpiry, refreshExpired), Fail, "expired"},
		{"no refresh expiry", []byte(`{"claudeAiOauth":{"accessToken":"sk-test-x","refreshToken":"rt-test-x","expiresAt":1}}`), Warn, "health unknown"},
		{"refresh token expiring soon", credsJSON("a", staleExpiry, refreshSoon), Warn, "expires in"},
		{"healthy snapshot", credsJSON("a", staleExpiry, refreshOK), OK, "valid for 30d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			saveState(t, a, store.State{Accounts: []store.Account{{UUID: "uuid-a", Email: "a@x.com"}}})
			if tt.snap != nil {
				writeSnapshot(t, a, "uuid-a", tt.snap)
			}
			c := findCheck(t, a.Doctor(), "account a@x.com")
			if c.Status != tt.want || !strings.Contains(c.Detail, tt.detail) {
				t.Errorf("check = %+v, want status %v mentioning %q", c, tt.want, tt.detail)
			}
		})
	}
}

func TestDoctorDuplicates(t *testing.T) {
	tests := []struct {
		name     string
		accounts []store.Account
	}{
		{"duplicate uuid", []store.Account{{UUID: "uuid-a", Email: "a@x.com"}, {UUID: "uuid-a", Email: "b@x.com"}}},
		{"duplicate email", []store.Account{{UUID: "uuid-a", Email: "a@x.com"}, {UUID: "uuid-b", Email: "a@x.com"}}},
		{"duplicate alias", []store.Account{{UUID: "uuid-a", Email: "a@x.com", Alias: "w"}, {UUID: "uuid-b", Email: "b@x.com", Alias: "w"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newTestApp(t)
			saveState(t, a, store.State{Accounts: tt.accounts})
			if c := findCheck(t, a.Doctor(), "duplicates"); c.Status != Fail {
				t.Errorf("duplicates = %+v, want Fail", c)
			}
		})
	}

	t.Run("no duplicates emits no check", func(t *testing.T) {
		a := newTestApp(t)
		saveState(t, a, twoAccountState())
		if hasCheck(a.Doctor(), "duplicates") {
			t.Error("duplicates check present without duplicates")
		}
	})
}

func TestDoctorDanglingActiveMarker(t *testing.T) {
	a := newTestApp(t)
	saveState(t, a, store.State{Active: "uuid-gone", Accounts: []store.Account{{UUID: "uuid-a", Email: "a@x.com"}}})
	writeSnapshot(t, a, "uuid-a", credsJSON("a", staleExpiry, refreshOK))
	c := findCheck(t, a.Doctor(), "active marker")
	if c.Status != Warn || !strings.Contains(c.Detail, "uuid-gone") {
		t.Errorf("active marker = %+v, want Warn naming uuid-gone", c)
	}
}

func TestDoctorOrphans(t *testing.T) {
	a := newTestApp(t)
	saveState(t, a, store.State{})
	writeSnapshot(t, a, "uuid-ghost", credsJSON("g", freshExpiry, refreshOK))
	c := findCheck(t, a.Doctor(), "orphans")
	if c.Status != Warn || !strings.Contains(c.Detail, "uuid-ghost") {
		t.Errorf("orphans = %+v, want Warn naming uuid-ghost", c)
	}
}

func TestDoctorStorePermissions(t *testing.T) {
	a := newTestApp(t)
	saveState(t, a, store.State{})
	if err := os.Chmod(a.Store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	c := findCheck(t, a.Doctor(), "store")
	if c.Status != Fail || !strings.Contains(c.Detail, "755") {
		t.Errorf("store = %+v, want Fail naming mode 755", c)
	}
}

func TestDoctorCorruptStateStopsEarly(t *testing.T) {
	a := newTestApp(t)
	if err := os.MkdirAll(a.Store.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Store.Dir(), "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	checks := a.Doctor()
	if c := findCheck(t, checks, "state"); c.Status != Fail {
		t.Errorf("state = %+v, want Fail", c)
	}
	if len(checks) != 2 {
		t.Errorf("got %d checks, want doctor to stop after store+state: %+v", len(checks), checks)
	}
	if Healthy(checks) {
		t.Error("Healthy = true with a corrupt state")
	}
}

func TestDoctorLiveCredentials(t *testing.T) {
	t.Run("corrupt live credentials fail", func(t *testing.T) {
		a := newTestApp(t)
		writeLiveCreds(t, a, []byte("junk"))
		if c := findCheck(t, a.Doctor(), "live credentials"); c.Status != Fail {
			t.Errorf("live credentials = %+v, want Fail", c)
		}
	})
	t.Run("loose permissions warn", func(t *testing.T) {
		a := newTestApp(t)
		writeLiveCreds(t, a, credsJSON("a", freshExpiry, refreshOK))
		if err := os.Chmod(a.Env.CredentialsPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		c := findCheck(t, a.Doctor(), "credentials permissions")
		if c.Status != Warn || !strings.Contains(c.Detail, "644") {
			t.Errorf("credentials permissions = %+v, want Warn naming mode 644", c)
		}
	})
}

func TestDoctorUnregisteredLiveAccount(t *testing.T) {
	a := newTestApp(t)
	writeLiveCreds(t, a, credsJSON("s", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-stranger", "s@x.com"))
	c := findCheck(t, a.Doctor(), "registration")
	if c.Status != Warn || !strings.Contains(c.Detail, "s@x.com") {
		t.Errorf("registration = %+v, want Warn naming the account", c)
	}
}

// TestDoctorNeverLeaksTokens sweeps the full doctor output — text and JSON —
// for the sentinel token prefixes that every fixture in this world carries.
func TestDoctorNeverLeaksTokens(t *testing.T) {
	w := newSwitchWorld(t)
	// Make one snapshot malformed while still containing tokens, so even
	// error details are swept.
	writeSnapshot(t, w.a, "uuid-b", append(credsJSON("b", freshExpiry, refreshOK), '{'))

	checks := w.a.Doctor()
	out, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(out) + fmt.Sprintf("%+v", checks)
	for _, needle := range []string{"sk-test-", "rt-test-"} {
		if strings.Contains(blob, needle) {
			t.Errorf("doctor output leaks a token value (found %q)", needle)
		}
	}
}

func TestHealthy(t *testing.T) {
	if !Healthy(nil) {
		t.Error("Healthy(nil) = false")
	}
	if !Healthy([]Check{{Status: OK}, {Status: Warn}}) {
		t.Error("warnings must not make Healthy false")
	}
	if Healthy([]Check{{Status: OK}, {Status: Fail}}) {
		t.Error("a failure must make Healthy false")
	}
}

func TestCheckStatusMarshalsAsText(t *testing.T) {
	out, err := json.Marshal([]Check{{Name: "x", Status: Warn}, {Name: "y", Status: Fail}, {Name: "z", Status: OK}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"status":"warn"`, `"status":"fail"`, `"status":"ok"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("JSON %s missing %s", out, want)
		}
	}
}
