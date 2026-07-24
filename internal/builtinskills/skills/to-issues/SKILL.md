---
name: to-issues
description: "Decompose a PRD and/or SPEC into implementable Issues and create them in your chosen platform (GitHub, Local, or Baidu iCafe). Use after /prd (and optionally /prd-to-spec) to turn requirements into actionable tickets. Triggers on: create issues, to-issues, 创建issue, 拆解issue, 生成卡片, 创建卡片, generate issues from PRD, issues from spec."
user-invocable: true
---

# to-issues — PRD/SPEC to Issues

Decompose a PRD and/or technical SPEC into small, independent, implementable Issues, then create them in your chosen platform. Works standalone — you don't need to have run `/prd` first.

---

## The Job

1. **Locate input** — find a PRD or SPEC file (auto-detect or user-specified)
2. **Decompose into Issues** — break User Stories into implementable tickets
3. **Review with user** — present Issue list for approval and adjustment
4. **Choose platform** — GitHub / Local / Baidu iCafe
5. **Create Issues** — create all tickets and print summary

---

## Step 1: Locate Input

Find the input document:

```
What should I base the Issues on?

A. Auto-detect: scan tasks/ for recent PRDs and SPECs
B. Specific PRD file (e.g., tasks/prd-priority-system.md)
C. Specific SPEC file (e.g., tasks/spec-priority-system.md)
D. Both PRD and SPEC (best: PRD for requirements, SPEC for technical contracts)
E. Paste requirements directly
```

If auto-detecting, list available files and let the user choose.

If both PRD and SPEC are available, use the SPEC's Section 10.2 (Issue Mapping) as the primary guide, supplemented by PRD's User Stories. If only PRD is available, generate Issues directly from User Stories.

---

## Step 2: Decompose into Issues

Based on the input document(s), generate a list of Issues. Follow these rules:

- **One Issue per User Story** — each US-XXX becomes at least one Issue
- **Split large stories** — if a US has 5+ acceptance criteria or spans frontend + backend, split into 2-3 smaller Issues with clear dependencies
- **Merge tiny stories** — if a US has only 1-2 trivial criteria, merge it with a related US into a single Issue
- **Each Issue must be independently implementable** — a single agent session should be able to complete it
- **Number Issues sequentially** starting from 1
- **If SPEC is available** — enrich Issues with SPEC references (API endpoints, data model sections, error handling contracts)

**Issue format:**

```
Issue #N: [Title]
---
Description: [From US description, with context]
Acceptance Criteria:
- [ ] [From US acceptance criteria]
- [ ] ...
Dependencies: [None / Issue #X]
Type: [backend / frontend / fullstack / ui / infra]
Priority: [high / medium / low]
SPEC Reference: [Section X.Y — only if SPEC available]
```

**Present the Issue list for review:**

```
📋 Generated N Issues from [PRD/SPEC]:

#1: Add priority field to database (backend, high)
#2: Display priority indicator on task cards (frontend, high) — depends on #1
#3: Add priority selector to task edit (frontend, medium) — depends on #1
#4: Filter tasks by priority (frontend, medium) — depends on #1, #2

Please review. You can:
- Remove issues: "remove #3"
- Merge issues: "merge #2 and #3"
- Add issues: "add an issue for sorting by priority"
- Adjust: "change #2 priority to high"
- Confirm: reply OK to proceed
```

Wait for user confirmation before creating any Issues.

---

## Step 3: Choose Creation Mode

After user confirms the Issue list, ask:

```
Choose where to create these Issues:

A. GitHub (via gh CLI)
B. Local (save as .md files)
C. Baidu iCafe (via icafe-cli)

Your choice:
```

---

## Step 4: Mode-Specific Creation

### Mode A: GitHub

**Prerequisites:** `gh` CLI installed and authenticated.

**Actions:**
1. For each Issue, run:
   ```bash
   gh issue create --title "[Title]" --body "[Description + Acceptance Criteria]" --label "[type]" --label "priority: [priority]"
   ```
2. If labels don't exist, create them first or skip the `--label` flag
3. Report created Issue numbers and URLs

### Mode B: Local

**Ask user:**
```
Where should I save the Issue files? (default: .autoresearch/issues)
```

**Actions:**
1. If the specified folder does not exist, create it with `mkdir -p`
2. For each Issue #N, save a file named `issue-NNN-[slug].md` (zero-padded to 3 digits):
   ```markdown
   # [Title]

   ## Description
   [Description from Issue]

   ## Acceptance Criteria
   - [ ] [criterion 1]
   - [ ] [criterion 2]

   ## Dependencies
   [None / Issue #X]

   ## Type
   [backend / frontend / fullstack / ui / infra]

   ## Priority
   [high / medium / low]
   ```
3. Report created file paths

### Mode C: Baidu iCafe

**Ask user:**
```
Please provide the iCafe space prefix code (--space):
```

Optionally ask:
```
Target branch for iCode CR? (default: master)
```

**Prerequisites:** `icafe-cli` installed and logged in.

**Actions:**
1. For each Issue, run:
   ```bash
   icafe-cli card create --space [SPACE] --title "[Title]" --description "[Description + Acceptance Criteria]" --cardtype "[Task/Bug/Story]"
   ```
   - Map Issue `type` to iCafe card type: `bug` → `Bug`, `ui`/`frontend` → `Story`, others → `Task`
   - Map `priority`: high → `高`, medium → `中`, low → `低`
2. If iCafe card creation fails for an Issue, log the error and continue with remaining Issues
3. Report created card sequence numbers

---

## Step 5: Summary Report

After all Issues are created, print a summary:

```
✅ Issue creation complete!

Source: [PRD/SEC path]
Mode: [GitHub / Local / Baidu iCafe]
Issues created: N

#  | Title                                    | Identifier
---|------------------------------------------|------------
1  | Add priority field to database           | #42 (GitHub) / issue-001-*.md (Local) / #22210 (iCafe)
2  | Display priority indicator               | #43 / issue-002-*.md / #22211
3  | Add priority selector                    | #44 / issue-003-*.md / #22212
4  | Filter tasks by priority                 | #45 / issue-004-*.md / #22213

💡 Tip: Now implement each Issue with /goal:
  /goal 42                    # GitHub mode
  /goal issue-001-*.md        # Local mode
```

---

## Edge Cases & Fallback

| Scenario | Handling |
|----------|----------|
| No PRD/SPEC found in tasks/ | Ask user to provide file path or paste requirements |
| PRD has no User Stories | Derive Issues from Functional Requirements instead |
| SPEC has Issue Mapping (Section 10.2) | Use it as primary source, cross-reference with PRD |
| `gh` CLI not authenticated for GitHub mode | Show error, suggest `gh auth login`, offer to switch to Local mode |
| `icafe-cli` / `icode-cli` not installed for Baidu mode | Show error, suggest installation, offer to switch to Local mode |
| Issue folder does not exist for Local mode | Auto-create the folder |
| User declines Issue creation | Print the Issue list as a text summary, let user create manually later |

---

## Relationship to Other Skills

```
/prd  →  /prd-to-spec (optional)  →  /to-issues  →  /goal  →  /review-it  →  /ship-it
 │              │                        │              │
 │  Requirements │  Technical design     │  Tickets     │  Implementation
 │  (what)       │  (how)                │  (units)     │  (code)
```

- **/prd** — produces the PRD (input to this skill)
- **/prd-to-spec** — produces the SPEC (optional, enriches Issues with technical detail)
- **/to-issues** — produces the Issues (this skill)
- **/goal** — implements Issues one by one
