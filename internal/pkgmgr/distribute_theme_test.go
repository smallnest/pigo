package pkgmgr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDistributeTheme verifies a theme package is stored under
// $PIGO_HOME/themes/<name>/ with its files intact.
func TestDistributeTheme(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg := writePkg(t, `{"name":"pi-dark","version":"1.0.0","pi":{"type":"theme"}}`, map[string]string{
		"theme.json":     `{"bg":"#000"}`,
		"assets/logo.txt": "logo",
	})

	files, err := DistributeTheme(pkg, "pi-dark")
	if err != nil {
		t.Fatalf("DistributeTheme: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "themes", "pi-dark", "theme.json")); err != nil {
		t.Errorf("theme.json not stored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "themes", "pi-dark", "assets", "logo.txt")); err != nil {
		t.Errorf("theme asset not stored: %v", err)
	}
	if len(files) == 0 {
		t.Error("created files empty")
	}
}

// TestDistributeThemeReinstallReplaces verifies reinstall clears stale files.
func TestDistributeThemeReinstallReplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PIGO_HOME", home)

	pkg1 := writePkg(t, `{"name":"pi-t","version":"1.0.0"}`, map[string]string{
		"theme.json": `{}`,
		"old.txt":    "old",
	})
	if _, err := DistributeTheme(pkg1, "pi-t"); err != nil {
		t.Fatalf("first install: %v", err)
	}
	pkg2 := writePkg(t, `{"name":"pi-t","version":"2.0.0"}`, map[string]string{
		"theme.json": `{}`,
	})
	if _, err := DistributeTheme(pkg2, "pi-t"); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "themes", "pi-t", "old.txt")); !os.IsNotExist(err) {
		t.Errorf("stale theme file survived reinstall: %v", err)
	}
}
