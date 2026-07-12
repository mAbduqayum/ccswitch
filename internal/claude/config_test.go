package claude

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

var newProfile = json.RawMessage(`{
  "accountUuid": "new-uuid-9999",
  "emailAddress": "new@example.com",
  "organizationUuid": "org-9999",
  "displayName": "New User",
  "billingType": "stripe_subscription",
  "organizationRole": "member",
  "profileFetchedAt": 1752400000000
}`)

// compact normalizes whitespace without reparsing values, so token bytes
// (number literals, escapes) must match exactly.
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.String()
}

func copyFixture(t *testing.T, name string, perm os.FileMode) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPatchOAuthAccountPreservesEverythingElse(t *testing.T) {
	fixtures := []string{"config_basic.json", "config_huge.json", "config_no_oauth.json"}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			path := copyFixture(t, name, 0o600)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var in map[string]json.RawMessage
			if err := json.Unmarshal(before, &in); err != nil {
				t.Fatal(err)
			}

			if err := PatchOAuthAccount(path, newProfile); err != nil {
				t.Fatalf("PatchOAuthAccount: %v", err)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]json.RawMessage
			if err := json.Unmarshal(after, &out); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}

			if got, want := len(out), lenWithOauth(in); got != want {
				t.Errorf("key count = %d, want %d", got, want)
			}
			for k, v := range in {
				if k == "oauthAccount" {
					continue
				}
				got, ok := out[k]
				if !ok {
					t.Errorf("key %q lost", k)
					continue
				}
				if compact(t, got) != compact(t, v) {
					t.Errorf("key %q changed:\n got %s\nwant %s", k, got, v)
				}
			}
			if compact(t, out["oauthAccount"]) != compact(t, newProfile) {
				t.Errorf("oauthAccount not replaced:\n%s", out["oauthAccount"])
			}
			if bytes.HasSuffix(after, []byte("\n")) {
				t.Error("output has a trailing newline; Claude Code writes none")
			}
		})
	}
}

func lenWithOauth(in map[string]json.RawMessage) int {
	if _, ok := in["oauthAccount"]; ok {
		return len(in)
	}
	return len(in) + 1
}

func TestPatchOAuthAccountGolden(t *testing.T) {
	path := copyFixture(t, "config_basic.json", 0o600)
	if err := PatchOAuthAccount(path, newProfile); err != nil {
		t.Fatalf("PatchOAuthAccount: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "config_basic.golden")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s:\n%s", golden, got)
	}
}

func TestPatchOAuthAccountPreservesMode(t *testing.T) {
	for _, perm := range []os.FileMode{0o600, 0o644} {
		path := copyFixture(t, "config_basic.json", perm)
		if err := PatchOAuthAccount(path, newProfile); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != perm {
			t.Errorf("mode = %o, want %o", got, perm)
		}
	}
}

func TestPatchOAuthAccountErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		err := PatchOAuthAccount(filepath.Join(t.TempDir(), "nope.json"), newProfile)
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("malformed config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(path, []byte(`{"a":`), 0o600); err != nil {
			t.Fatal(err)
		}
		err := PatchOAuthAccount(path, newProfile)
		if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
			t.Errorf("error = %v, want mention of invalid JSON", err)
		}
		// The malformed original must be untouched — no partial writes.
		got, _ := os.ReadFile(path)
		if string(got) != `{"a":` {
			t.Error("malformed config was modified")
		}
	})
}

func TestReadOAuthAccount(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		raw, p, err := ReadOAuthAccount(filepath.Join(t.TempDir(), "nope.json"))
		if err != nil || raw != nil || p != (Profile{}) {
			t.Errorf("got %s, %+v, %v; want nil, zero, nil", raw, p, err)
		}
	})
	t.Run("no oauthAccount key", func(t *testing.T) {
		path := copyFixture(t, "config_no_oauth.json", 0o600)
		raw, _, err := ReadOAuthAccount(path)
		if err != nil || raw != nil {
			t.Errorf("got %s, %v; want nil raw, nil err", raw, err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		path := copyFixture(t, "config_basic.json", 0o600)
		raw, p, err := ReadOAuthAccount(path)
		if err != nil {
			t.Fatal(err)
		}
		if raw == nil {
			t.Fatal("raw is nil")
		}
		if p.AccountUUID != "old-uuid-1111" || p.EmailAddress != "old@example.com" {
			t.Errorf("profile = %+v", p)
		}
	})
	t.Run("malformed file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(path, []byte(`nope`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadOAuthAccount(path); err == nil {
			t.Error("want error for malformed config")
		}
	})
}
