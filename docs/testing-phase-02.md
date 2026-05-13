# Testing Phase 2 Timing Report

This report records the Phase 2 classifier/indexer reduction. Phase 1's
baseline remains in `docs/testing-baseline.md`.

## Scope

Phase 2 started with the `internal/agent` classifier and indexer tests. The
fast indexer representatives now use no-git in-memory fixtures, and
realism-heavy indexer scenarios remain in the extended regression suite through
`testing.Short` guards.

The second implementation pass also reduced the remaining `internal/agent`
fast-suite budget pressure outside the classifier/indexer slice: KB freshness
tests now use a fake git runner, polling/sleep-based tests use shorter
deterministic windows, one slow review-helper protocol-violation regression is
owned by the extended suite, and the source-identifier budget test uses a
smaller input that still exercises the timeout contract.

## Fast Suite

- **Profile command**: `go test -json ./... -short -count=1`
- **Verification command**: `go test ./... -count=1 -short`
- **Phase 1 total wall time**: 115s
- **Phase 2 profile wall time**: 96.02s after the classifier/indexer pass
- **Phase 2 verification wall time**: 96.61s after the classifier/indexer pass
- **Target**: <=30s on a warm build cache

Iteration 2 did not re-baseline the full fast-suite wall clock because later
roadmap phases still own the packages that dominate the repo-wide run. It did
re-measure the affected package on a warm cache.

## Internal Agent Package

| Measurement | Phase 1 | Phase 2 | Budget |
|-------------|---------|---------|--------|
| Repo-wide JSON package elapsed for `internal/agent` | 80.750s | 8.819s | noisy scheduling profile |
| Targeted affected-package command package elapsed, `go test ./internal/agent/... -count=1 -short` | not separately captured | 4.640s | 6s per package |
| Targeted prompt package elapsed, same command | 5.187s | 0.209s | 6s per package |

`internal/agent` is much faster after Phase 2 and fits the 6s package budget
when measured through the affected-package command on a warm cache. The
repo-wide JSON package elapsed remains useful for ranking later phases, but it
can include cross-package compile and scheduling delay that is not attributable
to `internal/agent` test bodies.

| Test | Elapsed |
|------|---------|
| `TestWaitForShutdownIntent` | 0.08s |
| `TestBuildSession_GuidelinesNotInjected` | 0.07s |
| `TestBuildSession_GuidelinesInjection` | 0.07s |
| `TestBuildSession_GuidelinesFinalReviewer` | 0.07s |
| `TestBuildSession_GuidelinesAdditionalDirs` | 0.07s |

## Post-Phase-2 Slowest Packages

| Package | Elapsed | Budget |
|---------|---------|--------|
| `github.com/doordash-oss/agentic-orchestrator/internal/git` | 103.314s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/feature` | 72.981s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/session` | 49.040s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui` | 28.332s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/orchestrator` | 25.450s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/ports` | 18.556s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm` | 18.301s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef` | 18.288s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun` | 18.148s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/config` | 17.781s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/permission` | 17.734s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/codex` | 17.530s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/llm/claude` | 14.308s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/utilskill` | 13.607s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/skilldef` | 13.253s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/tui/markdown` | 12.612s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/e2e` | 11.676s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agentdef` | 11.580s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/observe` | 9.377s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent` | 8.819s | above 6s in repo-wide profile; targeted package command is within budget |
| `github.com/doordash-oss/agentic-orchestrator/test/eval` | 8.196s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/integration` | 8.069s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks` | 8.051s | above 6s |
| `github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts` | 5.880s | within 6s |
| `github.com/doordash-oss/agentic-orchestrator/cmd/agentic` | 4.601s | within 6s |

## Extended Ownership

The following indexer scenarios were moved out of short mode and remain owned
by the extended regression command, `go test ./... -count=1`:

- many-repo prediction accuracy and max-selection behavior
- persistence reload across real on-disk classifier state
- real-git incremental startup and reindex after repository changes
- realistic service/protobuf and end-to-end repository selection
- historical-feature ingestion, replay, partial-load handling, and IDF
  consistency across realistic on-disk state
- review-helper protocol-violation handling that trips the consecutive-failure
  rail

The Phase 2 extended regression run passed. The final verification run reported
`internal/agent` at 132.167s in non-short mode, where the gated realism-heavy
tests execute. A targeted non-short spot check also confirmed
`TestClassifierIndex_Predict_ManyRepos` executed instead of skipping and passed.

## Regeneration Sequence

Run from the repository root on a warm build cache:

```bash
go test -json ./... -short -count=1 > /private/tmp/agentic-fast-suite-phase2.json
jq -r 'select(.Action=="pass" and .Package != null and .Elapsed != null and (.Test|not)) | [.Elapsed, .Package] | @tsv' /private/tmp/agentic-fast-suite-phase2.json | sort -nr
/usr/bin/time -p go test ./internal/agent/... -count=1 -short
```
