// Package atomicio provides crash-safe filesystem primitives: atomic file
// writes with explicit permissions and private directory creation.
package atomicio

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile writes data to path atomically: a temp file in the same
// directory is written, synced, and renamed over path. perm is applied with
// an explicit Chmod so the process umask cannot alter it. The parent
// directory must already exist. On any error the temp file is removed and
// the destination is left untouched.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			_ = os.Remove(tmp) // best-effort cleanup; the write error wins
		}
	}()
	if err = f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err = os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	// Sync the directory so the rename itself survives power loss;
	// best-effort, since not every filesystem supports fsync on a dir.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// MkdirPrivate ensures dir exists with mode 0700, creating parents as
// needed. An existing directory with different permissions is tightened.
func MkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", dir, err)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}
