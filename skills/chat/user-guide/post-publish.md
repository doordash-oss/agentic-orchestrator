# Post-Publish Workflows

After a feature reaches **CodeReady** or **Published** state, several actions are available for iterating on the code. These workflows let you make manual adjustments, rebase onto the latest main, address PR review feedback, or re-run the full pipeline.

## Tweak (`t`)

Tweak launches an **interactive session** where you directly converse with the AI agent to make changes. Unlike the automated implementation phase, you control the session — type messages, give directions, and watch the agent work in real time.

### Starting a Tweak

Press `t` from the detail panel on a CodeReady or Published feature. When the feature spans 2+ repos, a cycle selector opens to choose which repo to tweak; for a single-repo feature the tweak dispatches directly without the selector.

If the worktree was previously cleaned, Agentic Orchestrator recreates it automatically before starting.

### Interacting

The watch view opens with the tweak session active. You can:
- Type messages and press `Enter` to send them to the agent
- Watch the agent's responses and tool use in real time
- Use message filtering (`Ctrl+F`) to reduce noise

### Finishing

Two ways to finish a tweak session:

- **`Ctrl+D`** — finishes immediately (commits and completes)
- **`Esc`** — opens an inline prompt with two options:
  - `f` or `Enter` — **Finish**: commit changes and complete
  - `d` — **Stop watching**: leave the session running without finishing

### After Finishing

When you finish a tweak session, Agentic Orchestrator runs this sequence:

1. **Auto-commit** — if there are uncommitted changes, they are committed with the message "Apply tweak changes"
2. **Push** — for Published features with changes, pull-rebases to sync with remote, then pushes. Conflicts open the rebase resolution flow.
3. **Transition** — feature returns to CodeReady (no PR) or Published (with PR)

### Recovery

Tweak sessions are interactive and cannot be automatically resumed after a crash. On recovery, tweak sessions can only be **killed** (not resumed or skipped). The feature remains in an interrupted state until you start a new tweak or take another action.

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

## Refactor (`Shift+F`)

Refactor re-runs the pipeline on a published or code-ready feature, letting the AI agent make deeper structural changes guided by your description.

### Starting

Press `Shift+F` from the detail panel. A textarea appears where you describe the refactoring you want — submit with `Ctrl+S` or cancel with `Esc`. When the feature spans 2+ repos, a cycle selector first opens to choose which repo to refactor and each repo's refactor runs independently; for a single-repo feature the refactor dispatches directly without the selector.

After submitting the prompt, a **pipeline selector** overlay appears so you can pick the profile for this refactor cycle — **Medium**, **Large** (default), or **Moonshot**. Navigate with `←`/`→` and confirm with `Enter`.

### Flow

The refactor transitions the feature back to an earlier pipeline phase based on the selected profile:

- **Medium** — transitions to **PlanReady** and starts with planning
- **Large / Moonshot** — transitions to **Inquiring** and runs the full inquiry → research → brainstorm → plan → implement cycle

After implementation completes:
1. Commits changes with "Apply refactor changes"
2. For Published features with a PR: pull-rebases and pushes
3. Returns to CodeReady or Published state

## Merge (`Shift+M`)

Merge is available only for **non-publishable features** (local repos with no remote) in the CodeReady state. It merges the feature branch into the local base branch.

### Flow

1. Commits any uncommitted changes with "Final changes before merge"
2. Merges the feature branch into the base branch at the original repo path
3. Marks the feature as Done

## Done (`Shift+D`)

Marks a feature as completed. Available from Published state, or from CodeReady state for non-publishable features.

Not available while post-publish cycles (tweak, rebase, review comments, refactor) are still active.

### What Happens

1. Transitions the feature to **Done** status
2. Writes a feature summary artifact (`observe-summary.yaml`) with timing data, cost data, and per-repo states

## Clean Worktree (`c`)

Removes the git worktree directory for a completed feature, freeing disk space. Available for features in **Published** or **Done** state that still have a worktree on disk.

### Worktree Recreation

If you later need to perform a post-publish action (tweak, rebase, review comments) on a feature whose worktree was cleaned, Agentic Orchestrator automatically recreates the worktree from the feature branch before starting the workflow.
