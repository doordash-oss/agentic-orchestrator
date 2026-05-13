# Git Operations

All git functions in `internal/git/` are thin wrappers around `git` and `gh` CLI commands via `exec.Command`.

## Worktree Management

Defined in `internal/git/worktree.go:19-28`:

### WorktreeManager

Manages worktrees under a configurable base directory (`~/.agentic-workflow/worktrees/`).

**Worktree path pattern**: `<baseDir>/<featureSlug>/<repoName>/` (see `worktree.go:31`)

| Method | Description |
|--------|-------------|
| `Create(repoPath, featureSlug, repoName, startPoint)` | Create git worktree |
| `Remove(path, deleteBranch)` | Remove worktree and optionally delete branch |
| `List()` | List all managed worktrees |
| `DetectStale(activeIDs)` | Find worktrees for deleted features |
| `ResetToBase(path, baseBranch)` | Hard reset worktree to base branch |
| `DefaultBranch(repoPath)` | Detect main/master branch |
| `CurrentBranch(repoPath)` | Get current branch name |

## Branch Naming

Defined in `internal/git/branch.go`:

- **Convention**: `feature/<slug>` (e.g., `feature/add-dark-mode`)
- `BranchName(slug)` — Generate branch name from feature slug

## Publishing

Defined in `internal/git/publish.go`:

| Function | Description |
|----------|-------------|
| `Push(worktreePath, branch)` | `git push -u origin <branch>` |
| `CreatePR(repoPath, branch, title, body, baseBranch)` | Create PR via `gh pr create` |
| `DiffSummary(path, baseBranch)` | Full diff of changes vs base branch |
| `DiffStatSummary(path, baseBranch)` | `--stat` summary of changes |
| `CommitAll(path, message)` | Stage all files and commit |
| `CommitLog(path, baseBranch)` | Formatted commit log since divergence |
| `HasUncommittedChanges(path)` | Check for uncommitted changes |

**Key reference**: `internal/git/publish.go:24` (`CreatePR`)

## Rebase & Conflict Detection

Defined in `internal/git/rebase.go` and `internal/git/conflict.go`:

| Function | Description |
|----------|-------------|
| `Fetch(path)` | `git fetch origin` |
| `Rebase(path, baseBranch)` | `git rebase origin/<base>` |
| `ForcePush(path, branch)` | `git push --force-with-lease` |
| `PRBaseBranch(repoPath, prURL)` | Get PR base branch via `gh api` |
| `IsBehindRemote(path, base)` | Check if local is behind remote |
| `DetectConflicts(worktrees)` | Find overlapping file changes across worktrees |

## PR Review Comments

Defined in `internal/git/review.go`:

| Function | Description |
|----------|-------------|
| `ParsePRURL(url)` | Extract owner/repo/number from PR URL |
| `FetchPRComments(repoPath, prURL)` | Get PR review comments via `gh api` with pagination |
| `ReplyToPRComment(repoPath, prURL, commentID, body)` | Reply to a specific review comment |
| `ReplyToIssueComment(repoPath, prURL, body)` | Post issue-level comment |
| `FetchReviewThreadMap(repoPath, prURL)` | Map comment IDs to thread node IDs |
| `ResolveReviewThread(repoPath, threadNodeID)` | Mark review thread as resolved |
| `LatestCommitSHA(path)` | Get HEAD commit SHA |

**Key reference**: `internal/git/review.go:56` (`FetchPRComments`)

## PR Signature

`InjectPRSignature(body)` (`internal/git/signature.go`) appends a "Generated with Agentic Workflow" signature to PR descriptions.
