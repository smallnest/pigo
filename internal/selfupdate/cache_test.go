package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCachedLatest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_HOME", dir)

	// No cache file yet → not fresh, empty latest.
	if latest, fresh := CachedLatest(); latest != "" || fresh {
		t.Errorf("empty cache = (%q,%v), want (\"\",false)", latest, fresh)
	}

	// Fresh cache → returns latest and fresh=true.
	writeCache("v0.4.0")
	if latest, fresh := CachedLatest(); latest != "v0.4.0" || !fresh {
		t.Errorf("fresh cache = (%q,%v), want (v0.4.0,true)", latest, fresh)
	}

	// Stale cache (older than TTL) → latest kept, fresh=false.
	stale, _ := json.Marshal(updateCache{CheckedAt: time.Now().Add(-25 * time.Hour), Latest: "v0.3.0"})
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if latest, fresh := CachedLatest(); latest != "v0.3.0" || fresh {
		t.Errorf("stale cache = (%q,%v), want (v0.3.0,false)", latest, fresh)
	}

	// Corrupt cache → silent ("", false), never an error.
	if err := os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if latest, fresh := CachedLatest(); latest != "" || fresh {
		t.Errorf("corrupt cache = (%q,%v), want (\"\",false)", latest, fresh)
	}
}

func TestStartBackgroundCheckDevNoWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PIGO_HOME", dir)
	// dev is not a release version → must not touch the network or write a cache.
	StartBackgroundCheck("dev")
	if _, err := os.Stat(filepath.Join(dir, cacheFileName)); !os.IsNotExist(err) {
		t.Errorf("dev build wrote a cache file; want none")
	}
}
