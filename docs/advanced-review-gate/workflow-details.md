# Workflow — Phase Details

Detailed descriptions of each phase, mapped to agentic's orchestration phases and command templates.

## Phase Mapping

| # | Phase | Agentic Phase | Agentic Status | Command Template |
|---|-----------|---------------|----------------|------------------|
| 1 | Task Assessment | Feature creation wizard | `StatusCreated` | (TUI wizard — no template) |
| 2 | Research | `PhaseResearch` | `StatusResearching` | `research-codebase.md` |
| 3 | Intent Clarification | `PhaseInquire` | `StatusInquiring` | `inquire.md` |
| 4a | Roadmap Generation | `PhaseRoadmap` | `StatusPlanning` | `create-roadmap.md` |
| 4b | Phase Planning | `PhasePlan` | `StatusPlanning` | `plan-phase.md` |
| 5 | Multi-Agent Plan Validation | Plan validation loop | `StatusPlanning` (gate) | `validate-roadmap-{architecture,scope}.md` (roadmap), `validate-phase-plan-{structural,grounding,scope}.md` (phase plans), `validate-plan-{security,performance,testing}.md` (high-risk add-ons) |
| 6 | Implementation | `PhaseImplement` | `StatusImplementing` | `implement_plan.md` |
| 7 | Artifact Validation | `PhaseReview` | `StatusReviewPassed` | `local_review.md` |

**Supplementary agentic phases** not directly in the 7-phase model:

| Agentic Phase | Purpose | Relationship |
|---------------|---------|------------------|
| `PhaseKnowledgeBase` | Build/refresh repo knowledge base | Pre-condition for Research (caches codebase understanding) |
| `PhaseBrainstorm` | Explore solution alternatives | Between Research and Plan (informs plan generation) |
| `PhasePublish` | Create PR via `gh` CLI | Post-implementation delivery (outside validation scope) |

---

## Phase 1: Task Assessment

**Purpose**: Classify the change as simple, moderate, or complex based on risk. This determines the autonomy level and which validation gates are required.

**Agentic mapping**: The feature creation wizard in the TUI collects:
- Feature name and description
- Target repository
- Exit criteria (what "done" looks like)
- Model selection per phase
- Inquireness level (none / medium / high)

**Risk classification heuristics**:

| Signal | Low Risk | Medium Risk | High Risk |
|--------|----------|-------------|-----------|
| File scope | Single file | Multi-file, single package | Cross-package or cross-service |
| API surface | No API changes | Internal API changes | Public API or schema changes |
| Data impact | Read-only or additive | Modifies existing data paths | Schema migration, data deletion |
| Security relevance | None | Input validation changes | Auth, crypto, or access control |
| Reversibility | Trivially revertible | Revertible with some effort | Requires migration rollback |

**Current state**: Risk classification in agentic is implicit — the user selects inquiry depth and max iterations, which serve as proxies for complexity. Explicit risk-level tagging on features is a future enhancement.

---

## Phase 2: Research

**Purpose**: Gather evidence before proposing changes. Understand existing patterns, architectural decisions, integration points, and constraints.

**Agentic mapping**: `PhaseResearch` via `RunResearchFromQuestions()`.

**What the research agent does**:
1. Reads the knowledge base (if available) for cached codebase understanding
2. Explores the codebase to understand relevant modules, types, and patterns
3. Identifies existing conventions (error handling, naming, package structure)
4. Locates ADRs, configuration, and integration points
5. Produces a research document summarizing findings

**Artifacts produced**: Research output document in `{state_dir}/{feature_id}/research/`

**Completion signal**: Agent writes `phase_complete` file to artifact directory.

**Key constraint**: The research agent has read-only intent — it gathers information but does not modify the codebase.

---

## Phase 3: Intent Clarification

**Purpose**: Help the engineer articulate clear, unambiguous intent. Surface implicit assumptions, identify decision points, and resolve ambiguity *before* planning.

**Agentic mapping**: `PhaseInquire` via `RunInquire()`.

**How it works**:
1. The planning session analyzes the feature description against the codebase
2. Identifies areas of ambiguity, missing requirements, and design decisions
3. Emits structured questions via the `AskUserQuestion` tool when user input is needed
4. Records human decisions as binding constraints for downstream phases

**Inquireness levels** (set at feature creation):
- **None**: Harness keeps eligible high-confidence planning recommendations hidden unless manual input is required
- **Medium**: Harness surfaces key planning questions that significantly affect the outcome
- **High**: Harness surfaces more planning questions at major decision points before proceeding

**Artifacts produced**: Inquiry output with questions and answers in `{state_dir}/{feature_id}/inquire/`

**Key value**: Human decisions recorded in this phase are *authoritative* — downstream validators and planners must respect them, even if they contradict default best practices.

---

## Phase 4: Planning (Two-Tier)

Planning uses a two-tier approach: a high-level roadmap breaks the feature into phases, then each phase gets its own detailed plan.

### Phase 4a: Roadmap Generation

**Purpose**: Break the feature into a sequence of implementation phases with clear boundaries and dependencies.

**Agentic mapping**: `PhaseRoadmap` via `RunRoadmapPlanningLoop()`.

**Roadmap contents**:
- Ordered list of implementation phases
- Phase boundaries and dependencies
- High-level scope for each phase

**Roadmap validation loop**:
1. Generate roadmap via interactive session (`create-roadmap` skill).
2. Run the multi-validator gate via `runRoadmapMultiValidatorPlanValidation()`. Axes are selected by `roadmapValidatorsForRisk()` — `validate-roadmap-architecture` and `validate-roadmap-scope` by default; high-risk roadmaps additionally pull in `validate-plan-security` and `validate-plan-performance`.
3. If any axis returns `CHANGES_REQUESTED`: revise roadmap using the `revise-roadmap` skill, preserving any axes that emitted a sticky `## Sticky Approval` block in their per-axis handoff file.
4. Repeat up to `MaxPlanIterations`.

**Artifacts produced**: Roadmap document in `{state_dir}/{feature_id}/roadmap/`

### Phase 4b: Phase Planning

**Purpose**: Produce a detailed implementation plan for each phase defined in the roadmap, grounded in research findings and clarified intent.

**Agentic mapping**: `PhasePlan` via `RunPhasePlanning()`.

**Phase plan contents**:
- Architectural approach with rationale and alternatives considered
- File-by-file change list with responsibilities
- Testing strategy (unit, integration, E2E)
- Integration points with failure modes
- Rollout considerations

**Phase plan validation loop**:
1. Generate phase plan via interactive session (`plan-phase` skill).
2. Run the multi-validator gate via `runPhasePlanMultiValidatorValidation()`. Axes are selected by `phasePlanValidatorsForRisk()` — `validate-phase-plan-structural`, `validate-phase-plan-grounding`, and `validate-phase-plan-scope` by default; high-risk phases additionally pull in `validate-plan-security`, `validate-plan-performance`, and `validate-plan-testing`.
3. If any axis returns `CHANGES_REQUESTED`: revise plan using the `revise-phase-plan` skill, preserving sticky-approved axes byte-equal.
4. Repeat up to `MaxPlanIterations`.

**Artifacts produced**: Phase plan documents in `{state_dir}/{feature_id}/phase-NN/plan/`

---

## Phase 5: Multi-Agent Plan Validation

**Purpose**: Evaluate the plan from 4 specialized perspectives before committing to implementation. Specialized review catches domain-specific issues that generalist review misses.

**Agentic mapping**: Plan validation gates within the roadmap and phase planning loops. Both gates always dispatch the specialized multi-validator pipeline. Roadmap validation runs `validate-roadmap-architecture` + `validate-roadmap-scope` (plus security/performance at high risk); phase-plan validation runs `validate-phase-plan-structural` + `validate-phase-plan-grounding` + `validate-phase-plan-scope` (plus security/performance/testing at high risk). The four generic per-domain validators are reused as the high-risk add-ons:

| Validator | Stage | Template | Focus Areas |
|-----------|-------|----------|-------------|
| **Architecture** (roadmap) | Roadmap | `validate-roadmap-architecture.md` | Pattern consistency, boundary correctness, integration semantics, ADR compliance, dependency direction |
| **Scope** (roadmap) | Roadmap | `validate-roadmap-scope.md` | Decomposition, phase boundaries, scope coverage |
| **Structural** (phase plan) | Phase plan | `validate-phase-plan-structural.md` | Phase plan format, exit criteria, verifiability |
| **Grounding** (phase plan) | Phase plan | `validate-phase-plan-grounding.md` | Symbol existence, prior-phase awareness |
| **Scope** (phase plan) | Phase plan | `validate-phase-plan-scope.md` | Per-phase scope adherence, no scope creep |
| **Security** | High-risk add-on | `validate-plan-security.md` | Auth/authz, input validation, data protection, rate limiting, security dependencies |
| **Performance** | High-risk add-on | `validate-plan-performance.md` | Scalability, query efficiency, resource management, latency impact, failure modes |
| **Testing** | High-risk add-on (phase plan) | `validate-plan-testing.md` | Coverage adequacy, edge cases, test type appropriateness, failure mode testing, regression protection |

**Current implementation**: Each validator returns `APPROVED` or `CHANGES_REQUESTED`. The gate aggregates results. If any validator requests changes, the plan is revised and re-validated.

**Future enhancement**: Numeric scoring (see [validation-scoring.md](validation-scoring.md)) with weighted aggregation and configurable thresholds.

**Human decisions are authoritative**: All validators are instructed to respect decisions recorded in the Intent Clarification phase. A human decision overrides best practices within a given validator's domain.

---

## Phase 6: Implementation

**Purpose**: Execute the approved plan. Write code, tests, and documentation. The implementation must trace back to the plan.

**Agentic mapping**: `PhaseImplement` via `RunImplementation()`.

**Implementation loop**:
1. Agent implements the plan using `implement_plan.md` template
2. Agent signals completion (`phase_complete`)
3. Review gate runs using `local_review.md` template
4. If `CHANGES_REQUESTED`: agent iterates with review feedback
5. Repeat up to `MaxIterations` (configurable, default 10)

**Safety rails**:
- `MaxConsecutiveFailures` (default 3): stops if the agent fails repeatedly
- `MaxConsecutiveNoProgress` (default 3): stops if review feedback repeats without improvement
- Permission handling: Bash commands surface to TUI for human approval (unless `-dsp` mode)

**Artifacts produced**: Code changes in the worktree, implementation logs in `{state_dir}/{feature_id}/implement/`

---

## Phase 7: Artifact Validation

**Purpose**: Verify that the implementation matches the approved plan and satisfies the original intent. This is the final quality gate before delivery.

**Agentic mapping**: `PhaseReview` — the review gate within the implementation loop, plus the final `StatusReviewPassed` transition.

**What the reviewer checks**:
- Exit criteria satisfaction
- Test passage
- Code quality against codebase conventions
- Plan adherence (did the implementation follow the approved plan?)

**Outcome**: `APPROVED` transitions to `StatusReviewPassed` → `StatusCodeReady`. `CHANGES_REQUESTED` loops back to implementation.

---

## Shadow Mode (Future)

Shadow mode is a 90-day observation period for new governance mechanisms (validators, policies, scoring thresholds):

1. **Days 1–30**: Log-only. Run validators but don't block on results. Collect data on agreement rates between AI validation and human review.
2. **Days 31–60**: Warning mode. Surface validator results as PR comments but don't block merge.
3. **Days 61–90**: Enforcing mode with human override. Validator results block merge, but humans can override with justification.
4. **Day 90+**: Full enforcement. Calibrate thresholds based on observed false-positive and false-negative rates.

**Status**: Not yet implemented. Current agentic workflow runs validators in the plan loop, which is functionally equivalent to enforcing mode for the plan phase.

---

## The Evolutionary Test

After many features processed by AI agents, apply this test:

> Can a new engineer read the codebase and understand why each component exists, how modules relate, and where boundaries lie?

**Coherence metrics** (future, measurable proxies):
- **Naming consistency**: Ratio of names following established conventions
- **Boundary violations**: Count of imports crossing module boundaries
- **Pattern proliferation**: Number of distinct patterns used for the same concern (e.g., error handling)
- **Dead code ratio**: Percentage of unreachable code introduced by AI changes
- **ADR coverage**: Percentage of architectural decisions documented in ADRs

If these metrics degrade over time, the governance needs strengthening — not the AI model.
