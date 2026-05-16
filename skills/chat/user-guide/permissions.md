# Permissions

Agentic Orchestrator mediates all agent tool use through a layered permission system. Some tools are auto-approved, while others require explicit user consent. This guide covers how permissions work, how they are cached, and how to configure them.

## Permission Types

### Auto-Approved (No Prompt)

The following tool categories are approved automatically and never prompt:

- **Read-only tools** — Read, Glob, Grep, WebSearch, WebFetch, LSP
- **File edits** — Edit and Write (during implementation phases)
- **Agent spawning** — Agent tool for sub-agent creation
- **Git read commands** — `git status`, `git diff`, `git log`, `git show` (and any arguments)

### Requires Approval

Any Bash command not covered by the default or cached rules requires user approval. Common examples:

- Build commands (`go build`, `npm run build`, `make`)
- Test commands (`go test`, `npm test`, `pytest`)
- Git write commands (`git push`, `git commit`, `git checkout`)
- Package management (`npm install`, `go mod tidy`)
- Any other shell command

## Default Permissions

On first launch, Agentic Orchestrator bootstraps a set of default global rules that auto-approve common read-only shell commands. These 16 defaults are:

| Category | Commands |
|----------|----------|
| File inspection | `ls`, `pwd`, `cat`, `head`, `tail`, `wc` |
| Search | `grep`, `rg` |
| File metadata | `stat`, `file`, `find`, `du` |
| Git read-only | `git status`, `git diff`, `git log`, `git show` |

Each default uses a wildcard pattern (e.g., `Bash(ls *)`) that matches the command with any arguments.

On every launch, Agentic Orchestrator ensures each default rule exists in `~/.agentic-orchestrator/permissions/global.json` (or in the legacy `~/.agentic-workflow/permissions/global.json` directory when an existing install is reused in place). Any missing defaults are appended; existing user-added rules are preserved (deduplicated by `tool_pattern` + `effect`).

## Permission Prompting

### In the Watch View

When an agent requests a tool that requires approval, a **permission menu** appears as an overlay in the watch view:

| Option | Key | Behavior |
|--------|-----|----------|
| Allow | `y` | Approves this single request |
| Allow & Remember | `r` | Approves and caches a wildcard pattern for future use |
| Deny | `n` | Denies the request |

Navigate options with `j`/`k`, confirm with `Enter`, or press the shortcut key directly.

The "Allow & Remember" option shows the inferred pattern below the option text, so you can see exactly what will be cached.

### From the Dashboard

When a feature is waiting for permission approval, a permission badge appears in the detail panel. Two keys are available:

| Key | Action |
|-----|--------|
| `y` | Approve the pending permission request |
| `Shift+A` | Approve and remember (caches the pattern) |

These operate on all pending permission requests for the selected feature.

## Pattern Inference

When you choose "Allow & Remember", Agentic Orchestrator infers a wildcard pattern from the command. The inference logic:

1. Extracts the shell command (strips JSON wrapper if present)
2. Normalizes: takes the last segment after `&&` chains, strips content after pipes (`|`)
3. Tokenizes the command into words
4. Builds the pattern:
   - **Single word** (e.g., `ls`) — `Bash(ls *)`
   - **First arg is a flag** (e.g., `ls -la /tmp`) — `Bash(ls *)` (flags are not subcommands)
   - **First arg is a subcommand** (e.g., `npm test --coverage`) — `Bash(npm test *)` (preserves the subcommand, wildcards the rest)

### Examples

| Command | Inferred Pattern |
|---------|-----------------|
| `npm test --coverage` | `Bash(npm test *)` |
| `go build ./...` | `Bash(go build *)` |
| `ls -la /tmp` | `Bash(ls *)` |
| `cd /path && make build` | `Bash(make build *)` |
| `git push origin main` | `Bash(git push *)` |

## Permission Caching

Cached permissions are stored as JSON files with deny-wins precedence.

### Scope

| Scope | File | Applies To |
|-------|------|------------|
| Global | `~/.agentic-orchestrator/permissions/global.json` | All repositories |
| Per-repo | `~/.agentic-orchestrator/permissions/<repoName>.json` | Specific repository only |

(Legacy installs keep using `~/.agentic-workflow/permissions/...` in place.)

"Allow & Remember" from the watch view caches to the per-repo scope. "Approve and remember" (`Shift+A`) from the dashboard also caches per-repo.

### File Format

```json
{
  "rules": [
    {"tool_pattern": "Bash(npm test *)", "effect": "allow"},
    {"tool_pattern": "Bash(rm *)", "effect": "deny"}
  ]
}
```

### Deny-Wins Precedence

When checking permissions, **deny rules are evaluated first**. If any deny rule matches, the request is denied immediately — allow rules are not checked. This means you can safely grant broad allow patterns while using targeted deny rules to block specific dangerous commands.

### Deduplication

Identical patterns are not duplicated. If you "Remember" a pattern that already exists in the cache, it is not re-appended.

### Atomic Persistence

Permission files are written atomically using a temp-file-and-rename strategy, preventing corruption from interrupted writes.

## Repository Settings Import

When a feature is created, Agentic Orchestrator imports permission patterns from the repo's `.claude/settings.json` and `.claude/settings.local.json` files:

- `allow` patterns become allow rules scoped to that repo
- `deny` patterns become deny rules scoped to that repo

Claude CLI's colon-wildcard syntax (e.g., `Bash(go:*)`) is normalized to space-wildcard syntax (`Bash(go *)`). Imported rules are deduplicated against existing cached rules.

This import runs automatically during TUI feature creation.

## `--dangerously-skip-permissions`

The `--dangerously-skip-permissions` flag disables all permission prompts. Every tool request is auto-approved without user confirmation.

```bash
agentico --dangerously-skip-permissions
```

When active, the TUI shows a visual warning:
- A skull icon appears next to the Agentic Orchestrator logo in the header
- A red `DSP` badge appears in the info line
- The color scheme shifts to a red-on-black theme

Use this flag only in trusted environments where you are confident in the agent's tool use.

## Permissions Directory Layout

```
~/.agentic-orchestrator/permissions/
  global.json              # Global rules (all repos)
  <repoName>.json          # Per-repo rules (e.g., myproject.json)
```

Legacy installs continue to use `~/.agentic-workflow/permissions/` in place.

## Permission Flow

When an agent requests a tool, the permission system evaluates in this order:

1. **Session handler** — checks if the tool is in the always-approved category (read-only tools, file edits during implementation)
2. **Cache check** — looks up the tool pattern in cached rules (global + per-repo), deny-wins
3. **TUI prompt** — if no cached rule matches, surfaces the permission request to the user in the watch view or dashboard
