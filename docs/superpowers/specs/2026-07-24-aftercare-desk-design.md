# Aftercare Desk Design

## Purpose

When a feature reaches an at-rest state, the center stage should stop behaving
like a live monitor. The default surface becomes an Aftercare desk that helps an
engineer understand the completed outcome and choose the next useful maintenance
cycle. The completed transcript remains available under a secondary **Run
record** tab, and completion changes remain available under **Changes** when the
runtime advertises them.

This applies to the existing at-rest statuses recognized by `isRunAtRest`:
`CodeReady`, `Published`, and `Done`.

## Experience

The Aftercare desk opens with a status-aware handoff statement:

- `CodeReady`: implementation is complete and the feature is ready to publish,
  merge, or maintain.
- `Published`: published work remains healthy and can enter another maintenance
  cycle.
- `Done`: the feature is complete; the record and any server-advertised cycles
  remain available.

The surface is organized as an operational instrument rather than a collection
of generic dashboard cards:

1. **Handoff header** — outcome language, status, active run number, and a quiet
   transition line that makes clear there is no agent currently running.
2. **Maintenance runway** — one track for each server-advertised cycle:
   rebase, review comments, and refactor. Each track shows availability and
   concise scope derived from the authoritative feature snapshot. Activating a
   track opens the existing guarded `CycleJourneys` flow focused on that cycle.
3. **Run ledger** — current-run duration, cost, artifact count, and completion
   state. Missing metrics render as `—`; their absence never blocks the desk.
4. **Repository readiness** — one compact row per repository using
   `repoStatus`, including freshness, PR presence, publishability, and the latest
   cycle state where available.

The tab order for at-rest features is **Aftercare**, **Run record**, then
**Changes** when changes are available. Active and review states keep their
current surfaces and labels.

## Visual Direction

The design extends Agentico's precision-instrument system:

- Basalt `#101514` and Mineral `#161c1b` provide the dark structural surfaces.
- Chalk `#e8eeec` carries primary reading text.
- Ion `#88a4ff` marks available intervention.
- Patina `#5fbe8e` marks completed and healthy state.
- Amber `#f0b15a` is reserved for stale, blocked, or attention-worthy state.

Barlow Condensed carries state and section headings, Atkinson Hyperlegible
carries guidance and actions, and IBM Plex Mono carries run and repository
telemetry.

The signature element is the **maintenance runway**: horizontal operational
tracks with a leading route mark, a plain-language action, authoritative scope,
and a trailing verb. On hover or keyboard focus, a restrained signal line
travels across the track. Motion is disabled under `prefers-reduced-motion`.
Everything around the runway remains quiet.

## Architecture

Create a focused `AftercareDesk` renderer component. It consumes the feature
snapshot, the already-loaded completion preflight, and current-run metrics. It
may fetch the current run detail and artifact list as read-only data so the
default terminal surface does not depend on mounting `CurrentRunInspection`.
Read failures degrade individual ledger values and do not replace the surface
with an error.

`FeatureCockpit` owns surface selection and cycle-modal state. It:

- adds an `aftercare` surface only for `isRunAtRest(snapshot.status)`;
- prefers `aftercare` whenever a newly loaded feature is at rest;
- renames the terminal live surface to `Run record`;
- passes a requested cycle into `CycleJourneys`;
- preserves the current live/document behavior for non-terminal states.

`CycleJourneys` accepts an optional initial cycle and marks the corresponding
journey as the modal's starting target. All preflight and mutation behavior stays
inside the existing component.

## Interaction and Accessibility

- The Aftercare desk is a labelled region.
- Runway tracks are native buttons with visible focus treatment and explicit
  accessible names.
- Repository readiness uses a semantic list.
- Missing optional data is rendered honestly rather than inferred.
- Narrow layouts stack the ledger beneath the runway and keep action labels
  visible.
- No auto-running preflight or mutation is added beyond behavior already owned
  by `CycleJourneys`.

## Testing

Renderer tests cover:

- at-rest features default to Aftercare and expose Run record;
- active features retain Live activity and do not expose Aftercare;
- CodeReady, Published, and Done use appropriate handoff copy;
- only server-advertised cycle tracks render;
- selecting a track opens the existing cycle journey at the intended target;
- run-data failures preserve the desk with unavailable ledger values;
- repository rows reflect authoritative status fields.

Screenshot coverage captures the Published desktop layout and a narrow at-rest
layout. The fast suite, renderer tests, Electron build, and static checks run
before handoff.
