# Testing

## Running Tests

```bash
go test ./... -race   # Unit + integration tests with race detector
go vet ./...                   # Static analysis
bash test/e2e/smoke.sh         # End-to-end smoke tests (builds binary, exercises CLI)
```

Integration tests are skipped with `-short` flag:
```bash
go test -short ./...           # Skip integration tests
```

## Test Utilities

Located in `test/testutil/`:

### git.go
- `InitGitRepo(t)` — Create a temporary git repository for test use

### events.go
JSONL constants for mock SDK sessions:
- `JSONLInit` — Mock system/init message
- `JSONLSuccess` — Mock success result
- `JSONLResult(text)` — Mock result with custom text
- `JSONLAssistant(text)` — Mock assistant message
- `TouchPhaseComplete(dir)` — Create `phase_complete` file in directory

### mock_agent.go
- `WriteScript(t, dir, name, content)` — Create executable mock scripts for testing
- `MockCommandBuilder` — Test command builder (non-interactive)
- `MockInteractiveCommandBuilder` — Test command builder (interactive)

## Fixtures

Located in `test/fixtures/`:

| File | Description |
|------|-------------|
| `mock-agent.sh` | Mock agent script with status signals |
| `sample-status-success.txt` | SUCCESS signal output |
| `sample-ink-status-success.txt` | Ink-formatted success with ANSI codes |
| `sample-permission.txt` | Permission prompt output |
| `sample-api-error.txt` | API rate limit error output |

## Integration Tests

### Phase Runner Tests
`internal/agent/integration_test.go` (~600+ lines):
- `TestImplementLoopSuccessFirstIteration`
- `TestPhaseRunnerResearchSuccess/Failure/NeedInput`
- `TestRunResearch_ResumeSessionID`

### Knowledge Base Tests
`internal/agent/kb_integration_test.go`:
- Knowledge base build/update tests

### Codex Integration Tests
`internal/session/codex_integration_test.go`:
- Attach channel delivery, ANSI stripping, deduplication

### Lifecycle Tests
`test/integration/lifecycle_test.go`:
- Full lifecycle test: feature creation through phase transitions

All integration tests are skipped with `testing.Short()`.

## CLI Tests

`cmd/agentic/cli_test.go` uses `rogpeppe/go-internal/testscript` for black-box CLI testing.

Scripts in `cmd/agentic/testdata/scripts/` test:
- `init` command
- `feature create` command
- `feature list` command
- `--help` output

Tests use txtar format (multi-file archives inline in test files).

## E2E Tests

`test/e2e/smoke.sh`:
- Builds the binary
- Exercises CLI commands
- Validates basic functionality

`test/e2e/tui_test.go`:
- TUI-level integration tests

## Test Patterns

1. **Table-driven**: Use `t.Run()` with descriptive subtest names
2. **Helpers**: Use `t.Helper()` in all test helper functions
3. **Public API only**: Never test private functions directly
4. **Temp directories**: Use `t.TempDir()` for test artifacts
5. **Mock scripts**: Use `testutil.WriteScript` for mock agent processes
6. **JSONL events**: Use `testutil.JSONL*` constants for mock SDK messages
