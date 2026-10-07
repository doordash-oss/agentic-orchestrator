# Supervisor Environment

## The server machine

You run on the machine that hosts the `agentico` server, not necessarily the machine the user sits at. The desktop app may be attached to this server from elsewhere, and the server may be headless: no display, no browser, no desktop session. Do not open windows, browsers or GUI tools, and do not assume the user can see this machine's screen or files. Describe paths and results in your replies instead of pointing the user at local files.

Feature work happens in git worktrees the server manages. Change features through the API, never by editing a feature's worktree or state files yourself.

## Runtime paths

Your system prompt lists the paths for this server:

- Runtime directory: the root of this server's runtime (skills, permissions, provider state, worktrees, logs, and the discovery file live under it).
- State directory: where feature records and runs are stored. Read them through the API, not from disk.
- Working directory: the directory this conversation runs in.
- Config file: the server's `config.yaml`. Change runtime settings through the runtime-config operations in [api-reference.md](api-reference.md), not by editing the file.
- Discovery file: the server's owner-only connection record.
- Supervisor skill: this skill's `SKILL.md`.
- Helper command: the absolute path of the `agentico` binary followed by `api`. `"$AGENTICO_BIN"` names the same binary.

## How the helper finds this server

The helper locates the discovery file in this order:

1. the `--runtime-dir <dir>` flag, when given;
2. the `AGENTICO_RUNTIME_DIR` environment variable, which the server sets for this conversation;
3. the default runtime directory, `~/.agentic-orchestrator/`.

Do not pass `--runtime-dir` in normal use: the environment variable already points at this server, and pointing the helper anywhere else would operate a different server. The helper checks that the discovery file is a regular file owned by you with no group or other permissions before trusting it.

When a call fails before reaching the server, the error is printed on stderr:

- discovery file missing: the server is not running or the runtime directory is wrong. Tell the user; do not hunt for other servers.
- discovery file unsafe or owned by someone else: report it as an environment problem and quote the remediation. Do not change the file's permissions yourself.
- server unreachable: the server stopped or is restarting. Say so; check once more with `"$AGENTICO_BIN" api GET /api/v1/health` only if the user asks.

## The token

The discovery file holds the server's bearer token. Never read, print, echo, copy, grep or summarise the discovery file or the token, and never put the token in a command, a file or a reply. The helper reads it and sends it for you, and never prints it. If the user asks for the token, explain that the desktop app and the helper handle authentication and that you will not reveal it.

## Writing helper commands

A Bash command that is exactly one bare helper call runs without asking the user. Anything else asks for approval each time: pipes, `&&`, `;`, redirects, `$(...)`, backticks, shell variables in the arguments, or a `&`, `<` or `>` anywhere in the command, including inside a quoted body or path.

- Put the path in single quotes when it has a query string (`?`), and use one query parameter per call where you can.
- Put the JSON body in single quotes. Keep the body free of characters the shell or the permission check treats specially, and write them as JSON escapes instead:
  - `'` (apostrophe) as `'`
  - `&` as `&`, `<` as `<`, `>` as `>`
  - `$` as `$`, backtick as ```, `|` as `|`, `;` as `;`
  - a line break as `\n` inside the JSON string, never a real newline
- The helper rejects a body on `GET` and a body that is not valid JSON before sending anything.
- Stream paths need `--timeout`; put flags before the method: `"$AGENTICO_BIN" api --timeout 60s GET /api/v1/events`.
