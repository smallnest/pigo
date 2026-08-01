// This file implements pigo's binary self-replacement for `pigo update` (issue
// #466). Given the current build version it discovers the latest release (via
// version.go), downloads the matching goreleaser archive for the running
// GOOS/GOARCH, verifies its SHA256 against the release's checksums.txt, and
// atomically replaces the running executable.
//
// The archive naming mirrors .goreleaser.yaml and install.sh exactly, so this
// stays a single source of truth with the release tooling. Replacement is
// atomic: the new binary is written to a temp file in the target's directory
// and os.Rename'd over the current executable, so a failure mid-download never
// leaves a truncated binary in place.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// checksumsFile is the goreleaser checksums artifact name (see .goreleaser.yaml).
const checksumsFile = "checksums.txt"

// Updater performs a self-replacement. Its fields are seams for testing;
// NewUpdater fills them with production defaults.
type Updater struct {
	HTTPClient *http.Client
	// Repo is "owner/name"; ReleaseBaseURL overrides the download host in tests.
	Repo           string
	ReleaseBaseURL string // e.g. https://github.com/smallnest/pigo/releases/download
	GOOS, GOARCH   string
	// ExecPath is the executable to replace; defaults to os.Executable().
	ExecPath string
}

// Run performs `pigo update` (self-update pigo). It discovers the latest
// release, compares it to current, and replaces the running binary when a
// newer release exists. When current is a source build ("dev"), it cannot
// compare and proceeds to install the latest. Returns a process exit code.
func Run(ctx context.Context, current string, out, errOut io.Writer) int {
	tag, err := LatestTag(ctx, nil, Repo)
	if err != nil {
		fmt.Fprintf(errOut, "pigo: 检查更新失败: %v\n", err)
		return 1
	}
	if avail, comparable := UpdateAvailable(current, tag); comparable && !avail {
		fmt.Fprintf(out, "已是最新版本 %s\n", current)
		return 0
	}
	u, err := NewUpdater()
	if err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	if err := u.Apply(ctx, tag, out); err != nil {
		fmt.Fprintf(errOut, "pigo: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "已更新到 %s\n", tag)
	return 0
}

// NewUpdater returns an Updater configured for the running process.
func NewUpdater() (*Updater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("selfupdate: locate executable: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a symlink.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return &Updater{
		HTTPClient:     &http.Client{Timeout: 60 * time.Second},
		Repo:           Repo,
		ReleaseBaseURL: "https://github.com/" + Repo + "/releases/download",
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		ExecPath:       exe,
	}, nil
}

// archiveName builds the goreleaser archive filename for a release version
// (without leading "v") on the updater's platform. It mirrors the
// name_template and format_overrides in .goreleaser.yaml.
func (u *Updater) archiveName(versionNoV string) string {
	osName := map[string]string{"darwin": "Darwin", "linux": "Linux", "windows": "Windows"}[u.GOOS]
	if osName == "" {
		osName = u.GOOS
	}
	arch := map[string]string{"amd64": "x86_64", "386": "i386"}[u.GOARCH]
	if arch == "" {
		arch = u.GOARCH // arm64 and others pass through
	}
	ext := "tar.gz"
	if u.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("pigo_%s_%s_%s.%s", versionNoV, osName, arch, ext)
}

// binaryName is the executable name inside the archive.
func (u *Updater) binaryName() string {
	if u.GOOS == "windows" {
		return "pigo.exe"
	}
	return "pigo"
}

// Apply downloads the release identified by tag, verifies its checksum, and
// atomically replaces the target executable. tag is like "v0.4.0".
func (u *Updater) Apply(ctx context.Context, tag string, out io.Writer) error {
	versionNoV := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	archive := u.archiveName(versionNoV)
	base := fmt.Sprintf("%s/%s", strings.TrimRight(u.ReleaseBaseURL, "/"), tag)

	fmt.Fprintf(out, "下载 %s ...\n", archive)
	archiveBytes, err := u.download(ctx, base+"/"+archive)
	if err != nil {
		return fmt.Errorf("selfupdate: download archive: %w", err)
	}

	sums, err := u.download(ctx, base+"/"+checksumsFile)
	if err != nil {
		return fmt.Errorf("selfupdate: download checksums: %w", err)
	}
	want, err := checksumFor(sums, archive)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archiveBytes)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("selfupdate: checksum mismatch for %s (archive corrupt or tampered)", archive)
	}

	binary, err := extractBinary(archiveBytes, u.binaryName(), u.GOOS == "windows")
	if err != nil {
		return err
	}
	if err := u.replace(binary); err != nil {
		return err
	}
	return nil
}

// download fetches url and returns the full body. A non-200 status is an error.
func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// replace atomically swaps the target executable with newBin: it writes a temp
// file in the target's directory, sets it executable, and renames it over the
// target. Writing to the same directory keeps the rename atomic (same
// filesystem). A permission error on the directory yields an actionable message.
func (u *Updater) replace(newBin []byte) error {
	dir := filepath.Dir(u.ExecPath)
	tmp, err := os.CreateTemp(dir, ".pigo-update-*")
	if err != nil {
		return fmt.Errorf("selfupdate: cannot write to %s: %w (尝试用 sudo 运行，或将 pigo 安装到可写目录)", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename

	if _, err := tmp.Write(newBin); err != nil {
		tmp.Close()
		return fmt.Errorf("selfupdate: write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("selfupdate: close new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("selfupdate: chmod new binary: %w", err)
	}
	if err := os.Rename(tmpName, u.ExecPath); err != nil {
		return fmt.Errorf("selfupdate: replace %s: %w", u.ExecPath, err)
	}
	return nil
}

// checksumFor finds the hex SHA256 for archive in a goreleaser checksums.txt
// body (lines of "<hex>  <filename>").
func checksumFor(sums []byte, archive string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == archive {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("selfupdate: %s not found in checksums.txt", archive)
}

// extractBinary pulls the named binary out of an archive (tar.gz, or zip when
// isZip). It returns the binary bytes.
func extractBinary(archive []byte, name string, isZip bool) ([]byte, error) {
	if isZip {
		return extractFromZip(archive, name)
	}
	return extractFromTarGz(archive, name)
}

func extractFromTarGz(archive []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: read tar: %w", err)
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("selfupdate: %s not found in archive", name)
}

func extractFromZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: zip reader: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("selfupdate: open %s in zip: %w", name, err)
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("selfupdate: %s not found in archive", name)
}
