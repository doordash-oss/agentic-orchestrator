# Build & Configuration

## Build Commands

```bash
go build -o bin/agentic ./cmd/agentic   # Build binary
go test ./... -race             # All tests with race detector
go vet ./...                             # Static analysis
go generate ./internal/tui/...           # Regenerate keybinding docs
```

## Go Module

- **Go version**: 1.24+
- **Module path**: Defined in `go.mod`

## Dependencies

| Dependency | Version | Purpose |
|------------|---------|---------|
| `charmbracelet/bubbletea` | v1.3.10 | TUI framework (Elm architecture) |
| `charmbracelet/bubbles` | v1.0.0 | TUI components (text input, viewport, textarea) |
| `charmbracelet/lipgloss` | v1.1.0 | Terminal styling and layout |
| `charmbracelet/x/ansi` | v0.11.6 | ANSI sequence handling |
| `charmbracelet/x/exp/teatest` | — | Bubbletea test utilities |
| `muesli/termenv` | v0.16.0 | Terminal environment detection |
| `gopkg.in/yaml.v3` | v3.0.1 | YAML serialization |
| `rogpeppe/go-internal` | v1.14.1 | testscript framework for CLI tests |

## External Tools

| Tool | Required | Usage |
|------|----------|-------|
| `claude` CLI | Yes | AI agent backend for all phases |
| `gh` CLI | Yes (for publishing) | GitHub PR creation and management |
| `git` | Yes | Worktree, branch, diff operations |
| `codex` CLI | Optional | Alternative AI backend |
| `terminal-notifier` | Optional | macOS desktop notifications |

## Configuration

### Config File

Default path: `~/.agentic-workflow/config.yaml` (override with `--config`)

| Function | Description |
|----------|-------------|
| `Load(path)` | Load and parse YAML config, apply defaults |
| `Save(path, cfg)` | Marshal and write config to disk |
| `LoadOrCreate(path)` | Load existing or create default config |
| `NewDefault()` | Create config with sensible defaults |
| `DiscoverRepos(cfg, dir)` | Auto-discover git repos in a workspace directory |

### Config Types

Defined in `internal/config/config.go:11-49`:

| Type | Description |
|------|-------------|
| `Config` | Top-level: `Defaults`, `Repos`, `Notifications`, `UI` |
| `DefaultsConfig` | Default models, exit criteria, inquireness, max iterations, auto-publish |
| `ModelConfig` | Per-phase model selection |
| `RepoConfig` | Repo path and verification command |
| `UIConfig` | Collapsed sections list |
| `NotificationConfig` | Terminal bundle ID override and input notification mute toggle |

### Config Schema

```yaml
defaults:
  models:
    research: opus          # Model for research phase
    planning: opus          # Model for planning phase
    implementation: opus    # Model for implementation phase
    review: codex           # Model for review gate
    chat: sonnet            # Model for chat mode
  exit_criteria: "Relevant tests pass"
  inquireness: medium       # low / medium / high
  max_iterations: 10
  max_consecutive_failures: 3
  max_consecutive_no_progress: 3
  auto_publish: false
repos:
  my-repo:
    path: /path/to/repo
    verification: "go test ./..."
notifications:
  terminal_bundle_id: "com.googlecode.iterm2"
  mute_feature_input: false
ui:
  collapsed_sections: []
```

## State Directory

Default path: `~/.agentic-workflow/features/` (override with `--state-dir`)

```
~/.agentic-workflow/
├── config.yaml                        Global configuration
├── features/                          Feature state
│   └── <featureID>/
│       ├── feature.yaml               Feature state (YAML)
│       ├── research/                  Research output
│       ├── plan/                      Plan output
│       ├── implement/                 Implementation output
│       │   └── iteration-NN/          Per-iteration directory
│       └── publish/                   Publish output
└── worktrees/                         Git worktrees
    └── <featureSlug>/
        └── <repoName>/
```

## CLI Entry Point

`cmd/agentic/main.go` (~230 lines) uses manual flag parsing (no framework).

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config <path>` | `~/.agentic-workflow/config.yaml` | Config file path |
| `--state-dir <path>` | `~/.agentic-workflow/features` | State directory |
| `--dangerously-skip-permissions` | false | Skip Claude CLI permission prompts |
| `--help` | — | Show help |
| `--version` | — | Show version |

### Subcommands

| Command | Description |
|---------|-------------|
| `run` (default) | Launch Bubbletea TUI dashboard |
| `init` | Generate default config.yaml |
| `feature list` | List all features |
| `feature create` | Create a new feature (flags: `--name`, `--description`, `--repo`, `--model-*`, `--exit-criteria`, `--auto-publish`, `--current-branch`) |
