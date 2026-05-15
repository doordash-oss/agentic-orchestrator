# Agentic Orchestrator

### One-shot the moonshot — then do it ten times in parallel.

Agentic Orchestrator is an AI development workflow orchestrator that turns any engineer into a force multiplier. Describe your features, make the high-level decisions, and AI handles the rest — research, planning, implementation, code review, pull request — all running concurrently from a single terminal.

> The local CLI is `agentico`. The Go module and GitHub repository are both `github.com/doordash-oss/agentic-orchestrator`.

<img width="1727" height="1049" alt="image" src="https://github.com/user-attachments/assets/1d0704a4-621f-4482-8f89-5424290b7ea5" />


## Why Agentic Orchestrator?

Most AI coding tools are single-threaded: one conversation, one task, one context window. Agentic Orchestrator breaks that model:

- **Parallel execution** — Run 5, 10, 20 features simultaneously. Each gets its own git worktree, its own agent session, its own branch. No conflicts, no waiting.
- **Structured pipeline** — Features flow through Research → Plan → Implement → Review → Publish with human approval gates between phases. You stay in control of *what* gets built; AI handles *how*.
- **Multi-model orchestration** — Claude handles research, planning, and implementation. Codex handles code review. Each model is used where it excels.
- **Multiplexed sessions** — Agent sessions run in background pseudo-terminals. Watch live work in real time, stop watching to let it continue, and come back whenever you want.
- **Plan validation** — Specialized AI critics (architecture, security, performance, testing) review plans *before* implementation begins, catching structural issues early.

## Quick Start

```bash
# Install (requires Go 1.24+)
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest

# Or clone and build
git clone https://github.com/doordash-oss/agentic-orchestrator.git
cd agentic-orchestrator && make install

# Launch
agentico
```

On first launch, Agentic Orchestrator walks you through a welcome flow to select your workspace directories. After that, you're on the dashboard.

**Three keys to remember**: `n` (new feature), `?` (help), `a` (watch active work; answer, approve, or review when prompted). Everything else is discoverable from the help overlay.

## Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| **Go 1.24+** | Build Agentic Orchestrator | [go.dev](https://go.dev/dl/) |
| **Claude Code** (`claude` CLI) | AI backend for research, planning, and implementation | [docs.anthropic.com](https://docs.anthropic.com/en/docs/claude-code) or `devbox ai` |
| **Codex** (`codex` CLI) | AI backend for code review | `npm install -g @openai/codex` |
| **`gh` CLI** | PR creation during publish | [cli.github.com](https://cli.github.com/) |
| **`git`** | Worktree and branch management | Pre-installed on most systems |

## How It Works

### The Feature Lifecycle

Every feature progresses through a structured pipeline:

```
┌─────────┐    ┌────────────┐    ┌──────────┐    ┌──────────────┐    ┌──────────┐
│ Created │───▸│ Researching│───▸│ Planning │───▸│Implementing │───▸│ Published│
└─────────┘    └────────────┘    └──────────┘    └──────────────┘    └──────────┘
                    │                 │                  │
                    ▾                 ▾                  ▾
              Human review      Human review       AI code review
               (optional)        (optional)         (automatic)
```

**Researching** — The agent explores your codebase, reads documentation, builds a knowledge base, and produces a research document answering questions about how to approach the feature.

**Planning** — Using research findings, the agent creates a phased implementation roadmap (Tracer Bullet + TDD methodology), then detailed per-phase plans. Specialized critics validate each plan for architectural soundness, security, performance, and test coverage.

**Implementing** — The agent follows the approved plan, delegating to sub-agents for parallel work. It tracks progress, runs verification (tests, linting, builds), and iterates until the review gate passes.

**Publishing** — Review the diff, edit the PR description, and publish — all from the TUI. When a feature spans multiple repositories, cross-reference links are automatically injected into all related PRs.

### Pipeline Profiles

When creating a feature, choose a pipeline depth:

| Profile | Phases | Best for |
|---------|--------|----------|
| **Medium** | Plan → Implement → Final Review → Publish | Small, well-understood changes |
| **Large** | KB → Inquire → Research → Brainstorm → Plan → Implement → Final Review → Publish | Most features (default) |
| **Moonshot** | Same as Large, with additional review gates | High-risk or complex changes |

### Worktree Isolation

Each feature runs in its own git worktree under `~/.agentic-orchestrator/worktrees/` (legacy installs continue to use `~/.agentic-workflow/worktrees/` until you opt in). This means:
- Multiple features can work on the same repo simultaneously
- No branch conflicts between concurrent features
- Your main working copy stays untouched
- Worktrees are cleaned up with `c` after completion

### Multiple Repositories

Every feature targets one or more repositories with the same lifecycle and state machine. When a feature spans more than one repo, Agentic Orchestrator:
- Creates worktrees in each target repo
- Builds an execution plan with dependency ordering across repos
- Runs implementation per-repo (sequentially or in parallel based on dependencies)
- Cross-references PRs across repos automatically

When a feature targets a single repo, the per-repo Repo Progress panel, the cycle-selector modal, and the cross-reference PR table collapse — the rest of the lifecycle is identical.

### Knowledge Base

Before diving into a feature, Agentic Orchestrator can build a per-repo knowledge base — a structured document graph covering architecture, conventions, API surface, dependencies, and verification methods. The KB is cached and incrementally updated (only when HEAD changes), so subsequent features in the same repo start faster.

### Plan Validation Gate

Plans are reviewed by specialized AI critics before implementation begins:

| Critic | Focus | When Active |
|--------|-------|-------------|
| **Architecture** | Pattern consistency, module boundaries, dependency direction | All risk levels |
| **Security** | Auth, injection, data protection (calibrated to project context) | Medium + High risk |
| **Performance** | Scalability, query efficiency, resource management | Medium + High risk |
| **Testing** | Coverage adequacy, edge cases, regression protection | Medium + High risk |
| **Scope** | Requirement coverage, phase sizing, over-engineering detection | All risk levels |

Critics run in parallel and produce independent verdicts. If any critic requests changes, the plan is revised and re-validated automatically.

## Usage

### TUI Dashboard

Launch with `agentico`. The dashboard shows all features organized by status:

- **In Progress** — actively being worked on (researching, planning, implementing)
- **Published** — PR created, awaiting merge
- **Completed** — marked as done

Features needing your attention (pending permissions, help requests) show a warning indicator.

### Creating a Feature

Press `n` from the dashboard to open the wizard:

1. **What** — Name and describe the feature. Supports pasting images (`Ctrl+V`) and attaching files (`@`).
2. **Where** — Select target repo(s). Browse for new directories or create repos on the fly.
3. **Pipeline** — Choose Medium, Large, or Moonshot. Toggle individual checkpoints (inquiry review, research review, design review, plan review, manual publish).
4. **Review** — Adjust risk level, models per phase, exit criteria. Submit to start.

### Interacting with Agents

**Watch** (`a`) — Open active live work in real time. The same key becomes **Answer**, **Approve**, or **Review** when the agent needs input. Filter the output (`Ctrl+F`) between All, No Tools, or Text Only views.

**Stop watching** (`Esc/Ctrl+]`) — Return to the dashboard. The agent keeps running.

### Post-Implementation Actions

Once a feature reaches code-ready or published state:

| Key | Action |
|-----|--------|
| `p` | Publish as PR (diff review → commit log → PR description → confirm) |
| `t` | Tweak — make a targeted change without re-running the full pipeline |
| `Shift+F` | Refactor — apply a refactoring prompt to the implementation |
| `b` | Rebase on main |
| `g` | View and resolve PR review comments |
| `D` | Mark as done |

### Ask Me Anything

Press `/` anywhere to open the built-in AI chat. It's a read-only Claude session that can explain how Agentic Orchestrator works, debug issues by reading feature logs and artifacts, search the codebase, and answer questions — without modifying any files.

### Keybindings

> For the complete reference, see [docs/keybindings.md](docs/keybindings.md).

## Configuration

Config lives at `~/.agentic-orchestrator/config.yaml` (auto-created on first launch). If a legacy `~/.agentic-workflow/` directory already exists, it is reused in place so existing installs keep working without a manual copy.

```yaml
defaults:
  models:
    research: opus           # Model for research phase
    planning: opus           # Model for planning phase
    implementation: opus     # Model for implementation phase
    review: gpt-5.4          # Model for review phase (Codex)
    utilities: sonnet        # Model for chat and utility tasks
    kb_build: "opus[1m]"     # Model for knowledge base builds
  exit_criteria: |
    - Feature fully implemented per plan
    - Unit tests added/updated as needed
    - Integration tests added/updated as needed
    - Code formatted per project standards
    - Relevant tests pass
    - No linting errors
  max_iterations: 10
  max_consecutive_failures: 3
  max_consecutive_no_progress: 3
  pipeline: large            # Default pipeline (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Scanned for git repos on startup
```

### Model Overrides

Each feature can override default models during creation via the wizard (step 4). Models can be specified with explicit provider prefixes (e.g., `claude:opus`, `codex:gpt-5.4`) or as bare names that are automatically routed to the best-matching provider.

### Launch Flags

```text
agentico [flags]

Flags:
  --config <path>                  Config file (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory (default: ~/.agentic-orchestrator/features)
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --providers <list>               Restrict to specific providers (claude,codex)
  --help, -h                       Show help
  --version, -v                    Show version
```

## Development

```bash
# Build
go build -o bin/agentico ./cmd/agentico

# Or use the make target (writes ./bin/agentico)
make build

# Everyday verification
make test-fast

# Generate keybinding docs
go generate ./internal/tui/...
```

Verification is split into named tiers so everyday checks stay fast while
extended coverage remains available.

| Tier | Command | Current wall time | Purpose |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Everyday all-package short-mode check before handoff. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Builds the binary and checks CLI flags plus embedded skill layout. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Lifecycle, state-machine, and protocol-violation coverage. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Full TUI and teatest behavior with the race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Observer-backed TUI event and feature-span integration coverage. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Extended all-package race/regression sweep. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Live skill/guideline discovery against real LLM CLIs. |

`go vet ./...` and `go build ./...` remain required static and build checks.
The tagged **TUI observability** tier is the explicit opt-in gate for slower
observer-backed TUI integration coverage. The race-enabled all-package sweep is
the **Race regression** tier, not the ordinary unit command. See
[AGENTS.md](AGENTS.md) and
[docs/testing-baseline.md](docs/testing-baseline.md) for timing details, and
see AGENTS.md for the isolated-run pattern for running a second instance without
colliding with the first.

## Contributing

Pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, branch and commit conventions.

Contributions to this project require agreeing to the DoorDash Contributor License Agreement.
See [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## License

Agentic Orchestrator is licensed under the [Apache License, Version 2.0](LICENSE.txt).

## Notices

See [NOTICE.txt](NOTICE.txt) for third-party components and attributions.
