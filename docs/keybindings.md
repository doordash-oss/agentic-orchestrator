# Agentic Orchestrator Keybinding Reference

_Auto-generated on 2026-05-14. Do not edit manually._

_Run `go generate ./internal/tui/...` to regenerate._

## Dashboard

| Key | Action |
|-----|--------|
| `n` | New feature (launch wizard) |
| `enter` | Focus right panel / expand |
| `a` | Watch active work; Answer, Approve, or Review when prompted |
| `Shift+R` | Resume all interrupted features |
| `tab` | Switch panel |
| `↑/k` | Move up |
| `↓/j` | Move down |
| `v` | View diff |
| `p` | Publish (when code ready) |
| `y` | Approve pending permissions |
| `Shift+A` | Approve & remember permissions |
| `Shift+N` | Toggle input notifications |
| `d` | Delete feature |

## Feature Detail

| Key | Action |
|-----|--------|
| `a` | Watch active work; Answer, Approve, or Review when prompted |
| `y` | Approve pending permissions |
| `Shift+A` | Approve & remember permissions |
| `h` | Answer agent's help question |
| `Shift+N` | Toggle input notifications |
| `r` | Restart current phase |
| `s` | Stop running feature |
| `ctrl+r` | Rewind to phase |
| `l` | View logs |
| `v` | View diff |
| `p` | Publish (when code ready) |
| `m` | Manual publish |
| `t` | Tweak implementation (code ready or published) |
| `b` | Rebase on main (code ready or published) |
| `e` | Edit config (when feature is idle) |
| `Shift+M` | Merge to base branch (local repos) |
| `Shift+D` | Mark as done |
| `g` | Review comments (published) |
| `c` | Clean worktree |
| `d` | Delete feature |
| `esc` | Back to dashboard |

## General

| Key | Action |
|-----|--------|
| `/` | Ask me Anything (AI chat) |
| `?` | Show this help |
| `q` | Quit |
| `ctrl+c` | Force quit |

## Watch View

| Key | Action |
|-----|--------|
| `ctrl+]/ctrl+x/esc` | Stop watching and return to dashboard |

## Wizard

| Key | Action |
|-----|--------|
| `enter` | Next step |
| `shift+tab` | Previous step |
| `tab` | Toggle / cycle selection |
| `↑/↓` | Move selection |
| `←/→` | Cycle model |
| `ctrl+v` | Paste image |
| `@` | File picker |
| `esc` | Cancel |

## Confirmations

| Key | Action |
|-----|--------|
| `y / Y` | Confirm |
| `any other key` | Cancel |

