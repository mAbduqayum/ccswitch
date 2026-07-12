package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

// ErrNotLoggedIn reports that no live Claude Code credentials exist.
var ErrNotLoggedIn = errors.New("not logged in — run `claude /login` first")

// CredentialStore abstracts where the live credentials live: a file on
// Linux/WSL, the Keychain on macOS. Credentials pass through as opaque raw
// bytes — never re-marshaled, so unknown fields and formatting survive and
// token values stay out of reach.
type CredentialStore interface {
	// Read returns the raw live credentials. Absence wraps ErrNotLoggedIn.
	Read() ([]byte, error)
	// Write replaces the live credentials atomically.
	Write(raw []byte) error
	// Location describes where the credentials live, for doctor and errors.
	Location() string
}

// NewCredentialStore picks the platform implementation. On darwin the
// plaintext file wins when it exists; otherwise the Keychain is used. run is
// only consulted on darwin.
func NewCredentialStore(env Env, run ExecRunner) CredentialStore {
	path := env.CredentialsPath()
	if env.GOOS == "darwin" {
		if _, err := os.Stat(path); err != nil {
			return &keychainStore{run: run, account: env.User}
		}
	}
	return &fileStore{path: path}
}

type fileStore struct{ path string }

func (s *fileStore) Read() ([]byte, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", s.path, ErrNotLoggedIn)
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	return raw, nil
}

func (s *fileStore) Write(raw []byte) error {
	// Ensure the parent exists, but never alter an existing directory's
	// permissions — ~/.claude belongs to Claude Code, not to us.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	if err := atomicio.WriteFile(s.path, raw, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func (s *fileStore) Location() string { return s.path }

// CredentialMeta is the displayable subset of the live credentials. The
// token fields are deliberately absent so no exported type can leak them.
type CredentialMeta struct {
	ExpiresAt             int64    `json:"expiresAt"`             // epoch milliseconds
	RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"` // epoch milliseconds
	RateLimitTier         string   `json:"rateLimitTier"`
	Scopes                []string `json:"scopes"`
	SubscriptionType      string   `json:"subscriptionType"`
}

func (m CredentialMeta) AccessExpiry() time.Time  { return time.UnixMilli(m.ExpiresAt) }
func (m CredentialMeta) RefreshExpiry() time.Time { return time.UnixMilli(m.RefreshTokenExpiresAt) }

// ParseCredentials extracts displayable metadata from raw credentials JSON.
func ParseCredentials(raw []byte) (CredentialMeta, error) {
	var wrapper struct {
		ClaudeAiOauth *CredentialMeta `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return CredentialMeta{}, fmt.Errorf("credentials are not valid JSON: %w", err)
	}
	if wrapper.ClaudeAiOauth == nil {
		return CredentialMeta{}, errors.New("credentials JSON has no claudeAiOauth object")
	}
	return *wrapper.ClaudeAiOauth, nil
}

// HasTokens reports whether the access and refresh tokens are present and
// non-empty, without exposing their values.
func HasTokens(raw []byte) (access, refresh bool) {
	var wrapper struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &wrapper) != nil {
		return false, false
	}
	return wrapper.ClaudeAiOauth.AccessToken != "", wrapper.ClaudeAiOauth.RefreshToken != ""
}
