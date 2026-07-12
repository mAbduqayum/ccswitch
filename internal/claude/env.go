// Package claude locates and manipulates Claude Code's on-disk account
// state: the live OAuth credentials and the oauthAccount profile inside its
// config JSON. It makes no network calls and never exposes token values.
package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Env carries every external input that path resolution and platform
// selection depend on, so tests can point the whole package at a temp
// directory.
type Env struct {
	Home            string // user home directory
	ClaudeConfigDir string // $CLAUDE_CONFIG_DIR, may be empty
	XDGDataHome     string // $XDG_DATA_HOME, may be empty
	User            string // keychain account name on darwin
	GOOS            string // platform selection, normally runtime.GOOS
}

// RealEnv builds an Env from the process environment.
func RealEnv() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("resolve home directory: %w", err)
	}
	return Env{
		Home:            home,
		ClaudeConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"),
		XDGDataHome:     os.Getenv("XDG_DATA_HOME"),
		User:            os.Getenv("USER"),
		GOOS:            runtime.GOOS,
	}, nil
}

// CredentialsPath is the live credentials file Claude Code reads and writes.
func (e Env) CredentialsPath() string {
	return filepath.Join(e.Home, ".claude", ".credentials.json")
}

// ConfigPath resolves Claude Code's config JSON:
//  1. $CLAUDE_CONFIG_DIR/.claude.json when the variable is set
//  2. ~/.claude/.claude.json when it exists and contains an oauthAccount key
//  3. ~/.claude.json otherwise
func (e Env) ConfigPath() string {
	if e.ClaudeConfigDir != "" {
		return filepath.Join(e.ClaudeConfigDir, ".claude.json")
	}
	nested := filepath.Join(e.Home, ".claude", ".claude.json")
	if raw, err := os.ReadFile(nested); err == nil {
		var top map[string]json.RawMessage
		if json.Unmarshal(raw, &top) == nil {
			if _, ok := top["oauthAccount"]; ok {
				return nested
			}
		}
	}
	return filepath.Join(e.Home, ".claude.json")
}

// StoreDir is ccswitch's own data directory.
func (e Env) StoreDir() string {
	base := e.XDGDataHome
	if base == "" {
		base = filepath.Join(e.Home, ".local", "share")
	}
	return filepath.Join(base, "ccswitch")
}
