# Publish Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a macOS-native Publish sheet, align title-less existing-PR updates across renderer and IPC, and safely publish rewritten branches whose only remote-only commits are provably redundant merges.

**Architecture:** The desktop renderer owns contextual form behavior and a dedicated sheet presentation while the shared IPC schema remains the trust boundary. The Git package owns destination inspection and explicit-lease pushing; the orchestrator translates Git outcomes into domain errors, and the server exposes stable codes that the desktop maps to concise guidance.

**Tech Stack:** React 19, TypeScript 5.9, Zod 4, Vitest, Electron, Playwright, Go, real local Git repositories

## Global Constraints

- Every new `.go`, `.sh`, or `.py` file begins with the repository's Apache 2.0 header using year 2026.
- The dialog accessible name remains `Publish reviewed changes`.
- Pure existing-PR updates do not render or submit PR title/body fields.
- New or mixed selections require a nonblank shared title; description remains optional.
- The Publish sheet is 680px wide subject to the existing responsive inset, uses the existing `.sheet` family, system typography, and application color tokens.
- `Cancel` is leading and the primary publish action is trailing in a pinned footer.
- Genuine remote-only work always fails closed; no plain force push, unconditional fetch plus implicit lease, or blind retry is allowed.
- `--force-if-includes` remains the generic uninspected force-push guard.
- A proven-redundant rewrite uses an explicit `refs/heads/<branch>:<inspected-sha>` lease and never retries after rejection.
- Tests touching subprocesses, mutable package hooks, process environment, or shared repositories do not call `t.Parallel()`.
- Add each regression first and observe the expected failure before production changes.
- Do not amend commits after hook failures; fix, restage, and create a new commit.

---

### Task 1: Align the Desktop Publish Request Contract

**Files:**
- Modify: `desktop/src/shared/ipc.test.ts`
- Modify: `desktop/src/shared/ipc.ts:1023-1031`
- Modify: `desktop/src/main/__tests__/featureService.test.ts`
- Modify: `desktop/src/main/features.ts:83-93`

**Interfaces:**
- Consumes: `FeatureActionRequestSchema`, `FeatureService.dispatchAction(request)`
- Produces: `type PublishFeatureActionRequest = Extract<FeatureActionRequest, { action: 'publish' }>` with optional `body.title` and `body.body`
- Produces: server remedies for `publish_remote_diverged` and `publish_remote_changed`

- [ ] **Step 1: Add the failing shared-schema regression**

Add this assertion beside the existing publish request assertion:

```ts
expect(
  FeatureActionRequestSchema.parse({
    featureId: 'abcd1234',
    action: 'publish',
    body: {
      source_revision: 'rev-1',
      repos: ['repo-a'],
    },
  }),
).toStrictEqual({
  featureId: 'abcd1234',
  action: 'publish',
  body: {
    source_revision: 'rev-1',
    repos: ['repo-a'],
  },
});
```

- [ ] **Step 2: Verify the schema test fails at `body.title`**

Run:

```bash
npm test -- --run src/shared/ipc.test.ts
```

Expected: FAIL because the publish branch requires `title`.

- [ ] **Step 3: Make PR metadata optional and export the narrowed request type**

Change the publish branch and add the alias after `FeatureActionRequest`:

```ts
title: z.string().trim().min(1).max(200).optional(),
body: z.string().max(4000).optional(),
```

```ts
export type PublishFeatureActionRequest = Extract<
  FeatureActionRequest,
  { action: 'publish' }
>;
```

- [ ] **Step 4: Verify the schema regression passes**

Run:

```bash
npm test -- --run src/shared/ipc.test.ts
```

Expected: PASS.

- [ ] **Step 5: Add a failing main-process forwarding test**

Add a service test that records the request body:

```ts
it('forwards an existing-PR update without unused PR metadata', async () => {
  const { service, calls } = makeService(() => ({
    status: 200,
    body: {
      api_version: 'v1',
      feature_id: 'abcd1234ef567890',
      result: 'published',
    },
  }));

  await service.dispatchAction({
    featureId: 'abcd1234ef567890',
    action: 'publish',
    body: { source_revision: 'rev-1', repos: ['repo-a'] },
  });

  expect(calls[0]?.init?.body).toStrictEqual({
    source_revision: 'rev-1',
    repos: ['repo-a'],
  });
});
```

- [ ] **Step 6: Verify forwarding and remedies**

Add these mappings to `REMEDY_BY_CODE`:

```ts
publish_remote_diverged:
  'Review and reconcile the pull-request branch on GitHub, then refresh and retry.',
publish_remote_changed:
  'Refresh the publish state and retry; Agentico did not overwrite the newer branch.',
```

Run:

```bash
npm test -- --run src/main/__tests__/featureService.test.ts src/shared/ipc.test.ts
```

Expected: PASS with the title-less body unchanged.

- [ ] **Step 7: Commit the desktop contract fix**

```bash
git add desktop/src/shared/ipc.ts desktop/src/shared/ipc.test.ts desktop/src/main/features.ts desktop/src/main/__tests__/featureService.test.ts
git commit -m "Allow existing pull requests to publish without unused metadata" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 2: Replace the Publish Card With a Contextual macOS Sheet

**Files:**
- Modify: `desktop/src/renderer/src/features/completion/completionShared.tsx`
- Modify: `desktop/src/renderer/src/features/completion/PublishModal.tsx`
- Modify: `desktop/src/renderer/src/features/completion/PublishModal.test.tsx`
- Modify: `desktop/src/renderer/src/wizard/ipcError.ts`
- Modify: `desktop/src/renderer/src/wizard/ipcError.test.ts`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.tsx`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`
- Modify: `desktop/src/renderer/src/styles/app.css`

**Interfaces:**
- Consumes: `PublishFeatureActionRequest`, `useModalDismiss`, `.sheet`, `.sheet__body`, `.sheet__footer`
- Produces: `PublishModal(props & { onClose(): void })`
- Produces: `dispatchAction(request: PublishFeatureActionRequest): Promise<FeatureActionResult>`
- Produces: failure results carrying `code`, `message`, and optional `remediation`

- [ ] **Step 1: Add failing contextual-form tests**

Replace the pure-update test's UI expectations with:

```ts
expect(screen.getByRole('dialog', { name: 'Publish reviewed changes' })).toBeVisible();
expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
expect(screen.queryByRole('button', { name: 'Generate PR narrative' })).not.toBeInTheDocument();
expect(screen.getByText('Rewrites the pull-request branch with a safety lease.')).toBeVisible();
expect(screen.getByRole('button', { name: 'Cancel' })).toBeVisible();
expect(screen.getByRole('button', { name: 'Publish updates' })).toBeEnabled();
```

Change the dispatch assertion to the typed request shape:

```ts
expect(dispatchAction).toHaveBeenCalledWith({
  featureId: 'abcd1234ef567890',
  action: 'publish',
  body: { source_revision: 'rev-1', repos: ['api'] },
});
```

Retain the new-PR test and assert the visible labels `Required` and `Optional`.
Add a mixed-selection test proving fields remain visible while an existing-PR-only selection hides them after the new repository is unchecked.

- [ ] **Step 2: Add failing sheet, keyboard, and error tests**

Assert the dialog uses the sheet family and footer order:

```ts
const dialog = screen.getByRole('dialog', { name: 'Publish reviewed changes' });
expect(dialog).toHaveClass('sheet', 'completion-publish-sheet');
const footer = dialog.querySelector('.sheet__footer');
expect(within(footer as HTMLElement).getAllByRole('button').map((button) => button.textContent)).toEqual([
  'Cancel',
  'Publish updates',
]);
```

Add an asynchronous divergence failure:

```ts
dispatchAction.mockRejectedValue(
  new Error(
    "publish_remote_diverged: pull-request branch contains remote work that is not in this workspace Review and reconcile the pull-request branch on GitHub, then refresh and retry.",
  ),
);
await user.click(screen.getByRole('button', { name: 'Publish updates' }));
expect(await screen.findByRole('alert')).toHaveTextContent(
  "The pull-request branch contains changes that aren't in this workspace.",
);
expect(screen.getByRole('alert')).not.toHaveTextContent('git push');
```

Verify Escape and scrim close while idle, and that Cancel/scrim/Escape do not close while the publish promise is pending.

- [ ] **Step 3: Run the focused renderer tests and observe failures**

Run:

```bash
npm test -- --run src/renderer/src/features/completion/PublishModal.test.tsx src/renderer/src/features/FeatureCockpit.test.tsx
```

Expected: FAIL because Publish is still a `CockpitModal`, pure updates still show narrative fields, and raw `ResultBox` text is rendered.

- [ ] **Step 4: Preserve structured failure information**

First extend `WizardError` and `parseIpcError` so the preload's attached
`error.code` and `error.remediation` properties remain separate from the
display message. Add a regression using an `Error` with those assigned
properties; assert the returned value has the exact code, message without the
remediation suffix, and remediation. Then extend the failure member of
`ActionResult` and populate it in `useCompletionAction`:

```ts
export type ActionResult =
  | { ok: true; result: string }
  | {
      ok: false;
      code: string;
      message: string;
      remediation?: string;
      reconciling?: boolean;
    };
```

Keep `ResultBox` backward-compatible for non-Publish completion surfaces;
Publish renders a dedicated compact notice from the structured fields.

- [ ] **Step 5: Build the dedicated sheet and typed submit path**

Rename the exported component to `PublishModal`, add `onClose`, and own the sheet shell:

```tsx
const requestClose = useCallback(() => {
  if (!publishAction.busy && !publishAction.reconciling) onClose();
}, [onClose, publishAction.busy, publishAction.reconciling]);
useModalDismiss(dialogRef, requestClose);

return (
  <div className="sheet-scrim completion-publish-sheet__scrim" onMouseDown={requestClose}>
    <div
      ref={dialogRef}
      className="sheet completion-publish-sheet"
      role="dialog"
      aria-modal="true"
      aria-label="Publish reviewed changes"
      tabIndex={-1}
      onMouseDown={(event) => event.stopPropagation()}
    >
      <div className="sheet__body">{/* title, lede, repository manifest, contextual fields */}</div>
      <footer className="sheet__footer">
        <button className="sheet__footer-secondary" onClick={requestClose}>Cancel</button>
        <button className="sheet__footer-primary" disabled={!canPublish}>...</button>
      </footer>
    </div>
  </div>
);
```

Construct the request as `PublishFeatureActionRequest` and dispatch it directly:

```ts
const request: PublishFeatureActionRequest = {
  featureId,
  action: 'publish',
  body: {
    source_revision: preflight.sourceRevision,
    repos: Array.from(publishRepos),
    ...(title === '' ? {} : { title }),
    ...(publishBody.trim() === '' ? {} : { body: publishBody }),
  },
};
await dispatchAction(request);
```

Render Pull request details only when `titleRequired` is true. Update both Publish render sites in `FeatureCockpit.tsx` to mount `PublishModal` directly; other completion actions retain `CockpitModal` and the generic unsafe callback is no longer used by Publish.

- [ ] **Step 6: Replace implicit repository placement and malformed selectors**

Repair every affected selector list by adding commas. Replace `display: contents` and auto-placement with explicit manifest structure:

```css
.completion-publish-sheet {
  width: min(680px, calc(100% - var(--space-6)));
}

.completion-workspace__publish-repo {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-2) var(--space-3);
}

.completion-workspace__publish-repo-main {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}

.completion-workspace__pending-note {
  grid-column: 1 / -1;
  border-left: 2px solid var(--attention);
  padding-left: var(--space-2);
}
```

Use existing tokens only, keep textarea height near five rows, add focus-visible styles, and include a narrow responsive rule that stacks repository metadata without changing reading order.

- [ ] **Step 7: Implement concise publish error copy**

Map stable codes in the Publish component:

```ts
const PUBLISH_FAILURE_COPY: Record<string, { title: string; next: string }> = {
  publish_remote_diverged: {
    title: "The pull-request branch contains changes that aren't in this workspace.",
    next: 'Review and reconcile the branch on GitHub, then refresh and retry.',
  },
  publish_remote_changed: {
    title: 'The pull-request branch changed while Agentico was publishing.',
    next: 'Refresh the publish state and retry. Agentico did not overwrite the newer branch.',
  },
};
```

Unknown failures use `Agentico couldn't prepare this publish.` and place the sanitized original message inside a closed `<details>` disclosure labeled `Show details`.

- [ ] **Step 8: Verify focused renderer tests pass**

Run:

```bash
npm test -- --run src/renderer/src/features/completion/PublishModal.test.tsx src/renderer/src/features/FeatureCockpit.test.tsx
```

Expected: PASS with no act warnings or console errors.

- [ ] **Step 9: Commit the Publish sheet**

```bash
git add desktop/src/renderer/src/features/completion/completionShared.tsx desktop/src/renderer/src/features/completion/PublishModal.tsx desktop/src/renderer/src/features/completion/PublishModal.test.tsx desktop/src/renderer/src/wizard/ipcError.ts desktop/src/renderer/src/wizard/ipcError.test.ts desktop/src/renderer/src/features/FeatureCockpit.tsx desktop/src/renderer/src/features/FeatureCockpit.test.tsx desktop/src/renderer/src/styles/app.css
git commit -m "Keep publish decisions focused on work users can control" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 3: Inspect and Safely Push Rewritten Remote Branches

**Files:**
- Create: `internal/git/rewrite_push.go`
- Create: `internal/git/rewrite_push_test.go`
- Modify: `internal/git/force_push_lease_test.go`

**Interfaces:**
- Consumes: a Git worktree and destination branch
- Produces: `PushRewrittenBranch(worktreePath, branch string) error`
- Produces: `RewritePushError{Kind RewritePushErrorKind, Branch string, RemoteOnlyCommits int, Err error}`
- Produces constants `RewritePushRemoteDiverged` and `RewritePushRemoteChanged`

- [ ] **Step 1: Add the Apache header and failing Taulu-shaped regression**

Create a real-Git fixture with this graph:

```text
base ── feature-A ─────────────── rewritten-A' ── HEAD
   └── master-1 ── master-2 ────────┘
          \                       /
           feature-A ── merge-M ─┘   (remote branch only)
```

The remote merge `M` must have two parents that are ancestors of local `HEAD`, and `git show --remerge-diff --format= --no-ext-diff M` must be empty. Assert:

```go
if err := PushRewrittenBranch(repo, branch); err != nil {
    t.Fatalf("PushRewrittenBranch() error = %v; redundant merge should be replaceable", err)
}
if got := remoteBranchSHA(t, bare, branch); got != localHeadSHA(t, repo) {
    t.Fatalf("remote tip = %s; want rewritten HEAD", got)
}
```

- [ ] **Step 2: Verify the regression fails because the function is absent**

Run:

```bash
go test ./internal/git -run TestPushRewrittenBranch_AllowsRedundantRemoteMerge -count=1
```

Expected: build FAIL with `undefined: PushRewrittenBranch`.

- [ ] **Step 3: Add failing safety regressions**

Add real-Git cases for:

```go
func TestPushRewrittenBranch_RejectsOrdinaryRemoteCommit(t *testing.T)
func TestPushRewrittenBranch_RejectsUniqueMergeResolution(t *testing.T)
func TestPushRewrittenBranch_RejectsRemoteMoveAfterInspection(t *testing.T)
```

The remote-move case calls an unexported helper with a `beforePush func()` hook; the hook pushes another commit from a second clone after inspection and before the explicit-lease push. Assert `errors.As` to `*RewritePushError`, exact kind, branch, and remote-only count. These subprocess tests do not use `t.Parallel()`.

- [ ] **Step 4: Implement isolated inspection and classification**

Add the typed error and production entry point:

```go
type RewritePushErrorKind string

const (
    RewritePushRemoteDiverged RewritePushErrorKind = "remote_diverged"
    RewritePushRemoteChanged  RewritePushErrorKind = "remote_changed"
)

type RewritePushError struct {
    Kind              RewritePushErrorKind
    Branch            string
    RemoteOnlyCommits int
    Err               error
}

func PushRewrittenBranch(worktreePath, branch string) error {
    return pushRewrittenBranch(worktreePath, branch, nil)
}
```

Inside `pushRewrittenBranch`:

1. Generate a cryptographically random hex suffix and build a ref under `refs/agentico/publish-inspection/`.
2. `defer git update-ref -d <inspection-ref>`.
3. Fetch only `refs/heads/<branch>:<inspection-ref>` with `--no-tags`.
4. Resolve the inspected SHA.
5. If inspected SHA is an ancestor of `HEAD`, invoke the existing normal push.
6. Enumerate `git rev-list <inspection-ref> ^HEAD`.
7. For each SHA, require at least two parents, every parent to be an ancestor of `HEAD`, and empty output from `git show --remerge-diff --format= --no-ext-diff <sha>`.
8. Return `RewritePushRemoteDiverged` on the first failed classification.
9. Invoke the optional test hook.
10. Push with `--force-with-lease=refs/heads/<branch>:<inspected-sha> -u origin HEAD:refs/heads/<branch>`.
11. If the push fails, query `git ls-remote origin refs/heads/<branch>` and
    compare the returned SHA with the inspected SHA. A different SHA becomes
    `RewritePushRemoteChanged`; an unchanged SHA remains a wrapped operational
    failure. Never retry either outcome.

Wrap command failures without including raw output in the typed error's public `Error()` string; retain sanitized command detail only through `Unwrap()`.

- [ ] **Step 5: Verify redundant and unsafe graph tests**

Run:

```bash
go test ./internal/git -run 'TestPushRewrittenBranch|TestForcePush' -count=1
```

Expected: PASS. The two existing `--force-if-includes` unseen-work rejection tests must remain green.

- [ ] **Step 6: Commit the Git safety primitive**

```bash
git add internal/git/rewrite_push.go internal/git/rewrite_push_test.go internal/git/force_push_lease_test.go
git commit -m "Preserve remote work while publishing rewritten branches" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 4: Expose Typed Publish Divergence End to End

**Files:**
- Create: `internal/orchestrator/publish_errors.go`
- Modify: `internal/orchestrator/remote_ops.go`
- Modify: `internal/orchestrator/publish.go`
- Modify: `internal/orchestrator/publish_test.go`
- Modify: `test/testutil/mocks/mock_git.go`
- Modify: `cmd/agentico/main.go`
- Modify: `cmd/agentico/server_mutation_target_test.go`
- Modify: `internal/server/mutation.go`
- Modify: `internal/server/completion_api_test.go`

**Interfaces:**
- Consumes: `git.PushRewrittenBranch` and `*git.RewritePushError`
- Produces: `RemoteOps.PushRewrittenBranch(worktreePath, branch string) error`
- Produces: `PublishRemoteDivergedError` and `PublishRemoteChangedError`
- Produces: optional `ActionConflictError.Code` for stable conflict subtypes
- Produces server codes `publish_remote_diverged` and `publish_remote_changed`

- [ ] **Step 1: Add failing orchestrator mapping tests**

Configure the remote mock to return each Git error and assert the domain error:

```go
pub.PushRewrittenBranchFn = func(path, branch string) error {
    return &git.RewritePushError{
        Kind: git.RewritePushRemoteDiverged,
        Branch: branch,
        RemoteOnlyCommits: 2,
    }
}

err := o.PublishWithOptions(featureID, orchestrator.PublishOptions{Repos: []string{"r1"}})
var diverged *orchestrator.PublishRemoteDivergedError
if !errors.As(err, &diverged) {
    t.Fatalf("error = %T %v; want PublishRemoteDivergedError", err, err)
}
```

Assert `RepoName`, `Branch`, and `RemoteOnlyCommits`. Add the equivalent remote-changed case. Also assert rewritten initial publish and existing-PR republish both call `PushRewrittenBranch`, while fast-forward republish still calls `Push`.

- [ ] **Step 2: Verify orchestrator tests fail on the missing interface**

Run:

```bash
go test ./internal/orchestrator -run 'TestOrchestrator_.*Publish.*Remote|TestOrchestrator_Republish' -count=1
```

Expected: build FAIL because `RemoteOps.PushRewrittenBranch` and the domain errors do not exist.

- [ ] **Step 3: Add domain errors and translate Git outcomes**

Create `publish_errors.go` with the required 2026 Apache header:

```go
type PublishRemoteDivergedError struct {
    RepoName          string
    Branch            string
    RemoteOnlyCommits int
}

func (e *PublishRemoteDivergedError) Error() string {
    return "pull-request branch contains remote work that is not in this workspace"
}

type PublishRemoteChangedError struct {
    RepoName string
    Branch   string
}

func (e *PublishRemoteChangedError) Error() string {
    return "pull-request branch changed while Agentico was publishing"
}
```

Add `PushRewrittenBranch` to `RemoteOps`, `gitRemoteOps`, and `MockRemoteOps`.
Route both lease-push sites in `publish.go` through it. Translate
`*git.RewritePushError` immediately so higher layers never depend on the Git
package's diagnostic wording.

- [ ] **Step 4: Verify orchestrator tests pass**

Run:

```bash
go test ./internal/orchestrator -run 'TestOrchestrator_.*Publish.*Remote|TestOrchestrator_Republish|TestOrchestrator_Publish' -count=1
```

Expected: PASS.

- [ ] **Step 5: Add failing command-adapter and API-code tests**

In `server_mutation_target_test.go`, make the orchestrator return each domain
error and assert the adapter produces an `ActionConflictError` with the exact
code and target fields. In `completion_api_test.go`, make the mutation target
return that coded conflict and assert:

```go
if got, want := body.Error.Code, "publish_remote_diverged"; got != want {
    t.Fatalf("error code = %q; want %q", got, want)
}
if got, want := recorder.Code, http.StatusConflict; got != want {
    t.Fatalf("status = %d; want %d", got, want)
}
```

Assert the divergence target contains `repo`, `branch`, and `remote_only_commits`; remote-changed target contains `repo` and `branch`.

- [ ] **Step 6: Map domain errors through a coded server conflict**

Add an optional `Code string` field to `ActionConflictError`. When nonblank,
`writeMutationError` uses it instead of the generic `conflict` code. Add stable
constants:

```go
const (
    ErrorCodePublishRemoteDiverged = "publish_remote_diverged"
    ErrorCodePublishRemoteChanged  = "publish_remote_changed"
)
```

```go
var diverged *orchestrator.PublishRemoteDivergedError
if errors.As(err, &diverged) {
    return &serverruntime.ActionConflictError{
        Err: err,
        Code: serverruntime.ErrorCodePublishRemoteDiverged,
        Message: diverged.Error(),
        Target: map[string]any{
            "repo": diverged.RepoName,
            "branch": diverged.Branch,
            "remote_only_commits": diverged.RemoteOnlyCommits,
        },
    }
}
```

Add this in `cmd/agentico/main.go`'s `actionConflictError`, plus the analogous
remote-changed case. The server mutation package remains independent of the
orchestrator package.

- [ ] **Step 7: Verify server and orchestrator tests pass**

Run:

```bash
go test ./cmd/agentico ./internal/server ./internal/orchestrator -run 'Publish|Mutation' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the domain and API integration**

```bash
git add internal/orchestrator/publish_errors.go internal/orchestrator/remote_ops.go internal/orchestrator/publish.go internal/orchestrator/publish_test.go test/testutil/mocks/mock_git.go cmd/agentico/main.go cmd/agentico/server_mutation_target_test.go internal/server/mutation.go internal/server/completion_api_test.go
git commit -m "Explain remote branch safety conflicts without exposing Git internals" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 5: Update the Packaged Publish Journeys

**Files:**
- Modify: `desktop/test/e2e/journeys/publish-partial-retry.spec.ts`
- Modify: `desktop/test/e2e/journeys/zero-gap-completion-global.spec.ts`
- Inspect: `desktop/test/e2e/helpers/completionFixture.ts`

**Interfaces:**
- Consumes: packaged app, bundled Go server, `Publish reviewed changes` dialog
- Produces: end-to-end coverage of a title-less existing-PR branch update

- [ ] **Step 1: Grep every changed journey contract**

Run:

```bash
grep -rn 'Close\|Generate PR narrative\|Enter PR title\|Enter PR description\|Force-updates the pull-request branch' desktop/test/e2e/journeys/
```

Record every affected assertion before editing it.

- [ ] **Step 2: Add the title-less update journey**

Seed an existing pull request, then add a local commit after the published SHA so preflight reports `unpublished_changes`. In the packaged app assert:

```ts
const publishModal = handle.page.getByRole('dialog', { name: 'Publish reviewed changes' });
await expect(publishModal.getByLabel('PR title')).toHaveCount(0);
await expect(publishModal.getByLabel('PR body')).toHaveCount(0);
await expect(publishModal.getByRole('button', { name: 'Generate PR narrative' })).toHaveCount(0);
await publishModal.getByRole('button', { name: 'Publish updates' }).click();
await expect(publishModal.getByRole('status')).toContainText(/published/i);
assertPublishedBranch(seeded, existingPrRepo);
```

The test must exercise renderer → preload → main → HTTP → Go publisher; do not replace the call with `page.evaluate` or a mocked IPC handler.

- [ ] **Step 3: Update intentional copy and role assertions**

Change Publish-sheet dismissal assertions from Close to Cancel. Keep the dialog accessible name and repository checkbox names unchanged. New-PR coverage in `publish-partial-retry` must continue to assert Generate narrative and both placeholders.

- [ ] **Step 4: Run focused Playwright source checks**

Run:

```bash
npm run typecheck --workspace desktop
```

Expected: PASS.

- [ ] **Step 5: Commit the packaged journey contract**

```bash
git add desktop/test/e2e/journeys/publish-partial-retry.spec.ts desktop/test/e2e/journeys/zero-gap-completion-global.spec.ts
git commit -m "Protect title-less pull request updates in the packaged app" -m "Co-authored-by: Codex <noreply@openai.com>"
```

### Task 6: Verify the Complete Publish Flow

**Files:**
- Inspect: all changes against the approved design spec
- Modify only if a verification failure reveals an in-scope defect

**Interfaces:**
- Consumes: Tasks 1–5
- Produces: verified desktop and server behavior with recorded tier names

- [ ] **Step 1: Run focused regression suites**

```bash
npm test -- --run src/shared/ipc.test.ts src/main/__tests__/featureService.test.ts src/renderer/src/features/completion/PublishModal.test.tsx src/renderer/src/features/FeatureCockpit.test.tsx
go test ./internal/git ./internal/orchestrator ./internal/server -run 'Publish|ForcePush|Mutation' -count=1
```

Expected: all focused tests pass.

- [ ] **Step 2: Run the Fast suite**

```bash
make test-fast
```

Expected: exit zero within the baseline's ordinary range.

- [ ] **Step 3: Run desktop static and unit/security tiers**

```bash
npm run check
npm test
npm run test:security
```

Expected: all commands exit zero with no OpenAPI drift.

- [ ] **Step 4: Build the packaged app once and run affected journeys**

```bash
npm run package:verify
npm run test:e2e:packaged -- test/e2e/journeys/publish-partial-retry.spec.ts
npm run test:e2e:packaged -- test/e2e/journeys/zero-gap-completion-global.spec.ts
```

Expected: package verification and both journeys pass.

- [ ] **Step 5: Run API/process and Go gates**

```bash
go test ./test/e2e/... -count=1 -race
go vet ./...
go build ./...
```

Expected: all commands exit zero.

- [ ] **Step 6: Review diff, accessibility contracts, and source headers**

```bash
git diff --check origin/main...HEAD
git status --short --branch
grep -rn 'Close\|Force-updates the pull-request branch' desktop/test/e2e/journeys/
head -15 internal/git/rewrite_push.go internal/git/rewrite_push_test.go internal/orchestrator/publish_errors.go
```

Expected: no unintended Publish assertions remain, every new Go file has the 2026 Apache header, and the worktree is clean.

- [ ] **Step 7: Record verification for handoff**

Name these tiers exactly in the final handoff or PR description: Fast suite, Desktop static checks, Desktop unit/component/security tests, Desktop packaged E2E, E2E Go, Go static-analysis gate, and Go build gate. If Race regression is skipped, record: `Skipped Race regression: publish changes do not introduce shared mutable state or concurrency.`
