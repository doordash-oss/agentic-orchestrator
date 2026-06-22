# Feature-Level Rebase UX Design

Date: 2026-06-22

## Purpose

Rebase should feel like a feature-level sync operation, not a hidden HTTP
mutation and not a per-repo agent cycle. The user should understand what is
happening while rebase is active, see mixed repo outcomes when they require
attention, and return to the normal feature lifecycle when the operation
settles.

The primary UX problem this solves is the current no-op case: the user presses
`[b] Rebase`, sees `Started Rebase`, and then gets no confirmation or durable
context explaining whether the branch was already up to date, changed locally,
failed, or moved into a conflict-resolution flow.

## Product Principles

- `[b] Rebase` is a feature-level action over all repos in the feature.
- While rebase work is active, the feature is `Rebasing`.
- Rebase is not permanently shown after success. Once settled, the feature
  returns to its normal lifecycle state, usually `CodeReady`.
- Successful per-repo rebase outcomes are transient progress, not persistent
  history.
- Failures and conflicts persist because they require user attention.
- Repo freshness is a general concept, not a rebase-specific one. The UI should
  show whether local code is in sync with remote whenever that applies,
  regardless of whether divergence came from rebase, tweak, refactor,
  review-comment work, Final Review fixes, or another workflow.

## Rebase Flow

### Start

When the user presses `[b] Rebase`, Agentic starts a single feature-level rebase
operation across all feature repos.

The feature row and detail status should show `Rebasing`. This is true even
when only one repo is currently being checked, because the operation belongs to
the feature and repos can interoperate.

### Harness Pass

The harness rebase pass runs before any smart agent starts. It checks every
feature repo and records transient per-repo progress such as:

- `checking`
- `rebasing`
- `up to date`
- `changed`
- `conflict`
- `failed`

The UI may show per-repo progress while the harness pass is running, but the
feature remains the unit of work.

No smart rebase agent starts until every repo has finished the harness pass.

### No-Op Result

If every repo is already up to date and no code changes:

- The operation ends.
- The feature returns to `CodeReady`.
- There is no persistent `Rebase complete` banner.
- Repo freshness shows the normal state, for example `in sync` or `local only`.

User takeaway: nothing needs attention.

### Clean Rebase With Code Changes

If one or more repos changed cleanly during rebase:

- Agentic runs Final Review with full feature context before any push.
- During Final Review, Agentic uses the existing Final Review UX.
- If Final Review approves, the feature returns to its normal lifecycle state.

For manual-publish features:

- The feature returns to `CodeReady`.
- Changed publishable repos show the general repo freshness indicator
  `local changes`.
- The normal `[p] publish` action remains available.
- There is no permanent `Rebase complete` banner.

For auto-publish features:

- After Final Review approves, Agentic pushes the changed publishable repos.
- Local or unpublishable repos are never pushed.
- The feature returns to the normal post-publish lifecycle state.

### Conflict Result

If one or more repos conflict and no repo has a non-conflict harness failure:

- Agentic waits for the full harness pass to finish.
- The feature remains `Rebasing`.
- Agentic starts one coordinated smart rebase session.

The smart rebase session is feature-level:

- One lead agent owns the repair.
- The agent may use sub-agents for help.
- The agent sees all feature repos and full feature context.
- The agent primarily resolves conflicted repos.
- The agent may edit other feature repos when cross-repo validation proves the
  edit is necessary.

After the smart rebase completes, Agentic runs Final Review with full feature
context. If Final Review approves, the same manual-publish or auto-publish
policy applies.

User takeaway: Agentic is resolving the feature-level rebase, not just one repo.

### Harness Failure Result

If any repo has a non-conflict harness failure:

- Agentic does not start smart rebase.
- Agentic does not run Final Review.
- The feature returns to its normal lifecycle state, usually `CodeReady`.
- The failed repo persists an actionable error in `Repo Status`.
- Successful and up-to-date repo progress clears.

User takeaway: fix or retry the failed repo issue before trusting the rebase.

## Visual Surfaces

### Feature List

While harness rebase, smart rebase, or rebase-triggered Final Review is active,
the feature row should present the feature as `Rebasing` or the existing
Final Review state, as appropriate. The active rebase state should not be
repo-specific.

After the operation settles, the feature row returns to the normal lifecycle
status, for example `Code Ready`.

### Detail Info Box

During active rebase work:

- `Status` should read `Rebasing`.

After active work settles:

- `Status` should return to the normal lifecycle status.
- The `Repos` line should show general repo freshness where applicable:
  - `in sync`
  - `local changes`
  - `local only`
  - `unknown`

The `Repos` line should not show rebase history such as `rebased` or
`up to date` after the operation is complete.

### Repo Status Block

During the harness pass, the block can show transient per-repo progress:

- `api checking`
- `web rebasing`
- `worker up to date`
- `mobile conflict: 3 files`

During smart rebase, the feature remains `Rebasing`. Repo rows may provide
context such as:

- `api conflict`
- `web validation context`
- `worker local only`

After success, successful transient rows clear.

After failure or conflict, the relevant repo rows persist actionable state:

- `docs failed: fetch failed`
- `api conflict: 3 files`

### Final Review

Rebase should use the existing Final Review surface. It should not introduce a
separate review UI.

## Repo Freshness

Repo freshness is a general indicator of whether local code appears synchronized
with remote. The approved first-level vocabulary is intentionally coarse:

- `in sync`
- `local changes`
- `local only`
- `unknown`

This signal should apply wherever a workflow can leave local code different
from remote. It is not tied to rebase history.

## Push Policy

Rebase should not push before Final Review approves code changes.

After Final Review approves:

- Manual publish: keep the feature `CodeReady`; the user presses `[p]`.
- Auto publish: push changed publishable repos.
- Local or unpublishable repos are never pushed.

## Non-Goals

- Do not add a permanent `Rebase complete` banner.
- Do not make successful no-op or clean rebase outcomes permanent repo history.
- Do not start one independent smart agent per conflicted repo.
- Do not force-push clean rebases before Final Review approves.
- Do not replace the existing Final Review UX.
