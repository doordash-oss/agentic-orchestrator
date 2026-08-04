# Post-Publish Workflows

The Agentic Orchestrator runtime has workflows for continuing work after a feature reaches `CodeReady` or `Published`. The Electron app exposes these workflows through the completion workspace and feature cockpit, authorized by the server action catalogue. This page explains their engine semantics and where each one surfaces in the desktop app.

Do not use retired terminal shortcuts to start these actions. Each post-publish action is reachable through a labeled desktop control.

## Rebase

The Rebase card in the aftercare workspace launches a rebase child pipeline with one click — no modal, no preflight. The child pipeline merges each behind repo's resolved target branch into the feature branch (merge, not rebase — no history rewriting). Integration lands on the parent's feature branch as a revertable merge commit through the existing child-integration transaction. After integration, the parent auto-publishes if configured, otherwise drops to `CodeReady`.

If nothing is behind, creation shows an "already up to date" notice instead of spawning a child. On a failed local merge in the completion workspace, a text hint (no button) points to the Rebase card in the feature's aftercare workspace.

**Desktop control:** the aftercare workspace exposes a Rebase card for code-ready and published features.

## Review Feedback

The review-feedback child pass is a child feature that fetches unresolved pull-request feedback, addresses each comment, replies with outcomes, and resolves inline threads. Like the rebase child, it runs as a self-contained feature with its own pipeline and integrates back into the parent on completion.

**Desktop control:** the aftercare workspace exposes a Review-feedback card for features with open PR feedback.

## Refactor

The refactor child pass creates a child feature with a new objective, sending the work back through an appropriate planning and implementation pipeline:

- Medium starts from planning.
- Large and Moonshot include the earlier inquiry, research, and design work.

The child feature retains the parent's repository identity and publish state, and integrates back into the parent on completion.

**Desktop control:** the aftercare workspace exposes a Refactor card with an objective prompt and pipeline selector.

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

For a feature that has reached a terminal or post-implementation state, the Electron app shows its authoritative row on Home and opens its feature tab. Current-run transcript content remains available when the runtime exposes it. The aftercare workspace delivers Rebase, Refactor, and Review-feedback cards, plus publish, merge, Done, worktree cleanup, and delete controls, an artifact browser, a diff viewer, and a pull-request review surface.
