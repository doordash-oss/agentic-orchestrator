# Post-Publish Workflows

After a feature reaches **CodeReady** or **Published** state, several actions are available for iterating on the code. These workflows let you make manual adjustments, rebase onto the latest main, or address PR review feedback.

## Rebase (`b`)

Rebase updates the feature branch to incorporate the latest changes from the base branch.

### Starting a Rebase

Press `b` from the detail panel on a CodeReady or Published feature. When the feature spans 2+ repos, a cycle selector opens for per-repo rebase; for a single-repo feature the rebase dispatches directly without the selector.

### Flow

For publishable (remote) features:
1. Fetches the latest remote refs
2. Determines the rebase target (PR base branch, repo base branch, or default branch)
3. Checks if the feature branch is behind the target — if already up to date, reports so and stops
4. Runs `git rebase` onto the remote target
5. On success: **force-pushes** the rebased branch

For non-publishable (local) features:
1. Determines the local base branch
2. Checks if behind — stops if up to date
3. Runs `git rebase` onto the local target (no push)

### Conflict Resolution

If the rebase encounters conflicts:

1. Agentic Orchestrator creates a **rebase plan** — a markdown document listing the conflict files and instructions for resolution
2. An autonomous implementation session runs using the rebase plan, where the agent resolves conflicts and continues the rebase
3. After resolution: commits any remaining changes and **force-pushes** (for publishable features)

## Review Comments (`g`)

Fetches PR review comments from GitHub and runs an autonomous session to address them.

### Starting

Press `g` from the detail panel on a Published feature with a PR. When the feature spans 2+ repos, a cycle selector opens to choose which repo's review comments to address; for a single-repo feature the cycle dispatches directly without the selector.

### Flow

1. **Fetch** — retrieves all review comments from the PR via the `gh` CLI
2. **Filter** — removes comments that were already addressed in previous iterations (tracked by comment ID)
3. **Review plan** — builds a markdown plan listing each comment with its context
4. **Implementation** — runs an autonomous agent session to address the comments
5. **Commit and push** — commits changes with "Address review comments", pull-rebases, pushes
6. **Reply** — posts replies to each comment on GitHub with the resolution (addressed with commit SHA, or dismissed with reason)
7. **Resolve threads** — attempts to resolve review threads for inline comments

If the worktree was previously cleaned, Agentic recreates it before starting.

## Merge (`Shift+M`)

Merge is available only for **non-publishable features** (local repos with no remote) in the CodeReady state. It merges the feature branch into the local base branch.

### Flow

1. Commits any uncommitted changes with "Final changes before merge"
2. Merges the feature branch into the base branch at the original repo path
3. Marks the feature as Done

## Done (`Shift+D`)

Marks a feature as completed. Available from Published state, or from CodeReady state for non-publishable features.

Not available while post-publish cycles (rebase, review comments) are still active.

### What Happens

1. Transitions the feature to **Done** status
2. Writes a feature summary artifact (`observe-summary.yaml`) with timing data, cost data, and per-repo states

## Clean Worktree (`c`)

Removes the git worktree directory for a completed feature, freeing disk space. Available for features in **Published** or **Done** state that still have a worktree on disk.

### Worktree Recreation

If you later need to perform a post-publish action (rebase, review comments) on a feature whose worktree was cleaned, Agentic Orchestrator automatically recreates the worktree from the feature branch before starting the workflow.
