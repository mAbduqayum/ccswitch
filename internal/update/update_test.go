package update

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
)

// makeArchive builds a .tar.gz holding a ccswitch binary plus the decoy files
// goreleaser bundles alongside it.
func makeArchive(t *testing.T, binContent []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name string
		data []byte
	}{
		{"completions/ccswitch.bash", []byte("# completions")},
		{"LICENSE", []byte("MIT")},
		{"README.md", []byte("# ccswitch")},
		{"ccswitch", binContent},
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(e.data)),
		}); err != nil {
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

func checksumsFor(archive []byte, name string) []byte {
	sum := sha256.Sum256(archive)
	return fmt.Appendf(nil, "deadbeef  some-other-file\n%s  %s\n", hex.EncodeToString(sum[:]), name)
}

// stubReleaser serves a fixed release and a URL→bytes map.
type stubReleaser struct {
	rel   Release
	blobs map[string][]byte
}

func (s stubReleaser) Latest(context.Context) (Release, error) { return s.rel, nil }

func (s stubReleaser) Fetch(_ context.Context, url string) ([]byte, error) {
	b, ok := s.blobs[url]
	if !ok {
		return nil, fmt.Errorf("no blob for %s", url)
	}
	return b, nil
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		current, tag  string
		newer, unknwn bool
	}{
		{"0.1.0", "v0.2.0", true, false},
		{"0.2.0", "v0.2.0", false, false},
		{"0.3.0", "v0.2.0", false, false},
		{"v0.1.0", "v0.2.0", true, false}, // running version carries a v
		{"1.9.9", "v1.10.0", true, false}, // numeric, not lexical, compare
		{"dev", "v0.2.0", true, true},     // unparseable current
		{"abc123def", "v0.2.0", true, true},
	}
	for _, c := range cases {
		t.Run(c.current+"_vs_"+c.tag, func(t *testing.T) {
			vc := compareVersions(c.current, c.tag)
			if vc.Newer != c.newer || vc.Unknown != c.unknwn {
				t.Errorf("compareVersions(%q,%q) = {newer:%v unknown:%v}, want {newer:%v unknown:%v}",
					c.current, c.tag, vc.Newer, vc.Unknown, c.newer, c.unknwn)
			}
			if vc.Latest != "0.2.0" && !strings.HasPrefix(c.tag, "v1") {
				t.Errorf("Latest = %q, want the v stripped", vc.Latest)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	got := AssetName("0.1.1", "linux", "amd64")
	if got != "ccswitch_0.1.1_linux_amd64.tar.gz" {
		t.Errorf("AssetName = %q", got)
	}
}

func TestVerifyChecksum(t *testing.T) {
	archive := makeArchive(t, []byte("BINARY"))
	name := "ccswitch_0.1.1_linux_amd64.tar.gz"
	sums := checksumsFor(archive, name)

	if err := verifyChecksum(archive, sums, name); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}
	if err := verifyChecksum(archive, sums, "missing.tar.gz"); err == nil {
		t.Error("missing checksum entry accepted")
	}
	if err := verifyChecksum([]byte("TAMPERED"), sums, name); err == nil {
		t.Error("checksum mismatch accepted")
	}
}

func TestChecksumForBinaryMode(t *testing.T) {
	sums := []byte("abc123  *ccswitch_0.1.1_linux_amd64.tar.gz\n")
	got, ok := checksumFor(sums, "ccswitch_0.1.1_linux_amd64.tar.gz")
	if !ok || got != "abc123" {
		t.Errorf("checksumFor binary-mode = %q,%v", got, ok)
	}
}

func TestExtractBinary(t *testing.T) {
	archive := makeArchive(t, []byte("REAL-BINARY"))
	bin, err := extractBinary(archive)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if string(bin) != "REAL-BINARY" {
		t.Errorf("extracted %q, want the ccswitch entry not a decoy", bin)
	}

	var empty bytes.Buffer
	gz := gzip.NewWriter(&empty)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "LICENSE", Typeflag: tar.TypeReg, Size: 3})
	_, _ = tw.Write([]byte("MIT"))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := extractBinary(empty.Bytes()); err == nil {
		t.Error("archive without a ccswitch binary should error")
	}
}

func TestDownloadEndToEnd(t *testing.T) {
	archive := makeArchive(t, []byte("NEW-BINARY"))
	name := "ccswitch_1.2.3_linux_amd64.tar.gz"
	sums := checksumsFor(archive, name)
	c := &Client{
		Releaser: stubReleaser{
			rel: Release{Tag: "v1.2.3", Assets: []Asset{
				{Name: name, URL: "https://dl/archive"},
				{Name: checksumsName, URL: "https://dl/sums"},
			}},
			blobs: map[string][]byte{"https://dl/archive": archive, "https://dl/sums": sums},
		},
		GOOS:   "linux",
		GOARCH: "amd64",
	}
	bin, err := c.Download(context.Background(), Release{Tag: "v1.2.3", Assets: c.Releaser.(stubReleaser).rel.Assets})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(bin) != "NEW-BINARY" {
		t.Errorf("Download returned %q", bin)
	}
}

func TestDownloadRejectsTamperedArchive(t *testing.T) {
	archive := makeArchive(t, []byte("GENUINE"))
	name := "ccswitch_1.2.3_linux_amd64.tar.gz"
	sums := checksumsFor(archive, name)
	tampered := makeArchive(t, []byte("MALICIOUS"))
	c := &Client{
		Releaser: stubReleaser{
			rel: Release{Tag: "v1.2.3", Assets: []Asset{
				{Name: name, URL: "https://dl/archive"},
				{Name: checksumsName, URL: "https://dl/sums"},
			}},
			// Serve the tampered archive against the genuine checksums.
			blobs: map[string][]byte{"https://dl/archive": tampered, "https://dl/sums": sums},
		},
		GOOS:   "linux",
		GOARCH: "amd64",
	}
	if _, err := c.Download(context.Background(), c.Releaser.(stubReleaser).rel); err == nil {
		t.Fatal("tampered archive passed verification")
	}
}

func TestDownloadUnsupportedPlatform(t *testing.T) {
	c := &Client{
		Releaser: stubReleaser{rel: Release{Tag: "v1.2.3"}},
		GOOS:     "windows",
		GOARCH:   "386",
	}
	if _, err := c.Download(context.Background(), Release{Tag: "v1.2.3"}); err == nil {
		t.Error("expected an error for a platform with no asset")
	}
}

func TestInstallWritesExecutable(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ccswitch")
	if err := Install([]byte("BIN"), dest); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "BIN" {
		t.Fatalf("read back = %q, %v", got, err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestManaged(t *testing.T) {
	if ok, _ := Managed("/nix/store/abc-ccswitch-1.0.0/bin/ccswitch"); !ok {
		t.Error("nix store path should be managed")
	}
	if ok, _ := Managed("/opt/homebrew/Cellar/ccswitch/1.0.0/bin/ccswitch"); !ok {
		t.Error("homebrew Cellar path should be managed")
	}
	writable := filepath.Join(t.TempDir(), "ccswitch")
	if ok, _ := Managed(writable); ok {
		t.Error("a writable dir should not be managed")
	}
}

func TestManagedNonWritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if ok, reason := Managed(filepath.Join(dir, "ccswitch")); !ok {
		t.Errorf("read-only dir should be managed, got reason %q", reason)
	}
}

func TestPathShadows(t *testing.T) {
	sep := string(os.PathListSeparator)
	local := "/home/u/.local/bin"
	managed := "/nix/store/x/bin"

	if !PathShadows(local, managed, local+sep+managed) {
		t.Error("local ahead of managed should shadow")
	}
	if PathShadows(local, managed, managed+sep+local) {
		t.Error("local behind managed should not shadow")
	}
	if PathShadows(local, managed, managed) {
		t.Error("local absent from PATH should not shadow")
	}
	if !PathShadows(local, managed, local) {
		t.Error("managed absent from PATH means local wins")
	}
}
