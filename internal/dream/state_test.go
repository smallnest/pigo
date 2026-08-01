package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	report := json.RawMessage(`{"merged":3,"deduped":1}`)
	want := State{
		LastRunAt:  time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		LastStatus: "ok",
		LastReport: report,
	}
	if err := SaveState(root, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !got.LastRunAt.Equal(want.LastRunAt) {
		t.Errorf("LastRunAt = %v, want %v", got.LastRunAt, want.LastRunAt)
	}
	if got.LastStatus != want.LastStatus {
		t.Errorf("LastStatus = %q, want %q", got.LastStatus, want.LastStatus)
	}
	// LastReport round-trips as raw JSON; compare semantically since
	// re-marshaling may reformat whitespace.
	var gotObj, wantObj map[string]any
	if err := json.Unmarshal(got.LastReport, &gotObj); err != nil {
		t.Fatalf("unmarshal got.LastReport: %v", err)
	}
	if err := json.Unmarshal(want.LastReport, &wantObj); err != nil {
		t.Fatalf("unmarshal want.LastReport: %v", err)
	}
	if !reflect.DeepEqual(gotObj, wantObj) {
		t.Errorf("LastReport = %v, want %v", gotObj, wantObj)
	}
}

func TestSaveStateCreatesDir(t *testing.T) {
	root := t.TempDir()
	if err := SaveState(root, State{LastStatus: "ok"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "global", "dream", "state.json")); err != nil {
		t.Errorf("state.json not created: %v", err)
	}
}

func TestLoadStateMissingIsZero(t *testing.T) {
	got, err := LoadState(t.TempDir())
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !got.LastRunAt.IsZero() || got.LastStatus != "" || got.LastReport != nil {
		t.Errorf("missing state = %+v, want zero-value", got)
	}
}

func TestLoadStateCorruptTolerated(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "global", "dream")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(root)
	if err != nil {
		t.Fatalf("LoadState returned error on corrupt JSON, want tolerated: %v", err)
	}
	if !got.LastRunAt.IsZero() {
		t.Errorf("corrupt state = %+v, want zero-value (never run)", got)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cfg := Config{Enabled: true, IntervalDays: 7, RecentSessions: 20}
	tests := []struct {
		name string
		cfg  Config
		last time.Time
		want bool
	}{
		{"due: interval elapsed", cfg, now.Add(-8 * 24 * time.Hour), true},
		{"due: exactly at interval", cfg, now.Add(-7 * 24 * time.Hour), true},
		{"not due: within interval", cfg, now.Add(-3 * 24 * time.Hour), false},
		{"zero LastRunAt never due", cfg, time.Time{}, false},
		{"disabled never due", Config{Enabled: false, IntervalDays: 7}, now.Add(-30 * 24 * time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := State{LastRunAt: tt.last}
			if got := s.Due(tt.cfg, now); got != tt.want {
				t.Errorf("Due = %v, want %v", got, tt.want)
			}
		})
	}
}
