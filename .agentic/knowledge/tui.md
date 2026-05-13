# TUI

## App Architecture

The TUI uses the Bubbletea framework (Elm architecture: Init/Update/View). `AppModel` (`internal/tui/app.go:26-220`) is the root model that manages view switching and global state.

**Current Version**: Resolved by `GetVersion()` in `internal/tui/dashboard.go`, which reads the `version` package variable. The variable is set at build time from git tags via ldflags (or falls back to Go module build info, then `"dev"`). It is **not** edited per-change; releases are cut with the `/release` skill (GoReleaser).

## Views

| View | Model | Description |
|------|-------|-------------|
| `ViewDashboard` | `DashboardModel` | Main feature list with split-panel layout |
| `ViewDetail` | `DetailModel` | Full-screen feature detail |
| `ViewWizard` | `WizardModel` | New feature creation wizard |
| `ViewAttach` | `AttachModel` | Live session output viewer |
| `ViewPublish` | `PublishModel` | PR publishing workflow |
| `ViewRecovery` | `RecoveryModel` | Session recovery after crashes |
| `ViewLogs` | `LogsModel` | Phase output log viewer |
| `ViewChat` | `ChatModel` | Interactive chat mode |
| `ViewReviewComments` | `ReviewCommentsModel` | PR review comment viewer |

## Dashboard Layout

The dashboard (`internal/tui/dashboard.go`) renders a split-panel layout:

- **Left panel**: Feature list grouped by status sections (Active, Waiting, Completed, etc.)
- **Right panel**: Selected feature detail preview
- **Header**: Version, feature count, attention badges
- **Footer**: Context-sensitive keybinding help

### Layout Modes

| Mode | Description |
|------|-------------|
| `layoutWide` | Split panels (left list + right detail) |
| `layoutNarrow` | List only |
| `layoutCompact` | Minimal display |

### Feature Sorting

Features are sorted by: attention-needing first → running → created → completed → done.

## Message Types

The TUI communicates via Bubbletea messages (`internal/tui/app.go`):

| Message | Description |
|---------|-------------|
| `TickMsg` | 3-second periodic refresh |
| `RefreshFeaturesMsg` | Reload feature list from disk |
| `SDKSessionEventMsg` | SDK message from a session |
| `SessionDoneTUIMsg` | Session completed |
| `PhaseCompletedMsg` | Phase completed, advance state machine |
| `ImplementLoopDoneMsg` | Implementation loop finished |
| `PlanLoopDoneMsg` | Plan validation loop finished |
| `StartPhaseMsg` | Start next phase for a feature |
| `FeatureCreatedMsg` | Feature created by wizard |
| `SplitCompleteMsg` | Feature split completed |
| `CheckDependentsMsg` | Check if blocked dependents can proceed |
| `PublishResultMsg` | PR publish result |
| `RebaseResultMsg` | Rebase operation result |

## Keybindings

Defined in `internal/tui/keys.go`. Generated reference at `docs/keybindings.md` via `go generate ./internal/tui/...`.

### Navigation
| Key | Action |
|-----|--------|
| `j` / `k` / `↑` / `↓` | Move up/down |
| `Tab` | Switch panels/sections |
| `Enter` | Open detail view |
| `Esc` | Go back |

### Actions
| Key | Action |
|-----|--------|
| `n` | New feature |
| `a` | Attach to session |
| `y` | Approve permission |
| `h` | Answer help request |
| `d` | Delete feature |
| `u` | Unblock dependency |

### Phase
| Key | Action |
|-----|--------|
| `r` | Restart current phase |
| `ctrl+r` | Restart from beginning (start over) |
| `l` | View logs |
| `v` | View diff |
| `t` | Tweak implementation |
| `Enter` | Advance phase |

### Publishing
| Key | Action |
|-----|--------|
| `p` | Publish PR |
| `m` | Mark manually published |
| `D` | Mark as done |
| `b` | Rebase on main |
| `c` | Clean worktree |
| `g` | Review comments |

### General
| Key | Action |
|-----|--------|
| `?` | Help overlay |
| `q` / `ctrl+c` | Quit |
| `/` | Chat mode |
| `R` | Resume all sessions |

## Notifications

`internal/tui/notify.go` implements macOS notifications via `terminal-notifier` and `osascript`:
- Phase completion notifications
- Permission request notifications
- Help request notifications

The terminal bundle ID is configurable via `config.yaml`.

## Other Components

| Component | File | Description |
|-----------|------|-------------|
| `ActivityModel` | `activity.go` | Activity log/event viewer |
| `ClipboardModel` | `clipboard.go` | Clipboard copy functionality |
| `IconsModel` | `icons.go` | Status/phase icon rendering |
| Styles | `styles.go` | Lipgloss style definitions (colors, borders, layout) |
| `HelpModel` | `help.go`, `help_data.go` | Help overlay with section-based keybinding reference |
