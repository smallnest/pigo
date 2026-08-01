package dream

import (
	"encoding/json"
	"testing"
)

func TestReportJSONTags(t *testing.T) {
	r := Report{
		Merged:       1,
		Deduped:      2,
		PathsCleaned: 3,
		Pruned:       4,
		Distilled:    5,
		BytesBefore:  100,
		BytesAfter:   80,
		FilesBefore:  10,
		FilesAfter:   9,
		DryRun:       true,
		Notes:        []string{"pruned stale entry"},
	}
	r.Reconciled.Indexed = 6
	r.Reconciled.Pruned = 7

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"merged", "deduped", "paths_cleaned", "pruned", "distilled",
		"bytes_before", "bytes_after", "files_before", "files_after",
		"dry_run", "notes", "reconciled",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON key %q in %s", key, data)
		}
	}
	rec, ok := m["reconciled"].(map[string]any)
	if !ok {
		t.Fatalf("reconciled not an object: %v", m["reconciled"])
	}
	if _, ok := rec["indexed"]; !ok {
		t.Errorf("reconciled.indexed missing: %v", rec)
	}
	if _, ok := rec["pruned"]; !ok {
		t.Errorf("reconciled.pruned missing: %v", rec)
	}
}

func TestReportZeroValueOmitsNotes(t *testing.T) {
	data, err := json.Marshal(Report{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["notes"]; ok {
		t.Errorf("empty notes should be omitted, got %s", data)
	}
}
