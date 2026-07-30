# Agentic Orchestrator Keybinding Reference

_Auto-generated on 2026-07-29. Do not edit manually._

_Run `go generate ./internal/tui/...` to regenerate._

## Dashboard

| Key | Action |
|-----|--------|
| `n` | New feature (launch wizard) |
| `enter` | Focus right panel / expand |
| `a` | Watch active work; Answer, Approve, or Review when prompted |
| `Shift+R` | Resume all interrupted features |
| `Shift+F` | Start refactor (eligible terminal feature) |
| `tab` | Switch panel |
| `↑/k` | Move up |
| `↓/j` | Move down |
| `v` | View diff |
| `p` | Publish (when code ready) |
| `y` | Approve pending permissions |
| `Shift+A` | Approve & remember permissions |
| `Shift+E` | Edit workspace config |
| `d` | Delete feature |

## Feature Detail

| Key | Action |
|-----|--------|
| `a` | Watch active work; Answer, Approve, or Review when prompted |
| `o` | Show overview |
| `y` | Approve pending permissions |
| `Shift+A` | Approve & remember permissions |
| `h` | Answer agent's help question |
| `r` | Restart current phase |
| `s` | Stop running feature |
| `ctrl+r` | Rewind to phase |
| `l` | Live Preview / View logs |
| `v` | View diff |
| `p` | Publish (when code ready) |
| `b` | Rebase on main (code ready or published) |
| `e` | Edit config |
| `Shift+E` | Edit workspace config |
| `Shift+F` | Start refactor (eligible terminal feature) |
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

