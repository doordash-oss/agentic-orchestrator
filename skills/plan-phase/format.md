# Per-Phase Plan Format

This file defines the **output contract** for a per-phase implementation plan. It is shared by the skill that creates phase plans (`plan-phase`) and the skill that revises them (`revise-phase-plan`) so the document shape is single-sourced.

This file describes what a phase plan **looks like**. It deliberately does **not** prescribe how to author one (`plan-phase/SKILL.md` owns that) or how to edit one (`revise-phase-plan/SKILL.md` owns that).

## Output Files

Every phase emits one file in the plan directory:

1. `phase-plan.md` — the implementation plan markdown described below.

## Per-Task Repo Tag

For multi-repo features, every `### Task N:` heading is followed by a `**Repo:** <name>` line that names the single repo this task touches. Single-repo features may omit repo tags entirely; if they use tags, every task must be tagged consistently.

## Markdown Template

Use this skeleton when emitting a phase plan. Section headings must match exactly so downstream section-matching (sticky-approval `frozen_sections`, validator routing) works. Every section below is mandatory unless its heading is annotated conditional (e.g. *tracer-bullet phases only*); never drop, reorder, or rename a section.

````markdown
# Phase N: [Slice Name] — Implementation Plan

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

- [ ] [repo: <name>] Build passes: `go build ./...`
- [ ] [repo: <name>] Tests pass: `go test ./... -race -short`
- [ ] [repo: <name>] Lint passes: `make lint`

Each bullet must contain the **complete executable command** in backticks, in `description: command` order (description first, command last). The contract extractor reads only the bullet line, so don't reference a command name and put the real invocation in a fenced block elsewhere.

In a multi-repo phase, every bullet begins with `[repo: <name>]`, using a repository assigned to a phase task. A single-repo phase may omit the annotation. The harness executes each command from that repository root, so paths are repo-relative (`README.md`, `./internal/...`): never add `cd <repo>` or prefix a path with the repository name.

The harness automatically records each command's exit code, stdout, stderr, working directory, duration, and run metadata. Do not request a second manual or behavioral transcript of the same command.

Use `- [ ] None required: <reason>` only when the phase has no meaningful executable verification, such as a prose-only documentation change whose correctness requires semantic or rendered review.

When a command needs an external login, credential, device, service, or
permission, declare the capability and a safe non-mutating probe on that same
line before the final command:

- [ ] [repo: api] Protected integration [agentico capability: Okta session; probe: okta auth status]: `make test-integration`

Do not add capability metadata based only on an expected error message. The
probe must directly answer whether the prerequisite is currently available.

### Manual Verification

- [ ] [One consolidated semantic review requirement, no backticks.]

Use at most one requirement. It is reserved for irreducible semantic judgment and must not restate automated, visual, or behavioral checks. Use `- [ ] None required: <reason>` when no such judgment is needed.

### Visual Evidence

- [ ] [Visual artifact requirement, such as a screenshot path or capture description.]

Use `- [ ] None required: <reason>` only when the phase has no meaningful rendered surface to capture. Keep visual evidence requirements here at the phase level; do not add per-task visual evidence sections.

Do not request a screenshot merely to prove an invariant already covered by an automated command.

### Behavioral Evidence

- [ ] [One consolidated primary-journey artifact, such as a trace, recording, or interaction log.]

Use at most one requirement. It may cover a short checklist within one artifact, but must not become several files. Use `- [ ] None required: <reason>` only when the phase has no meaningful primary user journey artifact beyond automated verification. Keep behavioral evidence requirements here at the phase level; do not add per-task behavioral evidence sections.

Do not request command transcripts here; command results already retain stdout, stderr, and run metadata.
````
