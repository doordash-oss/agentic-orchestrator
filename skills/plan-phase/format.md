# Per-Phase Plan Format

This file defines the **output contract** for a per-phase implementation plan. It is shared by the skill that creates phase plans (`plan-phase`) and the skill that revises them (`revise-phase-plan`) so the document shape is single-sourced.

This file describes what a phase plan **looks like**. It deliberately does **not** prescribe how to author one (`plan-phase/SKILL.md` owns that) or how to edit one (`revise-phase-plan/SKILL.md` owns that).

## Output Files

Every phase emits one file in the plan directory:

1. `<slug>.md` — the implementation plan markdown described below.

## Per-Task Repo Tag

For multi-repo features, every `### Task N:` heading is followed by a `**Repo:** <name>` line that names the single repo this task touches. Single-repo features may omit repo tags entirely; if they use tags, every task must be tagged consistently.

## Plan Metadata

Every newly-authored phase plan includes a mandatory top-level `## Metadata` section before `## Overview`. The section contains the plan-level `**Frontend:** true|false` field:

- `true` means this phase adds or changes a user-facing UI surface. A frontend phase must include at least one real checklist item under `### Visual Evidence`; it must not use `None required`.
- `false` means this phase does not add or change a user-facing UI surface. Non-UI phases may use `None required: <reason>` under `### Visual Evidence` when no rendered surface is meaningful.

The harness reads this field as the authoritative UI signal for the phase. Legacy plans with no `## Metadata` section or no `**Frontend:**` field are interpreted as `frontend: false` for compatibility, but new plans must write the field explicitly.

## Markdown Template

Use this skeleton when emitting a phase plan. Section headings must match exactly so downstream section-matching (sticky-approval `frozen_sections`, validator routing) works. Every section below is mandatory unless its heading is annotated conditional (e.g. *tracer-bullet phases only*); never drop, reorder, or rename a section.

````markdown
# Phase N: [Slice Name] — Implementation Plan

## Metadata

**Frontend:** true|false

## Overview

[What this phase accomplishes and why.]

## Tasks

### Task N: [Name of task]

**Repo:** <name> <!-- multi-repo features only; optional for single-repo features -->

#### What to build

A concise description of this vertical slice. Describe the end-to-end behavior, not layer-by-layer implementation.

Avoid specific file paths or code snippets — they go stale fast. Exception: if a prototype produced a snippet that encodes a decision more precisely than prose can (state machine, reducer, schema, type shape), inline it here and note briefly that it came from a prototype. Trim to the decision-rich parts — not a working demo, just the important bits.

#### Acceptance criteria

- [ ] Criterion 1
- [ ] Criterion 2
- [ ] Criterion 3

#### Blocked by

- A reference to the blocking ticket (if any)

Or "None - can start immediately" if no blockers.

## Success Criteria

### Automated Verification

- [ ] Build passes: `go build ./...`
- [ ] Tests pass: `go test ./... -race -short`
- [ ] Lint passes: `make lint`

Each bullet must contain the **complete executable command** in backticks, in `description: command` order (description first, command last). The contract extractor reads only the bullet line, so don't reference a command name and put the real invocation in a fenced block elsewhere.

### Manual Verification

- [ ] [Manual check description, no backticks.]

Use `- [ ] None required: <reason>` only when the phase has no meaningful manual verification surface.

### Visual Evidence

- [ ] [Visual artifact requirement, such as a screenshot path or capture description.]

Use `- [ ] None required: <reason>` only when the phase has no meaningful rendered surface to capture and `**Frontend:** false`. If `**Frontend:** true`, include at least one real visual evidence checklist item. Keep visual evidence requirements here at the phase level; do not add per-task visual evidence sections.

### Behavioral Evidence

- [ ] [Behavioral artifact requirement, such as a transcript, command log, or recording description.]

Use `- [ ] None required: <reason>` only when the phase has no meaningful primary user journey artifact beyond automated verification. Keep behavioral evidence requirements here at the phase level; do not add per-task behavioral evidence sections.
````
