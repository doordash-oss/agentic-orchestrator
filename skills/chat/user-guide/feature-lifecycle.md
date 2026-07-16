# Feature Lifecycle

A feature is Agentic Orchestrator’s durable unit of work. It owns isolated git worktrees, one or more runs, phase artifacts, and server-authorized actions. The runtime owns lifecycle state; the Electron app displays refreshed snapshots and never invents a transition locally.

## Pipeline Profiles

| Profile | Phase sequence | Intended depth |
|---------|----------------|----------------|
| **Medium** | Roadmap and phase planning → Implement → Final Review → Publish | Small, well-understood work |
| **Large** | KB Build → Inquire → Research → Design → roadmap loop → Final Review → Publish | Most complex features |
| **Moonshot** | Same sequence as Large, with higher effort and per-iteration implementation review | High-risk or ambiguous work |

The current Electron creation form shows the server’s selected pipeline as a read-only default. It does not yet provide a pipeline-profile picker or per-feature model/checkpoint editors. Configure defaults in `config.yaml` before creating the feature.

## Phases

### KB Build

Builds a per-repository knowledge base covering architecture, conventions, API surface, dependencies, and verification. A current knowledge base can be reused.

### Inquire

Clarifies intent and records structured questions and answers. `defaults.inquireness` controls how frequently eligible planning questions are surfaced by the workflow engine.

### Research

Investigates relevant code paths, existing patterns, and constraints, then writes findings for later phases.

### Design

Compares implementation approaches and records a chosen direction with its trade-offs.

### Plan

Planning has two levels:

1. a strategic roadmap divided into vertical phases; and
2. a tactical plan for each roadmap phase, with repository ownership and verification requirements.

### Implement

Executes the approved phase plan in verified iterations. Each iteration writes a structured handoff and verification report.

### Final Review

Reviews the completed implementation and cycles through fixes until it passes or reaches its limits. Final Review progress is tracked per repository while the feature remains in an implementing top-level state.

### Publish

Commits and prepares publishable branches and, when configured, creates pull requests with the `gh` CLI. Multi-repository features cross-reference their pull requests.

## Runtime States

The main path is:

```text
Created → BuildingKB → Inquiring → Researching → Designing → Planning
    → Implementing → ReviewPassed → CodeReady → Published → Done
```

Frequently seen states include:

| State | Meaning |
|-------|---------|
| `Created` | Durable setup is complete or in progress; orchestration has not started |
| `BuildingKB` | Knowledge-base construction is running |
| `Inquiring` | Inquiry is running |
| `InquireReady` | Inquiry completed and the next phase is eligible |
| `Researching` | Research is running |
| `Designing` | Design is running |
| `DesignReady` | Design completed and planning is eligible |
| `PlanReady` | A roadmap or phase plan is needed |
| `Planning` | Planning is running |
| `ImplementReady` | An approved plan is ready for implementation |
| `Implementing` | Implementation or per-repository Final Review is running |
| `ReviewPassed` | Final Review passed for every repository |
| `CodeReady` | Code is ready for publishing |
| `Published` | Publishing produced the expected remote result |
| `Done` | The feature is complete |
| `Failed` | A phase exhausted its allowed recovery path |
| `Interrupted` | Work was stopped or its session ended unexpectedly |

Review checkpoints can also pause the runtime in states such as `PromptNeedsReview`, `InquiryNeedsReview`, `ResearchNeedsReview`, `DesignNeedsReview`, and `PlanNeedsReview`.

## Current Electron Controls

Home orders feature rows by operational need: intervention states first, active work next, startable work after that, then inactive and completed work. Each row shows repository, server status, current phase, priority, and a safe failure message when present. Select **Open** or **Show tab** to enter the feature cockpit.

The cockpit currently exposes only actions present in the server action catalogue:

- **Run setup** or **Retry setup** for durable setup tasks;
- **Start** for a ready feature; and
- **Stop** for active work, behind an impact confirmation.

Disabled Start and Stop controls display every server-provided reason. Successful actions are confirmed only after refreshed feature and session snapshots arrive.

An active run appears as a semantic **Signal trace**. It combines bounded transcript history with live output, groups routine records, preserves selected raw records, and shows stream health. After a confirmed Stop, the completed transcript remains inspectable and Home reorders from the new authoritative state.

## Pending Desktop Lifecycle Controls

The engine contains lifecycle paths that the current Electron renderer does not yet expose. There are no supported desktop controls for:

- answering planning or review-gate questions;
- resuming interrupted work or resuming all features;
- retrying a failed orchestration phase;
- rewinding to an earlier phase;
- browsing or editing run artifacts;
- publishing, marking Done, or cleaning a worktree; or
- rebase, review-comments, refactor, merge, and other post-publish cycles.

Do not substitute retired terminal keybindings for these actions. A capability becomes available in the desktop app only when a labeled control is present and authorized by the current server snapshot.

## Checkpoints

Checkpoint defaults are runtime configuration:

| Checkpoint | Gates before |
|------------|--------------|
| Inquiry Review | Research |
| Research Review | Design |
| Design Review | Plan |
| Roadmap Review | Phase planning |
| Phase Plan Review | Implementation |
| Manual Publish | Publish |

The current desktop app can display the resulting status in its feature list or cockpit, but resolving these checkpoint states is pending.

## Timing and Cost

The runtime records phase timing and accumulated provider cost in feature state. A dedicated Electron timing/cost presentation is not part of the current cockpit; inspect runtime artifacts only through supported development tooling until that surface is delivered.
