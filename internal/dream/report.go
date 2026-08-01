package dream

// Report is the structured change summary produced by a /dream consolidation
// run. It is persisted inside State.LastReport (state.go) and emitted as a
// single line of JSON on the child process stdout (spec §4.2). Every count is a
// deterministic tally the runner fills in after the plan (Go-deterministic) and
// apply (LLM) phases; the zero value is a valid "nothing changed" report.
//
// The JSON tags match spec §3.2 exactly so the parent process (and any
// scripted/headless caller) can decode the stdout contract without a shared Go
// type.
type Report struct {
	Merged       int      `json:"merged"`        // entries merged away by the LLM apply step
	Deduped      int      `json:"deduped"`       // exact (content-hash) duplicates removed
	PathsCleaned int      `json:"paths_cleaned"` // stale local path references cleaned
	Pruned       int      `json:"pruned"`        // stale/contradictory entries pruned
	Distilled    int      `json:"distilled"`     // new memories distilled from session JSONL
	BytesBefore  int64    `json:"bytes_before"`
	BytesAfter   int64    `json:"bytes_after"`
	FilesBefore  int      `json:"files_before"`
	FilesAfter   int      `json:"files_after"`
	DryRun       bool     `json:"dry_run"`
	Notes        []string `json:"notes,omitempty"` // human-readable reasons (prune causes, etc.)
	Reconciled   struct {
		Indexed int `json:"indexed"`
		Pruned  int `json:"pruned"`
	} `json:"reconciled"`
}
