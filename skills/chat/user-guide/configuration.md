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
    research: opus
    planning: opus
    implementation: opus
    review: gpt-5.4
    utilities: sonnet
    kb_build: opus
```

| Role | Default | Used For |
|------|---------|----------|
| `research` | `opus` | Research, inquiry, design phases |
| `planning` | `opus` | Plan creation and roadmap generation |
| `implementation` | `opus` | Code implementation |
| `review` | `gpt-5.4` | Final Review loop |
| `utilities` | `sonnet` | Chat (AMA), utility skills |
| `kb_build` | `opus` | Knowledge base construction |

Model names can be bare (e.g., `opus`) or prefixed with a provider (e.g., `claude:opus`, `codex:gpt-5.4`). Bare names are resolved against the registry, which merges each provider's hardcoded catalog (see `internal/llm/claude` and `internal/llm/codex`). Configured model names are canonicalized at startup so the on-disk config reflects the registry's canonical spelling.

## Pipeline Configuration

`defaults.pipeline` sets the default pipeline profile for new features. Valid values: `medium`, `large`, `moonshot`. Default: `large`. See [Feature Lifecycle](feature-lifecycle.md) for details on what each profile includes.

## Checkpoint Configuration

Checkpoints are review gates that pause the pipeline between phases for human review:

```yaml
defaults:
  checkpoints:
    inquiry_review: false
    research_review: false
    design_review: false
    plan_review: false
    manual_publish: true
```

| Checkpoint | Gates Before | Default |
|------------|-------------|---------|
| `inquiry_review` | Research phase | `false` |
| `research_review` | Design phase | `false` |
| `design_review` | Plan phase | `false` |
| `plan_review` | Implementation phase | `false` |
| `manual_publish` | Publish step | `true` |

When a feature is created, these defaults are combined with the pipeline profile's defaults (see [Feature Lifecycle — Checkpoints](feature-lifecycle.md#checkpoints-review-gates)). Individual checkpoints can be toggled per-feature in the creation wizard.

## Iteration Limits

| Field | Default | Description |
|-------|---------|-------------|
| `max_iterations` | `10` | Maximum iterations per phase before failing |
| `max_consecutive_failures` | `3` | Consecutive failures before aborting a phase |
| `max_consecutive_no_progress` | `3` | Consecutive no-progress signals before aborting |
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
        design_review: true
        plan_review: true
        manual_publish: true
```

Named repositories with optional per-pipeline checkpoint overrides. Repos are auto-registered when selected in the feature creation wizard.

The `pipeline_gates` map is keyed by pipeline profile name and overrides the default checkpoints for that profile when creating features in this repo.

## Pipeline Preferences

`defaults.pipeline_preferences` stores last-used settings per pipeline profile (models, inquireness). When you customize these in the wizard, they are saved here and pre-filled the next time you use the same profile.

## Notifications

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

Launch flags configure how the TUI starts. Start Agentic Orchestrator with `agentico [flags]`. Feature name, description, repos, checkpoint selection, and publish gating are selected inside the feature creation wizard.

| Flag | Description | Default |
|------|-------------|---------|
| `--config <path>` | Config file path | `~/.agentic-orchestrator/config.yaml` |
| `--state-dir <path>` | State directory path | `~/.agentic-orchestrator/features` |
| `--providers <list>` | Comma-separated provider list (e.g., `claude,codex`) | all detected |
| `--dangerously-skip-permissions` | Skip all permission prompts | `false` |
| `--help`, `-h` | Print usage | - |
| `--version`, `-v` | Print version | - |

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
