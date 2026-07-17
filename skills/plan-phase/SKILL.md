---
description: Write a vertical-slice plan for a single roadmap phase
license: Apache-2.0 with incorporated MIT material; see LICENSE.upstream.txt
provenance: upstream-adapted
---

# Per-Phase Implementation Plan

You are taking one approved roadmap phase — a single vertical slice — and turning it into a concise set of behavior-centered tasks that the implementer can execute. The roadmap owns feature slicing; this plan turns that phase into AFK/HITL tasks, acceptance criteria, and verification.

**Your job is NEVER to write code.** Your only deliverable is the phase plan markdown inside the output directory.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `phase plan markdown` | `{artifact_dir}/phase-plan.md` | required | phase plan markdown matching the plan-phase format contract |

## Process

### 1. Read the Roadmap

Read the approved roadmap end-to-end. Focus on **Phase N** (the phase number named in the prompt).

If the prompt includes a Research Document, use it opportunistically after the roadmap: skim for sections relevant to Phase N, read those end-to-end before relying on them, and read the whole document if its structure is unclear, Phase N depends broadly on it, or a skim surfaces a conflict. The approved roadmap remains the source of phase scope and desired behavior — don't expand the phase just because research mentions adjacent possibilities.

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

Write the plan to `{artifact_dir}/phase-plan.md`.

For multi-repo features, every `### Task N:` heading **must** be followed by a `**Repo:** <name>` tag whose value is in `Feature.Repos`. The unified phase implementer reads these tags as the single source of truth for which repos this phase touches and which sub-agent gets each Task. Single-repo features may omit tags (every Task implicitly belongs to the only repo); a single-repo plan may still tag Tasks for clarity but must not mix tagged and untagged Tasks. There is no separate `execution-order.yaml`.

Set the mandatory plan metadata field `**Frontend:** true` when the phase adds or changes any user-facing UI surface. Otherwise set it to `false`. A `true` frontend flag must be paired with at least one real checklist item under the top-level `### Visual Evidence` section; never use `None required` for Visual Evidence when `**Frontend:** true` — unless the prompt declares automated-only verification mode (below), which mandates `None required: automated-only verification for this feature` there instead.

Do not add a grounding table, file inventory, stub inventory, testing strategy section, or deferrals section. Exact file selection, code-level grounding, and implementation ceremony belong to the implementer.

Derive verification from the phase and repository instead of interviewing the user about bookkeeping choices. Do not ask whether to include automated verification, how many manual checkboxes to create, whether an already-sequenced phase depends on its predecessor, or whether a fully specified task is AFK/HITL. Ask only when an unresolved product or scope decision would materially change the plan.

Keep verification minimal and non-overlapping:

- Put every deterministic invariant in an executable command. The harness captures its exit code, stdout, stderr, and run metadata automatically.
- Repo-scoped commands run from that repository root. In multi-repo phases, prefix every automated-verification description with `[repo: <name>]`; single-repo phases may omit it. Use paths such as `README.md` or `./internal/...`; never add `cd <repo>` or prefix paths with the repo name.
- Use one consolidated Manual Verification item only for irreducible semantic judgment that commands, visual evidence, and behavioral evidence cannot prove. Do not restate automated checks.
- Enumerate visual evidence as one checklist item per capture cell — surface/state/theme in prose plus a `[size: WxH]` tag (e.g. `- [ ] Home populated, dark theme [size: 1440x900]`). The harness validates each cell's file dimensions deterministically; a single item describing a whole capture matrix cannot be verified and will be rejected at review.
- End every Behavioral Evidence item with its packaged executable command in backticks (e.g. `` - [ ] Primary journey trace bundle: `npx playwright test e2e/journey.spec.ts` ``). Command-backed items are executed by the harness, which exports `AGENTICO_EVIDENCE_DIR` for the spec to write its trace and named screenshots into. Multiple journey items are allowed when every item carries a command; prose-only behavioral evidence must stay a single consolidated item.

**Verification contracting mode.** Your prompt may declare `## Verification Contracting Mode` stating the feature verifies through automated tests only. In that mode, put every verification command (including packaged end-to-end journeys) under `### Automated Verification`, and give `### Manual Verification`, `### Visual Evidence`, and `### Behavioral Evidence` exactly one `- [ ] None required: automated-only verification for this feature` item each. The full evidence-matrix rules below apply only when the prompt does not declare this mode.

## Plan Template

The plan's output shape is defined in [format.md](format.md). Read it before writing the plan and conform to its template exactly. Every section is mandatory unless its heading is annotated conditional; never drop, reorder, or rename a section.
