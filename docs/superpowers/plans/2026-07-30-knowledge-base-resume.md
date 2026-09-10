# Knowledge Base Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume each interrupted Knowledge Base repository through its saved provider-native session instead of restarting the complete KB prompt.

**Architecture:** Store resume identity in a feature/run-owned per-repository directory while keeping KB artifacts in the shared repository KB directory. Extend the existing single-shot continuation driver with distinct resume-directory and repository-lock lifecycle metadata, then have `ResumeFeature` claim all eligible KB repository records before restarting an interrupted feature.

**Tech Stack:** Go 1.25, existing `ResumeCoordinator`, session manager, OpenCode ACP resume support, standard `testing` package.

## Global Constraints

- Preserve the existing shared KB artifact layout and KB freshness behavior.
- Preserve per-repository parallelism for multi-repository features.
- Never let two features write the same KB directory concurrently.
- Fall back to a fresh KB session when a provider rejects native continuation.
- Every new `*.go` file must carry the repository's 2026 Apache 2.0 header; this plan creates no new Go files.
- Use TDD: each production behavior is preceded by a regression test observed failing for the missing behavior.

---

### Task 1: Feature-Owned KB Resume Identity

**Files:**
- Modify: `internal/agent/knowledgebase.go`
- Modify: `internal/agent/knowledgebase_test.go`
- Modify: `internal/agent/resume.go`
- Modify: `internal/agent/resume_test.go`

**Interfaces:**
- Produces: `KBResumeDir(stateDir string, f *feature.Feature, repoName string) string`
- Produces: Knowledge Base handling in `ResumePhaseKey(*feature.Feature) string`
- Consumes: `ActiveRunDir(stateDir string, f *feature.Feature) string`

- [ ] **Step 1: Write the failing path and phase-key tests**

Add tests with hand-derived expectations:

```go
func TestKBResumeDirUsesActiveRunAndRepository(t *testing.T) {
    f := &feature.Feature{ID: "feat-kb", ActiveRun: 2, RunCount: 2}
    got := KBResumeDir("/state/features", f, "payments")
    want := filepath.Join("/state/features", "feat-kb", "runs", "run-002", "knowledgebase", "payments")
    if got != want {
        t.Fatalf("KBResumeDir() = %q, want %q", got, want)
    }
}

func TestResumePhaseKeyKnowledgeBase(t *testing.T) {
    got := ResumePhaseKey(&feature.Feature{CurrentPhase: feature.PhaseKnowledgeBase})
    if got != "knowledgebase" {
        t.Fatalf("ResumePhaseKey(KB) = %q, want knowledgebase", got)
    }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./internal/agent -run 'TestKBResumeDirUsesActiveRunAndRepository|TestResumePhaseKeyKnowledgeBase' -count=1
```

Expected: compile failure because `KBResumeDir` is undefined and/or phase-key assertion failure because KB returns an empty key.

- [ ] **Step 3: Implement the path and phase key**

Add:

```go
func KBResumeDir(stateDir string, f *feature.Feature, repoName string) string {
    return filepath.Join(ActiveRunDir(stateDir, f), feature.PhaseKnowledgeBase.DirName(), repoName)
}
```

Add `feature.PhaseKnowledgeBase` to `ResumePhaseKey`, returning
`feature.PhaseKnowledgeBase.DirName()`.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run the command from Step 2 and require exit code 0.

### Task 2: KB Phase Runner Native Continuation

**Files:**
- Modify: `internal/agent/phase.go`
- Modify: `internal/agent/phase_test.go`

**Interfaces:**
- Extends: `singleShotResumeLaunch` with `resumeDir`, `repoName`, and `acquireContinuation`
- Produces: `func (l *singleShotResumeLaunch) coordinator() *ResumeCoordinator`
- Consumes: `KBResumeDir`, `NewChildResumeCoordinator`, `BuildSessionOpts.ResumeSessionID`

- [ ] **Step 1: Write a failing KB continuation test**

Create a test using the real `PhaseRunner` and test session manager. Capture
`BuildSessionOpts` for two launches. The first launch must initialize the
sidecar; the test then captures provider identity and sets pending intent through
the real coordinator. After stopping the first session and releasing its lock,
the second call to `RunKnowledgeBaseForRepo` must assert:

```go
if got := builds[1].ResumeSessionID; got != "ses-kb-prior" {
    t.Fatalf("second ResumeSessionID = %q, want ses-kb-prior", got)
}
if !strings.Contains(builds[1].Prompt, "index.md") {
    t.Fatalf("resume prompt does not identify KB contract: %q", builds[1].Prompt)
}
if got := builds[1].RepoName; got != "repo-a" {
    t.Fatalf("resume RepoName = %q, want repo-a", got)
}
```

Also assert the durable record lives in `KBResumeDir`, not `KBStateDir`.

- [ ] **Step 2: Run the continuation test and verify RED**

Run:

```bash
go test ./internal/agent -run TestRunKnowledgeBaseForRepo_ResumesPendingProviderSession -count=1
```

Expected: failure because no KB `resume.yaml` is created and the second build has an empty `ResumeSessionID`.

- [ ] **Step 3: Extend single-shot launch metadata**

Change the launch type to:

```go
type singleShotResumeLaunch struct {
    feature             *feature.Feature
    phase               feature.Phase
    artifactDir         string
    resumeDir           string
    repoName            string
    baseID              string
    buildOpts           BuildSessionOpts
    workDir             string
    supportsResume      bool
    acquireContinuation func() (func(), error)
}
```

Add a helper that defaults `resumeDir` to `artifactDir` and creates a child
coordinator for KB:

```go
func (l *singleShotResumeLaunch) coordinator() *ResumeCoordinator {
    dir := l.resumeDir
    if dir == "" {
        dir = l.artifactDir
    }
    if l.phase == feature.PhaseKnowledgeBase {
        return NewChildResumeCoordinator(dir, l.repoName, ResumeParentContext{
            PhaseKey: feature.PhaseKnowledgeBase.DirName(),
        })
    }
    return NewResumeCoordinator(dir)
}
```

Replace direct `NewResumeCoordinator(launch.artifactDir)` calls in the
single-shot continuation methods with `launch.coordinator()`.

- [ ] **Step 4: Add continuation lock acquisition**

In `DispatchSingleShotContinuation`, call `acquireContinuation` after session
options are built and before `StartSession`. On start failure invoke the returned
release function immediately; on success register it with
`sess.AddCleanupFunc`.

For KB launches provide:

```go
acquireContinuation: func() (func(), error) {
    locked, err := AcquireKBLock(kbDir, f.ID)
    if err != nil {
        return nil, err
    }
    if !locked {
        return nil, ErrKBLocked
    }
    return func() { _ = ReleaseKBLock(kbDir, f.ID) }, nil
},
```

- [ ] **Step 5: Integrate resume setup into `RunKnowledgeBaseForRepo`**

Build `freshBuildOpts` first. Create the child coordinator in
`KBResumeDir(pr.StateDir, f, repo.Name)`, inspect `PendingResume`, and when
pending set:

```go
buildOpts.ResumeSessionID = pending.ProviderSessionID
buildOpts.Prompt = resumeCoordinator.Prompt(singleShotResumeContext(feature.PhaseKnowledgeBase))
sessionID = fmt.Sprintf("%s-resume-%02d", baseSessionID, pending.ResumeCount+1)
```

Initialize a fresh resume record only for non-resume launches. Install provider
init capture, register `singleShotResumeLaunch`, retain `repo.Name` in observer
and cost metadata, and archive the KB output/stderr logs before a resume.

Update `singleShotResumeContext` so KB names `index.md` as its artifact contract.

- [ ] **Step 6: Run the continuation test and existing KB tests**

Run:

```bash
go test ./internal/agent -run 'TestRunKnowledgeBaseForRepo_ResumesPendingProviderSession|TestRunKnowledgeBaseForRepo|TestResume' -count=1
```

Expected: PASS.

### Task 3: Manual Multi-Repository Resume Claims

**Files:**
- Modify: `internal/orchestrator/manual_resume.go`
- Modify: `internal/orchestrator/manual_resume_internal_test.go`

**Interfaces:**
- Produces: `claimInterruptedKBResumes(f *feature.Feature) ([]*agent.ResumeClaim, error)`
- Consumes: `agent.KBResumeDir`, `agent.NewChildResumeCoordinator`, `ResumeClaim.Release`, `ResumeClaim.DispatchStarted`

- [ ] **Step 1: Write the failing multi-repository manual-resume test**

Create an interrupted feature at `PhaseKnowledgeBase` with two repositories and
eligible per-repository resume records. Use a real feature store and registry,
while replacing only the phase dispatch boundary. Inside the dispatch callback,
read both sidecars and assert both have `PendingResume=true`. After
`ResumeFeature`, assert exactly one feature dispatch occurred.

The records use literal identities:

```go
agent.ResumeRecord{
    ProviderSessionID: "thread-" + repoName,
    Provider: "codex",
    ResolvedModel: "model-a",
    PhaseKey: "knowledgebase",
    ChildKey: repoName,
    RunNumber: 1,
    OrchestratorSessionID: "manual-kb-kb-" + repoName,
}
```

- [ ] **Step 2: Run the test and verify RED**

Run:

```bash
go test ./internal/orchestrator -run TestResumeFeatureClaimsInterruptedKnowledgeBaseRepositories -count=1
```

Expected: failure because `Interrupted` routes directly to `StartFeature` and neither sidecar is pending.

- [ ] **Step 3: Implement all-or-nothing KB claims**

Add:

```go
func (o *Orchestrator) claimInterruptedKBResumes(f *feature.Feature) ([]*agent.ResumeClaim, error)
```

For each repository, construct a child coordinator with parent phase key
`knowledgebase`, evaluate/claim using the configured KB model, and collect only
eligible claims. If any claim operation errors, release every prior claim and
return the joined error.

In the interrupted branch of `ResumeFeature`, use this helper only for KB:

```go
claims, err := o.claimInterruptedKBResumes(f)
if err != nil {
    return err
}
err = o.StartFeature(featureID)
if err != nil {
    return errors.Join(err, releaseResumeClaims(claims, time.Now()))
}
for _, claim := range claims {
    claim.DispatchStarted()
}
return nil
```

When no record is eligible, `claims` is empty and the same start call preserves
fresh behavior.

Extend `resumeModelForFeature` with KB build model resolution using
`llm.PhaseKBBuild`.

- [ ] **Step 4: Run the focused test and manual-resume suite**

Run:

```bash
go test ./internal/orchestrator -run 'TestResumeFeature|KnowledgeBase' -count=1
```

Expected: PASS.

### Task 4: Automatic Resume, Locking, and End-to-End Regression

**Files:**
- Modify: `internal/orchestrator/phase_supervisor_test.go`
- Modify: `test/integration/recovery_resume_restart_test.go`

**Interfaces:**
- Consumes: existing `phaseSupervisor`, `SingleShotResumeDriver`, KB runner, and OpenCode-compatible fake session lifecycle
- Produces: regression coverage for KB automatic resume and manual restart

- [ ] **Step 1: Add a failing automatic-resume repository identity test**

Extend the phase-supervisor fixture with a KB launch carrying `repoName`. Drive
a transient failure followed by a successful continuation and assert:

```go
if got.PhaseKey != "knowledgebase" || got.ChildKey != "repo-a" || got.ResumeCount != 1 {
    t.Fatalf("FeatureResumed = %+v, want KB repo-a resume count 1", got)
}
```

- [ ] **Step 2: Run the focused supervisor test**

Run:

```bash
go test ./internal/orchestrator -run 'TestPhaseSupervisor.*KnowledgeBase' -count=1
```

Expected before any missing glue is added: FAIL on absent child/repository identity. After Tasks 1-3: PASS, or expose the remaining minimal wiring defect to fix.

- [ ] **Step 3: Add the integration reproduction**

Add an isolated integration case that starts a KB provider session, interrupts
the feature, calls the resume API, and verifies the replacement launch receives
the original provider session ID rather than the full fresh prompt. Keep all
state under `t.TempDir()` and do not use `t.Parallel()` because the fixture owns
session processes and feature state.

- [ ] **Step 4: Run targeted integration**

Run:

```bash
go test ./test/integration/... -run 'KnowledgeBase.*Resume|Resume.*KnowledgeBase' -count=1
```

Expected: PASS.

### Task 5: Required Verification

**Files:**
- Verify all modified Go files and committed design/plan documents.

**Interfaces:**
- Consumes: repository verification tiers from `AGENTS.md`
- Produces: fresh handoff evidence

- [ ] **Step 1: Format and inspect**

Run:

```bash
gofmt -w internal/agent/knowledgebase.go internal/agent/knowledgebase_test.go internal/agent/resume.go internal/agent/resume_test.go internal/agent/phase.go internal/agent/phase_test.go internal/orchestrator/manual_resume.go internal/orchestrator/manual_resume_internal_test.go internal/orchestrator/phase_supervisor_test.go test/integration/recovery_resume_restart_test.go
git diff --check
```

- [ ] **Step 2: Run Fast suite**

Run:

```bash
make test-fast
```

Expected: exit code 0.

- [ ] **Step 3: Run Isolated integration**

Run:

```bash
go test ./test/integration/... -count=1
```

Expected: exit code 0.

- [ ] **Step 4: Run static analysis and build**

Run:

```bash
go vet ./...
go build ./...
```

Expected: both commands exit 0.

- [ ] **Step 5: Review final diff**

Run:

```bash
git status --short
git diff --stat HEAD
git diff --check
```

Confirm only the approved KB resume implementation, its tests, and the plan are present.
