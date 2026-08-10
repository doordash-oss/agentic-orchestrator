# Publish Flow Design

## Context and goals

The desktop Publish flow currently combines three independent problems:

1. The modal has broken visual hierarchy. Several shared completion CSS selector
   lists omit commas, so intended publish layout rules never apply. The existing
   repository row then relies on implicit grid placement, leaving its rewrite
   warning and pull-request link stranded on a second row.
2. An existing-PR update may omit its title and body in the renderer, but the
   desktop IPC schema requires a title for every publish request. The request is
   rejected before it reaches the server and is mislabeled as a desktop/server
   version mismatch.
3. A rewritten pull-request branch can be rejected even when its only
   remote-only commit is a redundant GitHub merge whose parents and content are
   already represented by the rewritten local history. The generic
   `--force-if-includes` guard sees that the merge commit itself was never in the
   local branch reflog and correctly refuses to guess, but Agentico has no
   higher-level classification or safe recovery path.

The goal is a publish task that feels native to the macOS desktop application,
asks only for information that will be used, presents failures in the context
where they can be resolved, and safely handles redundant remote merge topology
without weakening protection for genuine remote work.

## Chosen direction

Use a dedicated Publish task sheet built on the existing `.sheet` family. Keep
the generic centered `CockpitModal` unchanged for other completion actions.
This scopes the visual change to Publish while reusing the app's established
top-attached macOS presentation, focus behavior, reduced-motion handling, and
pinned action footer.

Alternatives considered:

- Fix only the missing CSS commas and IPC schema. This is the smallest diff but
  retains misleading fields, implicit repository layout, raw errors, and the
  generic web-modal presentation.
- Redesign every completion modal around the sheet family. That may ultimately
  improve consistency, but it expands the request into unrelated Merge,
  Cleanup, and Mark done journeys.
- Use the dedicated Publish sheet described here. It fixes the complete user
  journey without changing unrelated surfaces and is therefore the selected
  option.

## Visual system

### Subject and job

The subject is a release handoff for engineers who have already reviewed a
feature. The sheet's single job is to let them confirm which repositories will
be delivered and then publish them safely.

### Tokens

Use existing application tokens so the sheet follows system appearance and
active/inactive window states:

- Content: `--content` (`#ffffff` in the current light theme)
- Raised row: `--raised` (`#f6f6f8`)
- Primary ink: `--text` (`#1b1d21`)
- Secondary ink and hairlines: `--muted` and `--hairline`
- Action and focus: `--accent`
- Rewrite attention: `--attention`, used only for a narrow risk rail and icon

Typography remains the system-oriented `--bench-font-text` stack for headings,
controls, and copy. Repository or branch identifiers use
`--bench-font-mono` only when they are displayed as technical identifiers.
No new typeface, gradient, or decorative palette is introduced.

### Layout and signature

The sheet is 680 pixels wide (bounded by the existing responsive sheet inset),
top-attached to the application window, and height-bounded by the viewport. Its
body scrolls independently; the footer never scrolls away.

```text
┌──────────────────────────────────────────────────────────┐
│ Publish updates                                          │
│ Choose the repositories whose reviewed work is ready.    │
│                                                          │
│ REPOSITORIES                                             │
│ ▌ ☑ taulu                              54 commits   PR ↗  │
│ ▌   Rewrites the pull-request branch with a safety lease │
│                                                          │
│ PULL REQUEST DETAILS                 Generate narrative  │
│ Title                                      Required       │
│ [                                                        ]│
│ Description                                 Optional       │
│ [                                                        ]│
│                                                          │
│ [compact actionable status or error]                     │
├──────────────────────────────────────────────────────────┤
│ [Cancel]                              [Publish updates]   │
└──────────────────────────────────────────────────────────┘
```

The repository manifest is the signature element: checkbox, repository name,
pending-work summary, pull-request link, and rewrite risk are grouped into one
stable row rather than scattered across an implicit grid. A restrained amber
risk rail makes history-rewriting repositories recognizable without turning
the whole sheet into a warning.

The current standalone top-right Close button becomes Cancel in the footer.
The primary action is trailing, following the existing sheet family. Escape,
clicking the scrim, and Cancel dismiss the sheet when no publish is running.
The dialog keeps the accessible name `Publish reviewed changes`.

## Contextual form behavior

Selected repositories fall into two user-relevant groups:

- New pull requests: selected `eligible` repositories without a recorded pull
  request. They need a shared title and may use a shared description.
- Existing pull-request updates: selected `unpublished_changes` repositories
  with a recorded pull request. Publishing pushes commits only; title and body
  are not consumed by the server.

When every selected repository is an existing-PR update, omit the entire Pull
request details section. The lede explains that Agentico will update the
selected pull-request branches, and the action reads `Publish updates`.

When at least one selected repository needs a new pull request, show Pull
request details. Mark title as Required and description as Optional. Place the
Generate narrative utility in the section heading, not between the two fields.
The primary action reads `Publish`. It remains disabled until a nonblank title
exists, at least one repository is selected, the preflight revision is present,
and any dirty-work confirmation is satisfied.

Mixed selections use the title and body only for repositories that need new
pull requests. Copy says this explicitly; existing pull requests are updated
without changing their narrative.

Already-published and non-publishable repositories remain visible as quiet,
read-only groups so the operator can understand the complete preflight result.

## Request contract and validation

Make `FeatureActionRequestSchema` match the established renderer and server
contract:

- `source_revision` and at least one repository remain required.
- `title` becomes optional at IPC.
- `body` remains optional.
- Renderer validation continues to require a title when the selected set
  contains a new-PR repository.

Replace the Publish modal's generic `(action, Record<string, unknown>)` callback
with a callback that accepts the `publish` member extracted from
`FeatureActionRequest`. Build that request inside the modal and dispatch it
directly through `window.agentico.dispatchFeatureAction`; the publish path no
longer passes through the unsafe completion-action cast. A title-less
existing-PR update must travel through renderer, IPC, main process, and HTTP
without coercion. Other completion actions keep their existing callback in this
change.

This is not a server compatibility change. The Go request already models title
and body as optional, and the orchestrator ignores them when updating an
existing pull request.

## Error behavior

Publish errors render in a compact notice immediately above the pinned footer,
where they remain visible beside the action that produced them. The notice has
a concise headline, one next step, and an optional disclosure for sanitized
technical details. It does not prefix text with `failed:` or expose raw Git
command guidance as the primary message.

Expected messages include:

- Required title: inline field guidance, `Add a title to create the pull
  request.` This state normally prevents submission rather than producing an
  alert.
- Remote branch divergence: `The pull-request branch contains changes that
  aren't in this workspace.` The next step says to review and reconcile the
  remote changes before retrying.
- Remote movement after inspection: `The pull-request branch changed while
  Agentico was publishing.` The next step says to refresh and retry; Agentico
  never retries the force push automatically.
- Unexpected/schema failure: `Agentico couldn't prepare this publish.` The
  disclosure may include a sanitized diagnostic code and path. It must not
  claim a desktop/server version mismatch unless compatibility negotiation
  actually identified one.

Successful or reconciling status remains announced with `role="status"`;
actionable failure uses `role="alert"`. Focus moves to the first invalid field
for local validation and to the error notice for an asynchronous failure.

## Safe rewritten-branch publishing

### Invariant

Agentico may rewrite a remote pull-request branch automatically only after it
has inspected a specific remote tip and proved that overwriting that exact tip
does not discard unique work. Any uncertainty fails closed.

### Inspection

Before a rewrite push, fetch the destination branch into a unique ref beneath
`refs/agentico/publish-inspection/` rather than refreshing `origin/<branch>`.
Record the fetched SHA as the lease expectation and compare it with local
`HEAD`.

- If the inspected remote tip is an ancestor of `HEAD`, use the normal
  fast-forward push.
- Otherwise enumerate commits reachable from the inspected remote tip but not
  from `HEAD`.
- A remote-only commit is redundant only when it is a merge commit, every
  parent is already an ancestor of `HEAD`, and `git show --remerge-diff
  --format= --no-ext-diff <sha>` produces no diff. Every remote-only commit must
  satisfy this rule.
- Ordinary remote-only commits, merges with a parent absent from `HEAD`, and
  merges containing unique conflict resolution are genuine divergence.

The temporary ref is cleaned up with `defer`/cleanup regardless of outcome. No
user branch, working tree, or standard remote-tracking ref is modified during
inspection.

### Push

For a fully redundant remote-only set, push with an explicit expectation:

```text
--force-with-lease=refs/heads/<branch>:<inspected-sha>
```

Omit `--force-if-includes` only on this proven-redundant path because the
explicit SHA protects against the remote moving after inspection. If the
remote changes before the push, the lease rejects it and Agentico reports the
typed remote-movement error without retrying.

Keep the existing generic `--force-with-lease --force-if-includes` operation
for callers that have not performed this inspection. Do not replace it with a
plain force push, an unconditional fetch plus implicit lease, or a blind retry.

### Typed failures

The Git boundary returns structured outcomes for genuine divergence and
post-inspection remote movement. The orchestrator maps these to stable publish
error codes and sanitized fields such as repository, branch, and remote-only
commit count. Raw Git stderr is retained only as diagnostic detail.

The Taulu case is the positive regression fixture: its remote-only GitHub merge
has both parents in the rebased local history and no unique remerge delta, so
the explicit-lease rewrite succeeds. A remote ordinary commit or unique merge
resolution remains blocked.

## Components

- `desktop/src/renderer/src/features/completion/PublishModal.tsx`: contextual
  sections, explicit repository-row structure, local validation, inline publish
  status, and pinned footer actions.
- `desktop/src/renderer/src/features/FeatureCockpit.tsx`: present Publish with
  the dedicated sheet at both existing render sites while preserving focus,
  Escape, scrim dismissal, and the dialog accessible name.
- `desktop/src/renderer/src/styles/app.css`: repair malformed completion selector
  lists; add Publish-specific sheet, manifest-row, risk-rail, field, notice, and
  responsive styles derived from existing tokens.
- `desktop/src/shared/ipc.ts`: align the publish request schema with optional PR
  metadata and restore compile-time checking at the renderer boundary.
- `desktop/src/main/features.ts`: forward title-less publish updates unchanged.
- `internal/git`: isolated destination inspection, redundant-merge
  classification, explicit-lease push, and typed errors.
- `internal/orchestrator/publish.go` and `remote_ops.go`: use the inspected
  rewrite operation for existing-PR updates and translate typed outcomes.
- Packaged journeys under `desktop/test/e2e/journeys/`: update Close to Cancel
  and cover a title-less existing-PR update plus compact error behavior.

## Test-first implementation

Use separate red-green cycles so each root cause stays isolated:

1. Renderer layout and context
   - Assert pure updates omit title, body, and Generate narrative.
   - Assert new and mixed selections show required title and optional body.
   - Assert repository metadata and rewrite guidance occupy one semantic row.
   - Assert Cancel and the primary action live in the footer and retain keyboard
     behavior.
2. IPC contract
   - Add a shared-schema test accepting publish with only revision and repos.
   - Add a main-service test proving the title-less body reaches the HTTP
     transport unchanged.
   - Remove the unsafe cast and let TypeScript enforce the request builder.
3. Rewrite safety
   - Reproduce the Taulu graph using local repositories and confirm the current
     guarded push rejects it.
   - Make the redundant-merge classifier and explicit-lease push pass that
     fixture.
   - Retain existing unseen-work rejection tests.
   - Add ordinary remote commit, unique merge resolution, and remote-moves-after-
     inspection rejection tests.
4. Packaged journey
   - Seed an `unpublished_changes` repository with an existing pull request.
   - Open `Publish reviewed changes`, confirm PR narrative fields are absent,
     click `Publish updates`, and verify the branch update completes.
   - Update journey assertions from Close to Cancel and grep all journeys for
     affected copy and roles.

## Verification

Run the canonical gates that match the changed boundaries:

- Fast suite: `make test-fast`
- Desktop static checks: `npm run check`
- Desktop unit/component/security tests: `npm test && npm run test:security`
- Desktop packaged E2E: package once, then run the affected publish journeys
- E2E Go: `go test ./test/e2e/... -count=1 -race`
- Go static analysis: `go vet ./...`
- Go build: `go build ./...`

The full Race regression gate is optional unless implementation introduces
shared mutable state or concurrency. The PR verification note must name every
tier run and explain any relevant skipped tier.

## Non-goals

- Rewriting the other completion modals.
- Changing pull-request titles or descriptions during existing-PR updates.
- Automatically merging, rebasing, or discarding genuine remote-only work.
- Removing the generic `--force-if-includes` safety guard.
- Changing the rebase-child model to persist the pull-request head; that is a
  possible follow-up, not required for safe publish recovery.
