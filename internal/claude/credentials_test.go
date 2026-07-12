package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sentinelCreds = `{
  "claudeAiOauth": {
    "accessToken": "SENTINEL-ACCESS-TOKEN",
    "refreshToken": "SENTINEL-REFRESH-TOKEN",
    "expiresAt": 1752300000000,
    "refreshTokenExpiresAt": 1783800000000,
    "rateLimitTier": "default",
    "scopes": ["user:inference", "user:profile"],
    "subscriptionType": "max"
  }
}`

func TestParseCredentials(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid", sentinelCreds, false},
		{"invalid json", `{"claudeAiOauth":`, true},
		{"missing claudeAiOauth", `{"other": {}}`, true},
		{"null claudeAiOauth", `{"claudeAiOauth": null}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, err := ParseCredentials([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCredentials: %v", err)
			}
			if meta.ExpiresAt != 1752300000000 {
				t.Errorf("ExpiresAt = %d", meta.ExpiresAt)
			}
			if meta.SubscriptionType != "max" {
				t.Errorf("SubscriptionType = %q", meta.SubscriptionType)
			}
			if len(meta.Scopes) != 2 {
				t.Errorf("Scopes = %v", meta.Scopes)
			}
		})
	}
}

// TestNoTokenLeak proves that no exported representation of parsed
// credentials can reproduce a token value.
func TestNoTokenLeak(t *testing.T) {
	meta, err := ParseCredentials([]byte(sentinelCreds))
	if err != nil {
		t.Fatal(err)
	}
	reprs := []string{
		fmt.Sprintf("%v", meta),
		fmt.Sprintf("%+v", meta),
		fmt.Sprintf("%#v", meta),
	}
	if out, err := json.Marshal(meta); err == nil {
		reprs = append(reprs, string(out))
	}
	for _, r := range reprs {
		if strings.Contains(r, "SENTINEL") {
			t.Errorf("token leaked into %q", r)
		}
	}
}

func TestHasTokens(t *testing.T) {
	access, refresh := HasTokens([]byte(sentinelCreds))
	if !access || !refresh {
		t.Errorf("HasTokens = %v, %v; want true, true", access, refresh)
	}
	access, refresh = HasTokens([]byte(`{"claudeAiOauth":{"accessToken":"x"}}`))
	if !access || refresh {
		t.Errorf("HasTokens = %v, %v; want true, false", access, refresh)
	}
	access, refresh = HasTokens([]byte(`not json`))
	if access || refresh {
		t.Errorf("HasTokens on garbage = %v, %v; want false, false", access, refresh)
	}
}

func TestFileStoreReadMissing(t *testing.T) {
	env := Env{Home: t.TempDir(), GOOS: "linux"}
	cs := NewCredentialStore(env, nil)
	_, err := cs.Read()
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("Read() error = %v, want ErrNotLoggedIn", err)
	}
}

func TestFileStoreWriteRead(t *testing.T) {
	env := Env{Home: t.TempDir(), GOOS: "linux"}
	cs := NewCredentialStore(env, nil)
	if err := cs.Write([]byte(sentinelCreds)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := cs.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != sentinelCreds {
		t.Error("round-trip altered raw bytes")
	}
	info, err := os.Stat(env.CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials perm = %o, want 600", perm)
	}
}

func TestFileStoreWriteKeepsParentPerm(t *testing.T) {
	env := Env{Home: t.TempDir(), GOOS: "linux"}
	dir := filepath.Join(env.Home, ".claude")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := NewCredentialStore(env, nil).Write([]byte(sentinelCreds)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Errorf("~/.claude perm changed to %o; must stay 755", perm)
	}
}

func TestNewCredentialStorePlatform(t *testing.T) {
	home := t.TempDir()
	t.Run("linux uses file", func(t *testing.T) {
		cs := NewCredentialStore(Env{Home: home, GOOS: "linux"}, nil)
		if _, ok := cs.(*fileStore); !ok {
			t.Errorf("got %T, want *fileStore", cs)
		}
	})
	t.Run("darwin without file uses keychain", func(t *testing.T) {
		cs := NewCredentialStore(Env{Home: home, GOOS: "darwin"}, nil)
		if _, ok := cs.(*keychainStore); !ok {
			t.Errorf("got %T, want *keychainStore", cs)
		}
	})
	t.Run("darwin with plaintext file uses file", func(t *testing.T) {
		env := Env{Home: home, GOOS: "darwin"}
		if err := os.MkdirAll(filepath.Dir(env.CredentialsPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(env.CredentialsPath(), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		cs := NewCredentialStore(env, nil)
		if _, ok := cs.(*fileStore); !ok {
			t.Errorf("got %T, want *fileStore", cs)
		}
	})
}
