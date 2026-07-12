package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Lock takes an exclusive, non-blocking flock on <dir>/lock, serializing
// mutating operations across ccswitch processes. The caller must invoke the
// returned unlock. Read-only paths don't lock: every write in the store is
// an atomic rename, so readers never observe partial files.
func (s *Store) Lock() (unlock func(), err error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another ccswitch instance is modifying %s — try again", s.dir)
		}
		return nil, fmt.Errorf("lock store: %w", err)
	}
	return func() {
		// Closing the fd releases the flock; the explicit unlock just makes
		// the release immediate and intentional.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
