package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is the persisted /dream run state, stored as JSON at
// <memoryRoot>/global/dream/state.json. A zero-value State (LastRunAt zero,
// empty status, nil report) means dream has never run.
type State struct {
	LastRunAt  time.Time `json:"last_run_at"`
	LastStatus string    `json:"last_status"` // "ok" | "failed" | "skipped"
	// LastReport holds the structured change report from the last run. It is
	// nil until dream has completed at least one non-dry-run pass.
	LastReport *Report `json:"last_report,omitempty"`
}

// statePath is the state file location under the memory root.
func statePath(memoryRoot string) string {
	return filepath.Join(memoryRoot, "global", "dream", "state.json")
}

// LoadState reads the dream state from <memoryRoot>/global/dream/state.json. A
// missing file returns a zero-value State (never run) with no error. Corrupt or
// unreadable JSON is tolerated the same way: the caller gets a zero-value State
// and no error, so a damaged state file degrades to "never run" rather than
// breaking dream entirely.
func LoadState(memoryRoot string) (State, error) {
	data, err := os.ReadFile(statePath(memoryRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		// Unreadable (permissions, transient IO): treat as never-run rather
		// than surfacing an error that would block dream.
		return State{}, nil
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt JSON: degrade to never-run.
		return State{}, nil
	}
	return s, nil
}

// SaveState writes the dream state to <memoryRoot>/global/dream/state.json,
// creating the parent directory lazily. The file is written atomically via a
// temp file + rename so a crash mid-write cannot leave a truncated state.json.
func SaveState(memoryRoot string, s State) error {
	dir := filepath.Join(memoryRoot, "global", "dream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := statePath(memoryRoot)
	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Due reports whether an auto-triggered consolidation is warranted at now given
// cfg. It returns true only when dream is enabled and the configured interval
// has elapsed since the last run. Two cases deliberately return false:
//   - cfg.Enabled is false: auto-trigger is disabled entirely (US-001).
//   - a zero LastRunAt (dream has never run): the first-ever run is NOT
//     auto-triggered — the user is prompted to run /dream manually instead — to
//     avoid a cold-start token cost for new users. See spec §11.1.
func (s State) Due(cfg Config, now time.Time) bool {
	if !cfg.Enabled {
		return false
	}
	if s.LastRunAt.IsZero() {
		return false
	}
	interval := time.Duration(cfg.IntervalDays) * 24 * time.Hour
	return now.Sub(s.LastRunAt) >= interval
}
