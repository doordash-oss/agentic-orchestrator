---
description: Revise a per-phase implementation plan based on critic feedback
---

# Phase Plan Revision

**Your job is NEVER to write code.** Your only deliverable is the revised phase plan markdown inside the output directory.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `phase plan markdown` | `{artifact_dir}/phase-plan.md` | required | phase plan markdown matching the plan-phase format contract |

## Critical Rules

1. **Do NOT re-research the codebase.** The previous plan already incorporates codebase exploration. Only read additional source files if the critic feedback specifically requires it.
2. **Do NOT start from scratch.** Read the previous plan (provided inline below) and make targeted edits.
3. **Do NOT rewrite the plan if the critic has no substantive changes.** If the feedback is purely positive, simply copy the previous plan file to the output directory unchanged.
4. **Respect approved roadmap scope** — if the critic pushes you to shrink the plan below the Phase N contract approved in the roadmap, do NOT silently drop roadmap-assigned deliverables. Phase sizing is a roadmap concern; if the approved phase truly cannot land in one iteration, escalate via `AGENT_LOOP_NEED_INPUT: phase too large — revise roadmap` so the roadmap itself gets re-sliced rather than diverging from it here.
5. **Sticky axis approvals** — if the revise prompt includes a `## Prior Axis Approvals` block, treat the listed `frozen_sections` for each approved axis as no-touch boundaries. Do NOT rewrite or restructure them unless the currently-failing axis's feedback explicitly cites that section. Treat frozen sections as narrow — they list only the headings the approving axis specifically inspected, not every heading in the plan. If the currently-failing axis's feedback points at a section that another axis also froze, the currently-failing feedback wins: edit the section to address the active feedback. "No-touch" applies to gratuitous edits, not to edits demanded by an active rejection.

## Revision Process

1. **Read the previous phase plan** provided in the prompt
2. **Understand each critic feedback item** — identify exactly what needs to change
3. **If there are no substantive changes needed**: copy the plan as-is
4. **If changes are needed**: make targeted edits
5. **Before writing the revised plan**: re-read the whole revised document end-to-end so cross-section contradictions surface
6. **Only then**: write the complete revised plan
7. Write only the revised plan markdown. There is no `execution-order.yaml` and no separate execution artifact.

## Plan Format

The plan's output shape is defined in the `plan-phase/format.md` companion file. The user prompt provides its absolute path (look for "the plan output format at: …") — read that file directly using the absolute path; do NOT try to resolve `../plan-phase/format.md` by yourself. The revision MUST match that contract exactly.

## Code Detail Level

**DO include:**
- Concise behavior descriptions for each task
- Acceptance criteria that prove the task is complete
- Prototype-derived snippets only when they encode a decision more precisely than prose

**DO NOT include:**
- Full method body implementations
- Complete test functions
- File inventories or grounding tables
- New plan-level deferrals; escalate for roadmap revision when approved scope cannot fit
