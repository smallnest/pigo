// Package selfupdate provides version discovery and comparison for pigo's
// self-update feature (issue #465). It queries the GitHub Releases API for the
// latest published tag of the pigo repository and compares it against the
// build-time version injected into the main package, so both `pigo update` and
// the interactive startup banner can decide whether a newer release exists.
//
// The build version ("dev" for `go build`/`go run` from source, a real
// vX.Y.Z for goreleaser builds) is not owned by this package; callers pass it
// in. Non-release versions ("", "dev", "unknown") are treated as
// non-comparable so a source build never reports a spurious update.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub "owner/name" whose releases back pigo's self-update. It
// matches the release target in .goreleaser.yaml and install.sh.
const Repo = "smallnest/pigo"

// latestReleaseURL builds the GitHub API endpoint for a repo's latest release.
func latestReleaseURL(repo string) string {
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
}

// release is the subset of the GitHub release JSON we consume.
type release struct {
	TagName string `json:"tag_name"`
}

// IsReleaseVersion reports whether v is a real release version that can be
// compared against a tag. The build defaults ("dev", "unknown") and the empty
// string are not release versions.
func IsReleaseVersion(v string) bool {
	switch strings.TrimSpace(v) {
	case "", "dev", "unknown":
		return false
	default:
		return true
	}
}

// LatestTag queries the GitHub Releases API for repo's latest release tag
// (e.g. "v0.4.0"). If client is nil a client with a short timeout is used. When
// the GITHUB_TOKEN environment variable is set it is sent as a bearer token to
// raise the API rate limit, mirroring install.sh.
func LatestTag(ctx context.Context, client *http.Client, repo string) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL(repo), nil)
	if err != nil {
		return "", fmt.Errorf("selfupdate: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("selfupdate: query latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("selfupdate: GitHub API returned %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("selfupdate: decode release JSON: %w", err)
	}
	tag := strings.TrimSpace(rel.TagName)
	if tag == "" {
		return "", fmt.Errorf("selfupdate: latest release has empty tag_name")
	}
	return tag, nil
}

// UpdateAvailable compares the current build version against a release tag and
// reports whether the tag is strictly newer. The comparable return is false
// when current is not a release version (source builds) or when either value
// cannot be parsed as a version — callers should treat non-comparable as "no
// update to offer" rather than an error.
func UpdateAvailable(current, latest string) (available, comparable bool) {
	if !IsReleaseVersion(current) {
		return false, false
	}
	cur, ok1 := parseVersion(current)
	lat, ok2 := parseVersion(latest)
	if !ok1 || !ok2 {
		return false, false
	}
	return compare(lat, cur) > 0, true
}

// parseVersion parses a semantic-ish version ("v0.4.0", "0.4.0-next") into its
// numeric major/minor/patch, ignoring any leading "v" and any pre-release or
// build suffix after "-" or "+". It reports ok=false when no numeric component
// can be read.
func parseVersion(v string) ([3]int, bool) {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "v")
	// Drop pre-release / build metadata: "0.4.0-next" -> "0.4.0".
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// compare returns -1, 0, or 1 as a is less than, equal to, or greater than b.
func compare(a, b [3]int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
