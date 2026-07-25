# Post-Implementation Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the cluttered at-rest feature cockpit with a quiet Aftercare launchpad and replace active aftercare tabs with a cycle-specific live-agent workspace, while presenting all feature-linked `need_user_input` gates in one floating free-text modal.

**Architecture:** Add one pure post-implementation presentation model, then build focused Aftercare, cycle, facts-rail, run-record, and gate-modal components around existing action and live-inspection primitives. `FeatureCockpit` remains the authoritative snapshot/action coordinator but delegates workspace resolution and rendering. The server read model exposes an optional stable cycle phase so renderer copy never parses runtime artifacts.

**Tech Stack:** Go 1.x, OpenAPI/oapi-codegen, TypeScript, React, Zod, Vitest/Testing Library, Electron renderer CSS.

## Global Constraints

- Preserve the existing Barlow Condensed / Atkinson Hyperlegible / IBM Plex Mono typography roles and token variables.
- A feature displays exactly one primary workspace: regular execution, Aftercare, focused cycle, failed cycle, or archive.
- `CodeReady`, `Published`, and `Done` use Aftercare when no cycle owns the stage.
- Active or failed cycles take precedence over the feature's at-rest status.
- Successful automatic watchdog recovery produces no renderer notification or phase.
- Review comments and refactor do not gain a fabricated feature-level final review.
- At-rest and cycle facts never render Durable setup.
- The renderer never reads runtime files directly.
- Every new Go source file requires the repository Apache 2.0 header; this plan creates no new Go file.

---

## File Map

- `internal/server/read_model.go` — authoritative cycle-phase projection.
- `internal/server/read_api_contract_test.go` — Go read-model contract coverage.
- `api/openapi.yaml`, `internal/server/serverapi.gen.go` — public optional `cycle.phase`.
- `desktop/src/shared/api/parse.ts`, `desktop/src/shared/api/schema.gen.ts` — server response parsing and generated API type.
- `desktop/src/shared/ipc.ts` — renderer `CycleView.phase`.
- `desktop/src/main/features.ts`, `desktop/src/main/__tests__/featureService.test.ts` — main-process mapping.
- `desktop/src/renderer/src/features/postImplementationModel.ts` — pure workspace/action/phase/receipt models.
- `desktop/src/renderer/src/features/postImplementationModel.test.ts` — model matrix.
- `desktop/src/renderer/src/features/FeatureFactsRail.tsx` — stats-only facts rail.
- `desktop/src/renderer/src/features/AftercareWorkspace.tsx` — quiet at-rest launchpad.
- `desktop/src/renderer/src/features/AftercareWorkspace.test.tsx` — runway/facts/receipt behavior.
- `desktop/src/renderer/src/features/CycleWorkspace.tsx` — active/failed cycle shell.
- `desktop/src/renderer/src/features/CycleWorkspace.test.tsx` — cycle spine, live canvas, failure controls.
- `desktop/src/renderer/src/features/NeedUserInputModal.tsx` — shared free-text gate dialog.
- `desktop/src/renderer/src/features/NeedUserInputModal.test.tsx` — draft, dismiss, resume, and routing coverage.
- `desktop/src/renderer/src/features/CurrentRunInspection.tsx` — cycle presentation mode.
- `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx` — suppress regular gauge/copy in cycle mode.
- `desktop/src/renderer/src/features/FeatureCockpit.tsx` — authoritative integration and modal/receipt state.
- `desktop/src/renderer/src/features/FeatureCockpit.test.tsx` — end-to-end renderer state transitions.
- `desktop/src/renderer/src/features/AftercareDesk.tsx`, `AftercareDesk.test.tsx` — remove superseded desk.
- `desktop/src/renderer/src/styles/app.css` — Aftercare, cycle, facts rail, modal, responsive and reduced-motion styling.
- `desktop/test/e2e/screenshot-capture/capture.tsx`, `capture.spec.ts` — updated visual fixtures.

---

### Task 1: Authoritative cycle-phase contract

**Files:**
- Modify: `internal/server/read_api_contract_test.go`
- Modify: `internal/server/read_model.go`
- Modify: `api/openapi.yaml`
- Regenerate: `internal/server/serverapi.gen.go`
- Modify: `desktop/src/shared/api/parse.ts`
- Modify: `desktop/src/shared/ipc.ts`
- Modify: `desktop/src/main/features.ts`
- Modify: `desktop/src/main/__tests__/featureService.test.ts`
- Regenerate: `desktop/src/shared/api/schema.gen.ts`

**Interfaces:**
- Produces: `CycleDTO.Phase string` JSON field `phase,omitempty`.
- Produces: `CycleView.phase?: string`.
- Produces: `cyclePhase(f *feature.Feature, cycle *feature.RepoCycleState) string`.

- [ ] **Step 1: Write failing Go read-model tests**

Add table coverage asserting:

```go
tests := []struct {
    name string
    mutate func(*feature.Feature)
    want string
}{
    {"rebase harness", func(f *feature.Feature) {
        f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning}
        f.RebaseOperation = &feature.RebaseOperationState{Stage: feature.RebaseStageHarness}
    }, "inspect_rebase"},
    {"rebase conflict resolver", func(f *feature.Feature) {
        f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleRunning}
        f.RebaseOperation = &feature.RebaseOperationState{Stage: feature.RebaseStageSmartRebase}
    }, "resolve_conflicts"},
    {"rebase final review", func(f *feature.Feature) {
        f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase, Status: feature.RepoCycleReviewing}
        f.RebaseOperation = &feature.RebaseOperationState{Stage: feature.RebaseStageFinalReview}
    }, "final_review"},
    {"review comments", func(f *feature.Feature) {
        f.ActiveCycle = &feature.CycleState{Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning}
    }, "address_validate"},
    {"refactor planning", func(f *feature.Feature) {
        f.ActiveCycle = &feature.CycleState{Type: feature.CycleRefactor, Status: feature.RepoCycleRunning}
        f.CurrentPhaseStatus = "refactor-planning"
    }, "plan_refactor"},
}
```

- [ ] **Step 2: Run the contract test and verify failure**

Run:

```bash
go test ./internal/server -run 'TestFeatureDetailProjectsActive.*Cycle' -count=1
```

Expected: cycle maps do not contain `phase`.

- [ ] **Step 3: Implement the read-model projection and schema**

Add `Phase` to `CycleDTO`, populate it in both `activeCycleDTO` paths, and define:

```go
func cyclePhase(f *feature.Feature, cycleType feature.RepoCycleType, status string) string {
    switch cycleType {
    case feature.CycleRebase:
        if f.RebaseOperation != nil {
            switch f.RebaseOperation.Stage {
            case feature.RebaseStageSmartRebase:
                return "resolve_conflicts"
            case feature.RebaseStageFinalReview:
                return "final_review"
            }
        }
        return "inspect_rebase"
    case feature.CycleReviewComments:
        return "address_validate"
    case feature.CycleRefactor:
        if f.CurrentPhaseStatus == "refactor-planning" {
            return "plan_refactor"
        }
        return "implement_validate"
    default:
        return ""
    }
}
```

Add optional `phase` to the OpenAPI Cycle schema, then run:

```bash
make generate-openapi
npm run generate:api
```

- [ ] **Step 4: Add desktop parsing/mapping assertions**

Extend the feature service fixture with `cycle: { type: "refactor", status: "running", phase: "plan_refactor" }` and assert:

```ts
expect(result.cycle).toEqual({
  type: 'refactor',
  status: 'running',
  phase: 'plan_refactor',
});
```

Map with:

```ts
...(feature.cycle.phase === undefined ? {} : { phase: feature.cycle.phase })
```

- [ ] **Step 5: Run focused Go and desktop tests**

Run:

```bash
go test ./internal/server -run 'TestFeatureDetailProjectsActive.*Cycle' -count=1
cd desktop && npm test -- src/main/__tests__/featureService.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add api/openapi.yaml internal/server/read_model.go internal/server/read_api_contract_test.go internal/server/serverapi.gen.go desktop/src/shared/api/parse.ts desktop/src/shared/api/schema.gen.ts desktop/src/shared/ipc.ts desktop/src/main/features.ts desktop/src/main/__tests__/featureService.test.ts
git commit -m "Make aftercare cycle progress trustworthy"
```

---

### Task 2: Pure post-implementation presentation model

**Files:**
- Create: `desktop/src/renderer/src/features/postImplementationModel.ts`
- Create: `desktop/src/renderer/src/features/postImplementationModel.test.ts`

**Interfaces:**
- Produces:

```ts
export type PostImplementationMode =
  | { kind: 'regular' }
  | { kind: 'aftercare' }
  | { kind: 'cycle'; cycle: CyclePresentation; failed: boolean };

export interface CyclePresentation {
  id: AftercareCycleId;
  count: number;
  stages: Array<{ id: string; label: string; state: 'done' | 'active' | 'upcoming'; conditional?: boolean }>;
  headline: string;
  current: string;
  next?: string;
}

export function resolvePostImplementationMode(
  snapshot: FeatureSnapshot,
  dismissedFailureId?: string,
): PostImplementationMode;

export function aftercareActions(snapshot: FeatureSnapshot): AftercareAction[];
export function cyclePresentation(snapshot: FeatureSnapshot): CyclePresentation | null;
```

- [ ] **Step 1: Write the failing model matrix**

Cover:

```ts
it.each(['CodeReady', 'Published', 'Done'])('%s resolves to aftercare', (status) => {
  expect(resolvePostImplementationMode(featureSnapshot({ status })).kind).toBe('aftercare');
});

it('lets a running cycle own a Published feature', () => {
  const mode = resolvePostImplementationMode(featureSnapshot({
    status: 'Published',
    cycle: { type: 'review-comments', status: 'running', count: 2, iteration: 1, phase: 'address_validate' },
  }));
  expect(mode.kind).toBe('cycle');
});

it('orders Publish before cycle actions only while enabled', () => {
  expect(aftercareActions(snapshot).map((action) => action.id)).toEqual([
    'publish', 'rebase', 'review-comments', 'refactor',
  ]);
});
```

Add exact cycle map/current-next assertions for all phases, review gate,
verification, need-input, and failed status.

- [ ] **Step 2: Run the model test and verify failure**

Run:

```bash
cd desktop && npm test -- src/renderer/src/features/postImplementationModel.test.ts
```

Expected: module not found.

- [ ] **Step 3: Implement minimal pure models**

Use the server action catalog as the only action-availability source. Treat
cycle statuses `running`, `reviewing`, `need_user_input`, and `failed` as cycle
ownership. Generate one stable failed identity:

```ts
export function cycleIdentity(snapshot: FeatureSnapshot): string | null {
  const cycle = snapshot.cycle;
  if (cycle?.type === undefined) return null;
  return `${cycle.type}:${cycle.count ?? 1}:${cycle.status ?? ''}`;
}
```

- [ ] **Step 4: Run test and typecheck**

```bash
cd desktop && npm test -- src/renderer/src/features/postImplementationModel.test.ts
npm run typecheck
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add desktop/src/renderer/src/features/postImplementationModel.ts desktop/src/renderer/src/features/postImplementationModel.test.ts
git commit -m "Give post-implementation states one source of truth"
```

---

### Task 3: Quiet Aftercare and facts rail

**Files:**
- Create: `desktop/src/renderer/src/features/FeatureFactsRail.tsx`
- Create: `desktop/src/renderer/src/features/AftercareWorkspace.tsx`
- Create: `desktop/src/renderer/src/features/AftercareWorkspace.test.tsx`
- Delete: `desktop/src/renderer/src/features/AftercareDesk.tsx`
- Delete: `desktop/src/renderer/src/features/AftercareDesk.test.tsx`

**Interfaces:**
- Consumes: `aftercareActions(snapshot)` from Task 2.
- Produces:

```ts
export function FeatureFactsRail(props: {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
}): React.ReactElement;

export function AftercareWorkspace(props: {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  receipt?: CycleReceipt;
  onAction(action: AftercareAction): void;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
}): React.ReactElement;
```

- [ ] **Step 1: Write failing component tests**

Assert:

```tsx
expect(screen.getByRole('heading', { name: 'Implementation complete.' })).toBeVisible();
expect(screen.getAllByRole('button').map((button) => button.textContent)).toContain('Prepare publish');
expect(screen.getByText('$95.18')).toBeVisible();
expect(screen.queryByText('Durable setup')).not.toBeInTheDocument();
expect(screen.queryByText(snapshot.name)).not.toBeInTheDocument();
```

Cover Published omitting Publish, record/change callbacks, receipt copy, and
narrow facts behavior.

- [ ] **Step 2: Run test and verify failure**

```bash
cd desktop && npm test -- src/renderer/src/features/AftercareWorkspace.test.tsx
```

Expected: module not found.

- [ ] **Step 3: Implement facts rail and Aftercare**

Render semantic action buttons from `AftercareAction`. Reuse existing completion
verbs/modal routing through `onAction`; do not duplicate preflight logic.

Facts rail renders only:

```ts
['Status', 'Repository', 'Branch', 'Run', 'Elapsed', 'Cost', 'PR', 'Freshness']
```

Delete the superseded desk files after parity tests pass.

- [ ] **Step 4: Run focused tests**

```bash
cd desktop && npm test -- src/renderer/src/features/AftercareWorkspace.test.tsx
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add desktop/src/renderer/src/features/FeatureFactsRail.tsx desktop/src/renderer/src/features/AftercareWorkspace.tsx desktop/src/renderer/src/features/AftercareWorkspace.test.tsx desktop/src/renderer/src/features/AftercareDesk.tsx desktop/src/renderer/src/features/AftercareDesk.test.tsx
git commit -m "Let completed features rest without losing context"
```

---

### Task 4: Cycle-mode live inspection and focused workspace

**Files:**
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx`
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx`
- Create: `desktop/src/renderer/src/features/CycleWorkspace.tsx`
- Create: `desktop/src/renderer/src/features/CycleWorkspace.test.tsx`

**Interfaces:**
- Consumes: `CyclePresentation` from Task 2.
- Produces:

```ts
export interface CurrentRunInspectionProps {
  // existing props...
  presentation?: 'regular' | 'cycle';
}

export function CycleWorkspace(props: {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  presentation: CyclePresentation;
  attentionFooter?: ReactNode;
  onRunMetrics(metrics: RunMetrics | null): void;
  onStop(): void;
  onRetry(): void;
  onReturnToAftercare(): void;
  onOpenRunRecord(): void;
}): React.ReactElement;
```

- [ ] **Step 1: Write failing cycle-mode inspection tests**

Render `CurrentRunInspection presentation="cycle"` and assert:

```tsx
expect(screen.queryByRole('region', { name: /Roadmap progress/ })).not.toBeInTheDocument();
expect(screen.queryByText('Mutable current run')).not.toBeInTheDocument();
expect(screen.getByText('Live agent activity')).toBeVisible();
```

- [ ] **Step 2: Write failing CycleWorkspace tests**

Assert each cycle's phase labels, current/next line, standard live inspection,
facts rail, Stop, failed Retry/Return actions, and absence of repository/comment/
task dashboards.

- [ ] **Step 3: Run tests and verify failure**

```bash
cd desktop && npm test -- src/renderer/src/features/CurrentRunInspection.test.tsx src/renderer/src/features/CycleWorkspace.test.tsx
```

- [ ] **Step 4: Implement cycle presentation mode and workspace**

Keep `CurrentRunInspection` data fetching unchanged. Only switch its heading and
gauge presentation:

```tsx
{presentation === 'regular' ? <RoadmapGauge ... /> : null}
```

Render cycle spine stages with `aria-current="step"`. Keep facts outside the
live canvas. Failed copy uses the safe snapshot failure/repository error text.

- [ ] **Step 5: Run focused tests and typecheck**

```bash
cd desktop && npm test -- src/renderer/src/features/CurrentRunInspection.test.tsx src/renderer/src/features/CycleWorkspace.test.tsx
npm run typecheck
```

- [ ] **Step 6: Commit**

```bash
git add desktop/src/renderer/src/features/CurrentRunInspection.tsx desktop/src/renderer/src/features/CurrentRunInspection.test.tsx desktop/src/renderer/src/features/CycleWorkspace.tsx desktop/src/renderer/src/features/CycleWorkspace.test.tsx
git commit -m "Keep cycle execution focused on the live agent"
```

---

### Task 5: Floating need-user-input modal

**Files:**
- Create: `desktop/src/renderer/src/features/NeedUserInputModal.tsx`
- Create: `desktop/src/renderer/src/features/NeedUserInputModal.test.tsx`
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx`
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx`

**Interfaces:**
- Consumes: `AttentionItem & { kind: 'gate' }`, `AttentionDrafts`,
  `useAttentionDraftSaves`, `runAttentionSubmit`.
- Produces:

```ts
export function NeedUserInputModal(props: {
  item: AttentionGate;
  busy: boolean;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onAnswerLater(): void;
  onResolved(): Promise<void>;
}): React.ReactElement;
```

- [ ] **Step 1: Write failing modal tests**

Cover:

```tsx
expect(screen.getByRole('dialog', { name: 'Agent needs your input' })).toBeVisible();
await user.type(screen.getByLabelText(/deployment window/), 'After verification passes.');
await user.click(screen.getByRole('button', { name: 'Answer later' }));
expect(mock.api.saveGateDraft).toHaveBeenCalled();
expect(mock.api.resolveGate).not.toHaveBeenCalled();
```

Also cover all-questions-required, Resume payload/routing for feature and cycle
gates, focus trap, Escape draft preservation, and already-resolved refresh.

- [ ] **Step 2: Run tests and verify failure**

```bash
cd desktop && npm test -- src/renderer/src/features/NeedUserInputModal.test.tsx
```

- [ ] **Step 3: Implement modal with existing draft mutations**

Use one textarea per `item.questions`, prefilled through `gateDraftFor`. On
resume, save the current draft then call:

```ts
window.agentico.resolveGate({
  featureId: item.featureId,
  ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
  ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
  decision: 'resume',
});
```

Use `useModalDismiss` with a dismissal callback that saves draft and invokes
`onAnswerLater`.

- [ ] **Step 4: Remove inline gate presentation**

Filter `gate` out of the `attentionFooter` passed to
`CurrentRunInspection`. Preserve help, questions, permissions, and review
behavior.

- [ ] **Step 5: Run focused tests**

```bash
cd desktop && npm test -- src/renderer/src/features/NeedUserInputModal.test.tsx src/renderer/src/features/CurrentRunInspection.test.tsx
```

- [ ] **Step 6: Commit**

```bash
git add desktop/src/renderer/src/features/NeedUserInputModal.tsx desktop/src/renderer/src/features/NeedUserInputModal.test.tsx desktop/src/renderer/src/features/CurrentRunInspection.tsx desktop/src/renderer/src/features/CurrentRunInspection.test.tsx
git commit -m "Bring blocking questions into feature context"
```

---

### Task 6: FeatureCockpit state integration

**Files:**
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.tsx`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`

**Interfaces:**
- Consumes: all components/models from Tasks 2–5.
- Produces: exclusive workspace routing, transient receipt, failed-cycle
  dismissal, run-record modal, and gate-modal auto-open identity.

- [ ] **Step 1: Replace old at-rest tests with failing workspace assertions**

Add tests proving:

```tsx
expect(screen.queryByLabelText('Feature pipeline')).not.toBeInTheDocument();
expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
expect(screen.getByRole('region', { name: 'Feature aftercare' })).toBeVisible();
```

For a Published snapshot with a running cycle, assert Aftercare is absent and
the cycle workspace/live inspection is present. Add success transition, failed
dismissal, CodeReady Publish, and no repeated feature heading.

- [ ] **Step 2: Add failing gate integration tests**

Prove a pending gate auto-opens once on feature focus, Answer later does not
immediately reopen during the same gate identity, and an Attention jump reopens
it. Prove cycle routing includes `repoName` and `cycleType`.

- [ ] **Step 3: Run the focused cockpit suite and verify failures**

```bash
cd desktop && npm test -- src/renderer/src/features/FeatureCockpit.test.tsx
```

- [ ] **Step 4: Integrate the state resolver**

Resolve mode before constructing stage surfaces. In `aftercare` and `cycle`
mode, do not render the primary `PhaseSpine` or stage tabs. Route completion
actions from `AftercareWorkspace` into existing completion modals and cycle
actions into existing cycle modals.

Track:

```ts
const [dismissedFailureId, setDismissedFailureId] = useState<string | undefined>();
const previousCycleRef = useRef<CycleView | undefined>();
const [cycleReceipt, setCycleReceipt] = useState<CycleReceipt | undefined>();
const [dismissedGateId, setDismissedGateId] = useState<string | undefined>();
```

Reset dismissal when the cycle/gate identity changes. Keep watchdog recovery out
of these states.

- [ ] **Step 5: Add run-record modal**

Open a read-only overlay from both Aftercare and CycleWorkspace using existing
current-run/archive inspection components with `shouldStream={false}`. It must
not create a stage tab.

- [ ] **Step 6: Run cockpit and neighboring suites**

```bash
cd desktop && npm test -- src/renderer/src/features/FeatureCockpit.test.tsx src/renderer/src/features/WorkspaceShell.test.tsx src/renderer/src/features/AttentionInbox.test.tsx
npm run typecheck
```

- [ ] **Step 7: Commit**

```bash
git add desktop/src/renderer/src/features/FeatureCockpit.tsx desktop/src/renderer/src/features/FeatureCockpit.test.tsx
git commit -m "Give aftercare cycles exclusive ownership of the stage"
```

---

### Task 7: Visual system, responsive behavior, and screenshots

**Files:**
- Modify: `desktop/src/renderer/src/styles/app.css`
- Modify: `desktop/test/e2e/screenshot-capture/capture.tsx`
- Modify: `desktop/test/e2e/screenshot-capture/capture.spec.ts`
- Modify: `desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts`

**Interfaces:**
- Consumes: semantic classes from Tasks 3–6.
- Produces: token-only Aftercare/cycle/modal styling and visual regression fixtures.

- [ ] **Step 1: Add screenshot fixtures before CSS**

Add fixture states for CodeReady Aftercare, Published Aftercare, all three
active cycles, regular gate modal, cycle gate modal, and failed cycle.

- [ ] **Step 2: Run capture tests and verify visual/layout failures**

```bash
cd desktop && npm test -- test/e2e/screenshot-capture/capture.spec.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

- [ ] **Step 3: Implement CSS**

Use:

```css
.post-workspace { min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr) var(--cockpit-inspector-width); }
.cycle-workspace__spine { display: grid; border-bottom: 1px solid var(--color-border); }
.need-input-modal__primary { background: var(--color-signal); font-family: var(--font-display); }
```

Remove obsolete `.aftercare__ledger`, `.aftercare__repositories`, and old desk
rules after no component references them. Add narrow drawer/stacking behavior,
visible focus, and reduced-motion guards.

- [ ] **Step 4: Inspect screenshots**

Run the capture command used by the local harness, inspect desktop and narrow
images, and correct overflow, duplicate headings, and scroll ownership.

- [ ] **Step 5: Run renderer static checks**

```bash
cd desktop && npm run typecheck && npm run lint
```

- [ ] **Step 6: Commit**

```bash
git add desktop/src/renderer/src/styles/app.css desktop/test/e2e/screenshot-capture/capture.tsx desktop/test/e2e/screenshot-capture/capture.spec.ts desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts
git commit -m "Make post-implementation work visually legible"
```

---

### Task 8: Full regression and cleanup

**Files:**
- Modify only files implicated by failures.

**Interfaces:**
- Produces: verified feature with no stale imports, obsolete CSS, or contradictory tests.

- [ ] **Step 1: Run desktop verification**

```bash
cd desktop
npm run typecheck
npm run lint
npm test
```

- [ ] **Step 2: Run repository fast/static gates**

```bash
make test-fast
go vet ./...
go build ./...
```

- [ ] **Step 3: Run touched-package Go tests**

```bash
go test ./internal/server/... ./internal/orchestrator/... ./cmd/agentico/... -count=1
```

- [ ] **Step 4: Search for obsolete behavior**

```bash
rg -n 'AftercareDesk|Run record.*tab|Repository readiness|Run ledger|Durable setup' desktop/src/renderer/src
```

Expected: no superseded component/import or at-rest Durable setup rendering.

- [ ] **Step 5: Manual pass**

Run:

```bash
cd desktop && npm run dev
```

Verify CodeReady Publish, Published runway, all dedicated preflight modals,
exclusive active-cycle workspace, iteration/review transitions, silent
watchdog retry, gate modal notification/draft/resume, failure Retry/Return,
success receipt, run-record overlay, narrow inspector, keyboard focus, and
reduced motion.

- [ ] **Step 6: Commit any regression fixes**

Stage only the exact changed files and commit:

```bash
git commit -m "Preserve feature cockpit behavior through the aftercare overhaul"
```
