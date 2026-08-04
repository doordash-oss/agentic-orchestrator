# Warm Workspace Tabs

## Problem

`WorkspaceShell` renders only the active feature's `FeatureCockpit`. Switching
away unmounts the cockpit, and switching back mounts a new instance whose first
state is `loading`. The cockpit then refetches the full authoritative feature
snapshot before it can render. This makes ordinary tab navigation feel like a
page navigation and also discards local view state such as the selected stage,
scroll position, and open drawers.

## Goals

- Switching among already-open workspace tabs shows the retained view
  immediately, without a loading screen.
- Meaningful in-tab state survives tab switches, including scroll position,
  the selected stage, drafts, and open drawers.
- Feature data remains authoritative and reasonably current without creating
  unbounded background polling or duplicate refresh requests.
- Only the active panel participates in the accessibility tree.
- Closing a tab releases its renderer state and subscriptions.

## Non-goals

- Persisting every transient view detail across an application restart.
- Building a general-purpose client-side domain cache.
- Adding idle/LRU eviction before measurements show that retained cockpits cause
  material memory pressure.
- Changing server APIs or the persisted `TabsPrefs` schema.

## Considered Approaches

### 1. Retain each open panel and make activity explicit (selected)

Render Home, Settings, and every open feature panel concurrently. Hide inactive
panels with the HTML `hidden` attribute and pass an `active` signal into each
`FeatureCockpit`. React state and DOM state remain warm, while data refresh work
can be scheduled according to whether the panel is visible.

This is the smallest change that fixes both the loading flash and lost local
state. Its cost is additional DOM and component memory proportional to the
number of open tabs. Open tabs are already an explicit user-managed set, and
closing a tab immediately releases that cost.

### 2. Lift all cockpit state into `WorkspaceShell` or a shared store

Unmount inactive cockpits but move their snapshots and view state into a cache.
This can reduce retained DOM, but nearly every piece of cockpit-local state
would need serialization and restoration. It creates a large, invasive cache
surface and makes future cockpit features easier to break.

### 3. Serialize and evict inactive panels immediately

Capture a compact view-state record whenever a tab loses focus, unmount the
panel, and reconstruct it on activation. This bounds memory aggressively but
still requires remount work and careful restoration of scroll, focus, modal,
and nested component state. It is appropriate only if profiling later shows
that warm tabs are too expensive.

## Architecture

### Workspace panel ownership

`WorkspaceShell` remains the owner of tab identity and activation. Instead of a
single conditional panel branch, it renders one stable panel per workspace tab:

- Home and Settings each have a stable panel.
- Every entry in `tabs.open` has a keyed `FeatureCockpit` panel.
- Exactly one panel is visible; all others have `hidden` set.
- Each feature cockpit receives `active={active === tab.featureId}`.
- Removing an entry from `tabs.open` unmounts that cockpit.

The creation flow remains governed by the existing dirty-draft navigation
guard. It may occupy the Home panel, but a guarded navigation must resolve
before another panel becomes active.

### Feature refresh lifecycle

Each `FeatureCockpit` performs its initial authoritative load when first opened.
After that load succeeds, the last snapshot remains renderable during every
silent refresh.

The existing application-event stream remains the freshness source:

- A relevant invalidation for the active cockpit requests an immediate silent
  refresh.
- A relevant invalidation for an inactive cockpit marks it dirty and schedules
  one trailing refresh five seconds later. Multiple invalidations during that
  window collapse into one request.
- Activating a dirty cockpit cancels the trailing delay and flushes one silent
  refresh immediately.
- Refreshes are single-flight per cockpit. If another invalidation arrives
  during a request, one trailing refresh runs after the request completes.
- When the whole Electron document is hidden, background refreshes remain
  dirty rather than starting network work. Returning the document to visible
  schedules the five-second background refresh; activating a dirty cockpit
  flushes it immediately.

This is event-driven stale-while-revalidate behavior. There is no periodic
polling interval: an inactive, unchanged feature performs no refresh work.

### Rendering and errors

An initial load may show the existing loading view because there is no cached
snapshot yet. A later refresh never replaces loaded content with the loading
view.

If a silent refresh fails, the cockpit retains its last successful snapshot and
shows the existing stale/stream-health treatment. A foreground retry remains
available where the current cockpit already exposes one. An authoritative
`not_found` response still transitions to the existing missing-feature state,
because showing a deleted feature as live would be misleading.

### Accessibility and focus

Inactive panels use `hidden`, so their controls are neither focusable nor
announced by assistive technology. Tab buttons continue to expose
`aria-selected` and `aria-controls`, and only the selected panel is visible.
The active panel's internal focus and scroll state stay intact across a switch;
normal tab activation does not automatically move focus into panel content.

## Resource Policy

The first implementation retains every explicitly open workspace tab. This is
preferable to choosing an arbitrary time threshold without memory data. Manual
tab closure is the deterministic eviction mechanism.

If profiling later demonstrates meaningful pressure, add a separate LRU policy:
serialize essential view state, evict only clean inactive panels beyond a
measured tab or memory threshold, and restore cached content before silently
refreshing. That follow-up must never evict unsaved drafts without preserving
them.

## Testing

Renderer tests must prove the behavior at the user boundary:

- Opening two features loads each once; switching among them does not show the
  runtime loading status or issue another initial load.
- A feature's selected stage and other representative local state survive a
  round trip through another tab.
- Only the active tabpanel is visible and accessible.
- Closing a feature tab unmounts it and prevents later invalidations from
  refreshing it.
- Background invalidations coalesce, and activation flushes a dirty cockpit.
- A failed silent refresh retains the previously rendered snapshot.
- A `not_found` refresh still produces the missing-feature state.

Because this changes `desktop/src/renderer/`, the affected packaged Playwright
journey must also be run after grepping journey assertions for any changed role,
label, or visible text. No user-visible copy is intentionally changed.

Before handoff, run the repository's **Fast suite**, the affected desktop unit
tests, `go vet ./...`, `go build ./...`, and the targeted **Desktop packaged
journeys** gate described in `docs/testing-baseline.md`.
