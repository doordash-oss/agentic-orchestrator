# Feature Lifecycle

Features are the core unit of work in Agentic Orchestrator. Each feature gets an isolated git worktree and progresses through a series of phases driven by AI agents. This guide covers the full lifecycle from creation to completion.

## Pipeline Profiles

When creating a feature you choose a pipeline profile that determines which phases run and how much rigor is applied:

| Profile | Phases | Effort | Skip Iteration Review | Skip Plan Validation |
|---------|--------|--------|-----------------------|----------------------|
| **Medium** | Plan → Implement → Review → Publish | Medium | Yes | Yes |
| **Large** | KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish | High | Yes | No |
| **Moonshot** | KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish | Max | No | No |

- **Medium** is the fastest path — skips early research phases and plan validation, goes straight to planning and implementation.
- **Large** runs all eight phases with high effort. Skips per-iteration review but always runs a Final Review after implementation.
- **Moonshot** runs the same phases as Large but at maximum rigor. Retains per-iteration review gates in addition to the Final Review.

## Phases

Each phase runs in its own AI agent session. Here is what happens in each:

### KB Build (Knowledge Base)
Builds a knowledge base about the target repository — architecture, conventions, testing patterns, dependencies. This gives later phases a structured reference instead of raw codebase exploration.

### Inquire
Explores the problem space by analyzing the codebase and gathering context. The **inquireness** setting (none, medium, high) controls how often the harness surfaces planning questions to you.

### Research
Deep dives into the codebase to understand relevant code paths, existing patterns, and technical constraints. Produces research artifacts that inform planning.

### Design
Generates and evaluates implementation approaches. Considers trade-offs, risks, and alternatives before committing to a design direction. (Older docs and persisted state may refer to this phase as "Design" — it is the same phase under its legacy name and continues to load without migration.)

### Plan
Creates a detailed implementation plan. Uses **two-tier planning**:

1. **Roadmap** (strategic) — a high-level plan that breaks the feature into thin vertical-slice phases. Phase 1 proves the riskiest path first; later phases expand behavior progressively. Stubs are optional and used only when the roadmap explicitly calls for them.
2. **Phase Plans** (tactical) — concise per-phase plans derived from the approved roadmap. Multi-repo phase plans route work through per-task `**Repo:** <name>` tags and are validated by domain-specific critics.

Plan validation can be skipped in Medium mode but is enforced in Large and Moonshot.

### Implement
Writes code following the approved plan. Runs in an iterative loop — each iteration produces a verification report. In Moonshot mode, each iteration goes through a review gate. In Medium and Large, per-iteration review is skipped; quality gating is deferred to the Final Review.

### Review
Runs a **Final Review** loop after implementation: a review agent examines the code and writes feedback, then a fix agent addresses any issues. This cycle repeats until the review passes or limits are reached. All pipeline profiles run the Final Review.

Final Review is tracked **per repo** rather than as a top-level feature status. While the feature stays in `Implementing`, each repo's per-repo state advances through `awaiting_final_review` and `final_reviewing` (see `RepoImplStatus` in `internal/feature/feature.go`). The feature transitions to `ReviewPassed` once every repo's per-repo state reaches `review_passed`.

### Publish
Creates a pull request on GitHub via the `gh` CLI. When a feature spans 2+ repositories, cross-reference PR tables are injected into PR bodies and retroactively updated as new PRs are created.

## State Machine

Features progress through 22 possible top-level states (Final Review is carried by per-repo state, not a feature-level status). The main flow is:

```
Created → BuildingKB → Inquiring → Researching → Designing → Planning
    → Implementing → ReviewPassed → CodeReady → Published → Done
```

### Intermediate States

| State | Description |
|-------|-------------|
| `Created` | Feature created, not yet started |
| `BuildingKB` | Knowledge base construction in progress |
| `Inquiring` | Inquiry phase running |
| `InquireReady` | Inquiry complete, ready for next phase |
| `Researching` | Research phase running |
| `Designing` | Design phase running (legacy alias: `Designing`) |
| `DesignReady` | Design complete, ready for planning (legacy alias: `DesignReady`) |
| `PlanReady` | Plan needed |
| `Planning` | Plan creation in progress |
| `ImplementReady` | Plan approved, ready for implementation |
| `Implementing` | Implementation in progress (Final Review runs here, tracked per repo via `RepoImplStatus`) |
| `ReviewPassed` | Final Review passed for every repo |
| `CodeReady` | Code ready for publishing |
| `Published` | PR created |
| `Done` | Feature complete |

### Review Gate States

When checkpoints are enabled, features pause at these states for human review:

| State | Gates Before |
|-------|-------------|
| `PromptNeedsReview` | Inquiry phase |
| `InquiryNeedsReview` | Research phase |
| `ResearchNeedsReview` | Design phase |
| `DesignNeedsReview` | Plan phase |
| `PlanNeedsReview` | Implementation phase |

### Error States

| State | Description |
|-------|-------------|
| `Failed` | Phase failed after exhausting retries |
| `Interrupted` | Manually stopped or process crashed |

## Checkpoints (Review Gates)

Checkpoints pause the pipeline between phases so you can review artifacts before proceeding. Each profile enables different checkpoints by default:

| Checkpoint | Gates Before | Medium | Large | Moonshot |
|------------|-------------|---------|----------|----------|
| Inquiry Review | Research | Off | Off | Off |
| Research Review | Design | Off | Off | Off |
| Design Review | Plan | Off | On | On |
| Plan Review | Implementation | Off | Off | On |
| Manual Publish | (publish step) | On | On | On |

You can toggle any applicable checkpoint in the feature creation wizard (Step 4) or in `config.yaml`.

## Rewind and Retry

Press `Ctrl+R` on a feature to rewind it to a previous phase. This resets the feature's state and re-runs from the selected phase. Useful when you want to change the plan or re-run research after new context.

## Feature Creation

Create features from the TUI wizard by pressing `n` on the dashboard. The wizard guides you through name, description, target repos, pipeline profile, checkpoints, model settings, risk level, and exit criteria.

## Post-Publish Actions

After a feature is published, several actions are available:

- **Tweak** (`t`) — launches an interactive session to make manual changes, then auto-commits and pushes
- **Refactor** (`Shift+F`) — re-runs the full pipeline on a published feature
- **Rebase** (`b`) — rebases the feature branch onto the latest main
- **Review Comments** (`g`) — fetches and addresses PR review comments
- **Merge** (`Shift+M`) — merges to the base branch (local repos only)
- **Done** (`Shift+D`) — marks the feature as completed

## Timing and Cost

Agentic Orchestrator tracks phase timings and accumulated cost per feature. These are displayed in the feature detail view and persisted in the feature state.
