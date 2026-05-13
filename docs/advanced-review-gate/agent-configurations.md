# Agent Configurations

Validator agent configurations mapped to agentic's plan validation architecture. Each validator gets focused context and single-domain instructions to maximize signal-to-noise ratio.

## Overview

The [plan validation gate](workflow-details.md) uses specialized validator agents rather than a single generalist reviewer. This is based on the principle that domain-focused review catches issues that generalist review misses — a generalist agent asked to "review this plan" will surface obvious issues but miss domain-specific subtleties in security, performance, or architectural consistency.

Each validator:
- Receives a **focused prompt** with domain-specific evaluation criteria
- Gets **scoped context** — only the artifacts relevant to its domain
- Produces a **single-domain verdict** (`APPROVED` or `CHANGES_REQUESTED`)
- Operates **independently** — validators do not see each other's output

## Current Implementation

Plan validation always runs the specialized multi-validator pipeline. Each axis is dispatched in parallel by `runRoadmapMultiValidatorPlanValidation()` (roadmap) and `runPhasePlanMultiValidatorValidation()` (per-phase plan) in `internal/agent/plan_validation.go`. Each validator session:

1. Receives the plan path (the validator reads the artifact via tool use rather than having it inlined in the prompt).
2. Renders `validate_specialized.user.tmpl` with its axis-specific `DomainName` and skill template (e.g. `validate-roadmap-scope`, `validate-phase-plan-grounding`).
3. Spawns a bounded review helper using the feature's `Models.Review`. The helper has scoped write access to its `validation-<axis>-feedback.md` file only (via `permission.ReviewFeedbackHandler`); everything else in the worktree is read-only.
4. Parses the structured handoff file with `ParseReviewFeedback`, routing on the `## Verdict` section (`APPROVED` or `CHANGES_REQUESTED`). Malformed files short-circuit to `CHANGES_REQUESTED` with deterministic synthesized feedback.
5. When the verdict is `APPROVED` and the handoff includes a `## Sticky Approval` block, persists an axis-approved-<axis>.md artifact (with frozen-section digests) so revise attempts can preserve approved-axis sections byte-equal.

`composeValidatorResults()` aggregates the per-axis verdicts into a single overall result with combined feedback that the plan-revision session consumes. The legacy single-critic path (`runPlanValidation` / `runRoadmapValidation` / `runPhasePlanValidation`) was removed; the specialized pipeline is now the only path.

### Orchestration

The two-tier planning pipeline is managed by `RunRoadmapPlanningLoop()` and `RunPhasePlanningLoop()` in `plan_validation.go`. Each loop:

1. Generates the artifact via an interactive planning session (`create-roadmap`, `plan-phase`, etc.).
2. Hands the artifact to the corresponding multi-validator entry point — `runRoadmapMultiValidatorPlanValidation()` for the roadmap and `runPhasePlanMultiValidatorValidation()` for per-phase plans.
3. Each entry point fans out to all axis validators selected by `roadmapValidatorsForRisk()` / `phasePlanValidatorsForRisk()` (risk-aware), runs them in parallel, and composes their verdicts via `composeValidatorResults()`.
4. If any axis returns `CHANGES_REQUESTED`, the combined feedback is fed into the revise session (`revise-roadmap`, `revise-phase-plan`, ...).
5. The loop repeats up to `MaxPlanIterations`. If still not approved, the result is `needs_human_review`. Per-axis approvals stick across attempts via the `## Sticky Approval` section in each axis's handoff file so revision sessions only need to fix the still-failing axes.

## Specialized Validator Configurations

The following validators are dispatched by the multi-validator pipeline. See [validation-scoring.md](validation-scoring.md) for how their outputs are weighted.

Architectural soundness is checked at the roadmap stage by `validate-roadmap-architecture` and at the phase-plan stage by `validate-phase-plan-structural` (see [workflow-details.md](workflow-details.md)). The generic per-domain validators below are reused as high-risk add-ons by `roadmapValidatorsForRisk()` (Security + Performance) and `phasePlanValidatorsForRisk()` (Security + Performance + Testing).

### Security Validator

| Property | Value |
|----------|-------|
| Template | `skills/validate-plan-security/SKILL.md` |
| Weight | 30% |
| Model | Feature's `Models.Review` |
| Context | Plan + security-relevant research findings |

**Focus areas**:

| Area | What It Checks |
|------|---------------|
| Authentication & authorization | Explicit auth on all endpoints; AuthzService for authorization |
| Input validation | All user input validated before use in queries, commands, or paths |
| Data protection | PII handling, encryption at rest/in transit, secrets management |
| Rate limiting | Abuse prevention on public-facing endpoints |
| Security dependencies | No known-CVE dependencies introduced |

**Hard vetoes**: SQL injection vectors, authentication bypasses, hardcoded secrets, PII in logs. These trigger immediate `CHANGES_REQUESTED` regardless of other criteria. See [guardian policies](guardian-policies.md) for the full veto list.

### Performance Validator

| Property | Value |
|----------|-------|
| Template | `skills/validate-plan-performance/SKILL.md` |
| Weight | 20% |
| Model | Feature's `Models.Review` |
| Context | Plan + performance-relevant research findings |

**Focus areas**:

| Area | What It Checks |
|------|---------------|
| Scalability | Does the approach scale with data volume and concurrency? |
| Query efficiency | N+1 queries, missing indexes, unbounded result sets |
| Resource management | Connection pools sized, goroutine lifetimes bounded, memory budgeted |
| Latency impact | Hot-path changes assessed for latency regression |
| Failure modes | Graceful degradation under load; circuit breakers where needed |

### Testing Validator

| Property | Value |
|----------|-------|
| Template | `skills/validate-plan-testing/SKILL.md` |
| Weight | 20% |
| Model | Feature's `Models.Review` |
| Context | Plan + testing strategy from intent |

**Focus areas**:

| Area | What It Checks |
|------|---------------|
| Coverage adequacy | Are critical paths covered? Is the testing strategy proportional to risk? |
| Edge cases | Boundary conditions, empty inputs, concurrent access, error paths |
| Test type appropriateness | Unit for logic, integration for boundaries, E2E for user flows |
| Failure mode testing | Are failure scenarios tested (network errors, timeouts, invalid state)? |
| Regression protection | Do tests protect against the specific bug class being addressed? |

For UI changes, the testing validator also checks accessibility test coverage.

## Agent Interaction Pattern

### Current: Sequential Execution

Validators execute sequentially — the roadmap and phase plan validators each run as a single `--print` mode session. This is simpler to implement and debug but means validation time scales linearly with the number of validators.

### Future: Parallel Execution

When multi-validator execution is enabled, validators will run concurrently since they are independent (no shared state, no cross-references). Results are composed after all complete. This is a future optimization — the sequential model is correct; parallel is faster.

### Result Composition

Results from validators are composed by the plan validation gate:

| Current | Future |
|---------|--------|
| Roadmap/phase plan validator returns `APPROVED` or `CHANGES_REQUESTED` | Each validator returns a verdict independently |
| Any `CHANGES_REQUESTED` triggers plan revision | Weighted scoring determines overall verdict (see [validation-scoring.md](validation-scoring.md)) |
| Feedback from validator is passed to revision prompt | Feedback from all validators is concatenated and passed to revision prompt |

## Trust Model Progression

Validator authority follows a graduated trust model. New validators (or validators on a new codebase) start with low authority and earn enforcement power through demonstrated accuracy.

| Phase | Behavior | Duration | Graduation Criteria |
|-------|----------|----------|---------------------|
| **Shadow** | Log verdicts, don't block | 2–4 weeks | Agreement with human review > 80% |
| **Advisory** | Surface results to engineer, don't block | 2–4 weeks | False-positive rate < 10% |
| **Enforcing** | Block on `CHANGES_REQUESTED` | Ongoing | False-positive rate < 2% |
| **Adaptive** | Calibrate thresholds per-domain based on historical accuracy | Ongoing | Sufficient data for statistical confidence |

**Current status**: The plan validation gates in agentic operate in **enforcing** mode — `CHANGES_REQUESTED` from the roadmap or phase plan validator triggers plan revision. There is no shadow or advisory mode yet; this is part of the [evolutionary governance](workflow-details.md) roadmap.

## Pipeline Cost and Latency

Estimated overhead from specialized validation, based on the RFC projections:

| Component | Latency | LLM Calls | Notes |
|-----------|---------|-----------|-------|
| Research agent | ~30-60s | 1-3 | Existing phase, no change |
| Plan validation (4 validators, parallel) | ~20-40s | 4 | Parallel execution — wall-clock is max(validators), not sum |
| Plan validation (2 validators, low-risk) | ~15-25s | 2 | Low-risk skips security + performance |
| Artifact validation | ~20-30s | 1-2 | Existing review gate, no change |
| **Total pipeline overhead** | **~1-2 min (low-risk)** | **3-5** | Additive to existing CI |
| **Total pipeline overhead** | **~2-4 min (high-risk)** | **6-9** | Includes deeper research + all validators |

Validators use the feature's review model in `--print` mode (non-interactive, no tool access), which keeps context focused and cost low. Each validator sees only the plan text + feature description — not the full codebase.

## Adding a New Validator

To add a new specialized validator:

1. Create a skill in `skills/validate-plan-<domain>/SKILL.md` following the existing pattern
2. Define evaluation criteria specific to the domain (5–7 focus areas)
3. Include the "Human Decisions Are Authoritative" instruction
4. Include the "Scope of Review" instruction (reviewing a plan, not code)
5. Assign a weight relative to existing validators (weights must sum to 100%)
6. Start in shadow mode — log verdicts alongside the existing validators for comparison
7. Graduate to advisory after false-positive rate drops below threshold
