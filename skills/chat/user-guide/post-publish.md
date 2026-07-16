# Post-Publish Workflows

The Agentic Orchestrator runtime has workflows for continuing work after a feature reaches `CodeReady` or `Published`. The current Electron app does **not** expose controls for these workflows. This page explains their engine semantics for diagnosis and codebase exploration; it is not a set of desktop operating instructions.

Do not use retired terminal shortcuts to start these actions. Rebase, review-comments, refactor, merge, Done, and worktree cleanup remain pending in the desktop parity matrix.

## Rebase

Rebase updates a feature branch against its base branch. For a publishable repository, the runtime fetches remote refs, determines the target, rebases, and updates the remote branch. A local-only repository uses its local base branch and does not push.

If conflicts occur, the runtime can build a conflict plan and run an implementation session to resolve it before continuing. Multi-repository features track this cycle per repository.

**Electron status:** pending; no Rebase button or repository selector is currently available.

## Review Comments

The review-comments cycle fetches unresolved pull-request feedback through `gh`, filters comments already handled in prior iterations, creates a resolution plan, runs an implementation session, updates the branch, replies with outcomes, and attempts to resolve inline threads.

If a required worktree was previously removed, the runtime can recreate it from durable feature state before the cycle.

**Electron status:** pending; no review-comments inbox or action is currently available.

## Refactor

A refactor cycle accepts a new objective and sends an existing feature back through an appropriate planning and implementation path:

- Medium starts from planning.
- Large and Moonshot include the earlier inquiry, research, and design work.

The runtime retains the feature’s repository identity and publish state while tracking the refactor cycle per repository.

**Electron status:** pending; there is no refactor prompt, pipeline selector, or submit control in the current app.

## Merge

For a non-publishable local repository, merge can commit remaining work, merge the feature branch into its local base branch, and mark the feature complete.

**Electron status:** pending; no Merge control is currently available.

## Done

Marking a feature Done transitions its durable status and writes summary data such as timing, cost, and per-repository state after eligible work is complete.

**Electron status:** pending; the current app can display a feature already reported as Done but cannot initiate the transition.

## Clean Worktree

Worktree cleanup removes an isolated feature worktree after it is no longer needed. Durable feature state remains, and a later engine workflow can recreate the worktree when supported.

**Electron status:** pending; cleanup is manual outside the app in the current release. Confirm that no active session uses the worktree before changing it.

## What the Current App Can Do

For a feature that has reached a terminal or post-implementation state, the Electron app can show its authoritative row on Home and open its feature tab. Current-run transcript content remains available when the runtime exposes it. Do not expect post-publish action controls, an artifact browser, a diff editor, or a pull-request review surface until those capabilities are marked delivered.
