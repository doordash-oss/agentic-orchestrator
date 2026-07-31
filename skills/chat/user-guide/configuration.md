# Configuration

Agentic Orchestrator is configured via a YAML file that controls model selection, pipeline behavior, review gates, and more. This guide covers every configuration option.

## Config File Location

- **Config file**: `~/.agentic-orchestrator/config.yaml` (override: `--config <path>`)
- **State directory**: `~/.agentic-orchestrator/features/` (override: `--state-dir <path>`)
- **Worktrees**: `~/.agentic-orchestrator/worktrees/`

If a legacy `~/.agentic-workflow/` directory already exists, it is reused in
place as the runtime parent so existing installs keep working. Fresh installs
default to the new `~/.agentic-orchestrator/` namespace.

The config file is auto-created on first launch with sensible defaults.

## Model Configuration

The `defaults.models` section assigns a model to each phase role:

```yaml
defaults:
  models:
    research: "sonnet[200K]"
    planning: "opus[1M]"
    implementation: "opus[1M]"
    review: "gpt-5.4[272K]"
    utilities: "sonnet[200K]"
    kb_build: "sonnet[200K]"
```

| Role | Default | Used For |
|------|---------|----------|
| `research` | `sonnet[200K]` | Research, inquiry, design phases |
| `planning` | `opus[1M]` | Plan creation and roadmap generation |
| `implementation` | `opus[1M]` | Code implementation |
| `review` | `gpt-5.4[272K]` | Final Review loop |
| `utilities` | `sonnet[200K]` | Chat (AMA), utility skills |
| `kb_build` | `sonnet[200K]` | Knowledge base construction |

## Automatic Bash Review

Automatic Bash review uses two workspace keys:

```yaml
defaults:
  automatic_review_enabled: false
  models:
    automatic_review: ""
```

The full names are `defaults.automatic_review_enabled` and
`defaults.models.automatic_review`. The feature is disabled by default. When
the enabled key is absent, as in a legacy config, it remains disabled; a
missing reviewer-model key has the same empty **Automatic** value as a fresh
config. The workspace Models row remains editable while automatic review is
disabled; saved changes apply only to new sessions because each session
snapshots the enabled flag and resolved reviewer.

Automatic selection uses the fixed provider order Claude → OpenCode → Codex
and chooses the first eligible provider's preferred cheap model. A
provider-qualified explicit selection uses only the named provider and
canonical model; a bare selection must resolve uniquely. Neither form
substitutes another model or cascades to another provider. If the selection is
unavailable, deterministic fast-path commands can still run automatically
while all other commands use the normal human permission flow.

### Policy and guardrail boundary

Automatic review runs only for an unresolved canonical Bash request after the
existing permission policy genuinely defers. It never overrides an existing
allow, deny, or error, a remembered permission rule, skip-permissions
behavior, a safe-create decision, or a specialized restricted policy.

The deterministic guardrail is a fast path, not reviewer eligibility
filtering. It parses every visible chain and pipeline segment and approves the
command immediately only when every segment matches the curated policy.
Curated direct development families include test, lint/check, build/compile,
format, generation, documentation, benchmark, and static-analysis commands,
with command-specific flag and path rules. Project runners such as Make, Just,
Task, Bazel, Gradle, Maven, and package scripts require an explicit
conventional development target. Target names are split on `-`, `_`, and `:`;
a prohibited target component such as `install`, `deploy`, `clean`, `watch`,
`run`, or `push` prevents deterministic approval.

Strictly read-only source-control commands may fast-path. Descriptor-only
routing such as `2>&1` or `1>&2`, and stdout/stderr redirection to `/dev/null`,
are recognized. Ambiguous shell syntax, nested execution or substitution,
sensitive paths, direct networking, package mutation, non-read-only source
control, direct scripts or interpreters, unrecognized writes, prohibited task
targets, and other guardrail misses are not hard-denied: when reviewable, they
reach the model. This explicitly includes dangerous commands such as `rm -rf`,
`sudo`, and `curl | sh`; an exact model `ALLOW` auto-approves execution.

Automatic review does not sandbox commands and does not guarantee command
safety. The deterministic layer is only a velocity optimization. For
everything else, the reviewer model's judgment is the boundary before the
human, and that reviewer is a fallible, promptable component. Repo-controlled
build and test code is trusted-by-design: a fast-pathed command such as
`make test` or `go generate` may execute Makefiles, `package.json` scripts,
`//go:generate` directives, or `conftest.py`. Enable automatic review only
when that tradeoff is acceptable.

### Reviewer and lifecycle contract

The reviewer receives only the canonical tool name, command, working
directory, and declared writable roots. Interpolated prompt fields are
control-sanitized and enclosed as nonce-delimited untrusted data; the original
byte-exact command remains unchanged for caching and execution. The reviewer
runs through the selected provider's native zero-tool mode, with no session
persistence or user/project customization, at low effort, for one 30-second
attempt. The response must be the exact case-sensitive `ALLOW` or `DEFER`
token. There are no retries and no provider cascade after an attempt.
`DEFER`, timeout, malformed output, provider failure, unexpected interaction,
and cancellation silently return to the ordinary human prompt.

Exact `ALLOW` and `DEFER` decisions use a byte-exact session-only cache.
Long-tail requests are serialized within the session, and a model cache hit
launches no new reviewer process or side effect. Deterministic fast paths skip
this mutex and cache entirely. Two consecutive timeouts start a 30-second
cooldown; after it expires, one half-open attempt either restores review or
starts another cooldown. Cancellation does not count against reviewer health.
Two consecutive provider, protocol, malformed-response, or
unexpected-interaction failures mark only the reviewer unavailable for the
rest of that session. During either breaker state, long-tail requests return
immediately to the human prompt while build/test fast paths remain available.
Nothing is reused across sessions, and automatic approval creates no durable
permission rule, remembered cache entry, or permission audit record.

### Transparency and operational evidence

Every deterministic approval appends
`Auto-approved Bash (fast path): <summary>`. Only a fresh leading model
`ALLOW` appends `Auto-approved Bash: <summary>`. Both summaries collapse
whitespace, strip unsafe controls, apply the permission-audit secret redaction
vocabulary, stay valid UTF-8, and keep the complete line at most 200 bytes
including `...` when truncated. Live preview, active attach, completed-session
attach history, transcript reads, and session-output streaming preserve the
same text and message-log position.

Every deterministic approval emits one bounded operator event with outcome
`fast_path`, zero duration, and empty provider/model fields. Every actual model
attempt emits one bounded operator event with original feature/session
context, reviewer identity, total duration, cumulative process-launch,
first-output, and review-completion timings, redacted command summary, and an
outcome of `allow`, `defer`, `timeout`, `malformed_response`, `provider_error`,
`unexpected_interaction`, or `canceled`. Approval events also record whether
status persistence succeeded. Unreviewable commands, non-Bash requests,
existing decisions, disabled paths, and model session-cache hits emit neither
per-request status nor an automatic-review event. If automatic review is
enabled but no reviewer is available, Agentico emits one session-build status
and one `automatic_review.unavailable` operator event with the reason;
fast-path approvals continue. A timeout cooldown emits an
`automatic_review.unavailable` event each time it begins; a permanent
session-level breaker emits one final event and then stays silent for later
model requests. Status-delivery and observer failures are best-effort
side-effect failures: they never change the permission decision or cause a
retry.

Model names can use canonical context-window IDs (e.g., `opus[1M]`) or provider prefixes (e.g., `claude:opus[1M]`, `codex:gpt-5.4[272K]`, `opencode:anthropic/claude-sonnet-4-5`). Bare aliases such as `opus` are still accepted and resolved against the registry, which merges each provider's hardcoded catalog (see `internal/llm/claude`, `internal/llm/codex`, and `internal/llm/opencode`). Configured model names are canonicalized at startup so the on-disk config reflects the registry's canonical spelling.

### OpenCode model IDs

An OpenCode selection can take three forms, and they resolve differently:

- **`opencode:<provider>/<model>` prefix** — always routes to OpenCode. The value after the prefix is OpenCode's own backend id in `<provider>/<model>` form, for example `opencode:anthropic/claude-sonnet-4-5`, `opencode:openai/gpt-5`, or `opencode:ollama/llama3.1` for a local model. The prefix works even for a backend id Agentico does not pre-list, passing it straight through to OpenCode.
- **bare slash-form backend id** — a value such as `anthropic/claude-sonnet-4-5` with no `opencode:` prefix resolves to OpenCode when it matches OpenCode's catalog, because OpenCode's ids are the only slash-form ids in the registry. This is the form Agentico persists for the provider-neutral per-phase defaults when OpenCode is the only ready provider, so an OpenCode model **can** become a default without an explicit prefix.
- **plain alias** — a bare alias such as `opus`, `sonnet`, or `gpt-5.4` (no slash) resolves to its owning native provider (Claude or Codex) and never to OpenCode; OpenCode contributes only slash-form ids to the registry.

When a ready OpenCode CLI is detected, Agentico discovers its live model catalog with `opencode models --verbose` and contributes those entries to the per-phase pickers; if discovery fails it falls back to a small built-in OpenCode catalog. Context-window suffixes such as `[200K]` are Agentico selection metadata and are stripped before the native backend id is handed to OpenCode. When OpenCode does not report pricing for a model (for example a local Ollama model), Agentico records that session at zero cost rather than guessing — the run still completes; only the cost roll-up shows `$0.00`.

Use `agentico --refresh-models` when a provider CLI shows new models but Agentico still shows an older catalog. Refresh runs live discovery for all ready providers, updates the version-keyed cache on success, and falls back to the previous cache with a warning if discovery fails.

See [Provider Selection](#provider-selection) for installing, authenticating, and troubleshooting OpenCode.

## Provider Selection

Agentic Orchestrator needs at least one authenticated provider CLI. Claude, Codex, and OpenCode are co-equal: each phase's default is the best available model for that role across every detected provider, and you can override any phase per the table above.

| Provider | CLI | Minimum version | Authenticate | Readiness check |
|----------|-----|-----------------|--------------|-----------------|
| Claude | `claude` | 2.1.81 | `claude auth login` or `ANTHROPIC_API_KEY` | `claude auth status` |
| Codex | `codex` | 0.116.0 | `codex login` | `codex login status` |
| OpenCode | `opencode` | 1.17.9 | `opencode auth login` | `opencode models` |

Restrict the orchestrator to the CLIs you actually have with `--providers <list>` (e.g. `--providers claude,codex,opencode` or `--providers opencode`). With no flag, every installed and ready provider is registered.

### Custom CLI binary

Each provider invokes a default executable name (`claude`, `codex`, `opencode`). Override the binary Agentico launches with the `providers.<name>.cli` setting — useful when the CLI is installed under a different name or wrapped by a launcher script:

```yaml
providers:
  claude:
    cli: fcc-claude
  codex:
    cli: /opt/tools/bin/codex
```

The value can be a bare name resolved on `PATH` or an absolute path. It applies everywhere Agentico shells out to that provider — CLI detection, authentication/readiness checks, version checks, model discovery, and session launch — so the custom binary must be on `PATH` (or given as an absolute path) and behave like the provider CLI it replaces. When unset, the built-in default name is used, so existing configs are unaffected.

### OpenCode

Install OpenCode from [opencode.ai](https://opencode.ai) (`curl -fsSL https://opencode.ai/install | bash`) and authenticate a backend provider with `opencode auth login`. OpenCode is a router in front of a backend model (Anthropic, OpenAI, Google, a local Ollama model, and so on); Agentico selects it with the explicit `opencode:<backend/model>` prefix or a bare slash-form backend id, as documented under [Model Configuration](#opencode-model-ids).

Startup gates OpenCode on three checks before it can route a session: the `opencode` CLI is on `PATH`, its version is at least `1.17.9` (the minimum Agentico enforces for the ACP surface it uses), and `opencode models` reports at least one model (which only happens once a backend provider is authenticated). A provider that fails any check is filtered out with a one-line startup notice and the run continues on whatever else is ready; if OpenCode is the only selected provider and it fails, startup stops with that provider-specific remedy.

**Managed-session isolation.** Every OpenCode session runs against an Agentico-owned config generated under the state directory, passed via `OPENCODE_CONFIG`/`OPENCODE_CONFIG_CONTENT`. Agentico never writes into your global OpenCode configuration: the managed config pins the backend model, the role instructions, the permission map, and noninteractive runtime settings (no transcript sharing, no auto-update), while still merging your global provider credentials so authentication keeps working.

### Troubleshooting providers

| Symptom | Cause | Fix |
|---------|-------|-----|
| `Provider opencode CLI was not found` | OpenCode is not installed | `curl -fsSL https://opencode.ai/install \| bash`, then relaunch |
| `opencode … below the minimum supported version` | Installed OpenCode is older than `1.17.9` | Upgrade OpenCode, then relaunch |
| `Provider opencode is not configured` | No backend provider authenticated | `opencode auth login`, confirm with `opencode models` |
| OpenCode models missing from the picker | Live catalog discovery failed | Agentico falls back to a small built-in OpenCode catalog; rerun `opencode models --verbose` to debug, or select with the explicit `opencode:<backend/model>` form |
| OpenCode session cost shows `$0.00` | OpenCode reported no pricing for that model (e.g. a local model) | Expected — the run still completes; only the cost roll-up is zero |

## Pipeline Configuration

`defaults.pipeline` sets the default pipeline profile for new features. Valid values: `medium`, `large`, `moonshot`. Default: `large`. See [Feature Lifecycle](feature-lifecycle.md) for details on what each profile includes.

## Checkpoint Configuration

Checkpoints are review gates that pause the pipeline between phases for human review:

```yaml
defaults:
  checkpoints:
    inquiry_review: true
    research_review: true
    design_review: true
    roadmap_review: true
    phase_plan_review: true
    manual_publish: true
```

| Checkpoint | Gates Before | Default |
|------------|-------------|---------|
| `inquiry_review` | Research phase | `true` |
| `research_review` | Design phase | `true` |
| `design_review` | Plan phase | `true` |
| `roadmap_review` | Phase planning | `true` |
| `phase_plan_review` | Implementation phase | `true` |
| `manual_publish` | Publish step | `true` |

When a feature is created, these defaults are projected through the selected pipeline profile (see [Feature Lifecycle — Checkpoints](feature-lifecycle.md#checkpoints-review-gates)). Individual checkpoints can be toggled per-feature in the creation wizard.

Omitted checkpoint fields in `defaults.checkpoints` or repo `pipeline_gates` default to `true` when the checkpoint is compatible with the selected pipeline. Config saves write all checkpoint fields explicitly. The legacy `plan_review` key is ignored by new config handling; replace it with `roadmap_review` and `phase_plan_review`.

In the TUI, Roadmap Review controls the planning review group and Phase Plan Review appears beneath it. Turning Roadmap Review off also turns Phase Plan Review off; turning it back on enables Phase Plan Review by default. Advanced YAML can still set `phase_plan_review: true` with `roadmap_review: false`, and that runtime combination is honored.

## Iteration Limits

| Field | Default | Description |
|-------|---------|-------------|
| `max_iterations` | `10` | Maximum iterations per phase before failing |
| `max_consecutive_failures` | `3` | Consecutive failures before aborting a phase |
| `max_consecutive_no_progress` | `3` | Consecutive no-progress signals before aborting |
| `max_design_iterations` | `5` | Maximum Design author/critic attempts before human review |
| `max_phase_plan_iterations` | `10` | Maximum iterations for plan creation/validation |

## Inquireness

`defaults.inquireness` controls how often the harness surfaces planning questions to you. Valid values: `none` (hide eligible confidence-qualified answers unless manual input is required), `medium` (surface key planning questions), `high` (surface more planning questions). Default: `medium`.

Supported [grill-me] planning phases may include multiple-choice answers with confidence scores. When an answer meets the current inquireness threshold, the harness accepts it for the session and records it in `qa-answers.md` with an auto-picked confidence annotation.

## Exit Criteria

```yaml
defaults:
  exit_criteria: |
    - Feature fully implemented per plan
    - Unit tests added/updated as needed
    - Integration tests added/updated as needed
    - Code formatted per project standards
    - Relevant tests pass
    - No linting errors
```

A markdown checklist included in implementation prompts as success criteria. Customize this to match your project's quality standards.

## Workspace Roots

```yaml
workspace_roots:
  - /Users/you/Projects
  - /Users/you/Work
```

Directories scanned for git repositories at startup. Discovered repos appear in the feature creation wizard's repo picker. Workspace roots are configured during first launch and can be managed via `Shift+W` from the dashboard.

## Repository Configuration

```yaml
repos:
  my-project:
    path: /Users/you/Projects/my-project
    pipeline_gates:
      moonshot:
        inquiry_review: true
        research_review: true
        design_review: true
        roadmap_review: true
        phase_plan_review: true
        manual_publish: true
```

Named repositories with optional per-pipeline checkpoint overrides. Repos are auto-registered when selected in the feature creation wizard.

The `pipeline_gates` map is keyed by pipeline profile name and overrides the default checkpoints for that profile when creating features in this repo.

## Pipeline Preferences

`defaults.pipeline_preferences` stores last-used settings per pipeline profile (models, inquireness). When you customize these in the wizard, they are saved here and pre-filled the next time you use the same profile.

## Notifications

The workspace input-alert default can be changed from the TUI with `Shift+E` → Behavior → Input Alerts. A specific feature can override that default with `e` → Behavior → Input Alerts (`default`, `enabled`, or `muted`).

| Field | Description |
|-------|-------------|
| `notifications.terminal_bundle_id` | Overrides auto-detected terminal app bundle ID for macOS notifications |
| `notifications.mute_feature_input` | Suppresses notifications when an agent is waiting for manual input |

## UI Settings

| Field | Description |
|-------|-------------|
| `ui.keyboard_layout` | Keyboard layout for alternative keybindings. Supported: `""` (US default), `"nordic"` |
| `ui.collapsed_sections` | Dashboard sections collapsed by default (e.g., `["published", "completed"]`) |

## Observability

```yaml
observability:
  events: true
  otel_enabled: false
  otel_endpoint: ""
  otel_insecure: false
  otel_service_name: agentico
```

| Field | Default | Description |
|-------|---------|-------------|
| `events` | `true` | Enable JSONL event recording per feature |
| `otel_enabled` | `false` | Enable OpenTelemetry trace export |
| `otel_endpoint` | `""` | OTLP endpoint URL |
| `otel_insecure` | `false` | Allow insecure OTLP connections |
| `otel_service_name` | `"agentico"` | Service name for OTel traces |

## Launch Flags

Launch flags configure how the terminal UI or foreground server starts. `agentico [flags]` starts a local loopback REST runtime and connects the terminal UI through the API-backed client. `agentico server [flags]` starts the foreground loopback HTTP server. Feature name, description, repos, checkpoint selection, and publish gating are selected inside the feature creation wizard.

| Flag | Description | Default |
|------|-------------|---------|
| `--config <path>` | Config file path | `~/.agentic-orchestrator/config.yaml` |
| `--state-dir <path>` | State directory path | `~/.agentic-orchestrator/features` |
| `--providers <list>` | Comma-separated provider list (e.g., `claude,codex,opencode`) | all detected |
| `--refresh-models` | Refresh provider model catalogs before opening the TUI | `false` |
| `--dangerously-skip-permissions` | Skip all permission prompts | `false` |
| `--help`, `-h` | Print usage | - |
| `--version`, `-v` | Print version | - |

The same launch flags are accepted by `agentico server`.

## Updating

Run `agentico update` to upgrade to the latest stable release, or `agentico update [--check|-n]` to report the current and latest available versions without installing. The `--check` form (alias `-n`) exits `0` and prints an already-up-to-date message when you are already on the newest release.

| Invocation | Description |
|------------|-------------|
| `agentico update` | Upgrade to the latest stable release |
| `agentico update --check` / `agentico update -n` | Check for a newer release without installing |

## State Directory Layout

```
~/.agentic-orchestrator/
  config.yaml              # Main configuration
  features/<id>/           # Per-feature state (feature.yaml, phase output dirs)
  worktrees/<slug>/        # Isolated git worktrees per feature
  skills/                  # Reconciled skill definitions
  guidelines/              # Reconciled guideline definitions
  permissions/             # Permission cache (global.json, per-repo JSON)
  agentico.log             # Redirected stderr/log output while the TUI is running
```

Legacy installs that already have `~/.agentic-workflow/` keep using that
directory in place; the layout above is identical, only the parent name
differs.
