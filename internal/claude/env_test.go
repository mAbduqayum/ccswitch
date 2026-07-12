package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPath(t *testing.T) {
	tests := []struct {
		name   string
		nested string // content of ~/.claude/.claude.json, "" = absent
		cfgDir bool   // set ClaudeConfigDir
		want   func(home, cfgDir string) string
	}{
		{
			name:   "CLAUDE_CONFIG_DIR wins over everything",
			nested: `{"oauthAccount":{}}`,
			cfgDir: true,
			want:   func(_, cfgDir string) string { return filepath.Join(cfgDir, ".claude.json") },
		},
		{
			name:   "nested config with oauthAccount",
			nested: `{"oauthAccount":{"emailAddress":"a@b.c"}}`,
			want:   func(home, _ string) string { return filepath.Join(home, ".claude", ".claude.json") },
		},
		{
			name:   "nested config without oauthAccount falls through",
			nested: `{"numStartups":3}`,
			want:   func(home, _ string) string { return filepath.Join(home, ".claude.json") },
		},
		{
			name:   "malformed nested config falls through",
			nested: `{"oauthAccount":`,
			want:   func(home, _ string) string { return filepath.Join(home, ".claude.json") },
		},
		{
			name: "no nested config",
			want: func(home, _ string) string { return filepath.Join(home, ".claude.json") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			env := Env{Home: home}
			if tt.nested != "" {
				dir := filepath.Join(home, ".claude")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(tt.nested), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfgDir := ""
			if tt.cfgDir {
				cfgDir = t.TempDir()
				env.ClaudeConfigDir = cfgDir
			}
			if got, want := env.ConfigPath(), tt.want(home, cfgDir); got != want {
				t.Errorf("ConfigPath() = %s, want %s", got, want)
			}
		})
	}
}

func TestCredentialsPath(t *testing.T) {
	env := Env{Home: "/h"}
	if got, want := env.CredentialsPath(), filepath.Join("/h", ".claude", ".credentials.json"); got != want {
		t.Errorf("CredentialsPath() = %s, want %s", got, want)
	}
}

func TestStoreDir(t *testing.T) {
	if got, want := (Env{Home: "/h"}).StoreDir(), "/h/.local/share/ccswitch"; got != want {
		t.Errorf("StoreDir() = %s, want %s", got, want)
	}
	if got, want := (Env{Home: "/h", XDGDataHome: "/xdg"}).StoreDir(), "/xdg/ccswitch"; got != want {
		t.Errorf("StoreDir() with XDG = %s, want %s", got, want)
	}
}
