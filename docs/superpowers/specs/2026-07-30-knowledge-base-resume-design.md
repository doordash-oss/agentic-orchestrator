# Knowledge Base Resume Design

## Goal

Make every repository-scoped Knowledge Base session resume its provider-native
conversation after a manual interruption or process crash. A resumed feature
must continue incomplete KB work instead of launching the full KB prompt again.

The behavior applies independently to every repository in a multi-repository
feature. Repositories without an eligible saved session continue through the
existing fresh-start path.

## Current Failure

Knowledge Base sessions use a dedicated launch path in
`PhaseRunner.RunKnowledgeBaseForRepo`. Unlike the other single-shot phases, that
path does not:

- initialize a durable resume record;
- capture the provider session ID;
- register launch metadata with the single-shot resume driver;
- pass `ResumeSessionID` when relaunched; or
- emit a `feature.resumed` audit event.

There is a second routing problem in `Orchestrator.ResumeFeature`: an
`Interrupted` feature goes directly to `StartFeature`, while resume eligibility
and durable pending intent are evaluated only for `Failed` features.

## Chosen Approach

Treat each feature/repository KB build as a first-class single-shot resumable
unit while retaining the existing KB-specific prompt, artifact directory,
freshness checks, and lock behavior.

This is preferred over moving KB generation wholesale onto the generic
interactive phase launcher. The smaller integration reuses the established
resume coordinator and supervisor without changing unrelated KB behavior.

## Durable Identity

Each KB repository gets a feature-owned resume directory:

```text
features/<feature-id>/runs/run-<n>/knowledgebase/<repo-name>/resume.yaml
```

The resume record remains outside the shared repository KB artifact directory.
This prevents another feature building the same repository with the same model
and run number from claiming the wrong provider session.

The record uses:

- phase key `knowledgebase`;
- child key equal to the repository name;
- iteration `0`;
- the feature's active run number;
- provider and resolved-model identity; and
- the provider-native session ID captured from initialization.

The shared artifacts remain under:

```text
<state-parent>/knowledge-base/<repo-name>/
```

The single-shot launch metadata therefore distinguishes `resumeDir` from
`artifactDir`. Existing phases default `resumeDir` to their artifact directory,
so their behavior does not change.

## Fresh KB Launch

`RunKnowledgeBaseForRepo` will continue to perform freshness and lock checks
before launching. After building the fresh session options it will:

1. initialize the repository's resume coordinator;
2. install provider-init capture;
3. register the session with the existing single-shot resume driver;
4. start the session with the normal full or incremental KB prompt; and
5. retain repository identity in resumed observability and cost records.

The launch registration stores enough data to recreate the same KB session:
fresh build options, repository work directory, artifact directory, resume
directory, repository name, and a continuation preparation hook.

## Manual Resume

When `ResumeFeature` receives an `Interrupted` feature in the Knowledge Base
phase:

1. inspect the resume record for each incomplete repository;
2. claim every strict-match eligible record using child key = repository name;
3. stamp `pending_resume` durably before dispatch;
4. call the existing feature start path once; and
5. release all in-memory claims after dispatch has accepted the work.

If start fails, every acquired claim is released and its pending flag is
cleared. If no repository is eligible, the historical fresh-start behavior is
preserved.

During `RunKnowledgeBaseForRepo`, a pending record changes the launch to:

- `ResumeSessionID = record.ProviderSessionID`;
- the concise continuation prompt, naming `index.md` as the KB contract; and
- a distinct orchestrator session ID with a resume ordinal.

Other incomplete repositories without eligible records start fresh in parallel.

## Automatic Crash Resume

Registering KB sessions with the existing single-shot supervisor enables the
bounded automatic-resume loop already used by inquire, research, design, and
planning. Transient process failure resumes the saved provider session before
the feature is marked failed.

The established continuation updates `resume_count`, emits
`feature.resumed`, and preserves the provider-native session identity.

## KB Lock Lifecycle

Each process releases its KB lock through the existing session cleanup.
Before a replacement continuation starts, the stored continuation hook
reacquires the repository lock for the same feature. The lock is reentrant for
the owning feature, so this also handles cleanup/dispatch ordering safely.

If another feature owns the lock, continuation launch fails without starting a
second writer. The existing completion and waiter logic remains authoritative
for terminal cleanup and wake-up.

## Rejection and Fresh Fallback

If OpenCode rejects `session/load` or the resumed process fails the established
session-rejection heuristic:

1. mark the resume record rejected;
2. reacquire the KB lock;
3. archive the previous KB transcript;
4. launch one fresh KB process using the original prompt and system prompt; and
5. record the fresh-fallback reason and count.

The fallback keeps the same KB artifact directory so the fresh agent can
reconcile partial documents.

## Completion

Successful KB completion keeps the existing contract validation, read-only
repository enforcement, state freshness update, per-repository status update,
and advancement logic.

Before completion is dispatched, the single-shot supervisor marks that
repository's resume record completed. A completed record is never eligible for
later resume.

## Observability

Resumed KB sessions retain the repository name in:

- `session.started` and `session.ended`;
- session cost metadata;
- the PID file; and
- task progress events.

After native continuation is established, `feature.resumed` includes:

- phase key `knowledgebase`;
- child key equal to the repository name;
- active run number; and
- incremented resume count.

## Testing

Tests will be written before production changes and must fail against the
current branch.

### Phase Runner Regression

A KB phase-runner test will:

1. launch a fresh KB session with a resume-capable fake provider;
2. capture a provider session ID in the durable per-repo sidecar;
3. mark the record pending;
4. invoke `RunKnowledgeBaseForRepo` again; and
5. assert the second real build options contain the prior
   `ResumeSessionID`, the KB continuation prompt, and repository identity.

The test catches removal of KB resume initialization, provider-ID capture, or
resume-option injection.

### Manual Multi-Repository Regression

An orchestrator test will create an interrupted KB feature with two repositories
and two eligible sidecars. It will invoke `ResumeFeature` and assert that:

- both records are pending when dispatch begins;
- the feature is dispatched once;
- both repositories remain independently resumable; and
- a second concurrent resume is rejected.

The test catches routing `Interrupted` directly to an unclaimed fresh start.

### Lock Regression

A focused continuation test will verify that a KB replacement process
reacquires the repository lock after the previous process cleanup released it,
and refuses to launch if another feature owns the lock.

### Existing Coverage

Existing single-shot resume, KB integration, completion, server contract, and
TUI tests must continue to pass. The required handoff verification is:

- Fast suite: `make test-fast`
- Isolated integration:
  `go test ./test/integration/... -count=1`
- Static analysis: `go vet ./...`
- Build: `go build ./...`

The isolated integration tier is required because this change affects session
lifecycle and persisted resume state.
