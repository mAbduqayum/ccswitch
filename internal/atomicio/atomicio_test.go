package atomicio_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

func TestWriteFilePermIsUmaskSafe(t *testing.T) {
	tests := []struct {
		name  string
		umask int
		perm  os.FileMode
	}{
		{"restrictive umask cannot widen", 0o077, 0o644},
		{"permissive umask cannot loosen", 0o000, 0o600},
		{"default umask", 0o022, 0o600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := syscall.Umask(tt.umask)
			defer syscall.Umask(old)

			path := filepath.Join(t.TempDir(), "f.json")
			if err := atomicio.WriteFile(path, []byte("data"), tt.perm); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if got := info.Mode().Perm(); got != tt.perm {
				t.Errorf("perm = %o, want %o", got, tt.perm)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "data" {
				t.Errorf("content = %q, %v; want %q", got, err, "data")
			}
		})
	}
}

func TestWriteFileOverwriteAppliesRequestedPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicio.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("perm = %o, want 600", got)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

func TestWriteFileRemovesTempOnError(t *testing.T) {
	dir := t.TempDir()
	// Renaming a file over an existing non-empty directory fails, forcing
	// the error path after the temp file was created.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicio.WriteFile(target, []byte("x"), 0o600); err == nil {
		t.Fatal("WriteFile onto a non-empty directory should fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestMkdirPrivate(t *testing.T) {
	t.Run("creates nested with 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b")
		if err := atomicio.MkdirPrivate(dir); err != nil {
			t.Fatalf("MkdirPrivate: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("perm = %o, want 700", got)
		}
	})
	t.Run("tightens existing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loose")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := atomicio.MkdirPrivate(dir); err != nil {
			t.Fatalf("MkdirPrivate: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("perm = %o, want 700", got)
		}
	})
}
