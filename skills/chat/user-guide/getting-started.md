# Getting Started with Agentic Orchestrator

Agentic Orchestrator is a Go TUI (binary name: `agentico`) that drives the **KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish** lifecycle for AI-assisted development. It runs AI agent sessions in isolated git worktrees so you can work on multiple features concurrently without branch conflicts.

## Prerequisites

- **Go 1.25+** — for building from source
- **At least one provider CLI** — Claude, Codex, and OpenCode are co-equal backends; install whichever you use (one is enough to run the whole workflow):
  - **`claude` CLI >= 2.1.81** — Anthropic Claude Code (`npm install -g @anthropic-ai/claude-code`), authenticate with `claude auth login`
  - **`codex` CLI >= 0.116.0** — OpenAI Codex (`npm install -g @openai/codex`), authenticate with `codex login`
  - **`opencode` CLI >= 1.17.9** — OpenCode (`curl -fsSL https://opencode.ai/install | bash`), authenticate with `opencode auth login`; selected with the explicit `opencode:<backend/model>` form
- **`gh` CLI** — used for creating pull requests during the Publish phase
- **git** — worktree and branch operations

## Installation

Install the latest release:

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
```

Or build from source. The GitHub repository rename is still pending, so the
clone URL still uses the current GitHub path:

```bash
git clone https://github.com/doordash-oss/agentic-orchestrator.git
cd agentic
go build -o bin/agentico ./cmd/agentico
```

## First Launch

Run `agentico` to start the TUI. On the very first launch, Agentic Orchestrator walks you through workspace setup:

1. **Welcome screen** — displays the Agentic Orchestrator logo and a brief introduction. Press `Enter` to begin setup or `Esc` to skip.
2. **Directory picker** — a Miller-columns browser lets you navigate your filesystem and select a workspace root directory. Agentic Orchestrator scans the selected directory for git repositories.
3. **Confirmation** — shows the selected workspace root and the number of git repos found. Press `a` to add another workspace root, or `Enter` to continue to the dashboard.

Agentic Orchestrator creates:
- A config file at `~/.agentic-orchestrator/config.yaml`
- A state directory at `~/.agentic-orchestrator/features/`
- A worktrees directory at `~/.agentic-orchestrator/worktrees/`

If a legacy `~/.agentic-workflow/` directory already exists from a previous
install, it is reused in place as the runtime parent so existing data keeps
working without a manual copy.

When multiple LLM providers are detected (e.g., Claude, Codex, and OpenCode CLIs are installed), Agentic Orchestrator does not ask you to pick a preferred one. The detected providers are treated as co-equal, and each phase defaults to the best available model for that role across every provider — you can override any phase later. See [Configuration → Provider Selection](configuration.md#provider-selection) for installing, authenticating, and troubleshooting each provider.

## Creating Your First Feature

From the TUI dashboard, press `n` to open the feature creation wizard. The wizard has four steps:

### Step 1 — What

Enter a **feature name** and a **brief description** of what you want to build or fix.

### Step 2 — Where

Select one or more **repositories** to work in from your workspace. Use `Space` to toggle repos on/off. You can also browse for additional directories or create a new repo. If a selected repo is on a non-default branch, Agentic Orchestrator asks which branch to use.

### Step 3 — Pipeline

Choose a **pipeline profile** that controls how many phases your feature runs through:

| Profile | Phases | Effort |
|---------|--------|--------|
| **Medium** | Plan → Implement → Review → Publish | Medium |
| **Large** | KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish | High |
| **Moonshot** | Same as Large, with maximum rigor | High |

### Step 4 — Review

A summary screen lets you fine-tune settings before creating the feature:

- **Risk level** — low, medium, or high (cycles with `←`/`→`)
- **Models** — per-phase model selection (research, planning, implementation, review, KB build)
- **Inquireness** — how often the harness surfaces planning questions (none, medium, high)
- **Checkpoints** — toggle review gates (inquiry, research, design, plan, manual publish)
- **Exit criteria** — success criteria included in implementation prompts

Press `Shift+G` to create the feature. It enters the pipeline and progresses through phases automatically.

## Basic Workflow

### Monitoring Progress

The dashboard shows all features organized into three sections: **In Progress**, **Published**, and **Completed**. Navigate with `j`/`k` or arrow keys. Press `Tab` to switch between the feature list (left panel) and the detail view (right panel).

### Watching Live Work

Press `a` on a running feature to watch its live agent session. You can follow the AI work in real time and interact with it. Stop watching with `Ctrl+]` or `Esc` (on Nordic keyboards: `Ctrl+X` or `Esc`).

### Message Filtering

While watching, press `Ctrl+F` to cycle through message filter modes: **All** (everything), **No Tools** (hides tool use and thinking), **Text Only** (only assistant text and user messages).

### Handling Permission Prompts

When the agent needs to run a command or access a file, permission prompts surface in the TUI. Press `y` to allow or choose from the permission menu. Use `Shift+A` to approve and remember the permission for future use.

### Stopping and Resuming

Press `s` to stop a running feature. It can be resumed later with `r` — Agentic Orchestrator tracks progress and picks up where it left off. Use `Shift+R` from the dashboard to resume all interrupted features at once.

## State Directory

Agentic Orchestrator stores all state under `~/.agentic-orchestrator/` on fresh
installs. Existing installs that already have a `~/.agentic-workflow/`
directory continue using that legacy parent in place — no copy or migration
runs automatically:

```
~/.agentic-orchestrator/
  config.yaml              # Main configuration
  features/<id>/           # Per-feature state and artifacts
    feature.yaml           # Feature metadata and current state
    knowledgebase/         # KB Build phase outputs
    inquire/               # Inquiry phase outputs
    research/              # Research phase outputs
    design/            # Design phase outputs (directory name retained for legacy state compatibility)
    plan/                  # Plan artifacts (roadmap, phase plans)
    implement/             # Implementation logs and reports
    review/                # Review feedback
  worktrees/<slug>/        # Isolated git worktrees per feature
  skills/                  # Reconciled skill definitions
  guidelines/              # Reconciled guideline definitions
  permissions/             # Cached permission rules
  agentico.log             # Redirected stderr/log output while the TUI is running
```

## Next Steps

- Learn about the full [Feature Lifecycle](feature-lifecycle.md) and state machine
- Explore [TUI Navigation](tui-navigation.md) for all keybindings and views
- Customize your setup in [Configuration](configuration.md)
- Review [Verification](verification.md) before contributing changes from source
