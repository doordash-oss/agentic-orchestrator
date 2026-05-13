# Architecture

## Overview

Agentic is a Go TUI application that orchestrates AI-assisted software development through a phased lifecycle: **Knowledge Base → Research → Plan → Implement → Review → Publish**. Features run concurrently in isolated git worktrees, each managed by background `claude` CLI sessions communicating via a JSON stdin/stdout SDK protocol over pseudo-terminals.

## High-Level Data Flow

```
User → TUI (Bubbletea) → Feature Manager → Phase Runner → Session Manager → claude CLI
                                                                              ↕
                                                                        JSON SDK Protocol
                                                                              ↕
                                                                        stdin/stdout
```

1. **User** interacts with the Bubbletea TUI (keyboard-driven)
2. **TUI** dispatches actions to the **Feature Manager** (create, start phase, etc.)
3. **Feature Manager** validates state transitions and persists to YAML files
4. **Phase Runner** builds prompts and launches **Sessions** via the Session Manager
5. **Session Manager** spawns `claude` CLI subprocesses, reads JSON SDK messages from stdout
6. **Events** flow back through channels to the TUI for real-time updates

## Package Dependency Graph

```
cmd/agentic
  ├── internal/tui        (TUI views and app model)
  ├── internal/agent      (phase runners, command/prompt builders)
  ├── internal/feature    (state machine, store, dependencies)
  ├── internal/session    (session management, SDK protocol)
  ├── internal/config     (YAML config)
  └── internal/git        (worktree, branch, publish, rebase)
```

**Dependency directions**:
- `tui` → `agent`, `feature`, `session`, `config`, `git`
- `agent` → `feature`, `session`, `config`
- `feature` → `config`, `git`
- `session` → `feature` (for Phase type only)
- `git` — no internal dependencies (pure shell wrapper)
- `config` — no internal dependencies

## Core Design Patterns

| Pattern | Description |
|---------|-------------|
| **Elm Architecture** | TUI follows Bubbletea's Init/Update/View with message-driven state transitions |
| **Filesystem Persistence** | All feature state stored as YAML in `~/.agentic-workflow/features/<id>/feature.yaml` |
| **State Machine** | Features progress through validated status transitions, with Final Review tracked per repo during `Implementing` |
| **Event-Driven Concurrency** | Session events flow through channels to the TUI for real-time updates |
| **JSON SDK Protocol** | Claude CLI sessions communicate via structured JSON messages over stdin/stdout |
| **Embedded Templates** | Agent personas and skills compiled in via `go:embed` |

## Directory Structure

```
agentic/
├── cmd/agentic/              CLI entry point
│   ├── main.go               Flag parsing, subcommand dispatch, TUI bootstrap
│   ├── cli_test.go           Black-box CLI tests via testscript
│   └── testdata/scripts/     txtar scripts for CLI tests
├── internal/
│   ├── config/               YAML configuration loading, defaults, persistence
│   ├── feature/              Feature state machine, filesystem store, dependency graph, split
│   ├── session/              PTY/SDK session management, message log, recovery, permissions
│   ├── agent/                Phase runners, command builders, prompt builders, classifiers
│   ├── git/                  Worktree lifecycle, branch naming, PR publishing, rebase, reviews
│   └── tui/                  Bubbletea TUI: dashboard, detail, wizard, attach, publish views
├── agents/                   Embedded agent persona definitions (markdown)
│   ├── embed.go              go:embed for *.md files
│   └── *.md                  6 agent personas
├── skills/                   Embedded skill definitions, user guides, and helpers
│   ├── embed.go              go:embed for `SKILL.md` and related markdown assets
│   └── */                    Lifecycle skills and supporting documentation
├── test/
│   ├── e2e/                  E2E smoke tests
│   ├── integration/          Integration lifecycle tests
│   ├── testutil/             Shared test helpers (git, events, mock_agent)
│   └── fixtures/             Sample PTY output files for parser tests
├── docs/                     Generated keybinding reference
├── go.mod / go.sum           Go module (Go 1.24+)
└── CLAUDE.md                 AI assistant instructions
```

## Key Entry Points

| Component | Location | Description |
|-----------|----------|-------------|
| CLI entry | `cmd/agentic/main.go:23` | Flag parsing and subcommand dispatch |
| Feature struct | `internal/feature/feature.go:244` | Central domain entity |
| Feature Manager | `internal/feature/manager.go:16-20` | Feature lifecycle orchestration |
| Session | `internal/session/session.go:45-88` | Claude CLI subprocess management |
| Session Manager | `internal/session/manager.go:26-32` | Multi-session tracking and routing |
| PhaseRunner | `internal/agent/phase.go:60-74` | Phase execution orchestration |
| Implementation loop | `internal/agent/implement.go:58` | Implement→review cycle |
| TUI App | `internal/tui/app.go:26-220` | Root Bubbletea model, views, messages |
| Config | `internal/config/config.go:11-49` | Configuration types |

## Concurrency Model

- Each feature's phase runs in its own goroutine via the Session Manager
- Sessions communicate with the TUI through typed channels (`chan interface{}`)
- The TUI processes messages on the main Bubbletea event loop
- Multiple features can run concurrently in separate worktrees
- File-based locking prevents concurrent access to shared resources (e.g., knowledge base)
