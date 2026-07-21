# Post-Publish Workflows

The Agentic Orchestrator runtime has workflows for continuing work after a feature reaches `CodeReady` or `Published`. The Electron app exposes these workflows through the completion workspace and feature cockpit, authorized by the server action catalogue. This page explains their engine semantics and where each one surfaces in the desktop app.

Do not use retired terminal shortcuts to start these actions. Each post-publish action is reachable through a labeled desktop control.

## Rebase

Rebase updates a feature branch against its base branch. For a publishable repository, the runtime fetches remote refs, determines the target, rebases, and updates the remote branch. A local-only repository uses its local base branch and does not push.

If conflicts occur, the runtime can build a conflict plan and run an implementation session to resolve it before continuing. Multi-repository features track this cycle per repository.

**Desktop control:** the completion workspace exposes a Rebase handoff for code-ready and published features.

## Review Comments

The review-comments cycle fetches unresolved pull-request feedback through `gh`, filters comments already handled in prior iterations, creates a resolution plan, runs an implementation session, updates the branch, replies with outcomes, and attempts to resolve inline threads.

If a required worktree was previously removed, the runtime can recreate it from durable feature state before the cycle.

**Desktop control:** the completion workspace exposes a review-comments inbox with a resolve flow.

## Refactor

A refactor cycle accepts a new objective and sends an existing feature back through an appropriate planning and implementation path:

- Medium starts from planning.
- Large and Moonshot include the earlier inquiry, research, and design work.

The runtime retains the feature's repository identity and publish state while tracking the refactor cycle per repository.

**Desktop control:** the completion workspace exposes a refactor prompt with an optional pipeline selector and submit control.

## Merge

For a non-publishable local repository, merge can commit remaining work, merge the feature branch into its local base branch, and mark the feature complete.

**Desktop control:** the completion workspace exposes a Merge action with confirmation.

## Done

Marking a feature Done transitions its durable status and writes summary data such as timing, cost, and per-repository state after eligible work is complete.

**Desktop control:** the completion workspace exposes a Done action on the detail view.

## Clean Worktree

Worktree cleanup removes an isolated feature worktree after it is no longer needed. Durable feature state remains, and a later engine workflow can recreate the worktree when supported.

**Desktop control:** the completion workspace exposes a Cleanup action after completion; confirm that no active session uses the worktree before triggering it.

## What the App Surfaces

For a feature that has reached a terminal or post-implementation state, the Electron app shows its authoritative row on Home and opens its feature tab. Current-run transcript content remains available when the runtime exposes it. The completion workspace delivers publish, rebase, merge, refactor, review-comments, Done, worktree cleanup, and delete controls, plus an artifact browser, a diff viewer, and a pull-request review surface.
