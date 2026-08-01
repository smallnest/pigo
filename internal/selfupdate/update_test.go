package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveName(t *testing.T) {
	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "pigo_0.4.0_Darwin_arm64.tar.gz"},
		{"darwin", "amd64", "pigo_0.4.0_Darwin_x86_64.tar.gz"},
		{"linux", "amd64", "pigo_0.4.0_Linux_x86_64.tar.gz"},
		{"linux", "386", "pigo_0.4.0_Linux_i386.tar.gz"},
		{"windows", "amd64", "pigo_0.4.0_Windows_x86_64.zip"},
	}
	for _, tt := range tests {
		u := &Updater{GOOS: tt.goos, GOARCH: tt.goarch}
		if got := u.archiveName("0.4.0"); got != tt.want {
			t.Errorf("archiveName(%s/%s) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("abc123  pigo_0.4.0_Linux_x86_64.tar.gz\ndef456  pigo_0.4.0_Darwin_arm64.tar.gz\n")
	got, err := checksumFor(sums, "pigo_0.4.0_Darwin_arm64.tar.gz")
	if err != nil || got != "def456" {
		t.Errorf("checksumFor = (%q,%v), want (def456,nil)", got, err)
	}
	if _, err := checksumFor(sums, "missing.tar.gz"); err == nil {
		t.Error("expected error for missing archive")
	}
}

func TestExtractBinaryTarGz(t *testing.T) {
	want := []byte("#!fake pigo binary")
	archive := makeTarGz(t, "pigo", want)
	got, err := extractBinary(archive, "pigo", false)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("extracted = %q, want %q", got, want)
	}
	if _, err := extractBinary(archive, "nope", false); err == nil {
		t.Error("expected error for missing binary")
	}
}

func TestReplaceAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pigo")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &Updater{ExecPath: target}
	newBin := []byte("new binary content")
	if err := u.replace(newBin); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, newBin) {
		t.Errorf("after replace = %q, want %q", got, newBin)
	}
	// No leftover temp files in the directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file after replace, got %d", len(entries))
	}
}

func TestApplyEndToEnd(t *testing.T) {
	binary := []byte("brand new pigo v0.4.0")
	archive := makeTarGz(t, "pigo", binary)
	sum := sha256.Sum256(archive)
	archiveName := "pigo_0.4.0_Linux_x86_64.tar.gz"
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "pigo")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	u := &Updater{
		HTTPClient:     srv.Client(),
		Repo:           "smallnest/pigo",
		ReleaseBaseURL: srv.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &bytes.Buffer{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, binary) {
		t.Errorf("target after Apply = %q, want %q", got, binary)
	}
}

func TestApplyChecksumMismatch(t *testing.T) {
	archive := makeTarGz(t, "pigo", []byte("real content"))
	archiveName := "pigo_0.4.0_Linux_x86_64.tar.gz"
	// Wrong checksum on purpose.
	sums := "0000000000000000000000000000000000000000000000000000000000000000  " + archiveName + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case archiveName:
			_, _ = w.Write(archive)
		case checksumsFile:
			_, _ = w.Write([]byte(sums))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "pigo")
	_ = os.WriteFile(target, []byte("old"), 0o755)

	u := &Updater{
		HTTPClient:     srv.Client(),
		ReleaseBaseURL: srv.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecPath:       target,
	}
	if err := u.Apply(context.Background(), "v0.4.0", &bytes.Buffer{}); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	// Target must be untouched on checksum failure.
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Errorf("target modified despite checksum failure: %q", got)
	}
}

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}
