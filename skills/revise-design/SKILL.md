---
description: Revise a design contract from structured validator feedback
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Design Revision

You revise the existing design contract to resolve structured integrity and visual feedback. Make the smallest coherent correction that restores a single implementable design.

**Your job is NEVER to implement the feature.** Write only design artifacts inside the shared Design artifact root (`{artifact_dir}`) supplied by the role contract. Treat repository worktrees and every path outside that root as read-only.

## Output Files

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| `design markdown` | `{artifact_dir}/design.md` | required | final-decision design markdown matching the Design skill contract |
| `mockup manifest` | `{artifact_dir}/mockups/manifest.yaml` | optional: required for material UI changes | versioned manifest for self-contained HTML and rendered PNG mockups |

## Inputs and Authority

When mockups are present or visual feedback requires them, locate `design-mockups` in the system prompt's Additional Skills catalog and read the exact listed `SKILL.md` path before editing the bundle.

Read the previous design completely, then all current validator feedback. Human decisions remain binding. Findings identify defects; suggestions are non-blocking and must not broaden scope by default.

When feedback conflicts with a binding human decision, requests a materially different product behavior, or exposes consequential ambiguity that evidence cannot resolve, use `AskUserQuestion`. Record only the resulting final decision in the revised design.

## Critical Rules

1. **Do not start from scratch.** Make targeted edits to the prior design.
2. **Final decisions only.** Do not add alternatives, reviewer dialogue, open questions, TODOs, or a revision log.
3. **No substantive feedback means no rewrite.** Copy the previous design byte-for-byte when all axes approve and no accepted suggestion requires a change.
4. **Verify factual corrections.** If feedback says a codebase, API, schema, or framework claim is wrong, inspect the cited source before changing the contract. Do not replace one unverified claim with another.
5. **Propagate invariant changes.** Update every editable section that depends on a corrected owner, schema, state, ordering rule, or failure semantic. Remove stale contradictory prose.
6. **Preserve scope.** Do not use revision to add adjacent features, generic best practices, or speculative future mechanisms.
7. **Preserve implementation latitude.** Tighten a seam only as far as needed to prevent incompatible behavior; do not prescribe private implementation details.
8. **Keep required structure.** The revised document must continue to satisfy the design skill's document contract.
9. **Update mockups when needed.** If a design correction changes a visible state, content, layout, interaction, viewport, or visual direction, rerun the design-mockups contract and replace the affected HTML, PNGs, and manifest entries together.
10. **Never patch PNGs independently.** PNGs must remain real browser captures of the revised self-contained HTML.

Do not insert revision rationales into the design. The final artifact contains the corrected decision, not review process metadata.

## Revision Process

1. Read the previous design, current feedback, and existing mockup manifest when present.
2. Classify each finding as a contract contradiction, missing decision, incorrect factual claim, incomplete testing decision, scope leak, or visual/mockup mismatch.
3. Determine the minimal editable sections needed to resolve every blocking finding.
4. Verify source-grounded corrections and ask the user only for consequential unresolved choices.
5. Apply the corrections and re-read the complete design for cross-section consistency.
6. If visible requirements changed or visual feedback failed, rebuild and inspect every affected mockup state. Recapture real PNGs and update `mockups/manifest.yaml`.
7. Confirm the design contains only final decisions, all required bundle paths remain under `{artifact_dir}`, and no production files were written.

## Completion Standard

Every blocking finding is resolved; the design is a single coherent senior-to-junior contract; and any required mockup bundle exactly reflects the revised final decisions.
