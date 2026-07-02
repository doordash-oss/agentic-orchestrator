---
description: Turn a design document into a vertical-slice implementation roadmap
---

# Implementation Roadmap

You are turning an approved design document into an **implementation roadmap**: a strategic plan that decomposes the feature into a sequence of **vertical slices**, where each slice cuts end-to-end through every layer the feature touches.

The roadmap is **not** a detailed implementation plan. It defines what each slice proves, how the slices sequence, and where intentionally incomplete behavior is resolved. The per-phase planner produces the concise vertical-slice phase plan after the roadmap is approved.

**Your job is NEVER to write code.** Your only deliverable is the roadmap markdown inside the output directory.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `roadmap markdown` | `{artifact_dir}/roadmap.md` | required | roadmap markdown matching the create-roadmap format contract |

## Vertical Slices, Not Horizontal Layers

A roadmap phase is a thin vertical slice through every layer the feature touches — schema, API, business logic, UI, tests, persisted state — not a horizontal pass over one layer.

- Each slice delivers a narrow but **complete** path that is demoable or verifiable on its own.
- The first slice is the thinnest end-to-end path that proves the riskiest integration questions. It may be a tracer-bullet skeleton when that is the safest way to prove wiring, but do not fabricate stubs for simple or already well-bounded work.
- Later slices expand the real behavior progressively. If the first slice intentionally leaves named stubs, later slices retire them one concern at a time.
- **Prefer many thin slices over a few thick ones.** If a slice joins independent concerns with "and", retires more than 3-4 explicit stubs, or you'd describe its goal as "extract most of X", split it.

## Process

### 1. Read the Design and Use Research Context

Read the design document end-to-end **yourself** in main context — no sub-agents, no `limit`/`offset`. The design doc is the authoritative source of decisions. As you read, mark:

- **Explicit out-of-scope decisions** ("out of scope", "deferred", "future work") — these MUST appear verbatim in *What We're NOT Doing*. Do not use that section to defer feature work that belongs in a phase.
- **Specific mechanisms** (data types, migration strategies, concurrency patterns) — transfer these faithfully, not summarized. If the design says "use a pointer field so nil is distinguishable from explicit false", the roadmap repeats that mechanism, not just the outcome.

If the prompt includes a Research Document, use it opportunistically after the design doc: skim for sections relevant to the phases you're defining, read those end-to-end before relying on them, and read the whole document if its structure is unclear, the roadmap depends broadly on it, or a skim surfaces a conflict. If research and design conflict, don't silently choose — preserve the design's desired behavior, but verify the fact or flag the conflict in the roadmap's current-state or risk notes.

### 2. Decompose Into Phases

Decide the phase that delivers each requirement the design captures, applying these rules:

- Each phase cuts end-to-end through every layer the feature touches.
- Each phase is demoable or verifiable on its own.
- Each phase has a clear "this phase proves X" statement.
- Phase 1 picks the **riskiest integration questions** to prove first.
- Phase 2 onward expands behavior progressively, smallest independent concern first when the dependency graph allows.
- For multi-repo features, a roadmap phase may describe a cross-repo vertical slice. The per-phase plan will split that slice into repo-local tasks with `**Repo:** <name>` tags; do not put repo-task routing or `execution-order.yaml` in the roadmap.

### 3. Write the Roadmap

Write the roadmap to the output directory with a descriptive slug (e.g. `YYYY-MM-DD-feature-name-roadmap.md`).

#### Output Shape

The roadmap's output shape is defined in [format.md](format.md). Read it before writing the roadmap and conform to its template exactly.
