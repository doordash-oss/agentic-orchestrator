# Warm Workspace Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make switching among open Agentico workspace tabs instantaneous while keeping server snapshots fresh with coalesced, event-driven background refreshes.

**Architecture:** `WorkspaceShell` will retain one hidden tabpanel per open workspace tab instead of conditionally mounting only the active panel. Each `FeatureCockpit` receives an `active` signal and delegates invalidation timing to a small refresh scheduler: active panels refresh immediately, inactive panels coalesce changes for five seconds, and hidden application windows defer work until visible.

**Tech Stack:** React 19, TypeScript 5.9, Vitest, Testing Library, Electron, Playwright.

## Global Constraints

- The server remains authoritative; do not add a persisted client-side domain cache or change `TabsPrefs`.
- Only an initial load may replace the cockpit with the runtime loading view.
- Inactive panels must use the HTML `hidden` attribute so they leave the focus order and accessibility tree.
- Background invalidations use a trailing delay of exactly `5_000` milliseconds; unchanged tabs do not poll.
- Refresh work is single-flight per cockpit, with at most one trailing refresh after an in-flight request.
- Closing a feature tab must unmount its cockpit and release timers and application-event subscriptions.
- Manual close is the only eviction mechanism in this change; do not add idle or LRU eviction.
- Preserve all unrelated release/signing changes already present in the worktree.
- Stage files by exact path, and end every implementation commit with `Co-authored-by: Codex <noreply@openai.com>`.

## File Structure

- Create `desktop/src/renderer/src/features/featureRefreshScheduler.ts`: framework-independent dirty/coalescing/single-flight refresh lifecycle.
- Create `desktop/src/renderer/src/features/featureRefreshScheduler.test.ts`: deterministic fake-timer coverage for the scheduler.
- Modify `desktop/src/renderer/src/features/FeatureCockpit.tsx`: accept panel activity, use the scheduler, and retain loaded content across silent refresh failures.
- Modify `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`: cover active/inactive scheduling and stale-while-revalidate error behavior.
- Modify `desktop/src/renderer/src/features/WorkspaceShell.tsx`: retain stable Home, Settings, and feature tabpanels and make concurrent name updates safe.
- Modify `desktop/src/renderer/src/features/WorkspaceShell.test.tsx`: prove hidden-panel accessibility, local state retention, close cleanup, and no remount loading.
- Modify `desktop/test/e2e/journeys/start-watch-stop.spec.ts`: prove the packaged app retains the same cockpit DOM instance across a Home round trip.

---

### Task 1: Deterministic refresh scheduler

**Files:**

- Create: `desktop/src/renderer/src/features/featureRefreshScheduler.test.ts`
- Create: `desktop/src/renderer/src/features/featureRefreshScheduler.ts`

**Interfaces:**

- Consumes: a `refresh: () => Promise<void>` callback and initial `{ active, visible, backgroundDelayMs? }` state.
- Produces: `BACKGROUND_REFRESH_DELAY_MS` and `createFeatureRefreshScheduler(...)`, returning `{ invalidate, setActive, setVisible, dispose }`.

- [ ] **Step 1: Write failing scheduler tests**

Create `featureRefreshScheduler.test.ts` with focused fake-timer cases:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  BACKGROUND_REFRESH_DELAY_MS,
  createFeatureRefreshScheduler,
} from './featureRefreshScheduler';

afterEach(() => vi.useRealTimers());

describe('feature refresh scheduler', () => {
  it('coalesces inactive invalidations into one five-second refresh', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.invalidate();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS - 1);
    expect(refresh).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('flushes a dirty inactive tab immediately when activated', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.setActive(true);
    await vi.runAllTicks();
    expect(refresh).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('defers hidden-window work until visibility returns', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: false,
    });
    scheduler.invalidate();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).not.toHaveBeenCalled();
    scheduler.setVisible(true);
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).toHaveBeenCalledTimes(1);
  });

  it('runs one trailing refresh when invalidated during an active request', async () => {
    let finishFirst!: () => void;
    const refresh = vi
      .fn<() => Promise<void>>()
      .mockImplementationOnce(() => new Promise<void>((resolve) => (finishFirst = resolve)))
      .mockResolvedValue(undefined);
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: true,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.invalidate();
    expect(refresh).toHaveBeenCalledTimes(1);
    finishFirst();
    await vi.waitFor(() => expect(refresh).toHaveBeenCalledTimes(2));
  });

  it('cancels pending refreshes when disposed', async () => {
    vi.useFakeTimers();
    const refresh = vi.fn(() => Promise.resolve());
    const scheduler = createFeatureRefreshScheduler(refresh, {
      active: false,
      visible: true,
    });
    scheduler.invalidate();
    scheduler.dispose();
    await vi.advanceTimersByTimeAsync(BACKGROUND_REFRESH_DELAY_MS);
    expect(refresh).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run the scheduler test and verify RED**

Run `npm test --workspace desktop -- src/renderer/src/features/featureRefreshScheduler.test.ts`.

Expected: FAIL because `featureRefreshScheduler.ts` and its exports do not exist.

- [ ] **Step 3: Implement the minimal scheduler**

Create `featureRefreshScheduler.ts` with this public shape and state machine:

```ts
export const BACKGROUND_REFRESH_DELAY_MS = 5_000;

export interface FeatureRefreshScheduler {
  invalidate(): void;
  setActive(active: boolean): void;
  setVisible(visible: boolean): void;
  dispose(): void;
}

export function createFeatureRefreshScheduler(
  refresh: () => Promise<void>,
  options: { active: boolean; visible: boolean; backgroundDelayMs?: number },
): FeatureRefreshScheduler {
  let active = options.active;
  let visible = options.visible;
  let dirty = false;
  let inFlight = false;
  let disposed = false;
  let timer: ReturnType<typeof setTimeout> | null = null;
  const delay = options.backgroundDelayMs ?? BACKGROUND_REFRESH_DELAY_MS;
  const cancelTimer = () => {
    if (timer !== null) clearTimeout(timer);
    timer = null;
  };
  const schedule = () => {
    if (disposed || active || !visible || !dirty || inFlight || timer !== null) return;
    timer = setTimeout(() => {
      timer = null;
      void flush();
    }, delay);
  };
  const flush = async () => {
    if (disposed || !visible || !dirty || inFlight) return;
    cancelTimer();
    dirty = false;
    inFlight = true;
    try {
      await refresh();
    } finally {
      inFlight = false;
      if (!disposed && dirty && visible) {
        if (active) void flush();
        else schedule();
      }
    }
  };
  return {
    invalidate() {
      if (disposed) return;
      dirty = true;
      if (active && visible) void flush();
      else schedule();
    },
    setActive(next) {
      active = next;
      if (active) {
        cancelTimer();
        if (visible) void flush();
      } else schedule();
    },
    setVisible(next) {
      visible = next;
      if (!visible) cancelTimer();
      else if (active) void flush();
      else schedule();
    },
    dispose() {
      disposed = true;
      cancelTimer();
    },
  };
}
```

- [ ] **Step 4: Run scheduler tests and verify GREEN**

Run the command from Step 2. Expected: all scheduler tests PASS with fake timers restored after every test.

- [ ] **Step 5: Commit the scheduler**

```bash
git add desktop/src/renderer/src/features/featureRefreshScheduler.ts desktop/src/renderer/src/features/featureRefreshScheduler.test.ts
git commit -m "Keep background refresh work bounded" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 2: Cockpit stale-while-revalidate lifecycle

**Files:**

- Modify: `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.tsx:86-105,730-930`

**Interfaces:**

- Consumes: `FeatureRefreshScheduler` from Task 1 and an optional `active?: boolean` prop that defaults to `true` for standalone cockpit consumers.
- Produces: a cockpit that loads once per mount, schedules relevant invalidations by activity, and retains a loaded snapshot after non-`not_found` silent failures.

- [ ] **Step 1: Add failing cockpit lifecycle tests**

Update `renderCockpit` to accept `active = true`, render `active={active}`, and return a `setActive(next)` wrapper around Testing Library's `rerender`. Add these tests:

```ts
it('delays and coalesces invalidations while inactive, then flushes on activation', async () => {
  vi.useFakeTimers();
  const mock = installAgenticoMock();
  const view = renderCockpit(mock, false);
  await screen.findByRole('heading', { name: 'Search revamp' });
  const base = mock.api.getFeature.mock.calls.length;
  mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
  mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
  expect(mock.api.getFeature).toHaveBeenCalledTimes(base);
  await vi.advanceTimersByTimeAsync(4_999);
  expect(mock.api.getFeature).toHaveBeenCalledTimes(base);
  view.setActive(true);
  await vi.waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(base + 1));
});

it('keeps the loaded snapshot visible when a silent refresh fails', async () => {
  const mock = installAgenticoMock();
  renderCockpit(mock, true);
  expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeVisible();
  mock.api.getFeature.mockRejectedValueOnce(new Error('unavailable: runtime busy'));
  mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
  expect(await screen.findByText('Refreshing from the runtime…')).toBeVisible();
  expect(screen.getByRole('heading', { name: 'Search revamp' })).toBeVisible();
  expect(screen.queryByText(/Loading Search revamp from the runtime/)).not.toBeInTheDocument();
});

it('shows the missing state when a silent refresh reports not_found', async () => {
  const mock = installAgenticoMock();
  renderCockpit(mock, true);
  await screen.findByRole('heading', { name: 'Search revamp' });
  mock.api.getFeature.mockRejectedValueOnce(new Error('not_found: feature not found'));
  mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
  expect(await screen.findByText('This feature no longer exists on the server.')).toBeVisible();
});
```

Change `afterEach(cleanup)` to restore real timers as well.

- [ ] **Step 2: Run cockpit tests and verify RED**

Run `npm test --workspace desktop -- src/renderer/src/features/FeatureCockpit.test.tsx`.

Expected: FAIL because `FeatureCockpitProps` does not accept `active`, inactive invalidations refresh immediately, and silent failures replace loaded content.

- [ ] **Step 3: Integrate the scheduler and preserve loaded content**

1. Add `active?: boolean` to `FeatureCockpitProps`, default it to `true` in the component parameter list, and import `createFeatureRefreshScheduler`. This keeps existing standalone cockpit consumers valid until `WorkspaceShell` supplies explicit activity in Task 3.
2. Split `stale` into `streamStale` and `refreshFailed`; derive `const stale = streamStale || refreshFailed` for existing inspector rendering.
3. On successful `load`, clear `refreshFailed`. On a non-`not_found` failure with `options.silent === true`, set `refreshFailed(true)` and retain the current `CockpitState`; only initial failures enter `phase: 'error'`. Keep `not_found` authoritative for initial and silent loads.
4. Store `selectedRunNumber` in a ref so changing archive selection does not recreate the subscription or repeat the initial load.
5. In the mount effect, call `load()` once, create the scheduler with `() => load({ silent: true })`, subscribe to relevant app events, and call `scheduler.invalidate()` for relevant invalidations.
6. Add a visibility listener that calls `scheduler.setVisible(document.visibilityState === 'visible')`.
7. Add a separate effect that calls `scheduler.setActive(active)` without recreating the scheduler.
8. On cleanup, dispose the scheduler, remove the visibility listener, invalidate outstanding load request IDs, and unsubscribe.

Keep event relevance, deletion handling, current-run badges, and stream-status behavior unchanged.

- [ ] **Step 4: Run cockpit and scheduler tests and verify GREEN**

Run `npm test --workspace desktop -- src/renderer/src/features/featureRefreshScheduler.test.ts src/renderer/src/features/FeatureCockpit.test.tsx`.

Expected: all tests PASS with no React `act(...)` warnings or unhandled rejections.

- [ ] **Step 5: Commit cockpit lifecycle behavior**

```bash
git add desktop/src/renderer/src/features/FeatureCockpit.tsx desktop/src/renderer/src/features/FeatureCockpit.test.tsx
git commit -m "Keep loaded feature views usable during refresh" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 3: Stable workspace tabpanels

**Files:**

- Modify: `desktop/src/renderer/src/features/WorkspaceShell.test.tsx`
- Modify: `desktop/src/renderer/src/features/WorkspaceShell.tsx:176-220,360-610`

**Interfaces:**

- Consumes: `FeatureCockpit.active` from Task 2.
- Produces: one keyed, retained panel for Home, Settings, and every `tabs.open` entry; inactive panels are `hidden` and a closed panel unmounts.

- [ ] **Step 1: Add failing workspace retention tests**

Extend the existing review-feature test with this round trip:

```ts
const featurePanel = document.getElementById(`panel-${FEATURE_ID}`);
expect(featurePanel).not.toBeNull();
expect(featurePanel).not.toHaveAttribute('hidden');
await user.click(liveTab);
await user.click(screen.getByRole('tab', { name: 'Home' }));
expect(featurePanel).toHaveAttribute('hidden');
expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
const callsAfterHomeRefresh = mock.api.getFeature.mock.calls.length;
await user.click(screen.getByRole('tab', { name: 'Search revamp' }));
expect(featurePanel).not.toHaveAttribute('hidden');
expect(await screen.findByRole('tab', { name: /Live activity/ })).toHaveAttribute(
  'aria-selected',
  'true',
);
expect(mock.api.getFeature).toHaveBeenCalledTimes(callsAfterHomeRefresh);
expect(screen.queryByText(/Loading Search revamp from the runtime/)).not.toBeInTheDocument();
```

Add a close-cleanup test:

```ts
it('unmounts and unsubscribes a cockpit when its tab closes', async () => {
  const mock = installAgenticoMock({ settings: settingsWithTab(), features: [] });
  render(<WorkspaceShell />);
  await screen.findByRole('heading', { name: 'Search revamp' });
  const listenersBeforeClose = mock.appEventListenerCount();
  await userEvent.click(screen.getByRole('button', { name: 'Close Search revamp tab' }));
  await waitFor(() => expect(document.getElementById(`panel-${FEATURE_ID}`)).toBeNull());
  expect(mock.appEventListenerCount()).toBe(listenersBeforeClose - 1);
  const callsAfterClose = mock.api.getFeature.mock.calls.length;
  mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
  expect(mock.api.getFeature).toHaveBeenCalledTimes(callsAfterClose);
});
```

Add `SECOND_FEATURE_ID`, import `act`, and add a reverse-resolution hydration test:

```ts
it('persists concurrent authoritative tab names without losing either update', async () => {
  const settings: Settings = {
    ...defaultSettings(),
    tabs: {
      open: [
        { featureId: FEATURE_ID, titleHint: 'First draft' },
        { featureId: SECOND_FEATURE_ID, titleHint: 'Second draft' },
      ],
      activeFeatureId: FEATURE_ID,
    },
  };
  const mock = installAgenticoMock({ settings, features: [] });
  let resolveFirst!: (value: ReturnType<typeof featureSnapshot>) => void;
  let resolveSecond!: (value: ReturnType<typeof featureSnapshot>) => void;
  mock.api.getFeature.mockImplementation(
    (id: string) =>
      new Promise((resolve) => {
        if (id === FEATURE_ID) resolveFirst = resolve;
        else resolveSecond = resolve;
      }),
  );
  render(<WorkspaceShell />);
  await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(2));
  await act(async () =>
    resolveSecond(featureSnapshot({ id: SECOND_FEATURE_ID, name: 'Authoritative second' })),
  );
  await act(async () =>
    resolveFirst(featureSnapshot({ id: FEATURE_ID, name: 'Authoritative first' })),
  );
  await waitFor(() =>
    expect(mock.api.updateSettings).toHaveBeenCalledWith({
      tabs: {
        open: [
          { featureId: FEATURE_ID, titleHint: 'Authoritative first' },
          { featureId: SECOND_FEATURE_ID, titleHint: 'Authoritative second' },
        ],
        activeFeatureId: FEATURE_ID,
      },
    }),
  );
});
```

- [ ] **Step 2: Run workspace tests and verify RED**

Run `npm test --workspace desktop -- src/renderer/src/features/WorkspaceShell.test.tsx`.

Expected: FAIL because navigating Home disconnects `featurePanel`, loses the selected Live stage, and multiple retained cockpits are not supported.

- [ ] **Step 3: Render stable panels and make rename persistence concurrency-safe**

Replace the conditional panel branch with stable Home and Settings panels plus `tabs.open.map(...)`. Every inactive panel gets `hidden`; every `FeatureCockpit` gets `active={active === tab.featureId}` and callbacks that capture `tab.featureId` rather than the outer `active` value:

```tsx
{tabs.open.map((tab) => {
  const isActive = active === tab.featureId;
  return (
    <div
      key={tab.featureId}
      id={`panel-${tab.featureId}`}
      role="tabpanel"
      aria-labelledby={`tab-${tab.featureId}`}
      className="tab-panel tab-panel--cockpit"
      hidden={!isActive}
    >
      <FeatureCockpit
        active={isActive}
        featureId={tab.featureId}
        titleHint={tab.titleHint || tab.featureId}
        onClose={() => closeFeature(tab.featureId)}
        onDeleted={handleFeatureDeleted}
        onLoadedName={(name) => renameTab(tab.featureId, name)}
        attentionItems={attentionItems.filter(
          (item) =>
            item.kind !== 'recovery' && attentionOwnerFeatureId(item) === tab.featureId,
        )}
        refreshAttention={refreshAttention}
        attentionDrafts={activeAttentionDrafts}
        setAttentionDrafts={updateAttentionDrafts}
        attentionPreviewRequest={
          attentionPreviewRequest?.featureId === tab.featureId
            ? attentionPreviewRequest
            : null
        }
        onAttentionPreviewClose={closeAttentionPreview}
        selectedRunNumber={tab.selectedRunNumber ?? null}
        onSelectRun={(runNumber) => {
          persist({
            ...tabs,
            open: tabs.open.map((entry) =>
              entry.featureId === tab.featureId
                ? { ...entry, selectedRunNumber: runNumber }
                : entry,
            ),
          });
        }}
      />
    </div>
  );
})}
```

Rewrite `renameTab` as a functional `setTabs(current => ...)` update. Build `next` from `current`, persist that exact value, and return unchanged state when the tab is absent or the title is already current.

- [ ] **Step 4: Run workspace, cockpit, and scheduler tests and verify GREEN**

Run `npm test --workspace desktop -- src/renderer/src/features/featureRefreshScheduler.test.ts src/renderer/src/features/FeatureCockpit.test.tsx src/renderer/src/features/WorkspaceShell.test.tsx`.

Expected: all tests PASS; hidden panels are absent from default role queries, local stage state survives, and closing a tab removes its listener.

- [ ] **Step 5: Commit stable workspace panels**

```bash
git add desktop/src/renderer/src/features/WorkspaceShell.tsx desktop/src/renderer/src/features/WorkspaceShell.test.tsx
git commit -m "Make workspace tab switches instantaneous" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 4: Packaged regression and full verification

**Files:**

- Modify: `desktop/test/e2e/journeys/start-watch-stop.spec.ts:45-60`

**Interfaces:**

- Consumes: stable feature panel DOM identity from Task 3.
- Produces: a packaged journey assertion that fails if returning Home unmounts an open cockpit.

- [ ] **Step 1: Add a failing packaged journey assertion**

Before the first Home round trip, capture the cockpit element and prove the same node remains connected:

```ts
const retainedCockpit = await cockpit.elementHandle();
expect(retainedCockpit).not.toBeNull();
await handle.page.getByRole('tab', { name: 'Home' }).click();
expect(await retainedCockpit!.evaluate((node) => node.isConnected)).toBe(true);
await expect(cockpit).toBeHidden();
const featureList = handle.page.getByRole('region', { name: 'Existing features' });
await expect(featureList).toContainText('Packaged Signal Journey');
await featureList.scrollIntoViewIfNeeded();
await evidenceShot(handle, 'cockpit-intervention-dashboard-light-wide');
await handle.page.getByRole('tab', { name: 'Packaged Signal Journey' }).click();
expect(await retainedCockpit!.evaluate((node) => node.isConnected)).toBe(true);
await expect(cockpit).toBeVisible();
await expect(cockpit.getByText(/Loading .* from the runtime/)).toHaveCount(0);
```

- [ ] **Step 2: Grep packaged journeys for affected contracts**

Run `rg -n 'Loading .* from the runtime|Workspace tabs|tabpanel|Packaged Signal Journey' desktop/test/e2e/journeys`.

Expected: no copy, role, or accessible-name updates are required.

- [ ] **Step 3: Run desktop static and unit gates**

```bash
npm run check
npm test
npm run test:security
```

Expected: **Desktop static checks** and **Desktop unit/component/security tests** PASS.

- [ ] **Step 4: Build and run the targeted packaged journey**

From `desktop/`:

```bash
npm run package:verify
npm run test:e2e:packaged -- test/e2e/journeys/start-watch-stop.spec.ts -g "packaged real-server start, semantic watch, history, and authoritative stop"
```

Expected: **Desktop packaged E2E** PASS, including retained DOM identity.

- [ ] **Step 5: Run mandatory repository gates**

From the repository root:

```bash
make test-fast
go vet ./...
go build ./...
```

Expected: **Fast suite**, **Go static-analysis gate**, and **Go build gate** PASS. Race regression is intentionally skipped because this change affects only the single-threaded renderer lifecycle and adds no Go concurrency.

- [ ] **Step 6: Review the complete diff and commit the packaged regression**

```bash
git diff --check
git status --short
git diff -- desktop/src/renderer/src/features desktop/test/e2e/journeys/start-watch-stop.spec.ts
git add desktop/test/e2e/journeys/start-watch-stop.spec.ts
git commit -m "Guard warm tabs in the packaged desktop journey" -m "Co-authored-by: Codex <noreply@openai.com>"
```

Confirm that only planned warm-tab files are included and all unrelated release/signing changes remain uncommitted and untouched.
