// This file caches the latest-release check so pigo's startup banner can show
// "update available" without a network call on every launch (US-004, FR-10).
// The cache lives at $PIGO_HOME/update-check.json (or ~/.pigo/update-check.json)
// and records the last check time plus the latest tag seen. CachedLatest reads
// it synchronously (fast, local); StartBackgroundCheck refreshes it off the hot
// path when older than the TTL, so a fresh result shows on the next launch. All
// failures are silent: a missing or corrupt cache, an unresolvable home, or a
// network error never surfaces an error or blocks startup.
package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// checkTTL is the minimum interval between networked latest-release checks.
const checkTTL = 24 * time.Hour

// cacheFileName is the on-disk cache under the pigo home directory.
const cacheFileName = "update-check.json"

// updateCache is the on-disk shape of the latest-release check cache.
type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// cachePath returns the cache file path, or "" when the home dir is unavailable.
func cachePath() string {
	if dir := os.Getenv("PIGO_HOME"); dir != "" {
		return filepath.Join(dir, cacheFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pigo", cacheFileName)
}

// CachedLatest returns the latest tag recorded in the cache and whether the cache
// is still fresh (younger than checkTTL). A missing, corrupt, or unreadable cache
// yields ("", false). It never returns an error — the banner must not break on a
// bad cache.
func CachedLatest() (latest string, fresh bool) {
	p := cachePath()
	if p == "" {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return "", false
	}
	return c.Latest, time.Since(c.CheckedAt) < checkTTL
}

// StartBackgroundCheck refreshes the cache off the hot path when it is stale
// (older than checkTTL). It returns immediately; the actual network check runs in
// a goroutine so it never blocks banner rendering or first input. When current is
// not a release version (dev/unknown) it does nothing — there is nothing to
// compare against. All errors are swallowed: a failed check simply leaves the
// cache untouched for next time.
func StartBackgroundCheck(current string) {
	if !IsReleaseVersion(current) {
		return
	}
	if _, fresh := CachedLatest(); fresh {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tag, err := LatestTag(ctx, &http.Client{Timeout: 10 * time.Second}, Repo)
		if err != nil || tag == "" {
			return
		}
		writeCache(tag)
	}()
}

// writeCache persists the latest tag with the current time. Failures are silent.
func writeCache(latest string) {
	p := cachePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(updateCache{CheckedAt: time.Now(), Latest: latest})
	if err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}
