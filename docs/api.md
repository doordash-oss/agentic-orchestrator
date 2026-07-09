# Agentico Server API

The headless runtime exposes a loopback-only REST/SSE API under `/api/v1`.
`api/openapi.yaml` is the machine-readable contract for routes, response
shapes, auth, and stream envelopes.

Generated Go contract code lives in `internal/server/serverapi`. Regenerate it
with `make generate-openapi` or `go generate ./internal/server/serverapi`.
Tests fail if the committed generated code drifts from `api/openapi.yaml`.

## Discovery And Auth

`agentico server` writes `.agentico-server.json` in the runtime directory with
`0600` permissions. Clients read:

- `base_url`: loopback server origin.
- `auth_token`: bearer token required on every `/api/v1` route.
- `epoch`: event stream epoch for cursor invalidation.

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

## Session Output

Bulk agent output is not delivered through the global event stream.

- Backfill with `GET /api/v1/sessions/{session_id}/output?from=<offset>`.
- Tail with `GET /api/v1/sessions/{session_id}/output/stream?from=<offset>`.

Output stream event IDs are byte offsets. Reconnect using `Last-Event-ID` or
the next offset from the previous response. The global stream only emits
throttled `session.output.activity` signals for liveness and size.
