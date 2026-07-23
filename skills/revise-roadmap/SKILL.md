---
description: Revise an implementation roadmap based on critic feedback
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Roadmap Revision

**Your job is NEVER to write code.** Your only deliverable is the revised roadmap markdown inside the output directory.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `roadmap markdown` | `{artifact_dir}/roadmap.md` | required | roadmap markdown matching the create-roadmap format contract |

## Critical Rules

1. **DO re-research the codebase when the critic identifies a wrong claim.** If the feedback says "mechanism X doesn't work this way" or "callback Y is already used for Z", read the relevant source file to verify before responding. Do NOT guess or speculate — verify first, then correct.
2. **Do NOT start from scratch.** Read the previous roadmap provided by the user and make targeted edits to address each piece of critic feedback.
3. **Do NOT rewrite the roadmap if the critic has no substantive changes.** If the feedback is purely positive (e.g., "looks good", "approved", no actionable items), simply copy the previous roadmap file to the output directory unchanged.
4. **Preserve phase intent and sequencing.** Phase 1 must keep proving the riskiest integration questions unless the critic explicitly says that sequencing is wrong.
5. **Preserve explicit stub contracts when they exist.** Stubs may be reassigned to different phases, but named stubs from an existing **Stub Inventory** should not be silently removed. Do not add a stub inventory to roadmaps that intentionally avoid stubs.
6. **No new unverified claims.** When addressing feedback, do NOT introduce assertions that aren't in the design doc or verified against the codebase. This is the #1 cause of revision ping-pong: fixing one reviewer's concern by adding speculation triggers new concerns from the next reviewer.
7. **Propagate invariant fixes across the whole roadmap.** When feedback identifies a rule, owner, or invariant (for example an aggregate-vs-item rule or a completion-owner rule), update EVERY section that mentions that concept: Architecture Approach, What We're NOT Doing, phase goals, Key Tests, exit criteria, and any design-correction notes. Do not patch one section and leave stale contradictory statements elsewhere.
8. **Delete stale claims instead of layering exceptions on top of them.** If an old sentence now contradicts the corrected design, remove or rewrite it. Do NOT keep both and hope the reviewer infers precedence.
9. **Prefer phased migration language over absolute end-state claims.** If the feedback shows that multiple paths still exist today, revise the roadmap to say when centralization completes instead of asserting "X owns all Y" immediately.
10. **Do NOT re-title or renumber phases listed in any prior sticky `frozen_sections`** — keep exact heading text byte-equal. Renames break section-matching downstream, invalidate sticky approvals, and force validators to re-evaluate axes they have already cleared.
11. **Do not let revisions grow the roadmap.** Prefer narrowing claims over expanding them. If addressing feedback would noticeably increase the roadmap's size, look first for existing detail to remove rather than layering new mechanism details on top.

## Sticky Approval Respect

If the caller provides prior-attempt axis approvals (files named like `axis-approved-<axis>.md`, the per-axis `validation-<axis>-feedback.md` files from prior attempts containing a `## Sticky Approval` section, or a context block labelled `Prior approvals`), those approvals are **sticky**: sections listed under `frozen_sections` in any prior-approved axis MUST NOT be modified in this revision except where the currently-failing axis's feedback explicitly cites that section as the source of its failure. The point of sticky approval is to stop single-axis failures from re-planning the whole document and destabilizing sections that other axes have already cleared.

**Decision procedure:**

- Build the set `Frozen = union of frozen_sections across all prior axis approvals`.
- Build the set `ForcedEdits = sections explicitly named in the currently-failing axis's feedback` (the failing axis's numbered feedback items must cite a heading by name for that heading to enter `ForcedEdits`; vague complaints about "the roadmap as a whole" do not unlock frozen sections).
- **Editable sections = (not in `Frozen`) OR (in `Frozen` AND in `ForcedEdits`).**
- If editing a Frozen-but-Forced section, add a 1-line rationale at the top of the revised section: `Edited despite sticky approval because: <axis-name> flagged <reason>.` so downstream validators (and future revisers) can see why the freeze was broken.
- Never silently edit a `Frozen` section that is not also in `ForcedEdits`. If you believe an edit is needed but the failing axis did not cite that section, leave the section untouched and instead surface the tension in "What We're NOT Doing" or note it for the human.

**Worked example.**

Suppose two prior validator attempts produced these `## Sticky Approval` blocks (each in its own per-axis feedback file):

```
## Sticky Approval

axis: architecture
frozen_sections:
- Phase 3: Wire the hedging dispatcher
- Architecture Approach
```

```
## Sticky Approval

axis: scope
frozen_sections:
- Phase 3: Wire the hedging dispatcher
- What We're NOT Doing
```

The current rejection comes from the `testing` axis and cites "Phase 3's Key Tests do not cover the dispatcher fallback path." Then:

- `Frozen = {"Phase 3: Wire the hedging dispatcher", "Architecture Approach", "What We're NOT Doing"}`.
- `ForcedEdits = {"Phase 3: Wire the hedging dispatcher"}` (the testing feedback explicitly names that phase).
- Editable: Phase 3's **Key Tests** block (inside the Frozen-but-Forced phase) — revise it to add the missing fallback-path test and prepend `Edited despite sticky approval because: testing flagged missing dispatcher fallback test.` at the top of the tests block.
- Not editable: Phase 3's headline, Goal, Proves, Smoke Test, and sequencing — those stay byte-equal because `architecture` and `scope` already cleared them and the failing axis did not cite them. The heading text `Phase 3: Wire the hedging dispatcher` is never altered.
- Not editable: `Architecture Approach` and `What We're NOT Doing` — Frozen, and not in `ForcedEdits`.

## Revision Discipline

### Categorize Before Acting

For each feedback item, classify it before deciding how to respond:

| Category | How to Respond |
|----------|----------------|
| **Design doc covers this** | Transfer the detail faithfully from the design doc. This is a lossy-translation bug — the original roadmap dropped something it should have included. |
| **Design doc claim is wrong** | The reviewer found the design doc's codebase assertion is incorrect (e.g., "callback X is already used for Y", "mechanism Z doesn't support this flow"). **Read the actual code to verify the reviewer's claim.** If confirmed, correct the roadmap with the verified fact and note the design doc error. |
| **Design doc defers this** | Add to "What We're NOT Doing." Do NOT speculatively fill the gap. If the design explicitly said "deferred" or "future work", the roadmap must also defer. |
| **Internal contradiction** | Fix the contradiction directly. Re-read both conflicting sections and align them. |
| **Execution ordering / timing issue** | The reviewer identified that a guard/check fires at the wrong time or level. **Read the execution path in the code** to verify, then correct the roadmap with the verified ordering. |
| **Overstated ownership / source of truth** | Narrow the claim. If the roadmap wants eventual centralization but the current code still has multiple paths, rewrite it as phased migration and say when the new owner or source of truth becomes authoritative. |
| **Phase safety violation** | The reviewer found that an early phase exposes unsafe incomplete behavior. Restructure: either keep the old mechanism active until the replacement is real, prevent the early phase from reaching promoted states, or move the real behavior earlier. |
| **Phase sizing issue** | Split or restructure phases as requested. This is mechanical and low-risk. |
| **Missing test coverage** | Add specific test items to Key Tests. Low-risk. |

### Verify Before Asserting

When you add or modify any factual claim about the codebase:
1. Check if the design doc establishes this fact → use it directly
2. If not, read the relevant source file to verify → then add the verified fact
3. If you cannot verify → do NOT add the claim. Instead, defer it: "To be determined during per-phase planning"

When the feedback is about a boundary or source of truth, explicitly verify:
- **Dispatch vs execution boundary**: the place that decides to enter a flow is not always where the side effect executes
- **Ownership completeness**: if the roadmap says one subsystem owns a behavior, verify whether all relevant paths already go through it; otherwise rewrite as migration
- **Config ownership**: YAML-facing config types should live in the layer that already owns config parsing, with runtime packages consuming derived settings values
- **Optional sink coupling**: if summaries, exports, or rollups depend on another optional sink, state what happens when that upstream sink is disabled

## Revision Process

1. **Read the previous roadmap** provided in the prompt
2. **Categorize each feedback item** using the table above
3. **If there are no substantive changes needed**: copy the previous roadmap to the output directory as-is and complete. Do NOT rewrite it.
4. **If changes are needed**: make targeted edits and write the complete revised roadmap to the output directory — the roadmap must be self-contained

## Roadmap Format

The roadmap's output shape is defined in the `create-roadmap/format.md` companion file. The user prompt provides its absolute path (look for "the roadmap output format at: …") — read that file directly using the absolute path; do NOT try to resolve `../create-roadmap/format.md` by yourself. The revision MUST match that contract exactly.
