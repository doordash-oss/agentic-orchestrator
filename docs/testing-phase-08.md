# Testing Phase 8 Timing Report

This report records the Phase 8 prompt golden trim. Phase 1's baseline remains
in `docs/testing-baseline.md`.

## Scope

`internal/agent/prompts` now keeps one canonical golden for each stable prompt
template and one canonical golden for the shared partials whose prose is a
cross-system contract. Branch-specific prompt behavior moved to focused table
tests in the same package.

No template prose was changed. The only template edits tighten the multi-repo
predicate so the Target Repositories block requires `MultiRepo` and more than
one repository.

## Fast Suite

| Measurement | Before Phase 8 | Final Phase 8 |
|-------------|----------------|---------------|
| Fast-suite wall time, `go test ./... -short -count=1` | 32.56s from clean `HEAD` export | 26.17s warmed-cache final run |
| Prompt package targeted timing, `go test ./internal/agent/prompts/... -count=1` | 0.67s wall / 0.525s package elapsed | 0.67s wall / 0.511s package elapsed |
| `TestGoldenSnapshots` subtest count | 39 | 27 |
| `.golden` files under `internal/agent/prompts/testdata` | 43 | 27 |
| Per-package budget | 6s | within budget |

The targeted prompt timings use isolated local Go caches under `/private/tmp`
after one warm-up run per tree. This avoids measuring host Go-build-cache lock
contention as prompt-package cost; verbose prompt runs showed all prompt
subtests themselves completing at 0.00s.

## Retained Goldens

- Stable templates: `inquire_user_high_with_kb`, `brainstorm_user_multi_repo`,
  `roadmap_user_multi_repo`, `roadmap_revision_user`,
  `phase_plan_user_autonomous`, `phase_plan_revision_user`,
  `implement_user_iter2_with_evidence_injected`,
  `review_user_with_evidence_injected`, `pr_description_user_full`,
  `final_fix_user_with_manual`, `final_review_user_phase`, `summary_user`,
  `tweak_user`, `scout_user`, `research_from_questions_user`,
  `kb_build_full`, `system_final_review`, `system_validator`,
  `completion_protocol_with_clause`, `implement_completion_protocol`,
  `validate_specialized_grounding`.
- Shared partials: `preflight_all_three`, `skill_instruction`,
  `behavioral_evidence_implement`, `behavioral_evidence_review`,
  `visual_evidence_implement`, `visual_evidence_review`.

## Branch Coverage

Focused table tests now own the deleted snapshot branches for skill-instruction
injection, preflight gating, multi-repo gating, inquireness levels, KB build
mode, implement evidence ordering and feedback sections, review evidence
placement, completion-protocol clause inclusion, final-fix gating,
final-review phase/cycle variants, PR-description population, specialized
validator variants, and evidence partial empty/non-empty rendering.

The deleted golden names are not referenced by docs or launch-surface
assertions; searching the repository for the removed names only finds the new
branch-test row names.

## Manual Spot Checks

- Temporarily changed `partials/skill_instruction.tmpl` from `Before starting`
  to `Before beginning`; the retained `skill_instruction` golden failed and
  `TestSkillInstructionBranchBehavior` failed on the directive marker. The
  partial was restored.
- Temporarily changed the brainstorm multi-repo predicate back to
  `{{ if .MultiRepo }}`; only
  `TestMultiRepoPromptBranches/brainstorm_target_repositories_block_omitted_when_only_one_repo`
  failed, while `TestGoldenSnapshots/brainstorm_user_multi_repo` stayed green.
  The predicate was restored.

## Verification Notes

- `go test ./internal/agent/prompts/... -count=1` passed after the trim.
- `go test ./internal/agent/prompts/... -count=1 -update` passed, and the
  `internal/agent/prompts/testdata` diff before and after `-update` was
  byte-identical.
- `go test ./internal/agent/prompts/... -count=1 -run TestGoldenSnapshots`
  passed, covering the retained snapshots and the no-orphan guard.
- `go test ./... -short -count=1` passed with final wall time 26.17s.
- `go build ./...` and `go vet ./...` passed.
