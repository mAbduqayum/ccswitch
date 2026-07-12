package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

func (s *Store) accountDir(uuid string) string {
	return filepath.Join(s.accountsDir(), uuid)
}

// ReadSnapshot returns the raw credentials snapshot for the account. A
// missing snapshot keeps fs.ErrNotExist in the error chain.
func (s *Store) ReadSnapshot(uuid string) ([]byte, error) {
	if err := checkUUID(uuid); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(s.accountDir(uuid), "credentials.json"))
	if err != nil {
		return nil, fmt.Errorf("read credentials snapshot for %s: %w", uuid, err)
	}
	return raw, nil
}

// WriteSnapshot stores the raw credentials snapshot for the account.
func (s *Store) WriteSnapshot(uuid string, raw []byte) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	dir := s.accountDir(uuid)
	if err := atomicio.MkdirPrivate(dir); err != nil {
		return err
	}
	if err := atomicio.WriteFile(filepath.Join(dir, "credentials.json"), raw, 0o600); err != nil {
		return fmt.Errorf("write credentials snapshot for %s: %w", uuid, err)
	}
	return nil
}

// ReadProfile returns the stored oauthAccount snapshot, or nil when none was
// captured — profiles are best-effort, so absence is not an error.
func (s *Store) ReadProfile(uuid string) (json.RawMessage, error) {
	if err := checkUUID(uuid); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(s.accountDir(uuid), "profile.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profile snapshot for %s: %w", uuid, err)
	}
	return raw, nil
}

// WriteProfile stores the raw oauthAccount snapshot for the account.
func (s *Store) WriteProfile(uuid string, raw json.RawMessage) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	dir := s.accountDir(uuid)
	if err := atomicio.MkdirPrivate(dir); err != nil {
		return err
	}
	if err := atomicio.WriteFile(filepath.Join(dir, "profile.json"), raw, 0o600); err != nil {
		return fmt.Errorf("write profile snapshot for %s: %w", uuid, err)
	}
	return nil
}

// RemoveAccount deletes the account's snapshot directory.
func (s *Store) RemoveAccount(uuid string) error {
	if err := checkUUID(uuid); err != nil {
		return err
	}
	if err := os.RemoveAll(s.accountDir(uuid)); err != nil {
		return fmt.Errorf("remove snapshots for %s: %w", uuid, err)
	}
	return nil
}

// OrphanDirs lists snapshot directories that no state entry references —
// doctor reports them.
func (s *Store) OrphanDirs(st State) ([]string, error) {
	entries, err := os.ReadDir(s.accountsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	var orphans []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if st.IndexByUUID(e.Name()) == -1 {
			orphans = append(orphans, e.Name())
		}
	}
	return orphans, nil
}
