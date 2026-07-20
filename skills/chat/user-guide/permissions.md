# Permissions

Agentic Orchestrator mediates provider tool use through session handlers and cached permission rules. This page separates the runtime permission model from the controls that exist in the current Electron app.

## Current Desktop Availability

Permission request cards and Approve, Approve and remember, and Deny controls are **pending** in the Electron app. The current live **Signal trace** can show validated tool activity, but it is not a permission prompt and cannot answer one.

Do not use superseded single-key permission shortcuts. When a workflow is blocked on a permission request, the current desktop release has no supported control for resolving it. Start workflows only with permission rules appropriate for the provider and repository, or use the server launch option described below in a trusted environment.

## Runtime Permission Types

### Automatically handled

Session policy can approve categories such as:

- read-only tools, including Read, Glob, Grep, WebSearch, WebFetch, and LSP;
- file edits during implementation phases;
- delegated agent work; and
- read-only git commands such as `git status`, `git diff`, `git log`, and `git show`.

When the optional Automatic Bash review feature is enabled, an unresolved
canonical Bash request first checks a deterministic fast path. Curated
build/test commands auto-approve without a model. Every other reviewable
command reaches one model review—including dangerous commands such as
`rm -rf`, `sudo`, and `curl | sh`—and an exact `ALLOW` continues
automatically. `DEFER` or any failure requires a compatible permission client.
Automatic review creates no durable permission rule.

### Approval required

A shell command not covered by session policy or a cached rule requires an answer. Common examples include:

- builds (`go build`, `npm run build`, `make`);
- tests (`go test`, `npm test`, `pytest`);
- git writes (`git push`, `git commit`, `git checkout`); and
- package management (`npm install`, `go mod tidy`).

Whether a particular request pauses depends on the provider adapter, active phase policy, and matching cached rules.

## Default Cached Rules

Agentic Orchestrator bootstraps global allow rules for common read-only shell commands:

| Category | Commands |
|----------|----------|
| File inspection | `ls`, `pwd`, `cat`, `head`, `tail`, `wc` |
| Search | `grep`, `rg` |
| File metadata | `stat`, `file`, `find`, `du` |
| Git read-only | `git status`, `git diff`, `git log`, `git show` |

Each default is a wildcard pattern such as `Bash(ls *)`. On launch, missing defaults are appended and existing user rules are preserved. Identical `tool_pattern` and `effect` pairs are deduplicated.

## Pattern Inference

When an **Allow & Remember** decision is available through a compatible client, the runtime infers a bounded wildcard pattern from the command. It unwraps the shell input, uses the last command in a chain, removes piped suffixes, tokenizes the command, and retains a recognized subcommand when useful.

| Command | Inferred pattern |
|---------|------------------|
| `npm test --coverage` | `Bash(npm test *)` |
| `go build ./...` | `Bash(go build *)` |
| `ls -la /tmp` | `Bash(ls *)` |
| `touch /tmp/phase_complete` | `Bash(touch *)` |
| `cd /path && make build` | `Bash(make build *)` |
| `git push origin main` | `Bash(git push *)` |

## Permission Cache

Rules are stored as JSON with deny-wins precedence:

| Scope | File | Applies to |
|-------|------|------------|
| Global | `~/.agentic-orchestrator/permissions/global.json` | All repositories |
| Per-repo | `~/.agentic-orchestrator/permissions/<repoName>.json` | One repository |

Legacy installations keep using `~/.agentic-workflow/permissions/` in place.

```json
{
  "rules": [
    {"tool_pattern": "Bash(npm test *)", "effect": "allow"},
    {"tool_pattern": "Bash(rm *)", "effect": "deny"}
  ]
}
```

Deny rules are checked first, so a matching deny is not overridden by a broader allow. Cache updates use atomic temporary-file replacement.

## Repository Settings Import

When a feature is created, Agentic Orchestrator imports permission patterns from `.claude/settings.json` and `.claude/settings.local.json` in its repositories:

- `allow` patterns become repository-scoped allow rules;
- `deny` patterns become repository-scoped deny rules; and
- Claude CLI colon wildcards such as `Bash(go:*)` are normalized to `Bash(go *)`.

Imported rules are deduplicated against the existing cache.

## OpenCode tool mediation

OpenCode tool requests use the same Agentico permission layer as Claude and Codex. An ACP `session/request_permission` request is classified by tool kind and mapped to surfaces such as `Bash`, `Write`, `WebFetch`, `WebSearch`, or `AskUserQuestion`. Unknown permission kinds are not silently allowed.

Agentico supplies OpenCode with a managed per-session config under the state directory through `OPENCODE_CONFIG` and `OPENCODE_CONFIG_CONTENT`. It never edits the global OpenCode configuration. The managed config bounds execute, edit, write, fetch, search, task, question, and read access to the session’s mounted roots.

## `--dangerously-skip-permissions`

Starting a foreground server with `--dangerously-skip-permissions` auto-approves gated tool surfaces:

```bash
agentico --dangerously-skip-permissions
```

OpenCode’s question surface still pauses for you so `AskUserQuestion` retains the same semantics as Claude and Codex. Read-only mounts remain non-writable.

This flag removes a significant safety boundary. Use it only for trusted repositories, prompts, dependencies, and provider sessions. The current Electron app does not provide the previous client’s warning badges or color treatment, so confirm the server launch arguments outside the app.

## Evaluation Order

For each tool request, the runtime evaluates:

1. the session handler’s phase-specific policy;
2. global and per-repository cached rules, with deny winning; and
3. the deterministic Bash fast path, when automatic review is enabled;
4. one bounded automatic model review for other valid commands; and
5. a user decision through a compatible permission client if no earlier step allows the request.

Automatic review never overrides earlier decisions, stores no durable
permission rule, and is not command sandboxing. Valid commands receive up to
two model attempts sharing one overall one-minute timeout. Only transient
launch, handshake, transport, rate-limit, and server failures receive the
single retry. Two consecutive timeouts start a 30-second cooldown with a
half-open retry; two consecutive provider or protocol failures disable the
model path for the session. If no reviewer is available, deterministic
approvals still fast-path while other requests proceed to the compatible
permission client. The Electron permission client is still pending.

See
[Configuration — Automatic Bash Review](configuration.md#automatic-bash-review)
for the guardrail, reviewer, lifecycle, and evidence contract.
