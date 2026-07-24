---
name: prd-to-spec
description: "Transform a PRD into a technical SPEC document — architecture, API design, data model, error handling, and implementation contracts. Triggers on: prd-to-spec, prd to spec, prd转spec, 需求转设计, 需求转规格, generate spec from prd, design from prd, 技术方案, 设计方案."
user-invocable: true
---

# prd-to-spec — PRD to Technical Specification

Transform a Product Requirements Document (PRD) into a detailed technical SPEC that an engineer or AI agent can implement against. The PRD says *what* to build; the SPEC says *how* to build it.

---

## When to Use

- A `/prd` has been generated and you need to bridge the gap to implementation
- You want architecture decisions documented before coding starts
- Multiple developers/agents will implement the feature and need a shared contract
- You need to validate technical feasibility before committing to a PRD
- You want to catch design issues early — before code is written

---

## The Job

1. **Locate PRD** — find or receive the PRD document
2. **Analyze context (optional)** — if a codebase exists, scan it to understand current architecture, patterns, and constraints
3. **Ask clarifying questions** — resolve technical ambiguities (max 3-5 questions)
4. **Generate SPEC** — produce a structured technical specification
5. **Review** — present to user for feedback and iteration
6. **Save** — write final SPEC to agreed location

---

## Step 1: Locate PRD

Find the input PRD in one of these ways:

```
Provide the PRD to convert:

A. File path (e.g., tasks/prd-priority-system.md)
B. GitHub Issue URL
C. Paste PRD content directly
D. Auto-detect: scan tasks/ directory for recent PRDs
```

If auto-detecting, list available PRDs and let the user choose:

```
Found PRDs in tasks/:
  1. tasks/prd-priority-system.md (2024-03-15)
  2. tasks/prd-user-auth.md (2024-03-10)

Which PRD should I convert? [1/2]
```

---

## Step 2: Analyze Context (Optional)

**Skip this step if no codebase exists yet** (greenfield project). In that case, the SPEC will propose architecture from scratch based on the PRD requirements and clarifying questions.

If a codebase exists, scan it to understand:

- **Existing architecture** — how the current system is structured
- **Tech stack** — languages, frameworks, libraries already in use
- **Patterns** — naming conventions, file organization, error handling approach
- **Database** — current schema, migration tool, ORM
- **API style** — REST/GraphQL/gRPC, authentication method, response format
- **Testing** — test framework, coverage patterns, test utilities

This ensures the SPEC aligns with the existing system rather than proposing incompatible solutions.

---

## Step 3: Clarifying Questions

Ask only when the PRD leaves technical decisions ambiguous. Focus on:

- **Architecture choices** — where does this feature live? New service or extend existing?
- **Data storage** — new table? Extend existing? Cache strategy?
- **API design** — new endpoints? Extend existing? Breaking changes?
- **Dependencies** — any new libraries needed? Version constraints?
- **Performance** — expected load? Latency requirements? Batch size limits?

Format:
```
Technical questions before I generate the SPEC:

1. Where should the priority logic live?
   A. Extend existing TaskService
   B. New PriorityService
   C. Inline in controller
   D. Let me decide based on the codebase

2. Database migration approach?
   A. Add column to existing tasks table
   B. New priority table with FK
   C. JSON field on tasks
   D. Let me decide based on current schema

3. API versioning concern?
   A. Add to existing v1 endpoints
   B. New v2 endpoints
   C. No versioning needed
```

If user selects "let me decide" options, make the best choice based on codebase analysis and document the rationale in the SPEC.

---

## Step 4: SPEC Document Structure

```markdown
# SPEC: [Feature Name]

> Technical specification derived from: [PRD filename/link]
> Generated: [date] | Target branch: [branch] | Commit: [short-hash]

## 1. Summary

### 1.1 What This SPEC Covers
[One paragraph: what feature this specifies and the scope of implementation]

### 1.2 PRD Reference
- Source: [path or URL to PRD]
- User Stories covered: [US-001, US-002, ...]
- Functional Requirements covered: [FR-1, FR-2, ...]

### 1.3 Design Decisions Summary
| Decision | Choice | Rationale |
|----------|--------|-----------|
| ... | ... | ... |

---

## 2. Architecture

### 2.1 System Context
[Where this feature fits in the overall system — diagram or description]

### 2.2 Component Design
[New components/modules introduced, their responsibilities, and boundaries]

### 2.3 Module Interactions
[How new components interact with existing ones — sequence or data flow]

### 2.4 File Structure
[New files to create and existing files to modify]

```
src/
├── services/
│   └── priority.service.ts  [NEW]
├── controllers/
│   └── task.controller.ts   [MODIFY: add priority endpoints]
├── models/
│   └── priority.model.ts    [NEW]
└── migrations/
    └── 20240315_add_priority.ts [NEW]
```

---

## 3. Data Model

### 3.1 Schema Changes
[New tables, columns, indexes — with SQL or ORM notation]

### 3.2 Entity Definitions
[TypeScript interfaces / Go structs / Python dataclasses for new entities]

### 3.3 Relationships
[How new entities relate to existing ones — FK, embedded, reference]

### 3.4 Migration Plan
[Migration steps, backward compatibility, rollback strategy]

---

## 4. API Design

### 4.1 Endpoints

| Method | Path | Description | Auth | Request | Response |
|--------|------|-------------|------|---------|----------|
| ... | ... | ... | ... | ... | ... |

### 4.2 Request/Response Schemas
[Detailed shapes with field types, validation rules, and examples]

### 4.3 Error Responses
[Error codes, messages, and HTTP status codes for each failure mode]

### 4.4 Breaking Changes
[Any backward-incompatible changes and migration path for consumers]

---

## 5. Business Logic

### 5.1 Core Algorithms
[Step-by-step logic for key operations — pseudocode or structured description]

### 5.2 Validation Rules
[Input validation, business rule validation, with specific constraints]

### 5.3 State Machine
[If applicable: states, transitions, guards, and side effects]

### 5.4 Edge Cases
[Known edge cases and how they should be handled]

---

## 6. Error Handling

### 6.1 Error Taxonomy
| Error Code | HTTP Status | Condition | User Message |
|------------|-------------|-----------|--------------|
| ... | ... | ... | ... |

### 6.2 Retry Strategy
[Which operations are retryable, backoff policy, max attempts]

### 6.3 Failure Modes
[What happens when dependencies fail — graceful degradation plan]

---

## 7. Security

### 7.1 Authentication & Authorization
[Who can access what, permission model, role checks]

### 7.2 Input Validation
[Sanitization rules, injection prevention, size limits]

### 7.3 Data Protection
[Sensitive fields, encryption at rest/transit, audit logging]

---

## 8. Performance

### 8.1 Expected Load
[Estimated QPS, data volume, growth projection]

### 8.2 Optimization Strategy
[Caching, pagination, lazy loading, batch processing]

### 8.3 Database Considerations
[Index strategy, query patterns, N+1 prevention]

---

## 9. Testing Strategy

### 9.1 Unit Tests
[What to test, test boundaries, mock strategy]

### 9.2 Integration Tests
[API tests, database tests, service interaction tests]

### 9.3 Edge Case Tests
[Specific scenarios to cover based on Section 5.4]

### 9.4 Acceptance Criteria Mapping
| US/FR | Test | Type | Description |
|-------|------|------|-------------|
| US-001 | ... | unit | ... |
| FR-2 | ... | integration | ... |

---

## 10. Implementation Plan

### 10.1 Phases
[Order of implementation — what to build first, dependencies between steps]

### 10.2 Issue Mapping
[Map SPEC sections to PRD Issues for implementation tracking]

| Issue | SPEC Sections | Priority | Depends On |
|-------|--------------|----------|------------|
| #1 | 3.1, 3.4 | high | — |
| #2 | 4.1, 4.2, 5.1 | high | #1 |
| ... | ... | ... | ... |

### 10.3 Incremental Delivery
[How to ship incrementally — feature flags, dark launches, gradual rollout]

---

## 11. Open Questions & Risks

### 11.1 Unresolved Questions
- [Questions that need product/engineering input before implementation]

### 11.2 Technical Risks
| Risk | Impact | Mitigation |
|------|--------|-----------|
| ... | ... | ... |

### 11.3 Assumptions
- [Technical assumptions made during SPEC creation — validate before implementing]
```

---

## Step 5: Review & Iteration

After generating the SPEC, present it and ask:

```
SPEC generated from PRD. Please review:

- Are the architecture choices appropriate?
- Are there missing edge cases or error scenarios?
- Is the API design consistent with existing patterns?
- Should any section have more/less detail?

Reply OK to save, or provide feedback for iteration.
```

---

## Step 6: Save

Ask user for save location:

```
Where should I save the SPEC?

A. tasks/spec-[feature-name].md (alongside PRD, recommended)
B. docs/spec-[feature-name].md
C. Custom path: [specify]
```

---

## Mapping Strategy: PRD → SPEC

How PRD elements translate to SPEC sections:

| PRD Section | SPEC Section(s) | Transformation |
|-------------|-----------------|----------------|
| User Stories | 5. Business Logic, 9.4 Acceptance Mapping | Stories → algorithms + test cases |
| Functional Requirements | 4. API Design, 5. Business Logic | FRs → endpoints + logic |
| Acceptance Criteria | 9. Testing Strategy | Criteria → specific test scenarios |
| Non-Goals | 11.1 Open Questions | Clarify what's explicitly excluded |
| Technical Considerations | 2. Architecture, 8. Performance | Constraints → design decisions |
| Success Metrics | 8.1 Expected Load, 10.3 Delivery | Metrics → monitoring + rollout plan |

---

## Quality Criteria

A good SPEC should pass these checks:

- [ ] Every PRD User Story has corresponding SPEC sections
- [ ] Every Functional Requirement maps to an API endpoint or business logic rule
- [ ] Every Acceptance Criterion maps to at least one test case
- [ ] Architecture choices are justified with rationale
- [ ] API schemas are specific enough to generate client code
- [ ] Error handling covers all identified failure modes
- [ ] Implementation order respects dependencies
- [ ] No "TBD" or "TODO" items — resolve or move to Open Questions

---

## Edge Cases & Fallback

| Scenario | Handling |
|----------|----------|
| PRD is vague or incomplete | Generate SPEC with best-effort choices, mark assumptions in Section 11.3 |
| PRD conflicts with existing code | Flag conflicts explicitly, propose resolution in Section 11.1 |
| Feature is too large for one SPEC | Split into multiple SPECs (one per service boundary), link them |
| No existing codebase (greenfield) | Skip Step 2, propose architecture from scratch based on PRD + clarifying questions |
| PRD has no User Stories (just bullet points) | Infer structure, map bullets to SPEC sections, note in Summary |
| User wants SPEC without reading codebase | Skip Step 2, note that assumptions about existing code are unverified |
| Multiple PRDs need one SPEC | Merge PRD inputs, deduplicate requirements, note source for each |

---

## Anti-Patterns to Avoid

- **Don't restate the PRD.** The SPEC adds technical depth, not a copy of requirements in different words.
- **Don't over-specify trivial operations.** CRUD with no special logic doesn't need a full algorithm section.
- **Don't pick technologies without context.** Always check what the project already uses before suggesting new tools.
- **Don't design in isolation.** The SPEC must fit the existing system — same patterns, same conventions, same style.
- **Don't leave decisions implicit.** If you made a choice (e.g., "add column to existing table"), state it and say why.
- **Don't write implementation code.** The SPEC describes contracts and behavior, not code. Pseudocode is acceptable for complex algorithms.

---

## Relationship to Other Skills

```
/prd  →  /prd-to-spec  →  /goal  →  /review-it  →  /ship-it
 │              │              │
 │  Requirements │  Technical   │  Implementation
 │  (what)       │  (how)       │  (code)
```

- **/prd** produces the PRD (input to this skill)
- **/prd-to-spec** produces the SPEC (this skill)
- **/goal** implements Issues with SPEC as the technical reference
- **/code-to-spec** reverse-engineers SPEC from existing code (complementary — forward vs. reverse)
