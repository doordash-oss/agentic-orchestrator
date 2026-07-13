# TUI Navigation

Agentic Orchestrator uses a BubbleTea-based terminal UI with multiple views and context-sensitive keybindings. This guide covers every view and its controls.

## Dashboard

The dashboard is the main screen, split into two panels:

- **Left panel** — feature list organized into three collapsible sections: **In Progress**, **Published**, and **Completed**. The In Progress section cannot be collapsed.
- **Right panel** — detail view for the selected feature, showing status, phase progress, artifacts, and available actions.

Panel layout adapts to terminal width: under 80 columns shows single-panel mode (left only); 80–120 columns uses a 35%/65% split; over 120 columns uses 30%/70%.

When no features exist, a ghost prompt `"+ Create your first feature (n)"` appears in the list.

## Global Keys

These keys work from the dashboard regardless of panel focus:

| Key | Action |
|-----|--------|
| `q` | Quit |
| `/` | Open chat (ask-me-anything) |
| `?` | Open context-sensitive help overlay |
| `Ctrl+C` | Force quit |

## Dashboard — Left Panel (Feature List)

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `Enter` | Focus right panel (on feature) / toggle section collapse (on header) |
| `Tab` | Switch to right panel |
| `→` | Focus right panel |
| `n` | New feature (opens wizard) |
| `d` | Delete feature |
| `Shift+R` | Resume all interrupted features |
| `Shift+W` | Manage workspaces |

## Dashboard — Right Panel (Detail)

Keys available in the right panel depend on the feature's current status:

### Navigation

| Key | Action |
|-----|--------|
| `Esc` / `←` | Return to left panel |
| `Tab` | Switch to left panel |

### Running Features

| Key | Action |
|-----|--------|
| `a` | Watch active work; Answer, Approve, or Review when prompted |
| `o` | Show dashboard overview |
| `y` | Approve pending permission |
| `Shift+A` | Approve and remember permission |
| `h` | Answer agent's question |
| `s` | Stop feature |
| `r` | Restart current phase |
| `Ctrl+R` | Rewind to earlier phase |
| `Shift+R` | Retry failed repo |
| `l` | Live Preview from Overview, otherwise view logs |

### CodeReady Features

| Key | Action |
|-----|--------|
| `p` | Publish (create PR) |
| `v` | View diff |
| `Shift+F` | Refactor (re-run pipeline) |
| `b` | Rebase on main |
| `m` | Mark as manually published (skip PR) |
| `Shift+M` | Merge to base branch |
| `Shift+D` | Mark as done |

### Published Features

| Key | Action |
|-----|--------|
| `Shift+F` | Refactor (re-run pipeline) |
| `b` | Rebase on main |
| `g` | View PR review comments |
| `c` | Clean worktree |
| `Shift+D` | Mark as done |

Inside the review comments view (opened with `g`), press `Shift+A` to auto-address all comments with an autonomous agent session.

Other keys available in all states: `d` (delete feature), `Shift+N` (toggle input notifications), `a` on NeedsReview features (review artifacts).

## Watch View

Press `a` on a running feature to watch its live agent session and interact with the AI agent in real time.

### Basic Controls

| Key | Action |
|-----|--------|
| `Ctrl+]` / `Esc` | Stop watching and return to dashboard |
| `Ctrl+F` | Cycle message filter: All → No Tools → Text Only |
| `Enter` | Send message |
| `Ctrl+S` | Send message (alternative) |
| `Shift+Enter` | Insert newline in message |
| `Ctrl+V` | Paste image from clipboard (macOS) |

### Message Filter Modes

`Ctrl+F` cycles: **All** (everything) → **No Tools** (hides tool use/thinking) → **Text Only** (assistant text and user messages only).

### Repo Tabs

When a feature spans multiple repos, the watch view shows one tab per active repo. `Tab` switches to next repo tab, `Shift+Tab` to previous.

### Permission Prompts

When prompted: `y` = allow, `r` = allow and remember, `n` = deny. Navigate with `j`/`k`, confirm with `Enter`.

### Interaction Modes

- **Agent questions**: Options appear as a selectable list (`j`/`k` to navigate, `Enter` to select). Freeform input available when offered.
- **Plan review**: Press `Ctrl+D` to choose **Iterate more** or **Proceed**.

## Wizard View

The feature creation wizard has four steps:

| Step | Name | Description |
|------|------|-------------|
| 1 | What | Feature name and description |
| 2 | Where | Repository selection |
| 3 | Pipeline | Pipeline profile (medium / large / moonshot) |
| 4 | Review | Summary with editable settings |

### Wizard Navigation

| Key | Action |
|-----|--------|
| `Enter` | Advance to next step |
| `Shift+Tab` | Go back to previous step |
| `Esc` | Cancel wizard / go back |
| `Ctrl+C` | Cancel wizard |

### Step 4 (Review) Editing

| Key | Action |
|-----|--------|
| `Enter` | Edit selected field |
| `←` / `→` | Cycle values (risk, inquireness, models) |
| `Tab` / `Space` | Toggle checkpoints |
| `Shift+G` | Create feature |

In the description field: `Ctrl+V` pastes images (macOS), `@` triggers file autocomplete.

## Publish View

When publishing a feature (`p` from CodeReady state):

| Key | Action |
|-----|--------|
| `j` / `k` | Navigate repo list (when 2+ repos) |
| `Enter` | Advance / confirm |
| `Tab` | Toggle between PR title and body fields |
| `Esc` | Cancel publish |

## Recovery View

Shown at startup when interrupted sessions are detected:

| Key | Action |
|-----|--------|
| `r` | Resume session |
| `k` | Kill session |
| `s` | Skip (leave as-is) |
| `j` / `↓` | Move down |
| `↑` | Move up |
| `Enter` | Confirm all actions and continue |

## Logs View

View full phase logs for a feature (`l` from detail panel):

| Key | Action |
|-----|--------|
| `j` / `↓` | Scroll down |
| `k` / `↑` | Scroll up |
| `q` / `Esc` | Back to previous view |

## Chat View

Open the ask-me-anything chat with `/`. Send messages with `Enter`, close with `Esc`.

## Help System

Press `?` from any view to open a context-sensitive help overlay showing keybindings for the current view. Close with `?` or `Esc`. Scroll with `j`/`k` or `PgUp`/`PgDn`. Help contexts cover: Dashboard, Detail Panel, Wizard, Publish, Recovery, Logs, and Review Comments.

## Nordic Keyboard Layout

Set `ui.keyboard_layout: "nordic"` in `config.yaml` to add alternative bindings: `-` for `/` (chat) and `Ctrl+X` for `Ctrl+]` (stop watching), since `/` and `]` require AltGr on Nordic keyboards. The footer shows the active layout.
