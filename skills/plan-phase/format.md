# Per-Phase Plan Format

This file defines the **output contract** for a per-phase implementation plan. It is shared by the skill that creates phase plans (`plan-phase`) and the skill that revises them (`revise-phase-plan`) so the document shape is single-sourced.

This file describes what a phase plan **looks like**. It deliberately does **not** prescribe how to author one (`plan-phase/SKILL.md` owns that) or how to edit one (`revise-phase-plan/SKILL.md` owns that).

## Output Files

Every phase emits one file in the plan directory:

1. `phase-plan.md` — the implementation plan markdown described below.

## Per-Task Repo Tag

For multi-repo features, every `### Task N:` heading is followed by a `**Repo:** <name>` line that names the single repo this task touches. Single-repo features may omit repo tags entirely; if they use tags, every task must be tagged consistently.

## Plan Metadata

Every newly-authored phase plan includes a top-level `## Metadata` section before `## Overview`. The section contains the plan-level `**Frontend:** true|false` field: `true` when this phase adds or changes a user-facing UI surface, `false` otherwise. The harness reads this field as the UI signal for the phase. Plans without the field are read as `frontend: false`.

## Markdown Template

Use this skeleton when emitting a phase plan. Keep the headings so reviewers and revisions can find each part.

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

#### Blocked by

- A reference to the blocking task (if any)

Or "None - can start immediately" if no blockers.

## Verification

[How a reader confirms this phase works: the commands to run and what to look at. Write it for a person, in prose and command lines. When the phase spans several repos, say which repo each command runs in.]

Run in `api`:

```bash
go test ./...
```

Then start the app and check that [the screen, output, or behavior to look at, and what it should show].
````

Write commands exactly as they would be typed in a shell, from the root of the repo they belong to. Keep the section short: the checks that prove the acceptance criteria, not a restatement of them.
