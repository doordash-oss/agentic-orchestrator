# Agentic Orchestrator

### One-shot the moonshot — then do it ten times in parallel.

Agentic Orchestrator is an AI development workflow orchestrator that turns any engineer into a force multiplier. Describe your features, make the high-level decisions, and AI handles the rest — research, planning, implementation, code review, pull request — all running concurrently from a single terminal.

> The local CLI is `agentico`

<img width="3000" height="1800" alt="agentico-basic-flow-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Why Agentic Orchestrator?

The hard part of agentic coding is not asking a model to edit files. The hard part is getting from a vague, high-level feature request to a reviewable PR without losing context, skipping design work, or letting a bad plan produce a huge diff. Left unmanaged, this is how teams get AI slop: plausible-looking code produced faster than the context, tests, and review process needed to make it trustworthy. Agentic Orchestrator is built around that problem: it turns one feature prompt into a durable engineering workflow that gathers context, asks questions, designs the approach, decomposes the work, implements it, verifies it, reviews it, and publishes it.

That is the real "oneshot" value: an engineer can describe a large feature once, then supervise the checkpoints where judgment matters instead of manually shepherding every prompt, terminal session, worktree, test run, review pass, and PR step.

- **Context is built, not hoped for** — Large and Moonshot features start by building a per-repo knowledge base, then run inquiry, research, and design phases before planning. The implementation agent reads structured artifacts instead of relying on a single overloaded chat history.
- **Complexity is phased** — Planning produces a roadmap, then each roadmap phase gets its own detailed phase plan. A tracer-bullet phase establishes the path; later TDD fill-in phases retire stubs and expand coverage.
- **Quality gates happen before the diff gets expensive** — Plan validators review architecture, scope, structure, and, for high-risk work, security, performance, and testing. Implementation and Final Review loops use explicit verification evidence before the feature becomes publishable.
- **Human attention is reserved for decisions** — Optional gates pause on inquiry review, research review, design review, roadmap review, phase plan review, user-input, and publish decisions. You approve direction, request iteration, or answer targeted questions; the orchestrator keeps the workflow state.
- **Parallelism is the multiplier, not the premise** — Because every feature gets isolated worktrees, branches, sessions, and artifacts, you can run several complex workflows at once without mixing state or blocking your main checkout.
- **Provider orchestration is explicit** — One provider is enough to run the whole workflow; add more to split the work. Claude, Codex, and OpenCode are co-equal: each phase's default is the best available model for that role across every detected provider, and models can be overridden per phase and swapped at runtime. Use `--providers` to restrict the orchestrator to the CLIs you actually have installed.

The design follows patterns described in Anthropic's [Building Effective Agents](https://www.anthropic.com/engineering/building-effective-agents) article: prompt chaining, parallelization, orchestrator-workers, and evaluator-optimizer loops. It also codifies Claude Code's [explore → plan → code](https://code.claude.com/docs/en/best-practices) workflow and OpenAI's guidance on agent [orchestration and guardrails](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/).

## Quick Start

Use Homebrew if you have it; otherwise grab the prebuilt binary. Build from source only if you're working on agentico itself.

**Homebrew** (recommended — macOS/Linux):

```bash
brew install doordash-oss/agentic-orchestrator/agentico
```

**Prebuilt binary** — no Homebrew or Go (macOS/Linux, amd64/arm64):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/doordash-oss/agentic-orchestrator/releases/latest | sed 's@.*/@@')
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/doordash-oss/agentic-orchestrator/releases/download/${TAG}/agentic-orchestrator_${TAG#v}_${OS}_${ARCH}.tar.gz" | tar -xz -C ~/.local/bin agentico
# ensure ~/.local/bin is on your PATH
```

**From source** — for contributing to agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# or: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Then run `agentico`. Update any time with `agentico update` — it uses the right method for how you installed.

On first launch, Agentic Orchestrator walks you through a welcome flow to select your workspace directories. After that, you're on the dashboard.

**Three keys to remember**: `n` (new feature), `?` (help), `a` (watch active work; answer, approve, or review when prompted). Everything else is discoverable from the help overlay.

## Prerequisites

### Required

| Tool | Purpose | Install |
|------|---------|---------|
| **`git`** | Worktree, branch, commit, and rebase operations | Pre-installed on most systems |
| **`gh` CLI** | Push-time PR creation and cross-repo PR body updates during Publish | [GitHub CLI docs](https://docs.github.com/en/github-cli/github-cli), then `gh auth login` |

### Provider CLIs — install at least one

Agentic Orchestrator needs **at least one** AI provider CLI.

| Tool | Role | Install |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend for KB, inquiry, research, design, planning, implementation, and chat | [Claude Code setup](https://code.claude.com/docs/en/getting-started) or `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend for Final Review and Codex-backed review models | [Codex CLI setup](https://developers.openai.com/codex/cli) or `npm i -g @openai/codex@latest` |
| **OpenCode CLI >= 1.17.9** (`opencode`) | Co-equal backend for every phase and chat; selected with `opencode:<backend/model>` (e.g. `opencode:anthropic/claude-sonnet-4-5`) | [opencode.ai](https://opencode.ai) or `curl -fsSL https://opencode.ai/install \| bash` |

OpenCode routes a configured backend provider (Anthropic, OpenAI, Google, a local Ollama model, and so on) through one CLI. Authenticate it with `opencode auth login`, and confirm it is ready with `opencode models`. Agentico runs every OpenCode session against a managed, per-session config and never edits your global OpenCode configuration. Opt into it explicitly with `--providers opencode`, or let it join automatically when its CLI is installed and authenticated.

### Optional

| Tool | Purpose | Install |
|------|---------|---------|
| **Go 1.25+** | Only needed to build `agentico` from source — not required when using a [prebuilt release binary](#quick-start) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ and npm** | Only needed when installing Claude Code or Codex through npm | [nodejs.org](https://nodejs.org/) |

After installing your provider CLI(s), confirm each is authenticated — `claude auth status`, `codex login status`, and/or `opencode models` (it lists models only once a backend provider is configured) — plus `gh auth status`, before launching `agentico`. A provider whose CLI is missing, too old, or not yet authenticated is filtered out at startup with a one-line notice, and the orchestrator continues on whatever providers are ready.

## How It Works

### The Feature Lifecycle

The lifecycle is profile-dependent and checkpoint-driven. Medium starts at planning. Large and Moonshot first build context, clarify intent, and explore design options. All profiles then enter the roadmap loop: create a roadmap, plan one roadmap phase at a time, implement it, commit phase anchors, and continue until the final phase reaches Final Review.

<img width="1051" height="570" alt="image" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Knowledge Base Build** — Builds or refreshes a per-repo knowledge base covering architecture, conventions, API surface, dependencies, and verification. Fresh KBs are reused and the phase is skipped.

**Inquire, Research, Design** — Turns a high-level request into explicit answers, research findings, and a design direction. Q&A artifacts are persisted and fed forward so later phases do not depend on memory alone.

**Roadmap and Phase Planning** — Creates the top-level roadmap, then a detailed plan for each roadmap phase. Large and Moonshot run plan validators; Medium skips plan critics for lower overhead.

**Implementation** — Runs a unified phase implementation loop across the phase-scoped repo set. Medium and Large rely on Final Review; Moonshot also keeps per-iteration review during implementation.

**Final Review** — Runs once after the last roadmap phase, across every touched repo that has not already been published. The phase contains its own review/fix loop. Passing Final Review moves the feature to `CodeReady`; exhausting the loop or violating the phase contract fails the feature.

**Publishing** — If auto-publish is enabled, Agentic Orchestrator commits, rebases, pushes, creates PRs, and injects cross-repo PR links automatically. If manual publish is enabled, the TUI pauses at `CodeReady` so you can review the diff and PR description first.

### Pipeline Profiles

When creating a feature, choose a pipeline depth:

| Profile | Phases | Best for |
|---------|--------|----------|
| **Medium** | Roadmap plan → per-phase plan/implement loop → Final Review → Publish | Small, well-understood changes where you already know the approach |
| **Large** | KB → Inquire → Research → Design → roadmap loop → Final Review → Publish | Most complex features (default) |
| **Moonshot** | Same phase sequence as Large, with high effort and per-iteration implementation review | High-risk or highly ambiguous changes |

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
| **Architecture** | Roadmap-level pattern consistency, module boundaries, dependency direction | Large/Moonshot, all risk levels |
| **Structural** | Phase-plan completeness, required sections, executable task shape | Large/Moonshot, all risk levels |
| **Scope** | Requirement coverage, phase sizing, over-engineering detection | Large/Moonshot, all risk levels |
| **Security** | Auth, injection, data protection calibrated to project context | Large/Moonshot, high risk |
| **Performance** | Scalability, query efficiency, resource management | Large/Moonshot, high risk |
| **Testing** | Coverage adequacy, edge cases, regression protection | Large/Moonshot phase plans, high risk |

Critics run in parallel and produce independent verdicts. If any critic requests changes, the plan is revised and re-validated automatically. Medium skips plan critics but still runs Final Review before publish.

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
3. **Pipeline** — Choose Medium, Large, or Moonshot and see the available gate options.
4. **Review** — Adjust risk level, models per phase, checkpoints (inquiry review, research review, design review, roadmap review, phase plan review, manual publish), exit criteria. Submit to start.

### Interacting with Agents

**Watch** (`a`) — Open active live work in real time. The same key becomes **Answer**, **Approve**, or **Review** when the agent needs input.

**Overview** (`o`) — Switch the dashboard right panel from Live Preview to the detailed overview. Press `l` from Overview to return to Live Preview; outside Overview, `l` still opens logs.

**Stop watching** (`Esc/Ctrl+]`) — Return to the dashboard. The agent keeps running.

### Post-Implementation Actions

Once a feature reaches code-ready or published state:

| Key | Action |
|-----|--------|
| `p` | Publish as PR (diff review → commit log → PR description → confirm) |
| `Shift+F` | Refactor — apply a refactoring prompt to the implementation |
| `b` | Rebase on main |
| `g` | View and resolve PR review comments |
| `D` | Mark as done |

### Ask Me Anything

Press `/` anywhere to open the built-in AI chat. It's a read-only session — backed by whichever provider your `utilities` model selects (Claude, Codex, or OpenCode) — that can explain how Agentic Orchestrator works, debug issues by reading feature logs and artifacts, search the codebase, and answer questions, without modifying any files.

### Keybindings

> For the complete reference, see [docs/keybindings.md](docs/keybindings.md).

## Configuration

Config lives at `~/.agentic-orchestrator/config.yaml` (auto-created on first launch). If a legacy `~/.agentic-workflow/` directory already exists, it is reused in place so existing installs keep working without a manual copy.

```yaml
defaults:
  models:
    inquiry: "sonnet[200K]"      # Model for Clarify/Inquire phase
    research: "sonnet[200K]"     # Model for research phase
    planning: "opus[1M]"         # Model for planning phase
    implementation: "opus[1M]"   # Model for implementation phase
    review: "gpt-5.4[272K]"      # Model for review phase (Codex)
    utilities: "sonnet[200K]"    # Model for chat and utility tasks
    kb_build: "sonnet[200K]"     # Model for knowledge base builds
    automatic_review: ""         # Empty selects Automatic
  automatic_review_enabled: false
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
  inquireness: high          # How often planning questions are surfaced
  pipeline: large            # Default pipeline (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Scanned for git repos on startup
```

Automatic Bash review is disabled by default, including when its keys are
missing from an older config. Set `defaults.automatic_review_enabled: true`
and optionally choose `defaults.models.automatic_review`; an empty model means
Automatic provider selection: the first eligible provider in the fixed Claude
→ OpenCode → Codex order, using its preferred cheap model. An explicit model
uses only its resolved provider and canonical model, with no substitution or
provider cascade. The workspace model remains editable while the feature is
disabled, and changes apply only to new sessions.

This feature considers only unresolved Bash permission requests after existing
policy, remembered rules, and restricted handlers have deferred. Eligible
commands receive one low-effort 10-second attempt in native zero-tool mode and
must return exact `ALLOW` or `DEFER`; all other results use the ordinary human
prompt. Results are byte-exact, session-only cached and create no durable
permission rule. After two consecutive failed attempts, a session circuit
breaker stops launching the reviewer and immediately returns later requests to
the human prompt.

The guardrail treats repo-controlled build and test code as trusted-by-design:
auto-approving commands such as `make test` or `go generate` may execute code
from Makefiles, `package.json` scripts, `//go:generate` directives, or
`conftest.py`. Enable automatic review only for repositories whose development
automation you trust.

Only a fresh leading `ALLOW` adds
`Auto-approved Bash: <redacted summary>` before execution. Each actual model
attempt emits one bounded operator event; existing decisions, guardrail
deferrals, cache hits, and disabled paths emit no per-request status or event.
When automatic review is enabled but no reviewer resolves, Agentico emits one
session-build status and operator event. The circuit breaker likewise emits one
final operator event when it disables a failing reviewer. Automatic review
does not sandbox commands or guarantee their safety; deterministic guardrails
and human approval remain the safety boundary.

### Model Overrides

Each feature can override default models during creation via the wizard (step 4). The model editor shows the Inquire phase as **Clarify**, separately from **Research**, so requirement clarification and codebase research can use different models. Models can be specified with explicit provider prefixes (e.g., `claude:opus[1M]`, `codex:gpt-5.4[272K]`, `opencode:anthropic/claude-sonnet-4-5`) or as bare ids resolved against the provider registry. There are three ways a selection reaches OpenCode, and they are distinct:

- A **plain alias** such as `opus`, `sonnet`, or `gpt-5.4` (no slash) resolves to its owning native provider (Claude or Codex) and **never** to OpenCode — OpenCode contributes only slash-form backend ids.
- The explicit **`opencode:<provider>/<model>` prefix** always routes to OpenCode, passing the backend id straight through (it works even for a backend OpenCode discovers but Agentico does not pre-list).
- A **bare slash-form backend id** such as `anthropic/claude-sonnet-4-5` (no prefix) resolves to OpenCode when it matches OpenCode's catalog. This is the form Agentico persists for the provider-neutral per-phase defaults when OpenCode is the only ready provider, so an OpenCode model **can** be a default without any `opencode:` prefix in the config.

Use `agentico --refresh-models` when a provider CLI shows new models but Agentico still shows an older catalog. Refresh runs live discovery for all ready providers, updates the version-keyed cache on success, and falls back to the previous cache with a warning if discovery fails.

### Launch Flags

```text
agentico [flags]
agentico server [flags]

Flags:
  --config <path>                  Config file (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory (default: ~/.agentic-orchestrator/features)
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --providers <list>               Restrict to specific providers (claude,codex,opencode)
  --refresh-models                 Refresh provider model catalogs before opening the TUI
  --help, -h                       Show help
  --version, -v                    Show version
```

### Updating

```text
agentico update [--check|-n]
```

Run `agentico update` to upgrade to the latest stable release. Use
`agentico update --check` (alias `-n`) to report the current and latest
available versions without installing anything; it exits `0` and prints an
already-up-to-date message when you are on the newest release.

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
| E2E Go (process-launch / API-driven) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Full TUI and process-launch behavior with the race detector. |
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
