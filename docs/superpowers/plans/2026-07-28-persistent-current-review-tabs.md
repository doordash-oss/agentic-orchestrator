# Persistent Current Review Tabs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the current review batch in the live activity rail across renderer refreshes and remove duplicate lower review/verification summaries from mutable runs.

**Architecture:** Make cohort hydration iteration-aware using the durable `SessionSummary.iteration` supplied by the run-session API, and identify the current review batch by the durable validator-axis names in `ReviewGateView.validatorStatuses`. Keep the current live rail and transcript components as the only mutable-run review surface, while retaining the existing summary components for sealed records.

**Tech Stack:** React 19, TypeScript, Vitest, Testing Library, Electron renderer CSS.

## Global Constraints

- Restore only sessions from the current iteration; do not resurrect earlier retry batches.
- If current-iteration metadata is unavailable, retain the existing active-session fallback.
- Keep real session transcripts behind restored tabs; do not create synthetic tabs.
- Remove duplicate summaries from regular and cycle presentations.
- Keep sealed record summaries, roadmap status, phase metrics, artifacts, and logs unchanged.
- Preserve vertical tablist semantics, keyboard navigation, selected state, and accessible status labels.
- Do not modify or stage the user's existing unrelated Go changes.

---

## File Map

- `desktop/src/renderer/src/features/liveCohort.ts`: pure cohort membership and iteration-boundary rules.
- `desktop/src/renderer/src/features/liveCohort.test.ts`: unit coverage for hydration, iteration scoping, and fallback behavior.
- `desktop/src/renderer/src/features/useCohortTranscripts.ts`: passes iteration context into cohort reconciliation.
- `desktop/src/renderer/src/features/CurrentRunInspection.tsx`: supplies `currentIteration` and limits lower summaries to sealed records.
- `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx`: renderer behavior for restored tabs, duplicate removal, verification, and sealed records.
- `desktop/src/renderer/src/styles/app.css`: removes mutable-run review-summary layout rules that become unreachable.

### Task 1: Make Cohort Hydration Iteration-Aware

**Files:**

- Modify: `desktop/src/renderer/src/features/liveCohort.ts:30-80`
- Test: `desktop/src/renderer/src/features/liveCohort.test.ts:20-94`

**Interfaces:**

- Consumes: `SessionSummary.iteration?: number` from `desktop/src/shared/ipc.ts`.
- Consumes: current review-axis names from `ReviewGateView.validatorStatuses`.
- Produces: `computeCohort(previous, runSessions, currentPhase, currentIteration?, currentReviewAxes?)`.
- Produces: `CohortMembership.iteration?: number` as the durable membership boundary.

- [ ] **Step 1: Write failing hydration and boundary tests**

Add tests that prove initial hydration restores the complete current batch and
that an iteration change replaces the prior batch:

```ts
it('hydrates terminal and active sessions from only the current iteration', () => {
  const cohort = computeCohort(
    EMPTY_COHORT,
    [
      session({ id: 'old-craft', iteration: 1, label: 'Craft', status: 'completed' }),
      session({
        id: 'implementer',
        iteration: 2,
        kind: 'repo-impl',
        label: 'Implement',
        status: 'completed',
      }),
      session({ id: 'craft', iteration: 2, label: 'Craft', status: 'completed' }),
      session({
        id: 'functionality',
        iteration: 2,
        label: 'Functionality/Evidence',
        status: 'failed',
      }),
      session({ id: 'cleanliness', iteration: 2, label: 'Cleanliness', status: 'running' }),
    ],
    'implement',
    2,
    ['Craft', 'Functionality/Evidence', 'Cleanliness'],
  );

  expect(cohort.sessionIds).toEqual(['craft', 'functionality', 'cleanliness']);
  expect(cohort.iteration).toBe(2);
});

it('replaces a terminal cohort when the current iteration changes', () => {
  const cohort = computeCohort(
    { sessionIds: ['old-craft'], phase: 'implement', iteration: 1 },
    [
      session({ id: 'old-craft', iteration: 1, status: 'completed' }),
      session({ id: 'new-cleanliness', iteration: 2, status: 'running' }),
    ],
    'implement',
    2,
    ['Cleanliness'],
  );

  expect(cohort.sessionIds).toEqual(['new-cleanliness']);
  expect(cohort.iteration).toBe(2);
});
```

Add a fallback test showing that an undefined current iteration preserves the
existing active-only initialization when active and terminal sessions coexist:

```ts
it('falls back to active sessions when current iteration metadata is unavailable', () => {
  const cohort = computeCohort(
    EMPTY_COHORT,
    [
      session({ id: 'terminal', status: 'completed' }),
      session({ id: 'active', status: 'running' }),
    ],
    'implement',
  );

  expect(cohort.sessionIds).toEqual(['active']);
  expect(cohort.iteration).toBeUndefined();
});
```

- [ ] **Step 2: Run the focused unit test and verify failure**

Run:

```bash
npm test --workspace desktop -- src/renderer/src/features/liveCohort.test.ts
```

Expected: FAIL because `computeCohort` does not accept `currentIteration` and
`CohortMembership` does not expose `iteration`.

- [ ] **Step 3: Add iteration-aware cohort membership**

Extend membership and the compute signature:

```ts
export interface CohortMembership {
  sessionIds: string[];
  phase: string;
  iteration?: number;
}

export function computeCohort(
  previous: CohortMembership,
  runSessions: readonly SessionSummary[],
  currentPhase: string,
  currentIteration?: number,
  currentReviewAxes?: readonly string[],
): CohortMembership {
```

Within `computeCohort`, keep non-chat filtering, then derive the durable scope.
Only real validator sessions whose iteration and label match the current gate
belong to the refresh-restored review batch; this deliberately excludes the
terminal implementer from the same iteration:

```ts
const currentReviewAxisSet = new Set(currentReviewAxes ?? []);
const currentReviewIds =
  currentIteration === undefined || currentReviewAxisSet.size === 0
    ? []
    : candidates
        .filter(
          (session) =>
            session.kind === 'validator' &&
            session.iteration === currentIteration &&
            session.label !== undefined &&
            currentReviewAxisSet.has(session.label),
        )
        .map((session) => session.id);
const boundaryChanged =
  currentPhase.trim() !== previous.phase.trim() || currentIteration !== previous.iteration;
```

When `currentReviewIds` is non-empty, use it as the cohort regardless of prior
in-memory membership. This is what replaces a terminal implementer cohort with
the complete durable review batch when the gate begins or the renderer
refreshes. When there is no durable review batch, use the existing
active/all fallback for a changed boundary or empty membership; for a stable
non-empty membership, preserve the existing retention and disjoint retry
logic. Return the optional iteration without assigning `undefined`:

```ts
return {
  sessionIds: ordered,
  phase: currentPhase,
  ...(currentIteration === undefined ? {} : { iteration: currentIteration }),
};
```

Update the function comment to state that current-iteration terminal peers are
restored during hydration.

- [ ] **Step 4: Run the focused unit test and verify success**

Run:

```bash
npm test --workspace desktop -- src/renderer/src/features/liveCohort.test.ts
```

Expected: PASS for all `liveCohort` tests.

- [ ] **Step 5: Commit the cohort model**

```bash
git add desktop/src/renderer/src/features/liveCohort.ts \
  desktop/src/renderer/src/features/liveCohort.test.ts
git commit -m "fix(desktop): restore current review cohort"
```

### Task 2: Wire Current Iteration Through Live Inspection

**Files:**

- Modify: `desktop/src/renderer/src/features/useCohortTranscripts.ts:26-80`
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx:216`
- Test: `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx`

**Interfaces:**

- Consumes: `CurrentRunInspectionProps.currentIteration?: number`.
- Consumes: `computeCohort(previous, runSessions, currentPhase, currentIteration?)` from Task 1.
- Produces: `useCohortTranscripts(featureId, runNumber, currentPhase, shouldStream, currentIteration?, currentReviewAxes?)`.

- [ ] **Step 1: Write a failing renderer hydration test**

Add a `CurrentRunInspection` test whose mocked run sessions contain one old
reviewer and three current reviewers:

```ts
it('restores only the current review batch after initial hydration', async () => {
  const user = userEvent.setup();
  const mock = installAgenticoMock();
  mock.api.getLivePreview.mockResolvedValue({
    featureId: 'abcd1234ef567890',
    activity: 'Reviewing implementation',
    contextPercentage: 37,
    totalSeconds: 100,
    totalUsd: 1.5,
    transcript: [],
  });
  mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
  mock.api.listRunSessions.mockResolvedValue({
    runNumber: 8,
    sessions: [
      validator({ id: 'old-craft', label: 'Craft', iteration: 1, status: 'completed' }),
      validator({ id: 'craft', label: 'Craft', iteration: 2, status: 'completed' }),
      validator({
        id: 'functionality',
        label: 'Functionality/Evidence',
        iteration: 2,
        status: 'failed',
      }),
      validator({
        id: 'cleanliness',
        label: 'Cleanliness',
        iteration: 2,
        status: 'running',
      }),
    ],
  });
  mock.api.getSessionTranscript.mockImplementation(({ sessionId }: { sessionId: string }) =>
    Promise.resolve({
      sessionId,
      cursor: { total: 1, start: 0, end: 1 },
      messages: [
        {
          index: 0,
          role: 'assistant',
          type: 'text',
          text: `${sessionId} transcript`,
        },
      ],
    }),
  );

  render(
    <CurrentRunInspection
      featureId="abcd1234ef567890"
      runNumber={8}
      currentPhase="Implement"
      currentIteration={2}
      reviewGate={{
        reviewingGate: true,
        reviewFixing: false,
        validatingPlan: false,
        validatorStatuses: {
          Craft: 'APPROVED',
          'Functionality/Evidence': 'CHANGES_REQUESTED',
          Cleanliness: 'running',
        },
      }}
    />,
  );

  const tabs = await screen.findAllByRole('tab');
  expect(tabs.map((tab) => tab.getAttribute('aria-label'))).toEqual([
    'Craft — completed',
    'Functionality/Evidence — failed',
    'Cleanliness — running',
  ]);
  expect(screen.queryByText('old-craft transcript')).not.toBeInTheDocument();

  await user.click(screen.getByRole('tab', { name: 'Craft — completed' }));
  expect(await screen.findByText('craft transcript')).toBeVisible();
});
```

- [ ] **Step 2: Run the renderer test and verify failure**

Run:

```bash
npm test --workspace desktop -- src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: FAIL because only the active Cleanliness session hydrates.

- [ ] **Step 3: Thread `currentIteration` through the hook**

Add the optional hook argument:

```ts
export function useCohortTranscripts(
  featureId: string,
  runNumber: number,
  currentPhase: string,
  shouldStream: boolean,
  currentIteration?: number,
  currentReviewAxes?: readonly string[],
): CohortTranscripts {
```

Pass it to the pure model and add it to the reconciliation effect dependency
list:

```ts
const next = computeCohort(
  previous,
  runSessions,
  currentPhase,
  currentIteration,
  currentReviewAxes,
);
```

```ts
}, [runSessions, currentPhase, currentIteration, currentReviewAxes]);
```

Supply the value from `CurrentRunInspection`:

```ts
const currentReviewAxes = useMemo(
  () =>
    reviewGate.reviewingGate
      ? orderedReviewStatuses(reviewGate.validatorStatuses).map(([name]) => name)
      : undefined,
  [reviewGate.reviewingGate, reviewGate.validatorStatuses],
);
const live = useCohortTranscripts(
  featureId,
  runNumber,
  currentPhase,
  shouldStream,
  currentIteration,
  currentReviewAxes,
);
```

When comparing old and new membership, include
`next.iteration === previous.iteration` so an iteration-only boundary change
cannot be discarded as a no-op. Memoize the ordered review-axis array before
passing it to the hook so the effect dependency remains stable across renders.

- [ ] **Step 4: Run focused model and renderer tests**

Run:

```bash
npm test --workspace desktop -- \
  src/renderer/src/features/liveCohort.test.ts \
  src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: PASS, including tab ordering, transcript selection, arrow-key
navigation, and the new hydration test.

- [ ] **Step 5: Commit the renderer wiring**

```bash
git add desktop/src/renderer/src/features/useCohortTranscripts.ts \
  desktop/src/renderer/src/features/CurrentRunInspection.tsx \
  desktop/src/renderer/src/features/CurrentRunInspection.test.tsx
git commit -m "fix(desktop): persist current reviewer tabs"
```

### Task 3: Remove Mutable-Run Duplicate Summaries

**Files:**

- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx:515-526`
- Modify: `desktop/src/renderer/src/styles/app.css:1357-1371,1674-1684,9969-10021`
- Test: `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx`

**Interfaces:**

- Consumes: `presentation: 'regular' | 'cycle' | 'record'`.
- Preserves: `ReviewGateSummary` and `VerificationSummary` for `presentation === 'record'`.
- Produces: no lower summary for regular or cycle current-run inspection.

- [ ] **Step 1: Change review and verification expectations to fail**

In the existing review-gate test, configure `listRunSessions` with current
iteration Craft, Functionality/Evidence, Cleanliness, and Design validator
sessions corresponding to the gate fixture. Replace the summary assertions
with absence assertions while retaining a reviewer-tab assertion:

```ts
expect(await screen.findByRole('tablist', { name: 'Live agents' })).toBeVisible();
expect(screen.queryByLabelText('Review gate')).not.toBeInTheDocument();
expect(screen.queryByLabelText('Review axes')).not.toBeInTheDocument();
```

In the existing harness-verification test, assert that commands remain in the
live frame but the duplicate summary is absent:

```ts
const progress = await screen.findByLabelText('Verification progress');
expect(progress).toHaveTextContent('go test ./...');
expect(progress).toHaveTextContent('npm run build');
expect(screen.queryByLabelText('Verification commands')).not.toBeInTheDocument();
expect(
  screen.queryByRole('heading', { name: 'Verifying implementation · 2/4' }),
).not.toBeInTheDocument();
```

Add a sealed-record regression test:

```ts
it('keeps the review summary in sealed record presentation', async () => {
  const mock = installAgenticoMock();
  mock.api.getLivePreview.mockResolvedValue({
    featureId: 'abcd1234ef567890',
    activity: 'Run complete',
    contextPercentage: 42,
    totalSeconds: 73,
    totalUsd: 0.12,
    transcript: [],
  });
  mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
  mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

  render(
    <CurrentRunInspection
      featureId="abcd1234ef567890"
      runNumber={8}
      currentPhase="Implement"
      reviewGate={{
        reviewingGate: true,
        reviewFixing: false,
        validatingPlan: false,
        validatorStatuses: { Craft: 'APPROVED' },
      }}
      presentation="record"
      shouldStream={false}
    />,
  );

  expect(await screen.findByLabelText('Review gate')).toBeVisible();
  expect(screen.getByLabelText('Review axes')).toHaveTextContent('Craft✓');
});
```

- [ ] **Step 2: Run the renderer test and verify failure**

Run:

```bash
npm test --workspace desktop -- src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: FAIL because regular review and verification summaries are still
rendered.

- [ ] **Step 3: Restrict summary rendering to sealed records**

Replace the unconditional summary branch with:

```tsx
{initialLoading && presentation === 'record' ? null : (
  <>
    {presentation === 'record'
      ? verifying && verificationItems !== undefined
        ? <VerificationSummary items={verificationItems} />
        : (
            <ReviewGateSummary
              gate={reviewGate}
              currentPhase={currentPhase}
              currentRoadmapPhase={currentRoadmapPhase}
              cyclePhase={cycle?.phase}
            />
          )
      : null}

    <div className="current-inspection__archive">
      {/* existing resource sections remain unchanged */}
    </div>
  </>
)}
```

Do not remove `ReviewGateSummary`, `VerificationSummary`, their model imports,
or base `.review-gate` styles because sealed records still use them.

- [ ] **Step 4: Remove unreachable mutable-summary CSS**

Delete:

- `.cycle-workspace .review-gate`, `.review-gate__axes`, and
  `.review-gate__counts` rules;
- their responsive overrides under the cycle workspace media query;
- the `:has(.review-gate)` compact-height rules under
  `@media (max-height: 700px)`;
- `.cockpit__surface--live .review-gate` compact rules.

Retain base `.review-gate*` styles and
`.current-inspection[data-presentation='record'] > .review-gate`.

- [ ] **Step 5: Run focused tests and static renderer checks**

Run:

```bash
npm test --workspace desktop -- \
  src/renderer/src/features/liveCohort.test.ts \
  src/renderer/src/features/CurrentRunInspection.test.tsx
npm run check
npm run build
```

Expected: all tests, TypeScript, ESLint, Prettier, API drift, and Electron build
checks PASS.

- [ ] **Step 6: Commit duplicate-summary removal**

```bash
git add desktop/src/renderer/src/features/CurrentRunInspection.tsx \
  desktop/src/renderer/src/features/CurrentRunInspection.test.tsx \
  desktop/src/renderer/src/styles/app.css
git commit -m "refactor(desktop): consolidate live review status"
```

### Task 4: Repository Verification and Handoff

**Files:**

- Verify only; no planned source changes.

**Interfaces:**

- Consumes: completed Tasks 1-3.
- Produces: verification evidence for handoff.

- [ ] **Step 1: Run the required Fast suite**

Run:

```bash
make test-fast
```

Expected: PASS within the repository's fast-suite target.

- [ ] **Step 2: Run required Go static analysis**

Run:

```bash
go vet ./...
```

Expected: PASS.

- [ ] **Step 3: Run required Go build**

Run:

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Confirm the working tree contains no unintended files**

Run:

```bash
git status --short
git diff --check
```

Expected: only the user's pre-existing unrelated Go edits remain uncommitted;
no whitespace errors appear in this implementation.

- [ ] **Step 5: Prepare the handoff verification note**

Record:

```text
Verification:
- Fast suite: make test-fast
- Desktop renderer: focused Vitest tests, npm run check, npm run build
- Static analysis: go vet ./...
- Build: go build ./...
- Skipped E2E smoke shell: renderer-only layout/state change; no launch behavior, embedded skills, or release packaging changed.
- Skipped Isolated integration: no lifecycle, state-machine, runs layout, or protocol behavior changed.
- Skipped E2E Go: no server process launch or session lifecycle changed.
- Skipped Race regression: renderer-only state presentation change with focused hook/model coverage.
```
