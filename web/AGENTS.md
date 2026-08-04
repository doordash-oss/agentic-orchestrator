# AGENTS.md — Web UI

The Web UI is a client for the same Agentico runtime used by the TUI. Changes in
this folder must not break or race existing TUI workflows.

Before changing browser polling, recovery modals, SSE refresh behavior, REST
mutations, or API DTO assumptions, verify the corresponding API-backed TUI flow.
Browser `GET` requests should be observational; if a read endpoint performs
server-side reconciliation, confirm it is safe while the TUI is creating,
starting, retrying, or recovering a feature.

For Web UI changes that touch feature lifecycle, setup, recovery, sessions, or
prompt/control flows, run targeted `internal/tui` and `internal/server` tests in
addition to `pnpm --dir web build` and `make test-fast`.
