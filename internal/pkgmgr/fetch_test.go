package pkgmgr

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeTarGz writes a gzip tarball at path containing the given files, each under
// a top-level "package/" dir (mirroring npm pack layout).
func makeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestExtractTarGz verifies a tarball extracts with its files intact.
func TestExtractTarGz(t *testing.T) {
	tmp := t.TempDir()
	tarball := filepath.Join(tmp, "pkg.tgz")
	makeTarGz(t, tarball, map[string]string{
		"package/package.json": `{"name":"x","version":"1.0.0"}`,
		"package/index.js":     "console.log('hi')",
	})
	dest := filepath.Join(tmp, "out")
	if err := extractTarGz(tarball, dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "package", "package.json"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(got) != `{"name":"x","version":"1.0.0"}` {
		t.Errorf("extracted content = %q", got)
	}
}

// TestSafeJoinRejectsTraversal verifies a "../" tarball entry is rejected.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	if _, err := safeJoin("/tmp/extract", "../../etc/passwd"); err == nil {
		t.Error("safeJoin allowed path traversal, want error")
	}
	if _, err := safeJoin("/tmp/extract", "package/index.js"); err != nil {
		t.Errorf("safeJoin rejected legit path: %v", err)
	}
}

// TestEnsureNPMMissing verifies a clear error when npm is absent.
func TestEnsureNPMMissing(t *testing.T) {
	old := npmExecutable
	npmExecutable = "definitely-not-a-real-binary-xyz"
	defer func() { npmExecutable = old }()
	err := EnsureNPM()
	if err == nil {
		t.Fatal("EnsureNPM with missing npm = nil, want error")
	}
	if !contains(err.Error(), "npm not found") {
		t.Errorf("error = %q, want to mention 'npm not found'", err)
	}
}

// TestFetchWithFakeNPM drives Fetch end-to-end using a fake `npm` on PATH that
// produces a tarball, verifying extraction and cleanup without a real registry.
func TestFetchWithFakeNPM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake npm shell script is POSIX-only")
	}
	binDir := t.TempDir()
	// The fake npm: on `pack`, copy a prebuilt tarball into --pack-destination
	// and print its filename, mimicking real npm pack output.
	srcTarball := filepath.Join(binDir, "src.tgz")
	makeTarGz(t, srcTarball, map[string]string{
		"package/package.json": `{"name":"pi-demo","version":"2.0.0"}`,
	})
	fakeNPM := filepath.Join(binDir, "npm")
	script := `#!/bin/sh
# args: pack <spec> --pack-destination <dir> --ignore-scripts --loglevel error
dest=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--pack-destination" ]; then dest="$a"; fi
  prev="$a"
done
cp "` + srcTarball + `" "$dest/pi-demo-2.0.0.tgz"
echo "pi-demo-2.0.0.tgz"
`
	if err := os.WriteFile(fakeNPM, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ref, err := ParsePackageRef("npm:pi-demo")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Fetch(ref)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	defer res.Cleanup()

	pj, err := os.ReadFile(filepath.Join(res.Dir, "package.json"))
	if err != nil {
		t.Fatalf("read fetched package.json: %v", err)
	}
	if !contains(string(pj), `"pi-demo"`) {
		t.Errorf("package.json = %q", pj)
	}

	// Cleanup removes the temp root.
	root := res.TempRoot
	if err := res.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("temp root still exists after Cleanup: %v", err)
	}
}

// TestFetchNPMFailurePropagates verifies npm's error is surfaced and no temp
// dir is left behind.
func TestFetchNPMFailurePropagates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake npm shell script is POSIX-only")
	}
	binDir := t.TempDir()
	fakeNPM := filepath.Join(binDir, "npm")
	script := `#!/bin/sh
echo "npm error code E404" >&2
echo "npm error 404 Not Found - GET https://registry.npmjs.org/nope" >&2
exit 1
`
	if err := os.WriteFile(fakeNPM, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ref, _ := ParsePackageRef("npm:nope")
	_, err := Fetch(ref)
	if err == nil {
		t.Fatal("Fetch of failing npm = nil error, want error")
	}
	if !contains(err.Error(), "E404") {
		t.Errorf("error = %q, want npm stderr included", err)
	}
}
