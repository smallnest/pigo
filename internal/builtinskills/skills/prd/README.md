# PRD Generator Skill

Generate structured Product Requirements Documents (PRD) for new features. Focused solely on producing a clear, implementable PRD — Issue decomposition and technical design are handled by separate skills.

## Features

- Asks 3-5 clarifying questions with lettered options for quick iteration
- Generates a well-structured PRD with user stories, numbered functional requirements, non-goals, success metrics, and more
- Enforces verifiable acceptance criteria (observable / testable / verifiable)
- Supports user review and adjustment before saving
- Saves output to `tasks/prd-[feature-name].md`
- Bilingual (Chinese & English) edge case handling

## Workflow

The PRD skill is the first step in a three-stage pipeline:

| Stage | Skill | Purpose |
|-------|-------|---------|
| 1. Requirements | `/prd` (this skill) | Define *what* to build |
| 2. Technical design (optional) | `/prd-to-spec` | Define *how* to build it |
| 3. Decomposition | `/to-issues` | Break into implementable tickets (GitHub / Local / Baidu iCafe) |

After a PRD is confirmed, run `/prd-to-spec` for complex features, then `/to-issues` — or go straight to `/to-issues`.

## Usage

Trigger with prompts like:

- "create a prd for..."
- "write prd for..."
- "写PRD"
- "需求文档"
- "需求分析"

## Files

- `SKILL.md` — Skill definition and instructions
- `test-prompts.json` — Test prompts for validation

## Attribution

This skill is adapted from [ralph/skills/prd](https://github.com/snarktank/ralph/tree/main/skills/prd).
