# Testing Phase 3 Timing Report

## Internal Git Package

| Measurement | Before | After | Budget |
|-------------|--------|-------|--------|
| Targeted short JSON wall time, `go test ./internal/git/... -count=1 -short -json` | 32.67s | 4.32s | <=6s package elapsed |
| Targeted short JSON package elapsed, `github.com/doordash-oss/agentic-orchestrator/internal/git` | 32.380s | 4.004s | <=6s |
| Targeted short command package output, `go test ./internal/git/... -count=1 -short` | 27.383s | 5.449s | <=6s |
| Targeted full JSON wall time, `go test ./internal/git/... -count=1 -json` | not captured | 17.14s | extended |
| Targeted full JSON package elapsed, `github.com/doordash-oss/agentic-orchestrator/internal/git` | not captured | 16.845s | extended |
| Targeted full command package output, `go test ./internal/git/... -count=1` | not captured | 8.691s | extended |

## Fast Subset Slowest Tests

| Before test | Before elapsed | After test | After elapsed |
|-------------|----------------|------------|---------------|
| `TestWorktreeCreateAndRemove` | 2.05s | `TestRebase_LinearHistory` | 2.01s |
| `TestResetToBaseLocal` | 2.04s | `TestRebase_ConflictAborts` | 1.50s |
| `TestResetToBaseLocal_NoOriginNeeded` | 1.91s | `TestCommitAll/regular_file` | 0.93s |
| `TestDetectStale` | 1.38s | `TestCommitAll/orchestration_named_file` | 0.93s |
| `TestBranchExistsOnRemote` | 1.29s | `TestUpdatePRBody_Error` | 0.90s |
| `TestCurrentBranch` | 1.18s | `TestGetPRBody_Error` | 0.89s |
| `TestUpdatePRBody_Error` | 1.16s | `TestWorktreeCreateAndRemove` | 0.85s |
| `TestWorktreeList` | 1.13s | `TestResetToBaseLocal` | 0.84s |
| `TestPullRebase_ConflictAbortsCleanly` | 1.11s | `TestRemoteUpToDateGuards` | 0.83s |
| `TestPullRebase_BehindRemote` | 1.08s | `TestBranchExistsOnRemote` | 0.80s |

## Post-Phase-3 Slowest Packages

| Package | Elapsed | Budget |
|---------|---------|--------|
| `github.com/doordash-oss/agentic-orchestrator/internal/feature` | 49.073s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui` | 44.602s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/session` | 44.050s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/e2e` | 27.905s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/integration` | 27.864s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/skilldef` | 27.614s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/eval` | 27.521s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/testutil` | 27.104s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks` | 26.769s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/utilskill` | 26.719s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui/markdown` | 26.304s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/orchestrator` | 10.667s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/codex` | 8.466s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef` | 8.316s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/ports` | 7.825s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun` | 7.515s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/git` | 7.299s | targeted package elapsed within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/permission` | 6.933s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/claude` | 4.769s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent` | 4.577s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts` | 4.345s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm` | 3.018s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/config` | 2.722s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/observe` | 2.713s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agentdef` | 2.565s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/cmd/agentic` | 0.731s | within 6s |

## Extended Spot Checks

| Command | Result |
|---------|--------|
| `go test ./internal/git/... -count=1 -run 'TestWorkingTreeDiffPreviews_DeleteAndRename\|TestPullRebase_RemoteBranchAbsent\|TestWorktreeList'` | passed, 1.793s |
