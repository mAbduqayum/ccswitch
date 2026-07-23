package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mAbduqayum/ccswitch/internal/update"
)

// cliReleaser is a hermetic update.Releaser: a fixed release plus a URL→bytes
// map. No network, no real filesystem.
type cliReleaser struct {
	rel   update.Release
	blobs map[string][]byte
}

func (c *cliReleaser) Latest(context.Context) (update.Release, error) { return c.rel, nil }

func (c *cliReleaser) Fetch(_ context.Context, url string) ([]byte, error) {
	b, ok := c.blobs[url]
	if !ok {
		return nil, fmt.Errorf("no blob for %s", url)
	}
	return b, nil
}

func makeCLIArchive(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name string
		data []byte
	}{
		{"LICENSE", []byte("MIT")},
		{"ccswitch", bin},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Fixed fixture coordinates for the hermetic update tests. Every Client below
// sets GOOS/GOARCH to these so the archive asset name matches.
const (
	testTag    = "v1.2.3"
	testGOOS   = "linux"
	testGOARCH = "amd64"
)

func fakeReleaser(t *testing.T, bin []byte) update.Releaser {
	t.Helper()
	version := strings.TrimPrefix(testTag, "v")
	name := fmt.Sprintf("ccswitch_%s_%s_%s.tar.gz", version, testGOOS, testGOARCH)
	archive := makeCLIArchive(t, bin)
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	return &cliReleaser{
		rel: update.Release{Tag: testTag, Assets: []update.Asset{
			{Name: name, URL: "u://archive"},
			{Name: "checksums.txt", URL: "u://sums"},
		}},
		blobs: map[string][]byte{"u://archive": archive, "u://sums": []byte(sums)},
	}
}

// runUpd drives the CLI with an injected updater and version, on a fresh
// temp-dir app.
func runUpd(t *testing.T, tty bool, stdin, version string, upd *update.Client, args ...string) (int, string, string) {
	t.Helper()
	a := newTestApp(t)
	var out, errBuf bytes.Buffer
	code := Execute(Options{
		Version: version,
		App:     a,
		Update:  upd,
		IO:      IO{In: strings.NewReader(stdin), Out: &out, Err: &errBuf, IsTTY: tty},
	}, args)
	return code, out.String(), errBuf.String()
}

func TestUpdateCheckUpToDate(t *testing.T) {
	upd := &update.Client{Releaser: fakeReleaser(t, []byte("X")), GOOS: "linux", GOARCH: "amd64"}
	code, out, stderr := runUpd(t, false, "", "1.2.3", upd, "update", "--check")
	if code != 0 || !strings.Contains(out, "up to date") {
		t.Errorf("code=%d out=%q stderr=%q", code, out, stderr)
	}
}

func TestUpdateCheckAvailable(t *testing.T) {
	upd := &update.Client{Releaser: fakeReleaser(t, []byte("X")), GOOS: "linux", GOARCH: "amd64"}
	code, out, _ := runUpd(t, false, "", "1.0.0", upd, "update", "--check")
	if code != 0 || !strings.Contains(out, "update available: 1.0.0") || !strings.Contains(out, "1.2.3") {
		t.Errorf("code=%d out=%q", code, out)
	}
}

func TestUpdateInPlaceNoPrompt(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ccswitch")
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	upd := &update.Client{
		Releaser: fakeReleaser(t, []byte("NEWBIN")),
		GOOS:     testGOOS, GOARCH: testGOARCH,
		ExecPath: func() (string, error) { return exe, nil },
		HomeDir:  t.TempDir(),
	}
	// tty=false, no --yes: a writable install updates in place with no prompt.
	code, out, stderr := runUpd(t, false, "", "1.0.0", upd, "update")
	if code != 0 || !strings.Contains(out, "updated ccswitch 1.0.0") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if strings.Contains(stderr, "[y/N]") {
		t.Errorf("unexpected prompt for a writable install: %q", stderr)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "NEWBIN" {
		t.Errorf("binary not replaced: %q", got)
	}
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestUpdateManagedConfirmed(t *testing.T) {
	home := t.TempDir()
	exe := "/nix/store/abc-ccswitch-1.0.0/bin/ccswitch"
	upd := &update.Client{
		Releaser: fakeReleaser(t, []byte("NEWBIN")),
		GOOS:     testGOOS, GOARCH: testGOARCH,
		ExecPath: func() (string, error) { return exe, nil },
		HomeDir:  home,
		PathEnv:  filepath.Join(home, ".local", "bin"),
	}
	code, out, stderr := runUpd(t, true, "y\n", "1.0.0", upd, "update")
	if code != 0 {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if !strings.Contains(stderr, "package-managed") || !strings.Contains(stderr, "[y/N]") {
		t.Errorf("expected managed warning + prompt: %q", stderr)
	}
	dest := filepath.Join(home, ".local", "bin", "ccswitch")
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "NEWBIN" {
		t.Fatalf("self-managed copy = %q, %v", got, err)
	}
	if !strings.Contains(out, "shadows") {
		t.Errorf("expected shadow notice: %q", out)
	}
}

func TestUpdateManagedDeclined(t *testing.T) {
	home := t.TempDir()
	upd := &update.Client{
		Releaser: fakeReleaser(t, []byte("NEWBIN")),
		GOOS:     testGOOS, GOARCH: testGOARCH,
		ExecPath: func() (string, error) { return "/nix/store/x/bin/ccswitch", nil },
		HomeDir:  home,
	}
	code, _, stderr := runUpd(t, true, "n\n", "1.0.0", upd, "update")
	if code != 1 || !strings.Contains(stderr, "aborted") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "ccswitch")); err == nil {
		t.Error("declined update still wrote a binary")
	}
}

func TestUpdateManagedNonTTYRequiresYes(t *testing.T) {
	upd := &update.Client{
		Releaser: fakeReleaser(t, []byte("NEWBIN")),
		GOOS:     testGOOS, GOARCH: testGOARCH,
		ExecPath: func() (string, error) { return "/nix/store/x/bin/ccswitch", nil },
		HomeDir:  t.TempDir(),
	}
	code, _, stderr := runUpd(t, false, "", "1.0.0", upd, "update")
	if code != 1 || !strings.Contains(stderr, "--yes") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestUpdateManagedYesNonTTY(t *testing.T) {
	home := t.TempDir()
	upd := &update.Client{
		Releaser: fakeReleaser(t, []byte("NEWBIN")),
		GOOS:     testGOOS, GOARCH: testGOARCH,
		ExecPath: func() (string, error) { return "/nix/store/x/bin/ccswitch", nil },
		HomeDir:  home,
	}
	code, out, stderr := runUpd(t, false, "", "1.0.0", upd, "update", "--yes")
	if code != 0 || !strings.Contains(out, "updated ccswitch") {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "ccswitch")); err != nil {
		t.Errorf("--yes did not install: %v", err)
	}
}

func TestUpdateNilUpdaterReportsUnavailable(t *testing.T) {
	// run() wires no Update, mirroring a build without self-update.
	a := newTestApp(t)
	code, _, stderr := run(t, a, false, "", "update")
	if code != 1 || !strings.Contains(stderr, "self-update is not wired") {
		t.Errorf("code=%d stderr=%q", code, stderr)
	}
}

func TestUpdateOutputNoTokenLeak(t *testing.T) {
	a := newTestApp(t)
	seedTwoAccounts(t, a)
	writeLiveCreds(t, a, credsJSON("a-live", freshExpiry, refreshOK))
	writeLiveConfig(t, a, profileJSON("uuid-a", "a@x.com"))
	upd := &update.Client{Releaser: fakeReleaser(t, []byte("X")), GOOS: "linux", GOARCH: "amd64"}
	var out, errBuf bytes.Buffer
	Execute(Options{
		Version: "1.0.0",
		App:     a,
		Update:  upd,
		IO:      IO{In: strings.NewReader(""), Out: &out, Err: &errBuf, IsTTY: false},
	}, []string{"update", "--check"})
	blob := out.String() + errBuf.String()
	for _, needle := range []string{"sk-test-", "rt-test-"} {
		if strings.Contains(blob, needle) {
			t.Errorf("update output leaks a token value (%s)", needle)
		}
	}
}
