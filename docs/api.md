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

## Release Availability

`GET /api/v1/update` returns the authenticated, metadata-only release
availability snapshot. It never triggers a check and never mutates anything:
notification checks never download packages or manifests, probe candidates,
create installation receipts or staging, or change executable bytes.

`POST /api/v1/update/check` (empty JSON object, trusted-mutation headers)
accepts one explicit check and returns `202` with the current snapshot
immediately; the accepted check runs asynchronously tied to the runtime
lifetime, concurrent requests coalesce into one metadata worker, and a
disconnecting caller never cancels accepted work. It is refused with `403`
`forbidden` and the disabled-policy remediation under the off policy, `409`
`update_unsupported_install` for ineligible installations, and `429`
`update_check_failed` with a retry hint while a server-imposed retry deadline
is in force — the last without making any request. A check issued while an
install operation is active refreshes latest metadata without overwriting
the operation.

`POST /api/v1/update/install` (consent request, trusted-mutation headers)
accepts one consented install request and returns `202` with the current
snapshot promptly. `consent` must be true — anything else is `400`
`update_consent_required` — and `when` selects `now` or `idle`. `idle`
stages the operation and waits for active work to finish without
interrupting it. `now` without `stop_active_work` installs immediately
only when no work is active. `now` with `stop_active_work: true` authorizes
interrupting feature sessions and the singleton chat through the existing
pause-stop and chat-end semantics: repository work (clones, uploads, origin
checks, other repository activity), protected or unknown admission
reservations, and failed activity detection still refuse with `409`
`update_blocked_active_work` — before staging, again after staging, and
again under the closed admission gate, with nothing stopped. Stop dispatch
and completion confirmation share one ten-second deadline; any stop
failure, timeout, or unresolved work aborts the installation, leaves the
current build serving with already-stopped work interrupted, and requires
fresh consent for a new attempt. An optional `version` selector must name the
currently discovered latest stable release. Equivalent duplicates return
the existing operation; changing the target, `when`, or stop-work
permission requires canceling and resubmitting. The request is refused
with `403` `forbidden` and the disabled-policy remediation under the off
policy and `409`
`update_unsupported_install` for ineligible installations or a conflicting
active operation or target. Failed downloads, signature or digest
verification, and installs classify as the warning codes
`update_download_failed`, `update_signature_failed`, and
`update_install_failed`, each naming the target version and a sanitized
reason.

`DELETE /api/v1/update/install` (empty JSON object, trusted-mutation
headers) cancels the active install operation and returns `200` with the
current availability snapshot after cleanup. It is idempotent when nothing
is active, is refused with `403` `forbidden` and the disabled-policy
remediation under the off policy, and with `409` `update_in_progress` once
an explicit-stop operation entered its stopping interval or draining has
begun, because an install that crossed that boundary can no longer be
abandoned. During the stopping and draining interval, new work of every
kind — including chat turns, prompt and permission replies, and reads that
launch background work — is refused with `503` `update_in_progress` and a
`Retry-After` hint, while existing stop and completion paths keep
settling.

Every visible snapshot change emits an `update.updated` event (resource type
`update`); clients re-GET the snapshot. The snapshot reports the effective
startup policy (`--updates` over `AGENTICO_UPDATES` over
`server.updates.policy`, default `notify`; `auto` fails startup explicitly),
the reserved `server.updates.strategy` and `server.updates.window` settings
(validated and reported, never scheduling work), the classified installation,
check timing, and — when one exists — the sanitized public receipt
(`versions`, `outcome`, `times`, and a sanitized error only). While an
install operation is active the snapshot also reports its waiting `method`,
the actual `stop_active_work` permission the operation retains, and
`target_version`;
`scheduled_for` is the predicted install deadline and is explicitly `null`
while an idle wait has no deadline. `signature` reads `verified` only after
the operation verified the pinned candidate, whose verified server contract
then appears as `target_contract`; neither is ever trusted from feed
metadata alone. The required `active_work_summary` carries the truthful
current activity counts — features, chat, clones, uploads, origin checks,
pending admissions, parked count (always zero in this release), and
`detection_failed`, which refuses an immediate install — with
`quiescing_since` never set in this release. A suppressed
newest release stays visible as `latest_version` with
`failed`/`update_rolled_back` until a newer unsuppressed release becomes
available; failed refreshes retain the last successful metadata and its
timestamp. These startup settings never appear on the runtime-config REST
surface.

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
