# Amazingly Advanced Review Gate

## Core Insight

AI makes code generation cheap. That is the opportunity and the risk. When any engineer (or any AI agent) can produce plausible code on demand, **conceptual integrity erodes gradually, not visibly**. You don't get dramatic failures. You get drift. Each change is reasonable in isolation. The architecture degrades in aggregate.

*Plausible* code compiles, passes tests, follows familiar patterns. *Sound* code fits the domain, respects constraints, maintains integration semantics, and preserves the conceptual integrity of the system it modifies. AI excels at plausibility. Soundness requires understanding existing decisions, respecting invariants, and maintaining the system's ability to be reasoned about over time.

This system bridges the gap through structured validation: every AI-generated artifact passes through research, intent clarification, multi-perspective review, and policy enforcement before it reaches production.

**The ultimate test**: after a year of AI-accelerated change, can a new engineer read the codebase and explain why each module exists, what responsibility it owns, how it may change safely, and where its boundaries are? If the answer is no, governance has failed regardless of whether tests pass.

## 8 Core Principles

### 1. Guardian Function

Encode the judgment of senior and principal engineers as declarative policy. The guardian function acts as an architectural backstop — it cannot add new ideas, but it can veto changes that violate invariants. This separates the creative act (proposing changes) from the protective act (enforcing constraints), allowing AI to move fast while keeping the system sound.

See: [guardian-policies.md](guardian-policies.md)

### 2. Research Before Modification

Every change begins with evidence gathering, not assumptions. The agent must understand the existing codebase structure, conventions, ADRs, integration points, and failure modes *before* proposing a plan. This front-loads the cost of understanding and prevents the most expensive class of errors: structurally plausible changes that violate unstated constraints.

### 3. Bounded Autonomy Through Risk Classification

Not all changes carry equal risk. The system classifies tasks and grants autonomy proportionally:

| Risk Level | Scope | Autonomy | Review Gate |
|------------|-------|----------|-------------|
| **Low** | Single file, no API/schema changes, additive only | Agent proceeds autonomously after plan approval | Automated validation |
| **Medium** | Multi-file, internal API changes, behavioral modifications | Agent proceeds with human checkpoint at plan stage | Specialized validator review |
| **High** | Schema migrations, public API changes, security-sensitive, cross-service | Human approval required at plan AND implementation | Full multi-agent validation + human sign-off |

### 4. Tests Are Necessary But Not Sufficient

Tests verify behavioral contracts at call sites. They do not verify structural invariants — that module boundaries are respected, that dependency direction is correct, that architectural patterns are followed consistently. The system supplements testing with:

- **Specialized validators** that check architecture, security, performance, and test strategy independently
- **Guardian policies** that enforce binary invariants (MUST/MUST NOT rules)
- **Intent-plan traceability** that verifies implementation matches approved design

### 5. Business Alignment as Structural Anchor

For high-risk changes, technical correctness is necessary but not sufficient. The change must also serve a clear business objective with defined success metrics, guardrails, and rollback strategy. This prevents technically sound changes that solve the wrong problem or create unacceptable business risk.

### 6. Evolutionary Governance

Governance that cannot evolve becomes friction. Governance mechanisms — policies, scoring thresholds, validator weights — are versioned artifacts that adapt through measured feedback:

- Shadow mode observation before enforcement
- Threshold calibration based on false-positive/false-negative rates
- Policy exception tracking with expiry dates
- Periodic review of validator effectiveness

### 7. The Evolutionary Test

After many reshapes by AI agents, the system must remain *explainable*. A new engineer should be able to read the codebase and understand why each component exists, how modules relate, and where boundaries lie. If AI-generated changes erode this property, the governance has failed regardless of whether tests pass.

### 8. Policy Enforcement as Architectural Backstop

Declarative rules that encode invariants the codebase must always satisfy. Unlike scoring (which is evaluative and allows trade-offs), policies are binary: a change either satisfies the invariant or it doesn't. Policies carry VETO capability — a single policy violation blocks a change regardless of how well it scores elsewhere.

See: [guardian-policies.md](guardian-policies.md)

## System Workflow

The system defines 7 phases that map directly to agentic's orchestration:

| # | Phase | Purpose | Agentic Phase |
|---|-----------|---------|---------------|
| 1 | Task Assessment | Classify risk, determine autonomy level | Feature creation wizard |
| 2 | Research | Gather codebase context, ADRs, patterns | `PhaseResearch` |
| 3 | Intent Clarification | Refine requirements through structured inquiry | `PhaseInquire` |
| 4 | Plan Generation | Produce implementation plan from research + intent | `PhasePlan` |
| 5 | Multi-Agent Plan Validation | 4 specialized validators score the plan | Plan validation loop |
| 6 | Implementation | Code, tests, docs per approved plan | `PhaseImplement` |
| 7 | Artifact Validation | Verify implementation matches intent | `PhaseReview` |

See: [workflow-details.md](workflow-details.md) for detailed phase descriptions.

## What AI May Do Autonomously

- Read any file in the repository
- Run tests, linters, and build commands
- Generate research documents and plans (subject to validation)
- Implement approved plans for low-risk changes
- Create commits and branches in isolated worktrees
- Propose architectural alternatives with trade-off analysis
- Auto-fix linter errors and test failures during implementation

## What AI May NOT Do Without Escalation

- Modify database schemas or migrations (high-risk)
- Change public API contracts (high-risk)
- Alter authentication or authorization logic (high-risk, security-critical)
- Override a guardian policy VETO
- Skip a validation phase
- Merge to the default branch
- Modify CI/CD pipeline configuration
- Delete or disable existing tests
- Change dependency versions with known CVEs

## Human Roles and Authorities

The system defines clear ownership boundaries so that the right human makes the right decision at the right time.

| Role | Authority | Boundary |
|------|-----------|----------|
| **Engineer** | Define intent, choose risk classification, request policy exceptions, approve low-risk auto-merge | Cannot override guardian policy VETOs or suppress semantic escalation |
| **Senior / Architect** | Approve high-risk changes, grant policy exceptions (with justification + expiry), override validation scores (logged), calibrate thresholds, update architectural contracts | Cannot bypass compliance VETOs; threshold changes require team consensus |
| **Platform / SRE** | Define SLOs and error budgets, configure progressive delivery, trigger manual rollback | Cannot change business metric thresholds or disable guardian policies |
| **Security** | Define security guardian policies, approve security-related exceptions, review flagged PRs | Policies require team review before entering VETO mode |

The key principle: **escalation is structural, not cultural**. The system enforces that high-risk changes reach senior review — it doesn't depend on the engineer remembering to ask.

## Trade-offs and Limitations

### Accepted Trade-offs

| Trade-off | Rationale |
|-----------|-----------|
| Slower initial velocity | Research and validation phases add time upfront but prevent expensive rework |
| Multiple AI invocations per feature | Specialized review catches issues that generalist review misses |
| Rigid phase ordering | Prevents the "jump to implementation" failure mode common in AI-generated code |
| Conservative default thresholds | False negatives (blocking good changes) are cheaper than false positives (allowing bad ones) at adoption time; thresholds relax as confidence grows |

### Known Limitations

- **Intent drift**: Over many iterations, the implementation can drift from the original intent. Mitigated by intent-plan traceability but not eliminated.
- **Validator blind spots**: Specialized validators only catch issues within their domain. Cross-cutting concerns that span all four domains may slip through.
- **Policy staleness**: Guardian policies reflect the codebase state when they were written. They require periodic review to stay relevant.
- **Scoring calibration**: The 80/100 threshold is a starting point. Real-world calibration requires shadow mode data that may not yet exist.

## Implementation Status

This section maps the RFC vision to what is implemented today, so reviewers and contributors know exactly where the boundary is.

| RFC Feature | Status | Details |
|-------------|--------|---------|
| Risk classification (low/medium/high) | **Implemented** | `RiskLevel` on Feature struct, wizard step with keyword auto-suggestion |
| Specialized plan validators | **Implemented** | Roadmap axes (Architecture, Scope), phase-plan axes (Structural, Grounding, Scope), plus generic Security/Performance/Testing as high-risk add-ons |
| Parallel validator execution | **Implemented** | goroutine-based parallel execution in `runValidatorSet()` (called by `runRoadmapMultiValidatorPlanValidation` and `runPhasePlanMultiValidatorValidation`) |
| Composed multi-validator feedback | **Implemented** | Summary table + per-domain detail in revision feedback |
| Always-on specialized validation | **Implemented** | Multi-validator pipeline is the only plan-validation path; legacy single-critic and the generic feature-plan loop were removed |
| Low-risk: skip Security + Performance validators | **Implemented** | `roadmapValidatorsForRisk()` / `phasePlanValidatorsForRisk()` only add Security + Performance + Testing axes for high-risk features |
| Numeric composite scoring (weighted) | **Documented** | Scoring model described; current code uses binary APPROVED/CHANGES_REQUESTED |
| Deterministic scoring (LLM extracts, code scores) | **Documented** | Future enhancement; requires structured extraction from validator output |
| Threshold calibration via shadow mode | **Documented** | Concept described; requires production data collection infrastructure |
| Research context in validation | **Implemented** | Validators receive research findings to check plans against discovered constraints |
| Guardian policy engine (automated VETO) | **Specified** | Policy format, categories, severity, and exception process defined in [guardian-policies.md](guardian-policies.md). Enforcement is next priority alongside artifact validation |
| Artifact validation (Phase 7) | **Specified** | Validates implementation against intent: spec compliance (40%), test coverage (30%), code quality (20%), documentation (10%) + guardian policy VETO layer. See [validation-scoring.md](validation-scoring.md) |
| Architectural contracts | **Specified** | Structural invariants with PACELC trade-offs in [architectural-contracts.md](architectural-contracts.md). Loaded into validator context for artifact validation |

The progression path: Phase 5 plan validation (done) -> Phase 7 artifact validation against intent (next) -> guardian policy enforcement -> numeric scoring with shadow mode calibration.

## Reference

| Document | Purpose |
|----------|---------|
| [workflow-details.md](workflow-details.md) | Detailed phase descriptions mapped to agentic |
| [validation-scoring.md](validation-scoring.md) | Scoring model, thresholds, and hard vetoes |
| [guardian-policies.md](guardian-policies.md) | Guardian policy format, categories, and rollout |
| [architectural-contracts.md](architectural-contracts.md) | Structural invariants with PACELC trade-offs |
| [workflow-examples.md](workflow-examples.md) | End-to-end examples at low, medium, and high risk tiers |
| [agent-configurations.md](agent-configurations.md) | Validator agent configurations and trust model |
