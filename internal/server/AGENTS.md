# AGENTS.md — Server API

The REST/SSE server is shared by the TUI and Web UI. API changes must preserve
existing TUI behavior, not just browser behavior.

Keep read endpoints observational by default. If a `GET` handler performs
reconciliation or cleanup, prove that it cannot race an active TUI/client flow
such as create, setup, retry, recovery, session attach, or prompt handling.

When changing endpoints, action routing, refresh snapshots, SSE invalidation,
recovery, setup, or feature lifecycle DTOs, add or update API-backed TUI tests
alongside server tests. Prefer regression coverage that drives the same client
sequence the TUI uses.
