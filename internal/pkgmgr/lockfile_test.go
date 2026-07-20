package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMissingFileIsEmpty verifies a missing lockfile yields an empty,
// usable lockfile rather than an error.
func TestLoadMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.json")
	lf, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if len(lf.Packages) != 0 {
		t.Errorf("expected empty lockfile, got %d packages", len(lf.Packages))
	}
	if lf.Version != lockfileVersion {
		t.Errorf("version = %d, want %d", lf.Version, lockfileVersion)
	}
}

// TestSaveThenLoadRoundTrips verifies a written lockfile reads back identically.
func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "packages.json") // sub dir must be created
	lf, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	lf.Set(InstalledPackage{
		Name:    "pi-mcp-adapter",
		Source:  "npm:pi-mcp-adapter",
		Version: "1.2.3",
		Types:   []PackageType{TypeExtension},
		Files:   []string{"/home/u/.pigo/plugins/pi-mcp-adapter"},
	})
	lf.Set(InstalledPackage{
		Name:    "pi-web-access",
		Source:  "npm:pi-web-access",
		Version: "0.1.0",
		Types:   []PackageType{TypeExtension, TypeSkill},
		Files:   []string{"/home/u/.pigo/plugins/pi-web-access"},
	})
	if err := lf.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("reloaded %d packages, want 2", len(got.Packages))
	}
	p, ok := got.Get("pi-web-access")
	if !ok {
		t.Fatal("pi-web-access missing after reload")
	}
	if p.Version != "0.1.0" || len(p.Types) != 2 {
		t.Errorf("pi-web-access = %+v, unexpected", p)
	}
}

// TestSaveIsIndentedJSON verifies the on-disk format is human-readable.
func TestSaveIsIndentedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.json")
	lf, _ := Load(path)
	lf.Set(InstalledPackage{Name: "x", Source: "npm:x", Version: "1", Types: []PackageType{TypeSkill}})
	if err := lf.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !contains(string(data), "\n  ") {
		t.Errorf("expected indented JSON, got:\n%s", data)
	}
}

// TestLoadCorruptFileIsError verifies a malformed lockfile is surfaced, never
// silently overwritten.
func TestLoadCorruptFileIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packages.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for corrupt lockfile, got nil")
	}
}

// TestRemove verifies Remove reports existence and deletes the entry.
func TestRemove(t *testing.T) {
	lf, _ := Load("") // in-memory
	lf.Set(InstalledPackage{Name: "a", Source: "npm:a", Version: "1"})
	if !lf.Remove("a") {
		t.Error("Remove(a) = false, want true")
	}
	if lf.Remove("a") {
		t.Error("Remove(a) second time = true, want false")
	}
	if _, ok := lf.Get("a"); ok {
		t.Error("a still present after Remove")
	}
}

// TestListSorted verifies List returns packages ordered by name.
func TestListSorted(t *testing.T) {
	lf, _ := Load("")
	lf.Set(InstalledPackage{Name: "zebra", Source: "npm:zebra"})
	lf.Set(InstalledPackage{Name: "alpha", Source: "npm:alpha"})
	lf.Set(InstalledPackage{Name: "mango", Source: "npm:mango"})
	got := lf.List()
	want := []string{"alpha", "mango", "zebra"}
	for i, p := range got {
		if p.Name != want[i] {
			t.Errorf("List()[%d] = %q, want %q", i, p.Name, want[i])
		}
	}
}

// TestEmptyPathSaveIsNoop verifies an in-memory lockfile never touches disk.
func TestEmptyPathSaveIsNoop(t *testing.T) {
	lf, _ := Load("")
	lf.Set(InstalledPackage{Name: "a"})
	if err := lf.Save(); err != nil {
		t.Errorf("Save on empty-path lockfile: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
