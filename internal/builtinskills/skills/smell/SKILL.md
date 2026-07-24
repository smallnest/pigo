---
name: smell
description: "Detect software architecture bad smells, algorithmic complexity hotspots, and anti-patterns in a codebase. Produces a detailed markdown report identifying violations of architectural principles, design patterns, code quality, and performance complexity. Triggers on: smell, code smell, architecture smell, find anti-patterns, detect bad smells, complexity analysis, 代码坏味道, 架构坏味道, 反模式, 找出坏味道, 复杂度分析."
user-invocable: true
---

# Smell — Architecture Bad Smell Detector

Analyze a codebase to find violations of software architecture principles, anti-patterns, code "bad smells," and algorithmic complexity hotspots. Produce a comprehensive, actionable markdown report.

**Knowledge base:** This skill encodes architectural patterns, anti-patterns, code smells, and algorithmic complexity heuristics drawn from industry research and practice, including the classic code smells catalog by Martin Fowler / Kent Beck (as organized on refactoring.guru: Bloaters, Object-Orientation Abusers, Change Preventers, Dispensables, Couplers).

---

## The Job

1. Understand the scope — ask what part of the project to analyze (full project, specific module, or recent changes)
2. Scan the codebase using `find`, `grep`, and `Agent` (Explore subagent) to gather evidence
3. Identify architectural smells and anti-patterns
4. Generate a detailed markdown report saved to `tasks/smell-report-[timestamp].md`
5. Present a summary of findings to the user

---

## Step 1: Scope Clarification

Ask the user:

```
What scope should I analyze?
  A. Entire project (thorough, may take time)
  B. Specific module/directory: [please specify]
  C. Only recently changed files (git diff)
  D. Only architectural-level issues (skip low-level code smells)
```

If the user doesn't specify, default to option A for small projects (< 100 files) or C for large projects.

---

## Step 2: Evidence Gathering

**Use the Explore subagent** (`Agent` with `subagent_type: "Explore"`) to scan the codebase for architectural patterns and anti-patterns. Run multiple parallel explorations:

### Exploration Commands

Run these in parallel to gather evidence efficiently:

1. **Project Structure Scan:** Map the directory tree, identify the architectural style (layered, modular monolith, microservices, etc.)
2. **Dependency Analysis:** Find import/include patterns, check for circular dependencies, identify coupling hotspots
3. **Module/Component Scan:** Identify God Objects (files > 500 lines), check cohesion, check single responsibility violations
4. **Pattern Detection:** Look for known anti-pattern signatures (static cling, service locator abuse, leaky abstractions)
5. **Testing Scan:** Check test coverage patterns, test file locations, test-to-code ratios
6. **Naming & Clarity Scan:** Flag misleading names, overly generic names (Manager, Helper, Util), inconsistent naming conventions
7. **Complexity Scan:** Detect algorithmic complexity hotspots — nested loops, N+1 queries, repeated scans, sort-in-loop, expensive recomputation in render paths

### Key Heuristics

| Category | Smell | Detection Heuristic |
|----------|-------|-------------------|
| **Architecture** | Big Ball of Mud | No clear directory structure; everything in root or one flat folder; no separation of concerns |
| **Architecture** | Violated Layer Boundaries | Inner layers importing outer layers; infrastructure code in domain/core layer |
| **Architecture** | Missing Architecture | No `src/`, `lib/`, `core/` separation; SQL inline with UI code; HTTP handlers mixed with business logic |
| **Architecture** | Distributed Monolith | Microservices sharing a database; services that can't deploy independently |
| **Architecture** | Anemic Domain Model | Model/entity classes with only getters/setters and no behavior; all logic in services |
| **Architecture** | CQRS Without Need | Separate read/write models for simple CRUD; unnecessary complexity |
| **Architecture** | Over-Layered Architecture | Excessive layers/tiers that add pass-through code with no real value |
| **Architecture** | Over-Abstraction | So many indirections/interfaces/generics that you get lost following the code |
| **Architecture** | Futuristic Architecture | Speculative flexibility for requirements that may never come (predicting the future) |
| **Architecture** | Technology-Enthusiast Architecture | Shiny/unproven tech adopted in production because it's new, not because it fits |
| **Architecture** | Overkill Architecture | Heavyweight architecture/tech thrown at a simple problem |
| **Architecture** | Cloud/Visio Architecture | Diagrams disconnected from the actual code and runtime reality |
| **Coupling** | Circular Dependencies | Module A imports B, B imports A; detected via import graph analysis |
| **Coupling** | Content Coupling | One module directly accesses another's internal/private members |
| **Coupling** | Common Coupling | Excessive global variables/shared mutable state; singleton abuse |
| **Coupling** | Stamp Coupling | Passing large data structures when only a few fields are needed |
| **Cohesion** | God Object | Single class/module > 500 lines; > 20 public methods; handles unrelated concerns |
| **Cohesion** | Shotgun Surgery | A single change requires touching 5+ files across unrelated modules |
| **Cohesion** | Feature Envy | Method calls foreign class methods more than its own class methods |
| **Cohesion** | Data Clumps | Same group of 3+ parameters appearing together in multiple method signatures |
| **Design** | Leaky Abstractions | Implementation details (DB queries, HTTP calls) exposed through interfaces |
| **Design** | Static Cling | Excessive use of static methods; static state that prevents testability |
| **Design** | Service Locator Abuse | DI container passed around instead of proper constructor injection |
| **Design** | Violated SOLID | SRP violations, OCP violations (switch/if-else chains on types), ISP violations (fat interfaces) |
| **Design** | Switch Statements | Same `switch`/if-else chain on a type code appearing in multiple places; should be polymorphism |
| **Design** | Refused Bequest | Subclass inherits methods/fields it doesn't use or overrides them to throw/no-op |
| **Design** | Alternative Classes w/ Different Interfaces | Two classes do the same thing but have differently-named methods |
| **Design** | Parallel Inheritance Hierarchies | Creating a subclass in one hierarchy forces a matching subclass in another |
| **Design** | Speculative Generality | Unused abstract classes, hooks, params, or generics "for future needs" (YAGNI) |
| **Design** | Incomplete Library Class | Wrapping/patching a third-party class because it lacks needed methods |
| **Cohesion** | Divergent Change | One module changed for many unrelated reasons (opposite of Shotgun Surgery) |
| **Cohesion** | Data Class | Class with only fields + getters/setters, no behavior (anemic data bag) |
| **Cohesion** | Lazy Class | Class/module that does too little to justify its existence |
| **Coupling** | Inappropriate Intimacy | Two classes access each other's private/internal parts too much |
| **Coupling** | Message Chains | Long call chains `a.getB().getC().getD()` (Law of Demeter violation) |
| **Coupling** | Middle Man | Class that only delegates every call to another class |
| **Code** | Temporary Field | Instance field set/used only in certain circumstances, empty otherwise |
| **Code** | Duplicated Code | Identical/similar logic appearing in 3+ places; copy-paste patterns |
| **Code** | Long Method | Methods > 50 lines; deep nesting (> 3 levels) |
| **Code** | Long Parameter List | Methods with > 4 parameters |
| **Code** | Primitive Obsession | Using strings/ints instead of domain types (e.g., `string email` instead of `Email` type) |
| **Code** | Magic Numbers/Strings | Hardcoded literals without named constants |
| **Code** | Comments as Deodorant | Excessive comments explaining bad code instead of refactoring |
| **Code** | Dead Code | Unused imports, unreachable code, commented-out blocks |
| **Testing** | No Tests | Modules with zero test coverage |
| **Testing** | Test-Implementation Coupling | Tests that assert internal implementation details instead of behavior |
| **Testing** | Slow Tests | Tests doing real I/O, database calls, network requests without mocking |
| **Naming** | Vague Names | `Manager`, `Handler`, `Processor`, `Helper`, `Util`, `Service`, `Data`, `Info` used excessively without context |
| **Naming** | Inconsistent Naming | Snake_case and camelCase mixed; different patterns for same concept |
| **Readability** | Deep Nesting (Arrow Anti-Pattern) | Loops/conditionals nested > 3 levels deep; rightward-drifting "arrow" shape hard to trace |
| **Complexity** | Nested Loops (O(n^2)+) | Loop inside loop; forEach inside for; map inside map; nested iteration suggesting polynomial complexity |
| **Complexity** | Repeated Linear Scan | `includes()`/`indexOf()`/`.find()` inside a loop; O(n*m) membership check on list instead of Set/Map |
| **Complexity** | Sort-in-Loop | `.sort()` or `sorted()` called inside iterative code; repeated O(n log n) when sort-once suffices |
| **Complexity** | N+1 Query Pattern | Database/API/HTTP call inside a loop; `fetch`/`query`/`execute`/`findMany` per iteration instead of batch |
| **Complexity** | Render-Path Recompute | `.filter().map().sort()` chains in component render body; expensive transforms without memoization |
| **Complexity** | Pairwise Comparison | Nested iteration comparing every element with every other; O(n^2) when sort+two-pointer would be O(n log n) |
| **Complexity** | Unnecessary Recompute | Same expensive computation repeated without caching; missing `useMemo`/`memo`/lazy eval |
| **Complexity** | Wrong Data Structure | Array used where Set/Map would give O(1) lookup; List where Queue/Heap/Stack is natural fit |

---

## Step 3: Report Generation

Generate the report in this structure:

```markdown
# Architecture Smell Report

**Project:** [project-name]
**Scope:** [scope description]
**Date:** [date]
**Analyzer:** smell skill (Ducc)

---

## Executive Summary

[2-3 paragraph summary: architectural style detected, overall health assessment, and top 3-5 critical issues]

---

## Architectural Style Detected

[Identify the architectural style: Layered, Modular Monolith, Microservices, Hexagonal, Clean Architecture, or Big Ball of Mud]

### Style Expectations vs. Reality

| Expectation | Reality | Status |
|-------------|---------|--------|
| [e.g., Clear layer separation] | [what was found] | ✅/⚠️/🔴 |

---

## Findings by Category

### 🔴 Critical Issues (Must Fix)

[Issues that fundamentally undermine architecture]

### 🟡 Warnings (Should Fix)

[Issues that degrade maintainability but don't block function]

### 🔵 Suggestions (Nice to Fix)

[Minor improvements that would increase quality]

---

## Detailed Findings

### Finding #1: [Title]

- **Category:** [Architecture/Coupling/Cohesion/Design/Code/Testing/Naming/Complexity]
- **Severity:** 🔴 Critical / 🟡 Warning / 🔵 Suggestion
- **Anti-Pattern:** [Name of anti-pattern]
- **Location:** [file:line references]
- **Principle Violated:** [SOLID/DRY/KISS/etc.]
- **Description:** [What was found and why it's a problem]
- **Evidence:** [Code snippet or structure description]
- **Recommendation:** [Specific fix, with refactoring approach]

---

## Dependency Graph Analysis

[Summary of module dependencies, circular dependencies found, coupling hotspots]

---

## Module Health Scorecard

| Module | Lines | God Object Risk | Coupling | Cohesion | Test Coverage | Health |
|--------|-------|----------------|----------|----------|---------------|--------|
| [name] | [N] | [Low/Med/High] | [Low/Med/High] | [Low/Med/High] | [% or N/A] | 🟢/🟡/🔴 |

---

## Smell Distribution

| Category | Count | Critical | Warning | Suggestion |
|----------|-------|----------|---------|------------|
| Architecture | [N] | [N] | [N] | [N] |
| Coupling | [N] | [N] | [N] | [N] |
| Cohesion | [N] | [N] | [N] | [N] |
| Design | [N] | [N] | [N] | [N] |
| Code | [N] | [N] | [N] | [N] |
| Testing | [N] | [N] | [N] | [N] |
| Naming | [N] | [N] | [N] | [N] |
| Complexity | [N] | [N] | [N] | [N] |

---

## Refactoring Roadmap

### Immediate Actions (This Sprint)
1. [Actionable fix 1]
2. [Actionable fix 2]

### Short-Term (1-3 Months)
1. [Structural improvement 1]
2. [Structural improvement 2]

### Long-Term (3-12 Months)
1. [Architectural transformation 1]
2. [Architectural transformation 2]

---

## Appendix: Anti-Pattern Reference

[A condensed reference of anti-patterns checked, with brief descriptions]
```

---

## Step 4: Save and Present

Save the report to `tasks/smell-report-[YYYY-MM-DD-HHmm].md` and present a brief summary to the user.

---

## Anti-Pattern Knowledge Base

This section documents the architectural anti-patterns and bad smells the skill knows about.

### Architectural Anti-Patterns

#### Big Ball of Mud
The most common de-facto architecture. A haphazardly structured, sprawling system with no perceivable architecture. Characterized by:
- Promiscuous sharing of information between distant elements
- Global or duplicated important state
- Structure eroded beyond recognition or never defined
- Repeated expedient repair ("duct tape and bailing wire")
- Forces: Time pressure, cost, inexperience, complexity, change, scale
- **Remedy:** Define architecture boundaries, refactor incrementally, apply SHEARING LAYERS, KEEP IT WORKING

#### Distributed Monolith
Microservices that must be deployed together. Symptoms:
- Services share a database
- Synchronous chains of service calls
- Changes require coordinated deployments
- **Remedy:** Decouple data stores, introduce async messaging, enforce bounded contexts

#### Anemic Domain Model
Domain objects with only getters/setters (data bags), all logic in services. Violates:
- "Tell, Don't Ask" principle
- Rich Domain Model pattern from DDD
- **Remedy:** Move behavior into domain objects, use domain services only for cross-aggregate operations

#### God Object
A class that knows too much or does too much. Characteristics:
- > 500 lines or > 20 public methods
- Handles unrelated concerns
- Difficult to test in isolation
- Single Responsibility Principle violation
- **Remedy:** Extract cohesive groups of methods into dedicated classes

#### Leaky Abstractions
Abstractions that expose implementation details. Signs:
- Interface methods named after implementation (e.g., `SaveToPostgres`, `FetchFromRedis`)
- Consumers catching implementation-specific exceptions
- Configuration details exposed through abstractions
- **Remedy:** Design interfaces from the consumer's perspective, hide implementation details

#### Static Cling
Excessive use of static methods/state. Problems:
- Untestable (can't mock static calls)
- Hidden dependencies
- Thread-safety issues with static state
- **Remedy:** Use dependency injection, convert stateless statics to instance methods

#### Service Locator Abuse
Using a service locator instead of dependency injection. Issues:
- Hidden dependencies (dependencies not visible in constructor)
- Runtime errors instead of compile-time errors
- Testing difficulty
- **Remedy:** Use constructor injection, register dependencies at composition root

#### Violated Layer Boundaries (Clean/Onion/Hexagonal Architecture)
In layered architectures:
- **Clean Architecture:** Outer layers (frameworks) leaking into inner layers (use cases, entities)
- **Onion Architecture:** Infrastructure concerns in domain core
- **Hexagonal Architecture:** Business logic coupled to specific adapters instead of ports
- **Remedy:** Apply dependency inversion, define clear port interfaces

#### CQRS Overuse
Applying CQRS to simple CRUD. Signs:
- Separate read/write models for trivial data access
- Event sourcing when events don't add business value
- Unnecessary complexity
- **Remedy:** Use CQRS only when read/write models genuinely differ or have different scaling needs

#### Vertical Slice Contamination
In Vertical Slice Architecture:
- Cross-slice coupling (one feature directly calling another)
- Shared service classes undermining slice independence
- **Remedy:** Use events/messages for cross-slice communication, duplicate simple logic if needed

### Top Ten Software Architecture Mistakes

A set of architecture-level anti-patterns describing over- and under-engineering. The common thread: **architecture disconnected from real needs and reality.** The opposite extreme (too little architecture) is equally a smell.

#### Over-Layered / Multitier Architecture
"Layers on layers on layers." Adding tiers beyond what the problem needs:
- Each layer just forwards calls to the next with no transformation or value
- Simple read requires touching 6+ classes across 4 layers
- **Remedy:** Collapse pass-through layers; keep only layers that carry real responsibility

#### Over-Abstraction
Abstraction piled on until the code is impossible to follow:
- Excessive interfaces, generics, factories, and indirection for single implementations
- You can't tell what actually runs without stepping through many hops
- **Remedy:** Inline single-implementation abstractions; abstract only at real variation points (rule of three)

#### Futuristic Architecture
Solution built for imagined future requirements that no one can actually predict:
- Extensibility points, plugin systems, config knobs nothing uses
- Most speculative flexibility is wasted effort — closely related to Speculative Generality and YAGNI
- **Remedy:** Build for today's known requirements; add flexibility when a real second case arrives

#### Technology-Enthusiast Architecture
New/shiny technology put into production because the architect liked it:
- Unproven tech adopted without validating it fits the problem or scales
- Chasing trends over stability
- **Remedy:** Evaluate tech against actual requirements; prefer proven tools; prototype before committing

#### Overkill Architecture
A simple problem solved with a disproportionate amount of architecture and technology:
- Microservices, event sourcing, k8s for a CRUD app with a handful of users
- **Remedy:** Match architecture weight to problem size (KISS); start simple, evolve when justified

#### Cloud / Visio Architecture
"Architecture" that exists only in nice diagrams, disconnected from the code and runtime reality:
- Diagrams don't match what's actually deployed; boxes and arrows with no code correspondence
- **Remedy:** Keep architecture docs grounded in and verified against the real system

> **Note on the opposite extreme:** total *lack* of architecture (no boundaries, no structure) is equally a smell — see [Big Ball of Mud](#big-ball-of-mud) and Missing Architecture. Both under- and over-engineering are failures.

### Coupling & Cohesion Smells

#### Circular Dependencies
Module A → Module B → Module A. Detected via:
- Import graph analysis
- "Cannot access before initialization" errors
- **Remedy:** Extract shared interface/common module, apply dependency inversion

#### Content Coupling
One module directly modifying another's internal state. Signs:
- Direct field access across module boundaries
- `friend`/package-private abuse
- **Remedy:** Use public APIs, encapsulate internal state

#### Common Coupling (Global State)
Multiple modules depending on shared global mutable state:
- Global variables, singletons with mutable state
- Ambient context (e.g., `CurrentUser` static property)
- **Remedy:** Parameterize, use dependency injection, make state explicit

#### Stamp Coupling
Passing entire data structures when only a few fields needed:
- Functions receiving large DTOs but using one field
- **Remedy:** Create focused parameters or smaller interfaces (ISP)

#### Shotgun Surgery
A single change requires modifications across many files:
- Adding a field touches 5+ files in different modules
- **Remedy:** Consolidate related behavior, apply Single Responsibility

#### Feature Envy
A method that uses another class's methods more than its own:
- Method calls `other.foo()`, `other.bar()`, `other.baz()` with few self-calls
- **Remedy:** Move the method to the class it envies

#### Data Clumps
Same group of fields appearing together in multiple places:
- `(street, city, zip)` appearing in 5 method signatures
- **Remedy:** Extract into a value object

#### Divergent Change
One module/class is repeatedly changed for many *unrelated* reasons (the opposite of Shotgun Surgery):
- "I always change these three methods for DB changes, and those two for UI changes" in the same class
- **Remedy:** Split the class along its axes of change (Single Responsibility)

#### Inappropriate Intimacy
Two classes are too entangled with each other's internals:
- Reaching into another class's private fields, tight bidirectional references
- **Remedy:** Move methods/fields to the class they belong to, extract a shared class, or replace with delegation

#### Message Chains
Long navigation chains like `a.getB().getC().getD().doThing()`:
- Client coupled to the whole object graph; violates the Law of Demeter
- **Remedy:** Hide delegation — add a method on the first object that returns what the client needs

#### Middle Man
A class that delegates almost all of its work to another class:
- Most methods just forward calls; adds indirection without value
- **Remedy:** Remove the middle man and let clients talk to the real object (inline the delegation)

#### Parallel Inheritance Hierarchies
Every time you add a subclass to one hierarchy, you must add one to another:
- `Shape`/`ShapeRenderer`, `Employee`/`EmployeePermission` growing in lockstep
- **Remedy:** Merge hierarchies or make one hierarchy reference the other instead of mirroring it

### Code-Level Smells

#### Long Method
- Methods > 50 lines (or whatever suits the language)
- Deep nesting > 3 levels
- Multiple levels of abstraction mixed
- **Remedy:** Extract methods at same abstraction level, compose

#### Long Parameter List
- Methods with > 4 parameters
- Boolean flags controlling behavior
- **Remedy:** Introduce parameter object, split method, remove flag arguments

#### Duplicated Code
- Identical or near-identical logic in 3+ places
- Copy-paste with slight variations
- **Remedy:** Extract shared method, apply Template Method or Strategy pattern

#### Primitive Obsession
Using primitives instead of domain types:
- `string` for Email, PhoneNumber, URL
- `int` for Money, Age, Quantity
- `decimal` without Currency context
- **Remedy:** Create value objects with validation and behavior

#### Magic Numbers/Strings
- Hardcoded literals without explanation
- `if (status == 3)` instead of `if (status == Status.COMPLETED)`
- **Remedy:** Extract named constants or enums

#### Comments as Deodorant
- Comments that explain what code does (code should be self-documenting)
- Commented-out code blocks
- "TODO" comments accumulating without resolution
- **Remedy:** Refactor to make code clear, delete dead code, track TODOs as issues

#### Deep Nesting (Arrow Anti-Pattern)
Loops and conditionals nested so deeply the code drifts rightward into an "arrow" shape:
- `if { if { for { if { ... } } } }` — hard to trace which conditions hold at any point
- Usually > 3 levels of indentation in one function
- **Remedy:** Guard clauses / early returns, extract nested blocks into methods, invert conditions, replace conditional with polymorphism

#### Dead Code
- Unused imports, variables, functions
- Unreachable branches
- Commented-out code in version control
- **Remedy:** Delete it (git history preserves it if needed)

#### Data Class
A class that is only fields plus getters/setters, with no meaningful behavior:
- A "data bag" other classes reach into and manipulate from outside
- Closely related to Anemic Domain Model at the class level
- **Remedy:** Move the behavior that operates on the data into the class ("Tell, Don't Ask")

#### Lazy Class
A class/module that no longer does enough to justify its existence:
- Left over after refactoring, or an abstraction that never grew
- **Remedy:** Inline it into its caller or collapse the hierarchy

#### Speculative Generality
Abstractions, hooks, parameters, or generics added for hypothetical future needs:
- Unused abstract base classes, unused parameters, "just in case" configuration
- Violates YAGNI
- **Remedy:** Remove unused abstraction; add it when a real second use case appears

#### Temporary Field
An instance field that is only set/used in certain circumstances and empty otherwise:
- Fields populated only during one algorithm, confusing readers the rest of the time
- **Remedy:** Extract the field + the methods that use it into their own class (Extract Class / introduce a Method Object)

### Testing Smells

#### No Tests
- Modules with zero test coverage
- Business logic without unit tests
- **Remedy:** Write characterization tests first, then add behavior tests

#### Test-Implementation Coupling
- Tests asserting internal method calls, private state, or implementation details
- Tests breaking on refactoring without behavior changes
- **Remedy:** Test through public APIs, assert behavior not implementation

#### Test Environment Dependency
- Tests depending on file system, network, database, system clock without mocking
- Non-deterministic tests (flaky tests)
- **Remedy:** Use test doubles, control environment, use DI

### Complexity Smells (Algorithmic Anti-Patterns)

Complexity smells indicate code whose runtime grows inefficiently with input size. These are not mere "micro-optimizations" — they are algorithmic choices that cause real performance degradation at scale.

#### Nested Loops (O(n^2) and Worse)
Two or more loops nested inside each other, producing polynomial complexity.
- **Detection:** `for`/`while` inside another `for`/`while`; `forEach`/`map` inside `forEach`/`map`; loop containing another loop (any depth)
- **Impact:** O(n^2) for double-nested, O(n^3) for triple; explodes with moderate data sizes
- **Remedy:**
  - Build a Map/Set index for the inner collection → O(n+m)
  - Sort + two-pointer approach → O(n log n)
  - Group/bucket data before iterating
  - Sweep-line for interval/range problems
- **Correctness checks:** Does order matter? Are there duplicate keys? Is the original picking first/last/all matches?

#### N+1 Query Pattern
A database query, API call, or I/O operation inside a loop body.
- **Detection:** `fetch()`/`axios()`/`query()`/`execute()`/`findMany()`/`findOne()`/`findUnique()`/`select()`/`where()` inside any loop construct
- **Impact:** 1 + N round-trips instead of 1; network latency multiplied by item count
- **Remedy:**
  - Batch fetch by IDs: `SELECT * FROM x WHERE id IN (...)` then join in memory
  - Use ORM eager-loading / `include` / `preload` / DataLoader
  - Bulk API endpoints accepting arrays
  - Preserve: auth filters, tenancy isolation, ordering, pagination, error semantics
- **Correctness checks:** Don't fetch records the original per-item logic wouldn't authorize; preserve missing-record behavior

#### Repeated Linear Scan (Missing Index)
Linear search (`includes`, `indexOf`, `.find`, `in_array`) inside a loop, where a Set/Map would give O(1) lookup.
- **Detection:** `.includes()` / `.indexOf()` / `.find()` / `.findIndex()` / `in_array()` / `contains()` inside a loop body
- **Impact:** O(n*m) instead of O(n+m) — each iteration scans the entire collection
- **Remedy:** Build a `Set` (for membership) or `Map` (for key→value lookup) once before the loop
- **Correctness checks:** Does equality semantics change after Set conversion? JavaScript object identity vs. value equality; Python hashability

#### Sort-in-Loop
Sorting inside a loop body, repeating O(n log n) work unnecessarily.
- **Detection:** `.sort()` / `sorted()` / `sort()` inside any iterative block
- **Impact:** O(k * n log n) instead of O(n log n) — sort repeated k times
- **Remedy:**
  - Sort once outside the loop
  - Maintain a heap (PriorityQueue) if incremental top-K is needed
  - Use binary search/insertion into sorted collection
- **Correctness checks:** Is each intermediate sorted state externally observable? Does comparator depend on loop-local state?

#### Render-Path Recompute (UI Complexity)
Expensive data transformation (filter→map→sort chains) inside UI component render bodies, recomputed on every render.
- **Detection:** `.filter().map().sort().reduce()` chains inside React/Vue/Svelte component function bodies; inside `function Component()` or `const Component = () =>` in JSX/TSX
- **Impact:** Re-derivation on every state change even if inputs unchanged; jank with large collections
- **Remedy:**
  - `useMemo` / `computed` / `derived` with correct dependency arrays
  - Move derivation to selectors, loaders, or server-side
  - Virtualize long lists (windowing)
  - Stabilize callbacks and object props only when child renders are affected
- **Correctness checks:** Dependency arrays must include every semantic input; memoization must not hide mutations of mutable inputs

#### Pairwise Comparison
Comparing every element with every other element using double-nested iteration.
- **Detection:** Two nested loops iterating the same or similar collections, comparing pairs
- **Impact:** O(n^2) for pair matching, overlap detection, conflict checking, nearest-neighbor
- **Remedy:**
  - Sort + two-pointer for pair/range matching
  - Sweep-line for interval overlaps
  - Spatial hashing or grid bucketing for proximity
  - Union-find for connectivity
- **Correctness checks:** Order stability; tie-breaking in equality cases

#### Unnecessary Recompute (Missing Memoization)
Same pure computation repeated with same inputs without caching.
- **Detection:** Identical function calls with same arguments in hot paths; repeated expensive transforms; recursive calls without memoization
- **Impact:** Linear/polynomial wasted work; especially bad with recursive Fibonacci-style patterns (O(2^n) → O(n) with memo)
- **Remedy:** Add memoization/caching with proper invalidation; use `lru_cache`/`memoize`/`useMemo` as appropriate

#### Wrong Data Structure
Using a suboptimal data structure for the access pattern.
- **Detection:**
  - Array/List used for frequent membership tests → should be Set
  - Array/List used for key-value lookups → should be Map/Object
  - Array used as queue with `shift()`/`pop(0)` (O(n) per dequeue) → should use proper Queue
  - Sorted insertion into array (O(n) per insert) → should use Heap
- **Remedy:** Replace with the data structure whose complexity matches the access pattern:
  - Set → O(1) has/add/delete
  - Map → O(1) get/set
  - Heap → O(log n) push/pop for priority
  - Queue/Deque → O(1) enqueue/dequeue

#### What NOT to Flag
- **Cold paths:** Complexity that only runs on startup, config loading, or tiny N (< 100) is rarely worth fixing
- **Intentional tradeoffs:** Clear, readable O(n) code where O(n log n) would add complexity with no measurable gain
- **Already optimized:** Map/Set already in use; batch loading already implemented; memoization already present

### Design Principle Violations

#### SOLID Violations Checklist
- **S (SRP):** Class/module has multiple reasons to change → God Object smell
- **O (OCP):** switch/if-else chains on type codes → Strategy/Polymorphism needed
- **L (LSP):** Subclass changes behavior of base class unexpectedly → Check pre/post conditions
- **I (ISP):** Fat interfaces with methods clients don't use → Split interfaces
- **D (DIP):** High-level modules depending on low-level details → Introduce abstractions

#### Other Principle Violations
- **DRY Violation:** Same knowledge repeated in multiple places
- **KISS Violation:** Over-engineered solutions; premature abstractions
- **YAGNI Violation:** Code for hypothetical future requirements; unused abstractions

#### Object-Orientation Abusers (from Fowler / refactoring.guru)

##### Switch Statements (Type-Code Conditionals)
Repeated `switch`/if-else chains that branch on a type code or enum:
- The same conditional structure duplicated in several places
- Adding a new type forces editing every switch (OCP violation)
- **Remedy:** Replace conditional with polymorphism (Strategy/State), or Replace Type Code with Subclasses

##### Refused Bequest
A subclass inherits methods/fields it doesn't need:
- Overrides inherited methods to throw, no-op, or do something unrelated
- Signals the inheritance relationship is wrong
- **Remedy:** Push down unused members, or replace inheritance with delegation

##### Alternative Classes with Different Interfaces
Two classes perform the same role but expose differently-named methods:
- `sort()` vs `arrange()`, `getUser()` vs `fetchUser()` for interchangeable classes
- **Remedy:** Unify the interface (rename methods, extract a common superclass/interface)

##### Incomplete Library Class
A third-party/library class lacks methods you need and can't be modified:
- Scattered helper functions or copy-paste wrappers around the library
- **Remedy:** Introduce a Foreign Method or wrap it in an adapter/local extension class

> Other refactoring.guru smells are documented in their thematic sections above:
> **Divergent Change**, **Data Class**, **Lazy Class**, **Speculative Generality**,
> **Temporary Field**, **Parallel Inheritance Hierarchies**, **Inappropriate Intimacy**,
> **Message Chains**, and **Middle Man**.

---

## Edge Cases & Fallback

| Scenario | Handling |
|----------|----------|
| User doesn't specify scope | Default to recent changes (`git diff`) for repos > 200 files, full analysis otherwise |
| Project has no clear architecture | Report "Big Ball of Mud" with evidence, recommend incremental refactoring |
| Empty/monorepo project | Report that architecture analysis requires code; ask user to specify module |
| Language not supported | Report general structural observations; note language-specific checks are limited |
| Report file path conflicts | Append `-2`, `-3`, etc. to filename |
| User wants a quick check | Run only Critical-level scans, skip Code and Naming categories |
| User wants only one category | Focus analysis on that category, skip others |

---

## Report Output Example

```
🔍 Architecture Smell Analysis Complete

Project: goal-workflow
Style: Modular Monolith (with some layering violations)
Files Analyzed: 47
Health: 🟡 Fair

Critical: 3  |  Warnings: 6  |  Suggestions: 9

🔴 Critical Issues:
  1. Anemic Domain Model — `models/` classes have only getters/setters,
     all logic in `services/`. Violates DDD Rich Domain Model principle.
  2. N+1 Query Pattern — `services/order.ts:142` fetches user per order in loop;
     should batch-load users by IDs (O(n*m) → O(n+m)).
  3. Static Cling — `util/ApiClient.ts` uses all static methods,
     making consumer code untestable.

🟡 Warnings:
  1. God Object — `services/workflow.ts` at 847 lines handles too many concerns
  2. Nested Loop O(n^2) — `analytics.ts:89` pairwise comparison of events;
     sort+two-pointer would be O(n log n)
  3. Leaky Abstraction — `repositories/user.ts` exposes MongoDB query syntax
  4. Duplicated Code — validation logic duplicated across 4 controllers
  5. Circular Dependency — `auth` ↔ `user` modules depend on each other
  6. Magic Numbers — ~23 hardcoded values without named constants

Full report: tasks/smell-report-2026-05-27-1530.md
```