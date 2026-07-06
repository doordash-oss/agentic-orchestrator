# Roadmap Format

This file defines the **output contract** for an implementation roadmap. It is shared by the skill that creates roadmaps (`create-roadmap`) and the skill that revises them (`revise-roadmap`) so the document shape is single-sourced.

This file describes what a roadmap **looks like**. It deliberately does **not** prescribe how to author one (`create-roadmap/SKILL.md` owns that) or how to edit one (`revise-roadmap/SKILL.md` owns that).

## Required Sections

A roadmap MUST contain these top-level sections, in this order:

1. Overview
2. Current State Analysis
3. Human Decisions (table)
4. What We're NOT Doing
5. Architecture Approach
6. Phase 1..N: vertical-slice phases (one section per phase)
7. Overall Exit Criteria

If the feature genuinely cannot be sliced (a pure data migration, a single-file fix), the roadmap may collapse to a single phase. The phase still uses `## Phase 1: Title`; the system detects collapsed mode from phase count, not from a header decoration.

## Phase Shape

Every phase is a vertical slice. It MUST contain:

- **Goal** — one paragraph: the increment this phase delivers and why it comes next.
- **Proves** — the integration, behavior, migration, or product questions this phase settles.
- **Scope** — the high-level work included in this phase. Name touched repositories for multi-repo slices when that helps clarify ownership, but do not include per-file tasks.
- **Key Tests** — roadmap-level verification intent: the critical behavior, migration, regression, or integration checks the later phase plan must make concrete.

### Conditional Stub Sections

Stubs are a tactic, not a universal roadmap requirement.

- Phase 1 may include **Stub Inventory** only when it intentionally leaves named fake, skeletal, or placeholder behavior for later phases.
- Later phases may include **Retires Stubs** only when they retire named stubs from an earlier **Stub Inventory**.
- If the feature is simple, already well-bounded, or safer to implement without fake behavior, omit both sections.

A phase that joins independent concerns with "and", retires more than 3-4 explicit stubs, or whose goal reads as "extract most of X" is too large and should be split.

## Phase Header Rule

Use exactly `## Phase N: Title` — no `Phase 1/3`, no `(Collapsed)`, no decorations before the colon. Single-phase collapsed roadmaps still use `## Phase 1: Title`.

This rule matters operationally: section-matching downstream (sticky-approval `frozen_sections`, validator routing) keys off the exact heading text. Renames break section matching, invalidate sticky approvals, and force validators to re-evaluate axes they have already cleared.

## Body Constraints

- **No file-level implementation details.** No file paths, no line numbers, no code snippets. The roadmap is strategic; per-file details belong in the per-phase plan.
- **Target 2-5 pages total.** A roadmap longer than five pages usually means slices are too thick or that mechanism details have leaked in from the design doc.
- **Do not add plan-level deferrals.** `What We're NOT Doing` records design-declared out-of-scope work. Feature work that must land belongs in a phase; work that cannot fit needs roadmap revision, not a roadmap-level punt.
- **Do not add repo-task routing.** A roadmap phase may span repositories. The per-phase plan splits that phase into repo-local tasks with `**Repo:** <name>` tags.
- **Each phase fits a one-paragraph goal + high-level scope + key tests.** Split if larger.

## Markdown Template

Start from this skeleton when emitting a roadmap. Section headings must match exactly so downstream section-matching works. Omit conditional stub sections when they do not apply.

````markdown
# [Feature/Task Name] — Implementation Roadmap

## Overview

[2-3 sentences: what we're building and why.]

## Current State Analysis

[What exists now that this feature touches; key constraints from the design doc and research grounding.]

## Human Decisions

| Question | Decision |
|----------|----------|
| [Question you asked] | [User's answer] |

## What We're NOT Doing

[Every out-of-scope or future-work decision the design doc declares. Do not add new deferrals for feature work that belongs in a phase.]

## Architecture Approach

[High-level strategy. One short paragraph.]

## Phase 1: [First Vertical Slice]

### Goal

[One paragraph: the increment this phase delivers and why it comes first.]

### Proves

- [Question this phase settles]
- [Behavior, migration, or integration risk this phase reduces]

### Scope

[High-level work included in this phase. For multi-repo slices, name the repositories touched when useful.]

### Key Tests

- [Critical verification intent for this phase.]

### Stub Inventory

Only include this section when the phase intentionally leaves named stubs.

| Component | Stub Behavior | Retired In |
|-----------|--------------|------------|
| [Component] | [What the stub does] | Phase N |

## Phase 2: [Next Vertical Slice]

### Goal

[One paragraph: the increment this phase delivers and why it comes next.]

### Proves

- [Question this phase settles]

### Scope

[High-level work included in this phase.]

### Retires Stubs

Only include this section when this phase retires named stubs from an earlier Stub Inventory.

- [Stub 1 from inventory]
- [Stub 2 from inventory]

### Key Tests

- [Critical verification intent for this phase.]

## Phase N: [Slice Name]

[Same shape.]

## Overall Exit Criteria

- [Final feature-level verification after all phases complete.]
````
