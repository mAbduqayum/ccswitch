// Package update implements ccswitch's opt-in self-update. It is the ONLY
// code path in ccswitch that reaches the network, and it runs solely when the
// user invokes `ccswitch update` — never from switch, discovery, or doctor. It
// never reads, touches, or transmits credential state: it downloads a release
// archive, verifies its SHA-256 against the release's checksums.txt, and
// installs the extracted binary.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mAbduqayum/ccswitch/internal/atomicio"
)

const (
	// binaryName is the executable's name inside every release archive.
	binaryName = "ccswitch"
	// checksumsName is the release asset holding the per-archive SHA-256 sums.
	checksumsName = "checksums.txt"
	// maxBinarySize caps the extracted binary so a hostile archive can't
	// exhaust memory. The real binary is a few megabytes.
	maxBinarySize = 200 << 20
	// maxDownloadSize caps any single HTTP body for the same reason.
	maxDownloadSize = 300 << 20
)

// Asset is a single downloadable file attached to a GitHub release.
type Asset struct {
	Name string
	URL  string
}

// Release is the subset of a GitHub release ccswitch needs.
type Release struct {
	Tag    string
	Assets []Asset
}

// Releaser is the network seam — the only interface that reaches the wire.
// Injected so the rest of the suite stays hermetic and can keep asserting that
// ccswitch makes no network calls.
type Releaser interface {
	// Latest returns the most recent published release.
	Latest(ctx context.Context) (Release, error)
	// Fetch downloads the bytes at an asset URL. If progress is non-nil it is
	// called as bytes arrive with the running and total counts (total is 0 when
	// unknown), so the caller can render download progress.
	Fetch(ctx context.Context, url string, progress func(done, total int64)) ([]byte, error)
}

// Client bundles the injectable seams the update flow depends on. The CLI owns
// all prompting; Client only computes and performs the update steps.
type Client struct {
	Releaser Releaser
	GOOS     string
	GOARCH   string
	// ExecPath resolves the running binary's real path (symlinks followed).
	// nil defaults to os.Executable + filepath.EvalSymlinks.
	ExecPath func() (string, error)
	// HomeDir anchors the ~/.local/bin fallback for managed installs.
	HomeDir string
	// PathEnv is the raw $PATH used to decide whether the fallback shadows a
	// package-managed copy. Empty defaults to os.Getenv("PATH").
	PathEnv string
	// OnProgress, if set, is called as the release archive downloads with the
	// bytes fetched so far and the total (0 when unknown). Only the archive
	// download reports progress; the tiny checksums fetch does not.
	OnProgress func(done, total int64)
}

// VersionCheck reports how the running version compares to the latest release.
type VersionCheck struct {
	Current string // version as reported by the running binary
	Latest  string // latest release version, without the leading v
	Newer   bool   // the latest release is newer than the running binary
	Unknown bool   // the running version couldn't be parsed (dev build)
}

// Check queries the latest release and compares it to current.
func (c *Client) Check(ctx context.Context, current string) (Release, VersionCheck, error) {
	rel, err := c.Releaser.Latest(ctx)
	if err != nil {
		return Release{}, VersionCheck{}, err
	}
	if strings.TrimSpace(rel.Tag) == "" {
		return Release{}, VersionCheck{}, errors.New("latest release has no tag")
	}
	if _, ok := parseSemver(strings.TrimPrefix(rel.Tag, "v")); !ok {
		return Release{}, VersionCheck{}, fmt.Errorf("cannot parse latest release tag %q", rel.Tag)
	}
	return rel, compareVersions(current, rel.Tag), nil
}

// InstallTarget describes where an update should be written and why.
type InstallTarget struct {
	Dest    string // where the new binary goes
	Exec    string // the running binary, resolved
	Managed bool   // the running binary can't be updated in place
	Reason  string // human explanation when Managed
}

// Target resolves the running binary and decides between an in-place update
// and a self-managed copy under ~/.local/bin.
func (c *Client) Target() (InstallTarget, error) {
	exe, err := c.resolveExec()
	if err != nil {
		return InstallTarget{}, err
	}
	managed, reason := Managed(exe)
	t := InstallTarget{Exec: exe, Managed: managed, Reason: reason}
	if managed {
		t.Dest = c.SelfManagedPath()
	} else {
		t.Dest = exe
	}
	return t, nil
}

// SelfManagedPath is the writable location a managed install is copied to.
func (c *Client) SelfManagedPath() string {
	return filepath.Join(c.HomeDir, ".local", "bin", binaryName)
}

// Shadows reports whether the self-managed copy at t.Dest precedes the
// package-managed binary on PATH, so it is the one that will be found.
func (c *Client) Shadows(t InstallTarget) bool {
	pathEnv := c.PathEnv
	if pathEnv == "" {
		pathEnv = os.Getenv("PATH")
	}
	return PathShadows(filepath.Dir(t.Dest), filepath.Dir(t.Exec), pathEnv)
}

// Download fetches the archive for the running platform from rel, verifies it
// against the release's checksums.txt, and returns the extracted binary. It
// never writes to disk and never returns unverified bytes.
func (c *Client) Download(ctx context.Context, rel Release) ([]byte, error) {
	version := strings.TrimPrefix(rel.Tag, "v")
	wantName := AssetName(version, c.GOOS, c.GOARCH)
	archiveAsset, ok := findAsset(rel.Assets, wantName)
	if !ok {
		return nil, fmt.Errorf("release %s has no build for %s/%s (looked for %s)", rel.Tag, c.GOOS, c.GOARCH, wantName)
	}
	sumsAsset, ok := findAsset(rel.Assets, checksumsName)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s to verify against", rel.Tag, checksumsName)
	}
	archive, err := c.Releaser.Fetch(ctx, archiveAsset.URL, c.OnProgress)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", wantName, err)
	}
	sums, err := c.Releaser.Fetch(ctx, sumsAsset.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", checksumsName, err)
	}
	if err := verifyChecksum(archive, sums, wantName); err != nil {
		return nil, err
	}
	return extractBinary(archive)
}

// Apply downloads and verifies the update for the running platform and writes
// it atomically to dest.
func (c *Client) Apply(ctx context.Context, rel Release, dest string) error {
	bin, err := c.Download(ctx, rel)
	if err != nil {
		return err
	}
	dir := filepath.Dir(dest)
	// MkdirAll leaves an existing directory's permissions untouched, so an
	// in-place update never re-modes the system bin dir; it only creates the
	// ~/.local/bin fallback when missing.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return Install(bin, dest)
}

// Install atomically writes binary to dest with executable permission. On
// Unix, renaming over the running executable is safe: the running process
// keeps its open inode while the path points at the new file.
func Install(binary []byte, dest string) error {
	return atomicio.WriteFile(dest, binary, 0o755)
}

// AssetName is the release archive filename for a version/platform, matching
// goreleaser's name_template (ccswitch_<version>_<os>_<arch>.tar.gz). version
// must be the bare semver, without a leading v.
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, version, goos, goarch)
}

// Managed reports whether execPath is a package-managed or otherwise
// read-only install that can't be updated in place, with a human reason.
func Managed(execPath string) (bool, string) {
	if strings.Contains(execPath, "/nix/store/") {
		return true, "it lives in the read-only Nix store"
	}
	if strings.Contains(execPath, "/Cellar/") {
		return true, "it is managed by Homebrew"
	}
	if dir := filepath.Dir(execPath); !dirWritable(dir) {
		return true, fmt.Sprintf("%s is not writable", dir)
	}
	return false, ""
}

// PathShadows reports whether localBin precedes managedDir on PATH, so a binary
// installed in localBin is found first. pathEnv is the raw $PATH.
func PathShadows(localBin, managedDir, pathEnv string) bool {
	localIdx, managedIdx := -1, -1
	for i, d := range filepath.SplitList(pathEnv) {
		clean := filepath.Clean(d)
		if localIdx == -1 && clean == filepath.Clean(localBin) {
			localIdx = i
		}
		if managedIdx == -1 && clean == filepath.Clean(managedDir) {
			managedIdx = i
		}
	}
	if localIdx == -1 {
		return false // not on PATH at all
	}
	if managedIdx == -1 {
		return true // the managed dir isn't on PATH, so local wins
	}
	return localIdx < managedIdx
}

func (c *Client) resolveExec() (string, error) {
	get := c.ExecPath
	if get == nil {
		get = defaultExecPath
	}
	return get()
}

func defaultExecPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

func findAsset(assets []Asset, name string) (Asset, bool) {
	for _, a := range assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// compareVersions normalizes the leading v off both sides and reports whether
// latestTag is newer. An unparseable current (dev build, VCS hash) is treated
// as "can't tell" and offers the latest.
func compareVersions(current, latestTag string) VersionCheck {
	latest := strings.TrimPrefix(latestTag, "v")
	vc := VersionCheck{Current: current, Latest: latest}
	lv, lok := parseSemver(latest)
	if !lok {
		return vc
	}
	cv, cok := parseSemver(strings.TrimPrefix(current, "v"))
	if !cok {
		vc.Unknown = true
		vc.Newer = true
		return vc
	}
	vc.Newer = semverLess(cv, lv)
	return vc
}

func parseSemver(s string) ([3]int, bool) {
	// Drop any pre-release / build-metadata suffix; releases are clean semver.
	s = strings.SplitN(s, "-", 2)[0]
	s = strings.SplitN(s, "+", 2)[0]
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func semverLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// verifyChecksum fails unless archive's SHA-256 matches the digest recorded for
// name in a sha256sum-format checksums file.
func verifyChecksum(archive, checksums []byte, name string) error {
	want, ok := checksumFor(checksums, name)
	if !ok {
		return fmt.Errorf("no checksum for %s in %s", name, checksumsName)
	}
	sum := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
		return fmt.Errorf("checksum mismatch for %s: refusing to install an unverified download", name)
	}
	return nil
}

// checksumFor parses sha256sum-format lines ("<hex>  <name>", with an optional
// leading '*' on the name in binary mode) and returns the digest for name.
func checksumFor(checksums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			return fields[0], true
		}
	}
	return "", false
}

// extractBinary returns the ccswitch executable from a .tar.gz archive,
// ignoring the completions, LICENSE, and README goreleaser bundles alongside.
func extractBinary(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != binaryName {
			continue
		}
		buf, err := io.ReadAll(io.LimitReader(tr, maxBinarySize))
		if err != nil {
			return nil, fmt.Errorf("read %s from archive: %w", binaryName, err)
		}
		if len(buf) == 0 {
			return nil, fmt.Errorf("%s in archive is empty", binaryName)
		}
		return buf, nil
	}
	return nil, fmt.Errorf("archive does not contain a %s binary", binaryName)
}

// dirWritable probes whether a file can be created in dir.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".ccswitch-writetest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}
