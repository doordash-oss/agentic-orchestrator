# Max-Iteration Retry Budget Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Retry and confirmed Restart extend exhausted iteration budgets by the established 10 general and 2 plan-specific iterations.

**Architecture:** Keep Retry classification authoritative in the Go server: only a persisted `max_iterations` failure receives the default deltas. Extend Electron's typed restart request so the renderer can send those existing API fields when its authoritative feature snapshot identifies the same failure, while ordinary restart and all other retry behavior stay unchanged.

**Tech Stack:** Go, TypeScript, React, Zod, Vitest, Testing Library

## Global Constraints

- General max-iteration retries add exactly 10 iterations.
- Plan-phase max-iteration retries additionally add exactly 2 plan iterations.
- Setup retry and non-`max_iterations` retry semantics must not change.
- Every new Go source file would require the Apache 2.0 header, but this plan creates no source files.
- Use test-first red-green-refactor for every production change.
- Run `make test-fast` before handoff plus `go vet ./...` and `go build ./...`.
- Because lifecycle/restart behavior changes, also run `go test ./test/integration/... -count=1` and `go test ./test/e2e/... -count=1 -race`.

---

### Task 1: Server-authoritative Retry budget extension

**Files:**
- Modify: `cmd/agentico/server_mutation_target_test.go`
- Modify: `cmd/agentico/main.go`

**Interfaces:**
- Consumes: `(*orchestrator.Orchestrator).RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int)`
- Produces: `serverMutationTarget.RetryFeature(featureID string)` behavior that passes `10, 2` only for persisted `feature.FailureMaxIterations`

- [ ] **Step 1: Write the failing max-iteration Retry test**

Update `TestServerMutationTargetRetryFeatureDispatchesFailedPhase` so its stored feature includes:

```go
ff.FailureType = feature.FailureMaxIterations
ff.MaxIterations = 10
ff.MaxPlanIterations = 3
```

After Retry, assert:

```go
if updated.MaxIterations != 20 {
	t.Fatalf("MaxIterations = %d, want 20", updated.MaxIterations)
}
if updated.MaxPlanIterations != 3 {
	t.Fatalf("MaxPlanIterations = %d, want unchanged 3 outside Plan", updated.MaxPlanIterations)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./cmd/agentico -run TestServerMutationTargetRetryFeatureDispatchesFailedPhase -count=1
```

Expected: FAIL because `RetryFeature` currently passes `0, 0`, leaving `MaxIterations` at 10.

- [ ] **Step 3: Add coverage for Plan and non-maximum failures**

Add table-driven or focused mutation-target tests that persist:

```go
{
	CurrentPhase:      feature.PhasePlan,
	FailureType:       feature.FailureMaxIterations,
	MaxIterations:     10,
	MaxPlanIterations: 3,
}
```

and expect `20, 5`; then persist a non-maximum failure with `10, 3` and expect both values unchanged.

- [ ] **Step 4: Implement the minimal server classification**

Define package constants beside the existing mutation constants:

```go
maxIterationsRetryDelta     = 10
maxPlanIterationsRetryDelta = 2
```

In `RetryFeature`, load the stored feature once. Preserve the setup-retry branch, then choose deltas:

```go
maxIterationsDelta, maxPlanIterationsDelta := 0, 0
if f != nil && f.FailureType == feature.FailureMaxIterations {
	maxIterationsDelta = maxIterationsRetryDelta
	maxPlanIterationsDelta = maxPlanIterationsRetryDelta
}
outcome, err := t.orch.RestartPhase(featureID, maxIterationsDelta, maxPlanIterationsDelta)
```

If the optional store load fails, preserve existing retry behavior by using zero deltas and allowing `RestartPhase` to return its authoritative load error.

- [ ] **Step 5: Run focused Go tests and verify GREEN**

Run:

```bash
go test ./cmd/agentico -run 'TestServerMutationTargetRetryFeature' -count=1
```

Expected: PASS.

---

### Task 2: Typed Electron Restart request body

**Files:**
- Modify: `desktop/src/shared/ipc.ts`
- Modify: `desktop/src/shared/ipc.test.ts`
- Modify: `desktop/src/main/__tests__/featureService.test.ts`

**Interfaces:**
- Produces: restart request branch `{ featureId, action: 'restart', body?: { max_iterations_delta: number; max_plan_iterations_delta: number } }`
- Consumes: `FeatureService.runOperationalAction`, which already forwards validated `input.body`

- [ ] **Step 1: Write failing IPC and feature-service tests**

Add an IPC schema test accepting:

```ts
{
  featureId: 'abcd1234ef567890',
  action: 'restart',
  body: {
    max_iterations_delta: 10,
    max_plan_iterations_delta: 2,
  },
}
```

Add a feature-service test that dispatches that request and expects:

```ts
expect(calls[0]?.init).toStrictEqual({
  method: 'POST',
  body: {
    max_iterations_delta: 10,
    max_plan_iterations_delta: 2,
  },
});
```

- [ ] **Step 2: Run focused Electron tests and verify RED**

Run:

```bash
npm test -- --run desktop/src/shared/ipc.test.ts desktop/src/main/__tests__/featureService.test.ts
```

Expected: FAIL because the current strict lifecycle branch rejects a restart body.

- [ ] **Step 3: Implement the restart request branch**

Split `restart` out of the bodyless lifecycle enum and add:

```ts
z.strictObject({
  featureId: FeatureIdSchema,
  action: z.literal('restart'),
  body: z
    .strictObject({
      max_iterations_delta: z.number().int().nonnegative(),
      max_plan_iterations_delta: z.number().int().nonnegative(),
    })
    .optional(),
}),
```

Keep bodyless `start`, `pause-stop`, `rewind`, `resume`, and `retry` unchanged.

- [ ] **Step 4: Run focused Electron tests and verify GREEN**

Run:

```bash
npm test -- --run desktop/src/shared/ipc.test.ts desktop/src/main/__tests__/featureService.test.ts
```

Expected: PASS.

---

### Task 3: Cockpit Restart sends deltas only for max-iteration failures

**Files:**
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.test.tsx`
- Modify: `desktop/src/renderer/src/features/FeatureCockpit.tsx`

**Interfaces:**
- Consumes: the typed restart request from Task 2
- Produces: confirmed max-iteration Restart dispatch with `10`/`2`; ordinary Restart remains bodyless

- [ ] **Step 1: Write failing cockpit behavior tests**

Render a failed snapshot with:

```ts
failure: { type: 'max_iterations', message: 'reached maximum iteration count' },
actions: [{ id: 'restart', enabled: true, disabledReasons: [] }],
```

Open More actions, choose Restart, confirm, and expect:

```ts
expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
  featureId: FEATURE_ID,
  action: 'restart',
  body: {
    max_iterations_delta: 10,
    max_plan_iterations_delta: 2,
  },
});
```

Add an ordinary interrupted snapshot and assert confirmed Restart sends:

```ts
{ featureId: FEATURE_ID, action: 'restart' }
```

- [ ] **Step 2: Run the focused cockpit tests and verify RED**

Run:

```bash
npm test -- --run desktop/src/renderer/src/features/FeatureCockpit.test.tsx
```

Expected: FAIL because `confirmRestart` currently dispatches every restart without a body.

- [ ] **Step 3: Implement conditional Restart body**

Change the shared lifecycle dispatcher to accept a concrete `FeatureActionRequest`, or give Restart its own dispatch path, so `confirmRestart` sends:

```ts
snapshot.failure?.type === 'max_iterations'
  ? {
      featureId,
      action: 'restart',
      body: {
        max_iterations_delta: 10,
        max_plan_iterations_delta: 2,
      },
    }
  : { featureId, action: 'restart' }
```

Update the confirmation note condition to require `failure.type === 'max_iterations'`; do not claim budget extension for unrelated failures.

- [ ] **Step 4: Run the focused cockpit tests and verify GREEN**

Run:

```bash
npm test -- --run desktop/src/renderer/src/features/FeatureCockpit.test.tsx
```

Expected: PASS.

---

### Task 4: Full verification and handoff

**Files:**
- Review all modified files

**Interfaces:**
- Consumes: completed Tasks 1–3
- Produces: verified implementation and exact tier results for handoff

- [ ] **Step 1: Format and inspect**

Run:

```bash
gofmt -w cmd/agentico/main.go cmd/agentico/server_mutation_target_test.go
npm run format:check --workspace desktop
git diff --check
```

- [ ] **Step 2: Run the Fast suite**

Run:

```bash
make test-fast
```

Expected: PASS within the repository's normal fast-suite envelope.

- [ ] **Step 3: Run static analysis and build**

Run:

```bash
go vet ./...
go build ./...
```

Expected: PASS.

- [ ] **Step 4: Run lifecycle extended gates**

Run:

```bash
go test ./test/integration/... -count=1
go test ./test/e2e/... -count=1 -race
```

Expected: PASS.

- [ ] **Step 5: Review the final diff and record verification**

Run:

```bash
git status --short
git diff --check
git diff --stat
```

Confirm only the design, plan, Retry server logic/tests, and Electron restart schema/UI/tests changed.
