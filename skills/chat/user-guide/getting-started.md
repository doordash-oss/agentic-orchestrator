# Getting Started with Agentic Orchestrator

Agentic Orchestrator is an Electron desktop application for supervising the **KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish** lifecycle. It runs AI agent sessions in isolated git worktrees so multiple features can progress without sharing branches or state.

The desktop app is the primary interface. `agentico` is the local server and administration CLI; running `agentico` starts the loopback server in the foreground and does **not** open the desktop app. A packaged desktop app launches and supervises its matched bundled server automatically.

## Prerequisites

- **At least one provider CLI** — Claude, Codex, and OpenCode are co-equal backends; one ready provider is enough:
  - **`claude` CLI >= 2.1.81** — authenticate with `claude auth login`
  - **`codex` CLI >= 0.116.0** — authenticate with `codex login`
  - **`opencode` CLI >= 1.17.9** — authenticate with `opencode auth login`
- **`gh` CLI** — used when the Publish phase creates pull requests
- **git** — used for branches and worktrees
- **Go 1.25+** — required only to build the server CLI from source

## Installation

Install the desktop package for your platform from the project’s GitHub Releases page, then open **Agentic Orchestrator** from the operating system. The desktop package contains the matched `agentico` server.

For headless use or development, install the standalone server CLI:

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
```

To build from source:

```bash
git clone https://github.com/doordash-oss/agentic-orchestrator.git
cd agentic-orchestrator
go build -o bin/agentico ./cmd/agentico
```

Run `agentico` or `agentico server` only when you want a foreground server. See [Configuration](configuration.md#launch-flags) for its flags.

## First Desktop Launch

The first-launch setup is a gated desktop flow backed by fresh server readiness data:

1. **Providers** — install and sign in to a provider in its own external terminal flow, then select **Check again**. The desktop app never handles provider credentials.
2. **Models** — the runtime verifies that usable models are available.
3. **Workspace** — select **Choose workspace folder…**. The runtime stores the workspace root and discovers repositories beneath it.
4. **Repository** — select an existing repository. If you choose a plain directory, the app asks for explicit consent before initializing it as a git repository.

When every readiness gate passes, the app opens the **Home** workspace. Fresh installs create runtime data under `~/.agentic-orchestrator/`. If `~/.agentic-workflow/` already exists, Agentico reuses that parent in place.

## Create a Feature

The **Features** list appears first on Home so work needing intervention stays prominent. The **New feature** form appears below it.

1. Enter a **Name** and optional **Description**.
2. Select one or more currently discovered **Repositories**.
3. Choose **New feature branch (server default)** or **Use each repository's current branch**.
4. Review the read-only **Server defaults** summary, then select **Create feature**.

The current desktop form uses the runtime’s pipeline, model, and inquireness defaults. Editing those defaults, per-feature checkpoints, models, risk, or exit criteria in the desktop app is not delivered yet; edit `config.yaml` before creation when those values must differ.

Creation opens a feature tab and starts durable setup. The tab shows setup tasks, attempts, safe errors, and a server-authorized **Retry setup** control when setup fails. Retry continues the same feature and re-runs only unfinished setup tasks.

## Start and Watch Work

After setup completes, the feature tab shows **Ready to start**. Select **Start** only when the runtime action catalogue enables it; if Start is disabled, the reasons shown beside it come from the server.

Once the runtime reports an active run, the same tab updates in place with:

- a phase spine and authoritative feature status;
- a **Signal trace** that backfills the current session and then receives live output;
- expandable groups for routine tool, task, progress, and file activity;
- a **Validated source** inspector for the selected trace entry; and
- a connecting, live, stale, resetting, or unavailable stream status.

The timeline follows new output while you are at the newest entry. Scrolling back preserves your reading position and reveals **Jump to live**. Select an entry to inspect its validated source record; select **Close raw inspector** to clear the inspector.

The only workspace-tab shortcuts currently implemented are Left Arrow and Right Arrow while focus is on a tab. Other actions use the labeled desktop controls.

## Stop Work

When the server action catalogue authorizes Stop, select **Stop** in the feature tab. The impact dialog names the feature, active phase, and affected live-session count.

- **Keep running** or Escape closes the dialog without changing the feature.
- **Confirm stop** asks the runtime to stop the feature and waits for authoritative refreshed state.

Validated transcript content remains available after the stream finishes. Stop does not expose Resume, Retry phase, Rewind, or post-publish actions; those desktop capabilities are pending.

## Current Desktop Scope

The current Electron app delivers first-launch readiness, the intervention-first Home list, feature creation and durable setup, server-authorized Start/Stop, live transcript history, raw inspection, reconnect/reset presentation, theme selection, and app-owned server recovery.

These workflow-engine capabilities do **not** yet have Electron controls: permission decisions, planning questions and review gates, phase resume/retry/rewind, artifact browsing or editing, runtime configuration editing, post-publish actions, notifications, tray behavior, signing, and in-app updates. Do not use terminal-era shortcuts for them; wait for a labeled desktop control in a later release.

## State Directory

Fresh installs use:

```text
~/.agentic-orchestrator/
  config.yaml              # Runtime configuration
  features/<id>/           # Feature state and run artifacts
  worktrees/<slug>/        # Isolated feature worktrees
  skills/                  # Reconciled skill definitions
  guidelines/              # Reconciled guideline definitions
  permissions/             # Cached permission rules
  agentico.log             # Runtime log output
```

Existing installs with `~/.agentic-workflow/` retain the same layout beneath that parent; no automatic copy or migration runs.

## Next Steps

- Learn the engine states and the currently exposed controls in [Feature Lifecycle](feature-lifecycle.md).
- Review the runtime schema in [Configuration](configuration.md).
- Understand current permission behavior and desktop limitations in [Permissions](permissions.md).
- Review [Verification](verification.md) before contributing source changes.
