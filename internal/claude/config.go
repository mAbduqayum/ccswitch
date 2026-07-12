package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

// Profile is the identity subset of the oauthAccount object.
type Profile struct {
	AccountUUID  string `json:"accountUuid"`
	EmailAddress string `json:"emailAddress"`
}

// ReadOAuthAccount returns the raw oauthAccount value from the config JSON
// plus its parsed identity. raw is nil when the file or the key is absent —
// that is not an error, Claude Code simply hasn't written a profile yet.
func ReadOAuthAccount(configPath string) (json.RawMessage, Profile, error) {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Profile{}, nil
	}
	if err != nil {
		return nil, Profile{}, fmt.Errorf("read claude config: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, Profile{}, fmt.Errorf("claude config %s is not valid JSON: %w", configPath, err)
	}
	raw, ok := top["oauthAccount"]
	if !ok || string(raw) == "null" {
		return nil, Profile{}, nil
	}
	var p Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, Profile{}, fmt.Errorf("claude config %s: oauthAccount: %w", configPath, err)
	}
	return raw, p, nil
}

// PatchOAuthAccount replaces only the oauthAccount key in the config JSON,
// preserving every other top-level value verbatim (whitespace aside) along
// with the file's permissions. Claude Code's config is large and holds
// unrelated per-project state — this function must never touch it.
func PatchOAuthAccount(configPath string, profile json.RawMessage) error {
	if len(profile) == 0 || string(profile) == "null" {
		return fmt.Errorf("refusing to write an empty oauthAccount to %s", configPath)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read claude config: %w", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return fmt.Errorf("stat claude config: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("claude config %s is not valid JSON: %w", configPath, err)
	}
	if top == nil { // the file contained JSON `null`
		return fmt.Errorf("claude config %s is not a JSON object", configPath)
	}
	top["oauthAccount"] = profile
	// 2-space indent, no trailing newline — the format Claude Code itself
	// writes. Values are json.RawMessage, so number literals, unicode, and
	// nested key order pass through untouched; only top-level key order
	// normalizes (alphabetical), which Claude Code does not depend on.
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("encode claude config: %w", err)
	}
	if err := atomicio.WriteFile(configPath, out, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write claude config: %w", err)
	}
	return nil
}
