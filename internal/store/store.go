// Package store persists ccswitch's own state under the user data
// directory: the account registry (state.json) plus raw per-account
// snapshots of Claude Code's credentials and profile. Everything is written
// atomically with private permissions (0700 dirs, 0600 files).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

type Store struct{ dir string }

// New returns a store rooted at dir (normally Env.StoreDir(); tests pass a
// temp dir). Nothing is created until Init or the first write.
func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) Dir() string { return s.dir }

// Init ensures the store layout exists with private permissions.
func (s *Store) Init() error {
	if err := atomicio.MkdirPrivate(s.dir); err != nil {
		return err
	}
	return atomicio.MkdirPrivate(s.accountsDir())
}

func (s *Store) statePath() string   { return filepath.Join(s.dir, "state.json") }
func (s *Store) accountsDir() string { return filepath.Join(s.dir, "accounts") }

// LoadState returns an empty v1 state when no state file exists yet.
func (s *Store) LoadState() (State, error) {
	data, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return State{Version: stateVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("%s is corrupt: %w", s.statePath(), err)
	}
	if st.Version > stateVersion {
		return State{}, fmt.Errorf("%s is schema version %d, but this ccswitch understands only version %d — upgrade ccswitch",
			s.statePath(), st.Version, stateVersion)
	}
	return st, nil
}

// SaveState writes the state atomically, stamping the current schema version.
func (s *Store) SaveState(st State) error {
	st.Version = stateVersion
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	out = append(out, '\n')
	if err := s.Init(); err != nil {
		return err
	}
	if err := atomicio.WriteFile(s.statePath(), out, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// checkUUID rejects UUIDs that cannot be used as a path element, guarding
// against a corrupt state file turning into path traversal.
func checkUUID(uuid string) error {
	if uuid == "" || uuid == "." || uuid == ".." ||
		strings.ContainsAny(uuid, `/\`) {
		return fmt.Errorf("invalid account uuid %q", uuid)
	}
	return nil
}
