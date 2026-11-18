# Feature Lifecycle

A feature is Agentic Orchestrator’s durable unit of work. It owns isolated git worktrees, one or more runs, phase artifacts, and server-authorized actions. The runtime owns lifecycle state; the Electron app displays refreshed snapshots and never invents a transition locally.

## Pipeline Profiles

| Profile | Phase sequence | Intended depth |
|---------|----------------|----------------|
| **Medium** | Roadmap and phase planning → Implement → Final Review → Publish | Small, well-understood work |
| **Large** | Inquire → Research → Design → roadmap loop → Final Review → Publish | Most complex features |
| **Moonshot** | Same sequence as Large, with per-phase review after each successful implementation iteration | High-risk or ambiguous work |

The Electron creation wizard exposes a pipeline-profile picker (Medium / Large / Moonshot) and a Review step for per-feature model, checkpoint, risk, exit-criteria, and inquireness editors. Defaults still come from `config.yaml`, but every value can be overridden per feature before creation.

## Phases

### Inquire

Clarifies intent and records structured questions and answers, probing the feature's exit criteria along the way. It reads just enough of the target repositories, including their instruction files such as `AGENTS.md`, to ask grounded questions. `defaults.inquireness` controls how frequently eligible planning questions are surfaced by the workflow engine.

### Research

Investigates relevant code paths, existing patterns, and constraints, then writes findings for later phases, including a short "Conventions and Verification" section: the conventions each investigated area follows and the commands that verify changes there.

### Design

Compares implementation approaches and records a chosen direction with its trade-offs, distilling the exit criteria into the design's `## Acceptance Criteria` section — the acceptance authority that downstream phases judge against.

### Plan

Planning has two levels:

1. a strategic roadmap divided into vertical phases (on design-less pipelines such as Medium, the roadmap distills the exit criteria into its `## Overall Exit Criteria` section); and
2. a tactical plan for each roadmap phase: tasks with acceptance criteria, each tagged with the repository it belongs to (`**Repo:**`), a `**Frontend:**` metadata field, and one prose `## Verification` section saying which commands to run and what to look at.

### Implement

Executes the approved phase plan in iterations. On every profile the implementer runs the plan's `## Verification` itself before reporting success, and writes free-form handoff notes to `progress.md` for the next session. Nothing parses those notes: whether an iteration succeeds or retries comes from the agent's completion outcome.

On Moonshot, each successful iteration is followed by a per-phase review that follows the review round rules below; its Functionality/Evidence reviewer runs the plan's verification itself. The handoff question asks whether to continue with guidance, accept and finish the phase, or stop, and stopping parks the feature as `Interrupted` in that phase. Medium and Large have no per-phase review and are verified at Final Review.

### Final Review

Reviews the completed implementation and cycles through fixes until it passes or reaches its limits. The QA reviewer runs the product and the full test suites.

Review rounds (here and in Moonshot's per-phase review) follow these rules:

- The first round runs every reviewer over the full diff.
- Later rounds rerun only the reviewers that requested changes, scoped to their open findings and what changed since.
- An earlier approval is confirmed once if code changed after it.
- The fixer (or, per phase, the implementer) may dispute a finding by asking you. Your answer is recorded in a decisions file that later reviewers read, and later approved decisions override the design's acceptance criteria.
- If a fix round changes nothing, or the same reviewer requests changes three rounds in a row, the loop asks you one question: continue with guidance, accept (Final Review proceeds to Publish), or stop. Stopping parks the feature as `Interrupted`; resuming re-enters the same review.

### Publish

Commits and prepares publishable branches and, when configured, creates pull requests with the `gh` CLI. Multi-repository features cross-reference their pull requests.

## Runtime States

The main path is:

```text
Created → Inquiring → Researching → Designing → Planning
    → Implementing → ReviewPassed → FinalReviewing → CodeReady → Published → Done
```

Frequently seen states include:

| State | Meaning |
|-------|---------|
| `SettingUpWorktrees` | Durable setup of the feature's worktrees is queued or running |
| `Created` | Durable setup is complete; orchestration has not started |
| `Inquiring` | Inquiry is running |
| `InquireReady` | Inquiry completed and the next phase is eligible |
| `Researching` | Research is running |
| `Designing` | Design is running |
| `DesignReady` | Design completed and planning is eligible |
| `PlanReady` | A roadmap or phase plan is needed |
| `Planning` | Planning is running |
| `ImplementReady` | An approved plan is ready for implementation |
| `Implementing` | Implementation is running, including Moonshot's per-phase review |
| `ReviewPassed` | Implementation finished for every repository; Final Review runs next, and the feature returns here after it passes |
| `FinalReviewing` | Final Review is running |
| `CodeReady` | Code is ready for publishing |
| `Published` | Publishing produced the expected remote result |
| `Done` | The feature is complete |
| `Failed` | A phase exhausted its allowed recovery path |
| `Interrupted` | Work was stopped, a review handoff was answered with stop, or its session ended unexpectedly |

Review checkpoints can also pause the runtime in states such as `PromptNeedsReview`, `InquiryNeedsReview`, `ResearchNeedsReview`, `DesignNeedsReview`, and `PlanNeedsReview`.

### Features saved by older versions

State saved by older versions still loads. A feature that was paused waiting for user input, or that was building a knowledge base, loads as `Interrupted` and resumes normally; a feature that was building a knowledge base resumes at Inquire. Older features still show a Knowledge Base entry in their history.

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
- the **Supervisor** conversation.

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
