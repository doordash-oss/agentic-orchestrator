# Getting Started with Agentic Orchestrator

Agentic Orchestrator is an Electron desktop application for supervising the **KB Build → Inquire → Research → Design → Plan → Implement → Review → Publish** lifecycle. It runs AI agent sessions in isolated git worktrees so multiple features can progress without sharing branches or state.

The desktop app is the primary interface. `agentico` is the local server and administration CLI; running `agentico` with no subcommand launches or focuses the installed desktop app, which starts and supervises its matched bundled server. Use `agentico server` only for an explicit foreground loopback server.

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

Run `agentico server` only when you want a foreground server. See [Configuration](configuration.md#launch-flags) for its flags.

## First Desktop Launch

The first-launch setup is a gated desktop flow backed by fresh server readiness data:

1. **Providers** — install and sign in to a provider in its own external terminal flow, then select **Check again**. The desktop app never handles provider credentials.
2. **Models** — the runtime verifies that usable models are available.
3. **Workspace** — select **Choose workspace folder…**. The runtime stores the workspace root and discovers repositories beneath it.
4. **Repository** — select an existing repository. If you choose a plain directory, the app asks for explicit consent before initializing it as a git repository.

When every readiness gate passes, the app opens the **Home** workspace. Agentico creates runtime data under `~/.agentic-orchestrator/`.

## Create a Feature

The **Features** list appears first on Home so work needing intervention stays prominent. Select **New feature** to open the creation wizard, which has four steps:

1. **What** — enter a **Name** and optional **Description**. Paste or drop images into the description, and use the `@` file picker to attach files with fuzzy autocomplete.
2. **Where** — select one or more currently discovered **Repositories**. Browse workspace directories and initialize new repositories on the fly with explicit consent.
3. **Pipeline** — choose a profile (Medium / Large / Moonshot) and review the effective checkpoint summary.
4. **Review** — adjust per-phase models, risk level, checkpoints, exit criteria, inquireness, and skill scoping, then select **Create feature**.

The wizard prefills from the runtime's pipeline, model, and inquireness defaults; every value on the Review step can be overridden per feature before creation.

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

Workspace tabs respond to Left Arrow and Right Arrow while focus is on a tab. The desktop app also exposes a full keyboard map, a command palette (Cmd/Ctrl+K), and a help overlay (Cmd/Ctrl+/) listing every reachable action and its shortcut.

## Stop Work

When the server action catalogue authorizes Stop, select **Stop** in the feature tab. The impact dialog names the feature, active phase, and affected live-session count.

- **Keep running** or Escape closes the dialog without changing the feature.
- **Confirm stop** asks the runtime to stop the feature and waits for authoritative refreshed state.

Validated transcript content remains available after the stream finishes. After Stop, the feature cockpit continues to expose catalogue-driven Resume, Retry phase, Rewind, and post-publish actions as the server authorizes them.

## Current Desktop Scope

The Electron app delivers first-launch readiness, the intervention-first Home list, the four-step creation wizard, feature creation and durable setup, server-authorized Start/Stop/Resume/Retry/Rewind, live transcript history, raw inspection, reconnect/reset presentation, theme selection, app-owned server recovery, permission decisions, planning and review gates, artifact browsing and editing, runtime and feature configuration editing, post-publish actions (publish, rebase, merge, refactor, review comments, Done, cleanup, delete), desktop notifications, recovery, Ask Me Anything chat, and in-app updates.

Every action is reachable through a labeled desktop control and is authorized by the current server action catalogue. Do not use retired terminal-era shortcuts; the desktop app exposes its own keyboard map, command palette, and help overlay.

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

## Next Steps

- Learn the engine states and the currently exposed controls in [Feature Lifecycle](feature-lifecycle.md).
- Review the runtime schema in [Configuration](configuration.md).
- Understand current permission behavior in [Permissions](permissions.md).
- Review [Verification](verification.md) before contributing source changes.
