# Post-Implementation Workspace Design

**Date:** 2026-07-25  
**Status:** Approved  
**Scope:** Desktop feature cockpit, post-implementation cycles, and feature-linked
`need_user_input` presentation.

## Purpose

Once the primary implementation run reaches an at-rest state, the feature tab
must stop presenting the completed pipeline as if it were still active. At rest,
the tab is a quiet launchpad for the next valid action. When an aftercare cycle
starts, that launchpad yields to a focused execution workspace using the same
polished live-agent experience as regular implementation.

This design replaces the current model in which:

- the completed Setup / Plan / Implement / Review / Publish spine remains above
  every post-implementation state;
- Aftercare, Run record, and Changes compete as tabs;
- an active cycle remains visually nested inside a completed feature;
- cycle progress is represented by the original run's roadmap gauge;
- repository, comment, and plan dashboards compete with the live agent;
- `need_user_input` renders inline in the live surface; and
- successful watchdog recovery is indistinguishable from stale “Running” state.

This specification supersedes
`2026-07-24-aftercare-desk-design.md` and the gate-specific presentation in
`2026-07-23-inline-live-preview-attention-design.md`. Other attention types
continue to use their existing behavior unless explicitly changed here.

## Product Principles

1. **One lifecycle at a time.** The primary feature pipeline, idle Aftercare,
   and an active aftercare cycle are mutually exclusive workspaces.
2. **Current work owns the stage.** Completed history stays reachable but never
   competes with active work.
3. **Cycle-specific chrome, shared execution canvas.** Rebase, review comments,
   and refactor have truthful phase names and completion behavior. Their main
   surface is the familiar live-agent interface, not three bespoke dashboards.
4. **Progressive disclosure.** Run evidence, changes, PRs, logs, and cycle
   records remain available through compact links, drawers, and overlays.
5. **Authoritative state only.** The UI never infers progress from elapsed time
   or invents stages the backend does not execute.
6. **Operational recovery stays quiet.** A watchdog retry that succeeds is
   invisible plumbing. Only a user decision or exhausted recovery interrupts
   the workspace.

## Workspace State Model

`FeatureCockpit` resolves one primary workspace from the authoritative feature
snapshot:

| State | Primary workspace |
| --- | --- |
| Setup, planning, implementation, review, publish in progress | Existing regular feature workspace |
| `CodeReady`, `Published`, or `Done`; no active/failed cycle selected | Aftercare workspace |
| Cycle `running`, `reviewing`, or `need_user_input` | Focused cycle workspace |
| Cycle `failed` and not dismissed | Focused failed-cycle workspace |
| Sealed historical run selected | Existing archive mode |

An active or failed cycle takes precedence over the feature's at-rest status.
This matters because post-implementation cycles intentionally leave the feature
status as `CodeReady` or `Published`.

The resolver should be a pure renderer model rather than scattered conditional
checks. It consumes:

- feature status;
- active cycle type, status, count, and iteration;
- per-repository cycle/rebase state;
- current phase status and review gate;
- selected archive run; and
- a renderer-local dismissal identity for a failed cycle.

## Quiet Aftercare Workspace

### Shell

Aftercare applies to `CodeReady`, `Published`, and `Done`.

At rest:

- hide the completed primary phase spine;
- hide the internal Aftercare / Run record / Changes stage tabs;
- let the application tab own the feature title—do not repeat it in a feature
  banner;
- render a slim state/action bar containing the status chip and secondary
  lifecycle controls;
- render the action runway in the main column; and
- retain a fixed right facts rail on wide layouts.

The right rail contains only durable feature/run facts:

- status;
- repository or repositories;
- branch;
- current run number;
- total elapsed time;
- total cost;
- PR link/state; and
- repository freshness.

The “Durable setup” section and permanent setup task are never rendered in an
at-rest or cycle workspace. Setup remains available in the regular workspace
where it is operationally relevant.

On narrow layouts, the facts rail becomes the existing inspector drawer.

### Action runway

The runway is the dominant Aftercare content. It combines all server-enabled
next actions in a stable order:

1. completion actions such as Publish or Merge;
2. Rebase;
3. Review comments;
4. Refactor.

Only enabled actions render as runway rows. A disabled action is omitted unless
its disabled state itself requires user intervention; in that case it may render
with one concise reason.

`CodeReady` therefore places Publish first when the action catalog enables it.
After publishing, the Publish row disappears and the headline changes from
“Implementation complete” to “Published. Choose what comes next.”

Each row has one title, one short explanation, and one direct verb. Activating a
row opens the already-approved dedicated preflight modal. No preflight data is
duplicated on the runway.

### Completed evidence

The current run and completion evidence become compact links beneath the runway:

- `Run #N record`;
- `View changes`; and
- `Open PR`, when available.

They open read-only overlays or the existing external PR destination. They do
not recreate stage tabs.

When a cycle succeeds, the UI returns directly to Aftercare and inserts a
compact receipt:

```text
Review comments completed
Changes pushed · Replies posted                    View cycle record
```

The receipt is derived from the completed cycle type and server state. It must
not promise a push, reply, merge, or publish unless that outcome is
authoritatively known. On the live transition, the renderer may show a richer
receipt using the just-completed cycle identity it observed. After reopening,
`repoStatus.cycleType` plus `repoStatus.cycleStatus === "completed"` supports a
generic `<cycle> completed` receipt; side-effect details are omitted because the
current read model does not preserve them.

No separate completion screen or “Return to Aftercare” confirmation is used on
success.

## Focused Cycle Workspace

Starting any aftercare cycle replaces Aftercare with a single cycle workspace.
There is no Aftercare tab and no Run record or Changes tab while the cycle owns
the feature.

### Shared shell

The cycle workspace contains:

1. a compact context header with cycle type/count, a link to the run record, and
   Stop;
2. a cycle-specific phase spine;
3. a current/next reading;
4. the standard live-agent execution canvas;
5. the compact feature facts rail; and
6. artifacts and bounded logs through the existing collapsible resources.

The main surface reuses `CurrentRunInspection` transcript, cohort selection,
conversation/signal toggle, verification state, review state, metrics,
artifacts, logs, and fullscreen behavior. Cycle mode suppresses:

- the regular roadmap gauge;
- “Mutable current run” copy;
- completed primary-pipeline language; and
- inline feature attention forms.

The live-agent canvas remains the visual focus. Repository matrices, individual
review-comment cards, refactor task dashboards, and permanent conflict counts
are intentionally excluded. Those details may appear in the transcript,
artifacts, logs, or a blocking gate when actionable.

### Cycle phase maps

The macro spine communicates the real execution topology without pretending all
cycles are identical.

#### Rebase

1. Inspect and rebase
2. Resolve conflicts — conditional
3. Final review
4. Publish

The deterministic harness may occupy the first stage without an LLM session.
The execution canvas then shows truthful live operational activity. If conflict
resolution dispatches an agent, the same canvas begins streaming that session.
Rebase is the only current cycle with a separate final-review stage.

#### Review comments

1. Comments ready
2. Address and validate
3. Push and reply

The comments were fetched before dispatch and become the implementation plan.
One session normally addresses the complete batch. Iteration and iteration
review are substate within “Address and validate,” not separate dashboard
columns or a fabricated feature-level final review.

#### Refactor

1. Plan refactor
2. Implement and validate
3. Deliver

Planning and plan revision are visible current states. Implementation and
iteration review share the second macro stage. The prompt becomes the workspace
headline; the resolved plan remains an artifact rather than permanent main-stage
content.

### Current/next reading

The line beneath the spine states exactly what is happening and what follows:

```text
Iteration 2 · Implementing the refactor       Next · Iteration review
```

Copy is derived from cycle type, cycle phase, iteration, review gate,
verification state, and finalization state. It never derives “working” from an
old timestamp.

Add an optional stable `phase` field to the cycle summary. The server read model
derives it from persisted orchestration state:

- Rebase uses `RebaseOperation.Stage`, repository rebase statuses, and active
  cycle status to project `inspect_rebase`, `resolve_conflicts`, or
  `final_review`.
- Review comments uses `CurrentPhaseStatus`, review-gate state, and active
  cycle status to project `address_validate`.
- Refactor uses `CurrentPhaseStatus`, review-gate state, and active cycle status
  to project `plan_refactor` or `implement_validate`.

`push_reply`, `deliver`, and `publish` are valid projected phases when the
orchestrator can expose them before clearing the cycle, but the UI does not
require observing those brief finalization windows. The renderer never parses
artifact paths or session identifiers.

Brief delivery steps may complete before a UI refresh; it is acceptable for the
spine to move directly from implementation/review to the Aftercare receipt.

## Need-User-Input Modal

Feature-linked `need_user_input` gates use the same notification path as agent
questions:

- desktop notification;
- global Attention count;
- affected feature-tab badge; and
- Attention inbox entry that jumps to the affected feature.

Opening or focusing a feature with a newly pending gate opens one shared
floating modal over the current workspace, whether the underlying workspace is
a regular run or an aftercare cycle.

### Modal contract

The modal contains:

- “Agent needs your input” title;
- phase/cycle and iteration context;
- one labelled free-text textarea per gate question;
- autosaved drafts;
- `Answer later`; and
- one primary `Resume agent` action.

All questions require non-empty answers before resume. Submission saves the
latest drafts, resolves the gate with `decision: resume`, refreshes attention
and feature state, then resumes the same phase/iteration/cycle.

`Answer later`, Escape, and backdrop dismissal close the modal without resolving
the gate. Drafts remain saved and the Attention/tab badges remain. Selecting the
attention item reopens the same modal.

The first presentation autofocuses the first unanswered textarea. The modal
uses the shared focus trap, scroll lock, focus restoration, and reduced-motion
rules.

Destructive gate abort remains available through a secondary overflow action
with the existing confirmation semantics; it is not placed beside the primary
resume action.

The old inline `AttentionDetail` gate form is removed from
`CurrentRunInspection`. Other attention types retain their existing
presentation.

## Recovery, Failure, and Stop

### Watchdog recovery

A successful automatic watchdog recovery is silent:

- no Attention item;
- no recovery banner;
- no phase-spine state;
- no toast; and
- no user action.

The transcript/cohort reconnects and continues. Existing low-level logs and
signal trace remain available for diagnosis.

This does not remove the separate runtime-orphan recovery workspace; that flow
handles recovery requiring explicit operator coordination rather than an
automatic provider-session retry.

### Failed cycle

When recovery is exhausted or a cycle fails, the focused cycle workspace
remains visible with:

- a failed spine/current-state treatment;
- the exact safe-display error;
- preserved last agent activity;
- `Retry cycle` as the primary action when enabled;
- `Return to Aftercare` as a secondary action; and
- artifacts/logs for diagnosis.

Returning to Aftercare dismisses that failed-cycle presentation locally and
surfaces the failed cycle as a compact actionable receipt/row. It does not
rewrite server state.

### Stop

Stop remains available while a cycle is active. After the server confirms the
cycle is no longer active, the workspace returns to Aftercare with a restrained
stopped receipt. The receipt states only what the server guarantees, for
example: “Cycle stopped · No completion action was dispatched.”

## Renderer Architecture

Extract post-implementation behavior from the already-large
`FeatureCockpit.tsx` into focused units:

- `postImplementationModel.ts`
  - pure workspace-state resolver;
  - Aftercare action ordering;
  - cycle phase/current-next model;
  - receipt model.
- `AftercareWorkspace.tsx`
  - idle headline, action runway, record links, receipt.
- `CycleWorkspace.tsx`
  - cycle header, spine, status/failure treatment, and cycle-mode live canvas.
- `FeatureFactsRail.tsx`
  - shared at-rest/cycle feature and run facts without setup.
- `NeedUserInputModal.tsx`
  - gate draft/resume presentation using existing attention mutation paths.
- `RunRecordModal.tsx`
  - read-only current/sealed run evidence, reusing existing archive/inspection
    components where possible.

The existing dedicated cycle preflight modals remain the only cycle start
surfaces.

`FeatureCockpit` continues to own authoritative snapshot loading, action
dispatch, modal selection, and attention refresh. It delegates workspace
selection and presentation to the extracted components.

## Data and State Flow

1. The snapshot loads from the server.
2. `postImplementationModel` resolves regular, Aftercare, or cycle mode.
3. At rest, action rows come only from enabled action-catalog entries.
4. Starting a cycle refreshes the snapshot; the active cycle immediately takes
   over the workspace.
5. Live preview, transcript cohort, metrics, artifacts, and logs continue
   through existing IPC calls.
6. Gate attention is derived from the existing `AttentionGate` item and
   autosave/resolve IPC methods.
7. Cycle success clears active cycle state; the resolver returns to Aftercare
   and presents a truthful receipt.

No renderer code reads runtime files directly.

## Visual System

The redesign stays inside Agentico's current tokens:

- Barlow Condensed for operational headings and current-state readings;
- Atkinson Hyperlegible for user-facing explanation and agent conversation;
- IBM Plex Mono for identifiers, telemetry, and phase labels;
- `--color-signal` for completion and primary confirmation;
- `--color-accent` for the active phase and current work;
- `--color-attention` for CodeReady and user-required state;
- danger only for terminal failure or destructive controls; and
- hairline borders rather than nested card chrome.

The signature element is the **cycle flight line**: a compact phase spine paired
with a single current/next reading. It changes vocabulary per cycle while the
execution canvas beneath remains stable.

The earlier bespoke repository/comment/task dashboards were rejected because
they overfit cycle-specific detail and displaced the agent—the actual unit of
progress.

Motion is limited to the existing active needle and modal enter transition,
both disabled under `prefers-reduced-motion`.

## Accessibility

- Exactly one primary workspace and one primary heading are exposed.
- Hidden completed spines and inactive surfaces are not left in the
  accessibility tree.
- The cycle spine has a cycle-specific accessible label and `aria-current`.
- Current/next state uses polite live announcements only on meaningful state
  transitions.
- The input modal is a labelled modal dialog with focus trap and restoration.
- Every gate question has a programmatic label and persisted draft.
- Failed state uses text and iconography in addition to color.
- Narrow mode preserves action labels and moves facts into a drawer.

## Testing

### Pure models

- workspace resolution prefers active/failed cycles over at-rest status;
- `CodeReady`, `Published`, and `Done` resolve to Aftercare when no cycle owns
  the stage;
- completion and cycle actions are ordered correctly;
- cycle phase/current-next copy covers rebase harness, conflict resolution,
  final review, review-comment iterations, refactor planning, iteration review,
  delivery, need-input, and failure;
- receipts never claim unsupported side effects.

### Renderer

- at-rest views hide the completed primary spine and stage tabs;
- the feature title is not repeated outside the application tab;
- the facts rail shows status/repository/branch/time/cost/PR/freshness and never
  shows Durable setup;
- CodeReady shows Publish when enabled and Published does not;
- starting each cycle replaces Aftercare with one focused cycle workspace;
- cycle workspaces render the standard live transcript and suppress the regular
  roadmap gauge;
- no permanent repository/comment/task dashboard renders;
- success returns directly to Aftercare with a receipt;
- failed cycle preserves activity and exposes Retry/Return;
- gate attention automatically opens the free-text modal once per gate;
- dismissing preserves drafts and badges;
- Attention navigation reopens the modal;
- submission resumes the correctly routed regular or cycle gate; and
- successful watchdog recovery creates no renderer notification.

### Integration and screenshots

Update screenshot-capture fixtures for:

- CodeReady Aftercare with Publish and facts rail;
- Published Aftercare;
- active Rebase;
- active Review comments;
- active Refactor;
- regular-run need-input modal;
- cycle need-input modal; and
- failed cycle.

Manual verification covers narrow layout, keyboard focus, reduced motion,
attention navigation, modal draft persistence, cycle success transition,
watchdog retry, and all existing completion/preflight modals.

## Out of Scope

- Changing the semantics of rebase, review-comment, or refactor execution.
- Inventing a feature-level final review for review comments or refactor.
- Redesigning the global Attention inbox beyond gate navigation.
- Redesigning Home, Settings, creation, archive history, or the regular
  implementation workspace except for the shared gate modal.
- Exposing raw runtime paths or provider-session identifiers in the UI.
