---
name: code-to-spec
description: "Reverse-engineer a SPEC document from an existing project. Analyzes code, config, tests, and structure to produce a comprehensive specification. Triggers on: code-to-spec, reverse spec, generate spec, 逆向规格, 生成规格文档, 生成设计文档, 生成设计方案, extract spec, document this project, what does this project do."
user-invocable: true
---

# to-spec — Reverse-Engineer Project Specification

Analyze an existing codebase and produce a structured SPEC document that captures what the project does, how it's built, and what contracts it exposes. The output is a living specification that could be used to rebuild the project from scratch or onboard new contributors.

---

## When to Use

- You want a comprehensive understanding of an existing project
- Onboarding new team members who need a high-level overview
- Documenting a project that was built without a spec
- Comparing actual implementation against intended design
- Preparing for a rewrite or major refactor
- Auditing what a project actually does vs. what people think it does

---

## The Job

1. **Scope confirmation** — ask user what to analyze (entire repo, specific directory, or specific aspect)
2. **Deep scan** — systematically read project structure, entry points, config, tests, and core logic
3. **Synthesize** — produce a structured SPEC document
4. **Review** — present to user for feedback and iteration
5. **Save** — write final SPEC to agreed location

---

## Step 1: Scope Confirmation

Before scanning, ask the user:

```
What should I analyze?

A. Entire repository (recommended for small-medium projects)
B. Specific directory or module: [path]
C. Specific aspect only (e.g., API surface, data model, auth flow)

Depth level:
1. Overview — high-level architecture + tech stack + key features (fast, ~5 min)
2. Standard — includes API contracts, data models, config, dependencies (default)
3. Deep — adds internal module interactions, error handling patterns, test coverage analysis
```

If the project is large (>500 files), recommend starting with Overview or a specific module.

---

## Step 2: Deep Scan

Systematically analyze the following (adapt to what exists):

### 2.1 Project Identity
- `package.json`, `go.mod`, `Cargo.toml`, `pyproject.toml`, `pom.xml`, etc.
- README, LICENSE
- Git history (first commit date, recent activity, contributor count)

### 2.2 Architecture
- Directory structure and organization pattern (monorepo, layered, hexagonal, etc.)
- Entry points (main files, CLI commands, server bootstrap)
- Module boundaries and dependency graph (internal)

### 2.3 Tech Stack
- Language(s) and version constraints
- Frameworks and major libraries
- Build tools and bundlers
- Runtime requirements (Node version, Docker, etc.)

### 2.4 Features & Behavior
- Route definitions / CLI commands / exported functions
- Business logic modules and their responsibilities
- Background jobs, cron tasks, event handlers

### 2.5 Data Model
- Database schemas, migrations, ORMs
- Key data structures and their relationships
- State management approach

### 2.6 API Surface
- HTTP endpoints (method, path, request/response shapes)
- GraphQL schema / gRPC protos / WebSocket events
- CLI interface (commands, flags, arguments)
- Exported library API (public functions, classes, types)

### 2.7 Configuration & Environment
- Environment variables and their purpose
- Config files and their schema
- Feature flags, toggles

### 2.8 External Dependencies
- Third-party services (databases, queues, APIs)
- Infrastructure requirements (cloud services, storage)
- Authentication/authorization providers

### 2.9 Testing & Quality
- Test framework and approach (unit, integration, e2e)
- Coverage patterns (what's tested, what's not)
- Linting, formatting, type checking setup

### 2.10 Deployment & Operations
- CI/CD configuration
- Deployment targets and strategies
- Monitoring, logging, health checks

---

## Step 3: SPEC Document Structure

Generate the SPEC with these sections. Omit sections that don't apply.

```markdown
# SPEC: [Project Name]

> Reverse-engineered specification — generated [date] from commit [short-hash]

## 1. Overview

### 1.1 Purpose
[One paragraph: what problem this project solves and for whom]

### 1.2 Key Capabilities
- [Bullet list of what the system can do, from a user's perspective]

### 1.3 Architecture Style
[e.g., "Monolithic Express.js API with React SPA frontend", "CLI tool with plugin system", "Microservices communicating over gRPC"]

---

## 2. Tech Stack

| Layer | Technology | Version |
|-------|-----------|---------|
| Language | ... | ... |
| Framework | ... | ... |
| Database | ... | ... |
| Build | ... | ... |
| Test | ... | ... |
| Deploy | ... | ... |

---

## 3. Project Structure

[Directory tree with annotations explaining each top-level directory's purpose]

---

## 4. Data Model

### 4.1 Core Entities
[For each entity: name, fields, relationships, constraints]

### 4.2 State Transitions
[If applicable: lifecycle states and valid transitions]

---

## 5. API Surface

### 5.1 [Interface Type: REST / CLI / Library / etc.]

[For each endpoint/command/function:]
| Method | Path/Command | Description | Auth |
|--------|-------------|-------------|------|
| ... | ... | ... | ... |

### 5.2 Request/Response Schemas
[Key request/response shapes with field types]

---

## 6. Configuration

| Variable / Key | Required | Default | Description |
|---------------|----------|---------|-------------|
| ... | ... | ... | ... |

---

## 7. External Dependencies

| Service | Purpose | Failure Impact |
|---------|---------|----------------|
| ... | ... | ... |

---

## 8. Business Rules & Constraints

- [Numbered list of invariants, validation rules, and business logic constraints discovered in the code]

---

## 9. Non-Functional Characteristics

### 9.1 Performance
[Observed patterns: caching, pagination, batch processing, etc.]

### 9.2 Security
[Auth mechanism, input validation patterns, secrets management]

### 9.3 Error Handling
[Error strategy: custom error types, error codes, retry policies]

---

## 10. Testing Strategy

| Type | Framework | Coverage Pattern |
|------|-----------|-----------------|
| Unit | ... | ... |
| Integration | ... | ... |
| E2E | ... | ... |

---

## 11. Known Gaps & Assumptions

- [Things that are unclear from the code alone]
- [Assumptions made during analysis]
- [Areas with no tests or documentation]

---

## 12. Appendix

### A. Dependency Graph
[Key module dependencies, import relationships]

### B. Environment Setup
[Steps to run the project locally, derived from config and scripts]
```

---

## Step 4: Review & Iteration

After generating the SPEC, present it and ask:

```
SPEC generated. Please review:

- Are there sections that need more detail?
- Are there inaccuracies I should correct?
- Should I add/remove any sections?
- Is the depth level appropriate?

Reply OK to save, or provide feedback for iteration.
```

Apply feedback and re-present until user confirms.

---

## Step 5: Save

Ask user for save location:

```
Where should I save the SPEC?

A. docs/SPEC.md (recommended)
B. SPEC.md (project root)
C. Custom path: [specify]
```

---

## Analysis Heuristics

### Identifying Purpose
- Look at README first line, package description field, CLI help text
- Check the main entry point — what does it bootstrap?
- Look at test descriptions — they often describe expected behavior in plain language

### Discovering Architecture
- Map `import`/`require` statements to build dependency graph
- Identify layers by directory naming: `controllers`, `services`, `models`, `routes`, `handlers`, `domain`, `infra`
- Check for dependency injection patterns, middleware chains, plugin registrations

### Extracting Business Rules
- Look for validation functions, guard clauses, assertion statements
- Check error messages — they often describe what went wrong in business terms
- Examine test assertions — they encode expected behavior

### Finding API Contracts
- Route registrations (Express: `app.get()`, FastAPI: `@app.get()`, Go: `mux.HandleFunc()`)
- OpenAPI/Swagger files if present
- Request validation schemas (Joi, Zod, Pydantic, struct tags)
- CLI flag/argument definitions (cobra, argparse, yargs)

### Detecting Data Models
- ORM model definitions (Prisma, SQLAlchemy, GORM, TypeORM)
- Migration files (in chronological order)
- Type/interface definitions for core domain objects
- Database seed files

---

## Edge Cases

| Scenario | Handling |
|----------|----------|
| Project has no README or documentation | Note this in "Known Gaps"; infer purpose from code |
| Monorepo with multiple services | Ask user which service(s) to analyze; produce one SPEC per service or a unified SPEC with clear boundaries |
| Project uses code generation | Document the generated code's purpose but focus on the source of truth (schemas, proto files, templates) |
| Legacy project with mixed patterns | Document all observed patterns, note inconsistencies in "Known Gaps" |
| Project is a library (no runtime) | Focus on exported API surface, type contracts, and usage patterns from tests |
| Incomplete or broken code | Document what exists, mark broken/incomplete areas explicitly |
| Project >1000 files | Start with entry points and trace key flows; don't exhaustively read every file |
| Multiple languages in one repo | Document each language's role and how they interact |

---

## Quality Criteria

A good reverse-engineered SPEC should pass these checks:

- [ ] A developer unfamiliar with the project could understand its purpose in 60 seconds
- [ ] The tech stack section is complete enough to set up a dev environment
- [ ] API contracts are specific enough to write a client against
- [ ] Data models are complete enough to recreate the schema
- [ ] Business rules are explicit (not buried in "see code")
- [ ] Known gaps are honestly listed (don't invent what you can't determine)
- [ ] The SPEC matches the actual code (not aspirational documentation)

---

## Anti-Patterns to Avoid

- **Don't invent intent.** If you can't determine WHY something exists, say so. Don't fabricate rationale.
- **Don't copy code into the SPEC.** Describe behavior and contracts, don't paste implementations.
- **Don't include transient state.** The SPEC describes the system's design, not its current runtime state.
- **Don't over-specify internals.** Focus on boundaries, contracts, and behavior. Internal implementation details belong in code comments, not specs.
- **Don't assume the README is accurate.** READMEs often lag behind code. Verify claims against actual implementation.
