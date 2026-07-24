# Aftercare Desk Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the default live monitor for CodeReady, Published, and Done features with a status-aware Aftercare desk that exposes useful maintenance cycles, run telemetry, and repository readiness while retaining the transcript as Run record.

**Architecture:** Add a focused `AftercareDesk` renderer component that loads only read-only current-run evidence and derives all cycle/repository presentation from the authoritative `FeatureSnapshot`. Keep surface selection, modal ownership, and refresh behavior in `FeatureCockpit`; keep every guarded cycle preflight and mutation in `CycleJourneys`.

**Tech Stack:** React 18, TypeScript, Vitest, Testing Library, Electron renderer IPC, Playwright screenshot capture, CSS custom properties.

## Global Constraints

- Apply only to statuses recognized by `isRunAtRest`: `CodeReady`, `Published`, and `Done`.
- Preserve the completed transcript under a secondary `Run record` tab.
- Render only server-advertised cycles and never infer mutation availability.
- Missing optional run evidence renders as `—` and never blocks the surface.
- Reuse the existing guarded `CycleJourneys` preflight and dispatch paths.
- Respect keyboard focus, semantic regions/lists, narrow layouts, and `prefers-reduced-motion`.
- Preserve all unrelated dirty-worktree changes.

---

### Task 1: Aftercare presentation model

**Files:**
- Create: `desktop/src/renderer/src/features/aftercareModel.ts`
- Create: `desktop/src/renderer/src/features/aftercareModel.test.ts`

**Interfaces:**
- Consumes: `FeatureSnapshot`, `FeatureActionView`, and `RepoStatusView` from `desktop/src/shared/ipc.ts`.
- Produces: `aftercareHeadline(status: string)`, `availableAftercareCycles(snapshot: FeatureSnapshot)`, and `aftercareRepositories(snapshot: FeatureSnapshot)`.

- [ ] **Step 1: Write failing model tests**

```ts
it.each([
  ['CodeReady', 'Implementation complete'],
  ['Published', 'Published and ready for what comes next'],
  ['Done', 'Work complete'],
])('maps %s to terminal handoff copy', (status, heading) => {
  expect(aftercareHeadline(status).heading).toBe(heading);
});

it('returns only enabled server-advertised maintenance cycles', () => {
  const snapshot = featureSnapshot({
    actions: [
      { id: 'rebase', enabled: true, disabledReasons: [] },
      { id: 'review-comments', enabled: false, disabledReasons: [] },
      { id: 'refactor', enabled: true, disabledReasons: [] },
    ],
  });
  expect(availableAftercareCycles(snapshot).map((cycle) => cycle.id)).toEqual([
    'rebase',
    'refactor',
  ]);
});
```

- [ ] **Step 2: Run the model test and verify RED**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/aftercareModel.test.ts`

Expected: FAIL because `aftercareModel` does not exist.

- [ ] **Step 3: Implement the typed presentation model**

```ts
export type AftercareCycleId = 'rebase' | 'review-comments' | 'refactor';

export interface AftercareCycle {
  id: AftercareCycleId;
  title: string;
  description: string;
  scope: string;
  verb: string;
}

export function availableAftercareCycles(snapshot: FeatureSnapshot): AftercareCycle[] {
  return CYCLE_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    return action?.enabled === true ? [cycleFrom(snapshot, id)] : [];
  });
}
```

Map repositories by `snapshot.repos` order and merge matching `repoStatus` rows without inventing freshness or PR state.

- [ ] **Step 4: Run the model tests and verify GREEN**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/aftercareModel.test.ts`

Expected: PASS with no warnings.

- [ ] **Step 5: Commit the model slice**

```bash
git add desktop/src/renderer/src/features/aftercareModel.ts desktop/src/renderer/src/features/aftercareModel.test.ts
git commit -m "Expose trustworthy aftercare choices"
```

### Task 2: Aftercare desk component

**Files:**
- Create: `desktop/src/renderer/src/features/AftercareDesk.tsx`
- Create: `desktop/src/renderer/src/features/AftercareDesk.test.tsx`
- Modify: `desktop/src/renderer/src/styles/app.css`

**Interfaces:**
- Consumes: `snapshot: FeatureSnapshot`, `run: RunDetailView | null`, and `onOpenCycle(id: AftercareCycleId): void`.
- Produces: `<AftercareDesk>` labelled region with handoff header, maintenance runway, run ledger, and repository readiness.

- [ ] **Step 1: Write failing component tests**

```tsx
render(
  <AftercareDesk
    snapshot={featureSnapshot({
      status: 'Published',
      activeRun: 8,
      actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      repoStatus: [{ name: 'repo-a', publishable: true, prUrl: 'https://example/pr/1', freshness: 'in sync' }],
    })}
    run={{
      runNumber: 8,
      artifactCount: 5,
      timing: { totalSeconds: 14700, byPhase: {} },
      cost: { totalUsd: 95.18, byPhase: {} },
    }}
    onOpenCycle={onOpenCycle}
  />,
);

expect(screen.getByRole('region', { name: 'Feature aftercare' })).toBeVisible();
expect(screen.getByRole('heading', { name: 'Published and ready for what comes next' })).toBeVisible();
expect(screen.getByText('4h 05m')).toBeVisible();
expect(screen.getByText('$95.18')).toBeVisible();
await user.click(screen.getByRole('button', { name: /Prepare rebase/ }));
expect(onOpenCycle).toHaveBeenCalledWith('rebase');
```

Also cover missing metrics, empty cycles, repository status fallbacks, and all three terminal headlines.

- [ ] **Step 2: Run the component test and verify RED**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/AftercareDesk.test.tsx`

Expected: FAIL because `AftercareDesk` does not exist.

- [ ] **Step 3: Implement the semantic component**

```tsx
export function AftercareDesk({
  snapshot,
  run,
  onOpenCycle,
}: AftercareDeskProps): React.ReactElement {
  const headline = aftercareHeadline(snapshot.status);
  const cycles = availableAftercareCycles(snapshot);
  const repositories = aftercareRepositories(snapshot);

  return (
    <section className="aftercare" aria-label="Feature aftercare">
      <header className="aftercare__handoff">...</header>
      <div className="aftercare__instrument">
        <section className="aftercare__runway" aria-labelledby="aftercare-runway-title">...</section>
        <section className="aftercare__ledger" aria-labelledby="aftercare-ledger-title">...</section>
      </div>
      <section className="aftercare__repositories" aria-labelledby="aftercare-repositories-title">...</section>
    </section>
  );
}
```

Use native buttons for runway tracks, a semantic definition list for the ledger, and a semantic list for repositories.

- [ ] **Step 4: Add the precision-instrument styling**

Build from existing tokens only. Use the runway's `::after` signal line as the signature interaction, keep Patina for terminal health, and add:

```css
@media (prefers-reduced-motion: reduce) {
  .aftercare__cycle::after {
    transition: none;
  }
}
```

At `max-width: 900px`, stack the ledger beneath the runway; at `max-width: 620px`, keep every cycle verb and repository label visible.

- [ ] **Step 5: Run the component tests and verify GREEN**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/AftercareDesk.test.tsx`

Expected: PASS with no accessibility-query failures or warnings.

- [ ] **Step 6: Commit the desk slice**

```bash
git add desktop/src/renderer/src/features/AftercareDesk.tsx desktop/src/renderer/src/features/AftercareDesk.test.tsx desktop/src/renderer/src/styles/app.css
git commit -m "Give completed work a useful landing place"
```

### Task 3: Terminal surface routing

**Files:**
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.tsx`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`
- Modify: `desktop/src/renderer/src/features/CycleJourneys.tsx`
- Create: `desktop/src/renderer/src/features/CycleJourneys.test.tsx`

**Interfaces:**
- Consumes: `AftercareDesk` and `AftercareCycleId`.
- Produces: terminal tab order `Aftercare`, `Run record`, `Changes`; cycle modal seeded with `initialCycle`.

- [ ] **Step 1: Write failing cockpit routing tests**

```tsx
it.each(['CodeReady', 'Published', 'Done'])(
  'defaults %s features to Aftercare while retaining Run record',
  async (status) => {
    renderCockpit(installAgenticoMock({ feature: featureSnapshot({ status }) }));
    expect(await screen.findByRole('tab', { name: 'Aftercare' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: 'Run record' })).toBeVisible();
    expect(screen.queryByRole('tab', { name: 'Live activity' })).not.toBeInTheDocument();
  },
);
```

Add a non-terminal assertion that `Live activity` remains unchanged and `Aftercare` is absent.

- [ ] **Step 2: Run the cockpit tests and verify RED**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/FeatureCockpit.test.tsx`

Expected: FAIL because the terminal Aftercare surface is absent.

- [ ] **Step 3: Add terminal routing and metric loading**

Extend `activeSurface` with `aftercare`. Build stage surfaces from `atRest = isRunAtRest(snapshot.status)`, choose `aftercare` as the terminal fallback, label the live terminal surface `Run record`, and mount:

```tsx
<AftercareDesk
  snapshot={snapshot}
  run={aftercareRun}
  onOpenCycle={(cycle) => {
    setInitialCycle(cycle);
    setCyclesDialog(true);
  }}
/>
```

When Aftercare is the default, load `getRun` once for the active run, store it
as `aftercareRun`, and also update `runMetrics` from its timing/cost totals for
the existing inspector. Degrade failures to null.

- [ ] **Step 4: Write a failing focused-cycle test**

```tsx
render(<CycleJourneys snapshot={snapshot} featureId={FEATURE_ID} initialCycle="refactor" onComplete={vi.fn()} />);
expect(screen.getByRole('region', { name: 'Refactor cycle' })).toHaveAttribute('data-featured', 'true');
```

- [ ] **Step 5: Run the cycle test and verify RED**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/CycleJourneys.test.tsx`

Expected: FAIL because `initialCycle` and the labelled focused journey do not exist.

- [ ] **Step 6: Implement focused existing journeys**

Add `initialCycle?: AftercareCycleId`, label each journey region, set `data-featured` on the matching journey, and focus its heading after the modal mounts without bypassing any existing preflight.

- [ ] **Step 7: Run both test files and verify GREEN**

Run: `npm test --prefix desktop -- --run src/renderer/src/features/FeatureCockpit.test.tsx src/renderer/src/features/CycleJourneys.test.tsx`

Expected: PASS with no warnings.

- [ ] **Step 8: Commit the routing slice**

```bash
git add desktop/src/renderer/src/features/FeatureCockpit.tsx desktop/src/renderer/src/features/FeatureCockpit.test.tsx desktop/src/renderer/src/features/CycleJourneys.tsx desktop/src/renderer/src/features/CycleJourneys.test.tsx
git commit -m "Put aftercare ahead of idle monitoring"
```

### Task 4: Visual evidence and responsive refinement

**Files:**
- Modify: `desktop/test/e2e/screenshot-capture/capture.tsx`
- Modify: `desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts`
- Modify: `desktop/src/renderer/src/styles/app.css`

**Interfaces:**
- Consumes: production `FeatureCockpit` terminal routing and screenshot fixtures.
- Produces: stable Published desktop and narrow Aftercare evidence.

- [ ] **Step 1: Add failing screenshot-scene assertions**

Add terminal fixtures with representative cost, duration, three advertised cycles, one PR, and repository freshness. Assert the Aftercare heading, runway, ledger, and repository list are visible; assert the old waiting transcript is absent from the default scene.

- [ ] **Step 2: Run the focused screenshot layout test and verify RED**

Run: `npm run test:e2e:screenshots --prefix desktop -- --grep "aftercare"`

Expected: FAIL until the terminal capture scene renders the new surface.

- [ ] **Step 3: Wire terminal fixtures and capture desktop/narrow screenshots**

Use the screenshot harness's existing mock API. Do not add production-only fixture branches. Capture at the established desktop and narrow viewport sizes.

- [ ] **Step 4: Inspect both images and refine once**

Check hierarchy, clipping, minimum target sizes, tab legibility, information density, and whether the runway—not decorative cards—is the visual signature. Remove any redundant decoration found during review.

- [ ] **Step 5: Re-run screenshot tests and verify GREEN**

Run: `npm run test:e2e:screenshots --prefix desktop -- --grep "aftercare"`

Expected: PASS and produce readable desktop and narrow evidence.

- [ ] **Step 6: Commit the visual slice**

```bash
git add desktop/test/e2e/screenshot-capture/capture.tsx desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts desktop/src/renderer/src/styles/app.css
git commit -m "Keep feature aftercare legible at every width"
```

### Task 5: Required verification

**Files:**
- Verify only.

**Interfaces:**
- Consumes: all Aftercare implementation slices.
- Produces: fresh evidence for handoff.

- [ ] **Step 1: Run focused renderer tests**

Run:

```bash
npm test --prefix desktop -- --run \
  src/renderer/src/features/aftercareModel.test.ts \
  src/renderer/src/features/AftercareDesk.test.tsx \
  src/renderer/src/features/FeatureCockpit.test.tsx \
  src/renderer/src/features/CycleJourneys.test.tsx
```

Expected: all tests pass.

- [ ] **Step 2: Run Electron typecheck/build**

Run: `npm run build --prefix desktop`

Expected: exit 0.

- [ ] **Step 3: Run static analysis**

Run: `go vet ./...`

Expected: exit 0.

- [ ] **Step 4: Run repository build**

Run: `go build ./...`

Expected: exit 0.

- [ ] **Step 5: Run Fast suite**

Run: `make test-fast`

Expected: exit 0. Record tier name **Fast suite** in the handoff.

- [ ] **Step 6: Run E2E smoke shell**

Run: `bash test/e2e/smoke.sh`

Expected: exit 0. Record tier name **E2E smoke shell** because the change touches desktop lifecycle presentation and embedded renderer behavior.

- [ ] **Step 7: Review the final diff**

Run: `git diff --check && git status --short`

Expected: no whitespace errors; only intended Aftercare files plus preserved pre-existing worktree changes remain.
