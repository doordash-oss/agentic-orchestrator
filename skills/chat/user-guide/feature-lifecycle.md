# Feature Lifecycle

A feature is Agentic Orchestrator’s durable unit of work. It owns isolated git worktrees, one or more runs, phase artifacts, and server-authorized actions. The runtime owns lifecycle state; the Electron app displays refreshed snapshots and never invents a transition locally.

## Pipeline Profiles

| Profile | Phase sequence | Intended depth |
|---------|----------------|----------------|
| **Medium** | Roadmap and phase planning → Implement → Final Review → Publish | Small, well-understood work |
| **Large** | KB Build → Inquire → Research → Design → roadmap loop → Final Review → Publish | Most complex features |
| **Moonshot** | Same sequence as Large, with higher effort and per-iteration implementation review | High-risk or ambiguous work |

The Electron creation wizard exposes a pipeline-profile picker (Medium / Large / Moonshot) and a Review step for per-feature model, checkpoint, risk, exit-criteria, and inquireness editors. Defaults still come from `config.yaml`, but every value can be overridden per feature before creation.

## Phases

### KB Build

Builds a per-repository knowledge base covering architecture, conventions, API surface, dependencies, and verification. A current knowledge base can be reused.

### Inquire

Clarifies intent and records structured questions and answers, probing the feature's exit criteria along the way. `defaults.inquireness` controls how frequently eligible planning questions are surfaced by the workflow engine.

### Research

Investigates relevant code paths, existing patterns, and constraints, then writes findings for later phases.

### Design

Compares implementation approaches and records a chosen direction with its trade-offs, distilling the exit criteria into the design's `## Acceptance Criteria` section — the acceptance authority that downstream phases judge against.

### Plan

Planning has two levels:

1. a strategic roadmap divided into vertical phases (on design-less pipelines such as Medium, the roadmap distills the exit criteria into its `## Overall Exit Criteria` section); and
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

## Electron Lifecycle Controls

Home orders feature rows by operational need: intervention states first, active work next, startable work after that, then inactive and completed work. Each row shows repository, server status, current phase, priority, and a safe failure message when present. Select **Open** or **Show tab** to enter the feature cockpit.

The cockpit exposes every action present in the server action catalogue:

- **Run setup** or **Retry setup** for durable setup tasks;
- **Start** for a ready feature;
- **Stop** for active work, behind an impact confirmation;
- **Resume** interrupted work, including a bulk resume action on the dashboard;
- **Retry** a failed orchestration phase;
- **Rewind** to an earlier phase, with a server-authored target picker, consequence preview, typed confirmation, and optional Advanced pipeline upgrade;
- **Review** gates for inquiry, research, design, roadmap, phase plan, and manual publish checkpoints;
- **Artifact browsing** for plans, roadmaps, Q&A, and diffs, with markdown rendering;
- **Live preview / logs** per run;
- **Publish, Rebase card, Merge, Refactor card, Review-feedback card, Done, Clean worktree, and Delete** from the aftercare workspace; and
- **Ask Me Anything** read-only chat.

Disabled controls display every server-provided reason. Successful actions are confirmed only after refreshed feature and session snapshots arrive.

An active run appears as a semantic **Signal trace**. It combines bounded transcript history with live output, groups routine records, preserves selected raw records, and shows stream health. After a confirmed Stop, the completed transcript remains inspectable and Home reorders from the new authoritative state.

A capability becomes available in the desktop app only when a labeled control is present and authorized by the current server snapshot. Do not substitute retired terminal keybindings for these actions.

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

The desktop app displays checkpoint status in its feature list and cockpit, and resolving these checkpoint states is delivered through the review workspace.

## Timing and Cost

The runtime records phase timing and accumulated provider cost in feature state. The feature cockpit surfaces elapsed time and accumulated cost in the current-run inspection panel.
