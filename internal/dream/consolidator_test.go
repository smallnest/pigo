package dream

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// canned builds an allowed-set + Plan pair for parser tests from a list of
// absolute paths.
func allowedSet(paths ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		m[filepath.Clean(p)] = struct{}{}
	}
	return m
}

func TestBuildConsolidatePromptListsEntriesAndHints(t *testing.T) {
	root := "/mem"
	a := "/mem/global/user/a.md"
	b := "/mem/global/user/b.md"
	idx := "/mem/global/MEMORY.md"
	plan := Plan{
		Files: []MemoryFile{
			{Path: a, Scope: "global", Type: "user", Size: 5, Body: "alpha body"},
			{Path: b, Scope: "global", Type: "user", Size: 5, Body: "beta body"},
			{Path: idx, Scope: "global", Type: "", Size: 3, Body: "- [a](user/a.md)"},
		},
		NearDupPairs:    []NearDupPair{{A: a, B: b, Similarity: 0.82}},
		InvalidPathRefs: []InvalidPathRef{{File: a, Ref: "./gone.go"}},
	}
	eligible := eligibleFiles(plan)
	if len(eligible) != 2 {
		t.Fatalf("eligibleFiles = %d, want 2 (MEMORY.md excluded)", len(eligible))
	}
	prompt := buildConsolidatePrompt(ConsolidateInput{Plan: plan, MemoryRoot: root, ProjectDir: ""}, eligible, defaultBodyBudget)

	for _, want := range []string{a, b, "alpha body", "beta body", "similarity 0.82", "./gone.go", "global only"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "MEMORY.md") {
		t.Errorf("prompt must not offer the MEMORY.md index as an entry:\n%s", prompt)
	}
}

func TestTruncateBody(t *testing.T) {
	if got := truncateBody("short", 100); got != "short" {
		t.Fatalf("no truncation expected, got %q", got)
	}
	long := strings.Repeat("x", 50)
	got := truncateBody(long, 10)
	if !strings.HasPrefix(got, strings.Repeat("x", 10)) || !strings.Contains(got, "truncated") {
		t.Fatalf("truncateBody = %q", got)
	}
}

func TestParseConsolidateResponseMergeAndPrune(t *testing.T) {
	a := "/mem/global/user/a.md"
	b := "/mem/global/user/b.md"
	c := "/mem/global/user/c.md"
	allowed := allowedSet(a, b, c)

	raw := "here you go:\n```json\n" + `{
	  "merges": [{"keep": "` + a + `", "body": "merged body", "remove": ["` + b + `"]}],
	  "prunes": [{"path": "` + c + `", "reason": "superseded by newer note"}],
	  "notes": ["did the thing"]
	}` + "\n```\n"

	res := parseConsolidateResponse(raw, allowed)
	if res.Merged != 1 {
		t.Errorf("Merged = %d, want 1", res.Merged)
	}
	if res.Pruned != 1 {
		t.Errorf("Pruned = %d, want 1", res.Pruned)
	}
	if got := res.MergedBodies[a]; got != "merged body" {
		t.Errorf("MergedBodies[a] = %q", got)
	}
	wantDel := map[string]bool{b: true, c: true}
	if len(res.Deletions) != 2 {
		t.Fatalf("Deletions = %v, want b and c", res.Deletions)
	}
	for _, d := range res.Deletions {
		if !wantDel[d] {
			t.Errorf("unexpected deletion %q", d)
		}
	}
	joined := strings.Join(res.Notes, "|")
	if !strings.Contains(joined, "superseded by newer note") || !strings.Contains(joined, "did the thing") {
		t.Errorf("notes missing content: %v", res.Notes)
	}
}

func TestParseConsolidateResponseRejectsUnknownAndUnsafePaths(t *testing.T) {
	a := "/mem/global/user/a.md"
	allowed := allowedSet(a)
	raw := `{
	  "merges": [{"keep": "/etc/passwd", "body": "x", "remove": ["` + a + `"]}],
	  "prunes": [{"path": "/mem/global/MEMORY.md", "reason": "index"}]
	}`
	res := parseConsolidateResponse(raw, allowed)
	if res.Merged != 0 || res.Pruned != 0 || len(res.Deletions) != 0 {
		t.Fatalf("unsafe/unknown paths must be ignored, got %+v", res)
	}
}

func TestParseConsolidateResponseConservativeOnEmptyReasonAndBadJSON(t *testing.T) {
	a := "/mem/global/user/a.md"
	allowed := allowedSet(a)

	// Empty prune reason → KEEP.
	res := parseConsolidateResponse(`{"prunes":[{"path":"`+a+`","reason":""}]}`, allowed)
	if res.Pruned != 0 || len(res.Deletions) != 0 {
		t.Fatalf("empty reason must KEEP, got %+v", res)
	}

	// Unparseable → empty result with a note, never a deletion.
	res = parseConsolidateResponse("the model rambled with no json", allowed)
	if res.Merged != 0 || res.Pruned != 0 || len(res.Deletions) != 0 {
		t.Fatalf("bad JSON must KEEP everything, got %+v", res)
	}
	if len(res.Notes) == 0 {
		t.Fatal("expected an explanatory note on unparseable response")
	}
}

func TestParseConsolidateResponseNoOpMergeIgnored(t *testing.T) {
	a := "/mem/global/user/a.md"
	allowed := allowedSet(a)
	// A merge that removes nothing must not touch the file.
	res := parseConsolidateResponse(`{"merges":[{"keep":"`+a+`","body":"rewritten","remove":[]}]}`, allowed)
	if len(res.MergedBodies) != 0 || res.Merged != 0 {
		t.Fatalf("no-op merge must be ignored, got %+v", res)
	}
}

func TestLLMConsolidatorUsesCompleter(t *testing.T) {
	root := "/mem"
	a := "/mem/global/user/a.md"
	b := "/mem/global/user/b.md"
	plan := Plan{Files: []MemoryFile{
		{Path: a, Scope: "global", Type: "user", Body: "one"},
		{Path: b, Scope: "global", Type: "user", Body: "two"},
	}}

	var gotSystem, gotUser string
	c := &llmConsolidator{complete: func(_ context.Context, sys, user string) (string, error) {
		gotSystem, gotUser = sys, user
		return `{"merges":[{"keep":"` + a + `","body":"merged","remove":["` + b + `"]}]}`, nil
	}}
	res, err := c.Consolidate(context.Background(), ConsolidateInput{Plan: plan, MemoryRoot: root})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if gotSystem != dreamSystemPrompt {
		t.Error("system prompt not passed through")
	}
	if !strings.Contains(gotUser, a) {
		t.Error("user prompt missing entry path")
	}
	if res.Merged != 1 || res.MergedBodies[a] != "merged" {
		t.Fatalf("merge not applied: %+v", res)
	}
}

func TestLLMConsolidatorPropagatesHardError(t *testing.T) {
	plan := Plan{Files: []MemoryFile{{Path: "/mem/global/user/a.md", Scope: "global", Type: "user", Body: "x"}}}
	c := &llmConsolidator{complete: func(context.Context, string, string) (string, error) {
		return "", errors.New("upstream 500")
	}}
	if _, err := c.Consolidate(context.Background(), ConsolidateInput{Plan: plan, MemoryRoot: "/mem"}); err == nil {
		t.Fatal("expected hard completion error to propagate")
	}
}

func TestLLMConsolidatorSkipsWhenNoEligibleFiles(t *testing.T) {
	called := false
	c := &llmConsolidator{complete: func(context.Context, string, string) (string, error) {
		called = true
		return "{}", nil
	}}
	// Only a MEMORY.md index → nothing eligible → no model call.
	plan := Plan{Files: []MemoryFile{{Path: "/mem/global/MEMORY.md", Scope: "global", Body: "idx"}}}
	if _, err := c.Consolidate(context.Background(), ConsolidateInput{Plan: plan, MemoryRoot: "/mem"}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if called {
		t.Fatal("model should not be called when no eligible files exist")
	}
}
