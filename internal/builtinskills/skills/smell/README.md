# Smell — Architecture Bad Smell Detector

Analyze a codebase to find violations of software architecture principles, anti-patterns, and code "bad smells." Produces a comprehensive, actionable markdown report.

## Features

- Scans project structure for architectural anti-patterns (Big Ball of Mud, Distributed Monolith, etc.)
- Detects coupling and cohesion issues (God Objects, circular dependencies, feature envy)
- Identifies design principle violations (SOLID, DRY, KISS, YAGNI)
- Finds code-level smells (Long Method, Primitive Obsession, Magic Numbers)
- Assesses testing health (missing tests, test-implementation coupling)
- Outputs a structured markdown report with severity levels and refactoring roadmap

## Knowledge Base

Built on architectural knowledge from:
- [awesome-software-architecture](https://github.com/mehdihadeli/awesome-software-architecture)
- Big Ball of Mud (Foote & Yoder, 1997)
- Clean Architecture, Onion Architecture, Hexagonal Architecture
- Domain-Driven Design, CQRS, Event-Driven Architecture
- SOLID, DRY, KISS, YAGNI, GRASP principles

## Usage

Trigger with prompts like:

- "smell" or "/smell"
- "find code smells"
- "detect architecture anti-patterns"
- "analyze architecture quality"
- "找出坏味道"
- "架构坏味道"
- "代码坏味道检测"
- "反模式分析"

## Files

- `SKILL.md` — Skill definition and comprehensive anti-pattern knowledge base
- `test-prompts.json` — Test prompts for validation