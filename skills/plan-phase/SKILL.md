---
description: Write a vertical-slice plan for a single roadmap phase
---

# Per-Phase Implementation Plan

You are taking one approved roadmap phase — a single vertical slice — and turning it into a concise set of behavior-centered tasks that the implementer can execute. The roadmap owns feature slicing; this plan turns that phase into AFK/HITL tasks, acceptance criteria, and verification.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `phase plan markdown` | `{artifact_dir}/phase-plan.md` | required | phase plan markdown matching the plan-phase format contract |

## Process

### 1. Read the Roadmap

Read the approved roadmap end-to-end. Focus on **Phase N** (the phase number named in the prompt).

### 2. Explore the Codebase Tactically (optional)

Use sub-agents (`codebase-locator`, `codebase-analyzer`, `codebase-pattern-finder`) for **targeted** lookups only. You are not re-discovering the architecture. You are confirming:

- The current state of files this phase touches.
- Existing patterns to follow for the new code (so the slice fits naturally).
- Existing test patterns to model your tests after.

### 3. Draft Vertical Tasks

Break the phase into vertical tasks. Each task should deliver a narrow, complete behavior within one repo. For multi-repo features, the phase as a whole may span repos, but each `### Task N:` is repo-local and declares exactly one `**Repo:** <name>` tag. Express cross-repo behavior as multiple repo-tagged tasks plus top-level verification, not as a multi-repo task tag.

Slices may be 'HITL' or 'AFK'. HITL slices require human interaction, such as an architectural decision or a design review. AFK slices can be implemented and merged without human interaction. Prefer AFK over HITL where possible.

<vertical-slice-rules>
- Each task delivers a narrow but complete repo-local behavior.
- A completed task is demoable or verifiable through its acceptance criteria.
- Prefer many thin tasks over few thick ones.
- If a roadmap-assigned deliverable does not fit this phase, stop and escalate for roadmap revision; do not create a plan-level deferral.
</vertical-slice-rules>

### 4. What to produce

A concise description of each vertical slice. Describe the end-to-end behavior, not layer-by-layer implementation.

Avoid specific file paths or code snippets — they go stale fast. Exception: if a prototype produced a snippet that encodes a decision more precisely than prose can (state machine, reducer, schema, type shape), inline it here and note briefly that it came from a prototype. Trim to the decision-rich parts — not a working demo, just the important bits.

Write the plan to the output directory with a descriptive slug (e.g. `YYYY-MM-DD-phase-NN-plan.md`).

For multi-repo features, every `### Task N:` heading **must** be followed by a `**Repo:** <name>` tag whose value is in `Feature.Repos`. The unified phase implementer reads these tags as the single source of truth for which repos this phase touches and which sub-agent gets each Task. Single-repo features may omit tags (every Task implicitly belongs to the only repo); a single-repo plan may still tag Tasks for clarity but must not mix tagged and untagged Tasks. There is no separate `execution-order.yaml`.

Do not add a grounding table, file inventory, stub inventory, testing strategy section, or deferrals section. Exact file selection, code-level grounding, and implementation ceremony belong to the implementer.

## Plan Template

The plan's output shape is defined in [format.md](format.md). Read it before writing the plan and conform to its template exactly. Every section is mandatory unless its heading is annotated conditional; never drop, reorder, or rename a section.
