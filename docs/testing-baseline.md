# Testing Baseline

This document records the current verification tiers referenced by the project
test contract.

| Tier | Command | Current Wall Time | Notes |
|------|---------|-------------------|-------|
| Fast suite | `make test-fast` | 23s, target <=30s | Everyday short-mode package check before handoff. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Opt-in observer-backed TUI integration coverage. |

