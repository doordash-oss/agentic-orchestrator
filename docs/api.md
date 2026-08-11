# Agentico Server API

The headless runtime exposes a loopback-only REST/SSE API under `/api/v1`.
`api/openapi.yaml` is the machine-readable contract for routes, response
shapes, auth, and stream envelopes.

Generated Go contract code lives in `internal/server/serverapi.gen.go`. Regenerate it
with `make generate-openapi` or `go generate ./internal/server`.
Tests fail if the committed generated code drifts from `api/openapi.yaml`.

## Discovery And Auth

`agentico server` writes `.agentico-server.json` in the runtime directory with
`0600` permissions. Clients read:

- `base_url`: loopback server origin.
- `auth_token`: bearer token required on every `/api/v1` route.
- `epoch`: event stream epoch for cursor invalidation.
- `name` (optional): the resolved server display name (from `--name`,
  `server.name`, or the persisted generated name). New servers always send
  it; consumers must tolerate its absence and must not treat unknown new
  fields as errors.

`/api/v1/health` carries the same optional top-level `name` field. Both
surfaces are strictly additive: the compatibility declaration
(`loopback-bearer-v1`, schema 1) and the discovery schema version are
unchanged, so older consumers keep working against named servers and newer
consumers tolerate name-less servers.

The server's bind address is selected with `--listen [host:]port` (loopback
hosts only: `127.0.0.1`, `localhost`, `[::1]`; a bare port binds
`127.0.0.1`). Non-loopback hosts and ports outside 1-65535 are rejected
before any socket is opened, and a busy port fails fast with the address
named and no discovery record written. Omitting the flag keeps the default
ephemeral `127.0.0.1` bind.

Programmatic clients send `Authorization: Bearer <auth_token>`. Browser
`EventSource` clients that cannot set headers may pass `access_token` only on
SSE endpoints. Mutations also keep the trusted local header,
`X-Agentico-Client: local`, as CSRF defense in depth.

The MCP adapter has been removed. The supported client surface is REST plus SSE.

## Snapshot Then Subscribe

Clients bootstrap from a snapshot and then consume ordered event deltas:

1. Read discovery metadata.
2. `GET` the needed snapshot endpoint with the bearer token.
3. Store `meta.as_of_seq` from the body or `X-Agentico-Seq` from the headers.
4. Connect to `/api/v1/events?after=<as_of_seq>&epoch=<epoch>`.
5. Apply events by `seq`. On `stream.reset`, re-read snapshots and reconnect
   from the returned sequence.

Event envelopes include `seq`, `epoch`, `kind`, `resource`,
`resource_version`, and `snapshot_required`. Snapshot responses include
`meta.as_of_seq`, and revisioned resources also expose `ETag`.

## Parent/Child Relationships

Top-level feature lists omit direct child rows. Each parent summary and detail
instead carries `active_child` plus the complete ordered `child_history`.
Direct child detail carries the same `relationship` projection, including the
stable display token, stored lifecycle status, explicit close outcome and
timestamp, pipeline, cost, integration attention, and cleanup warnings. A
closed child also carries `diff_summary`: the preserved read-only diff summary
captured at close time (empty when no diff was preserved).

Relationship lifecycle changes use one `lifecycle.updated` event whose resource
type is `relationship` and includes `parent_id`, `child_id`, and a stable
relationship resource ID. Clients fetch parent and child detail and apply them
as one refresh bundle. A completed cascade sets `relationship_deleted`; clients
refresh the top-level list and evict both detail records without issuing
expected-to-fail detail reads. Parent Delete always uses the durable cascade and
returns `completed`, `cleanup_pending`, or `attention_required`.

Destructive relationship actions — child Discard and parent Delete with linked
children — carry a server-authoritative `impact_preview` on their action
catalog entry enumerating affected sessions, worktrees, branches, knowledge
resources, child records, and retained outcomes; every category is present and
absent impact is an explicitly empty list. Clients render that projection
verbatim before confirmation instead of reconstructing mutable backend state,
and resolve an unavailable action against `disabled_reasons` for the typed
reason rather than substituting generic copy. In the desktop app this surfaces as a
per-parent "Refactor History" group beneath each parent (collapsed by default,
expanded per session), read-only closed-child inspection, and focused action
hints.

## Session Output

Bulk agent output is not delivered through the global event stream.

- Backfill with `GET /api/v1/sessions/{session_id}/output?from=<row_index>`.
- Tail with `GET /api/v1/sessions/{session_id}/output/stream?from=<row_index>`.

Output stream event IDs are transcript row indexes. Reconnect using
`Last-Event-ID` or the next row index from the previous response. The global stream only emits
throttled `session.output.activity` signals for liveness and size.
