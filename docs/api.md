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

The server checks GitHub for newer stable releases and can replace its own
binary on request. Field-level contracts live in `api/openapi.yaml`
(`UpdateSnapshot`, `UpdateInstallRequest`, `UpdatePublicReceipt`,
`UpdateActiveWorkSummary`). This section describes the behavior.

All endpoints require the bearer token. Mutations also require the
trusted-mutation headers. Every response carries the current snapshot,
except errors, which use the standard error envelope.

### Policy

The effective policy is `--updates`, then `AGENTICO_UPDATES`, then
`server.updates.policy`. The default is `notify`.

- `off` disables checks. No feed traffic occurs. Checks and installs are refused.
- `notify` checks on a schedule and on request. Installs happen only on an explicit request.
- `auto` checks like `notify` and also installs each newer release at the first idle moment. It never stops work.

`server.updates.window` (`HH:MM-HH:MM`, local time) bounds when an automatic
install may begin. `server.updates.strategy` accepts `idle` or `quiesce`;
`quiesce` behaves as `idle` in this release. Neither affects installs a
client requests. None of these settings appear on the runtime-config REST
surface.

### GET /api/v1/update

Returns the availability snapshot. It never triggers a check and has no
side effects. The response carries an `ETag`; a matching `If-None-Match`
yields `304`.

Snapshot highlights:

- `status`, `policy`, `installation`, and `remediation` say whether an update can happen here and why not otherwise.
- `current_version`, `latest_version`, and `latest_release_url` describe the discovered release. A release that rolled back stays visible as `latest_version` with `failed` and `update_rolled_back` until a newer one appears.
- `last_check_at`, `last_success_at`, `next_check_at`, and `retry_not_before` describe check timing. A failed refresh keeps the last successful metadata.
- `receipt` is the sanitized outcome of the last install, when one exists.
- `active_work_summary` reports current features, chat, clones, uploads, origin checks, and pending admissions. `detection_failed` means an immediate install will be refused.
- While an install is active: `method`, `stop_active_work`, `target_version`, `scheduled_for`, `signature`, and `target_contract`. `signature` is `verified` only after the pinned candidate was verified. `scheduled_for` is the next window opening an automatic install waits for, otherwise `null`.

### POST /api/v1/update/check

Body: `{}`. Runs one explicit check and returns `202` with the current
snapshot right away. Concurrent requests share one worker. Disconnecting
does not cancel the check. A check during an install refreshes metadata
without touching the install.

A check reads release metadata only. It never downloads packages, stages
files, probes candidates, or writes receipts.

| Status | Code | When |
|---|---|---|
| `403` | `forbidden` | Policy is `off`. |
| `409` | `update_unsupported_install` | This installation cannot update itself. |
| `429` | `update_check_failed` | A server-imposed retry deadline is in force. No request is made. |

### POST /api/v1/update/install

Body: `UpdateInstallRequest`. Returns `202` with the current snapshot.

- `consent` must be `true`.
- `when` is `idle` or `now`. `idle` stages the release and waits for work to finish without interrupting it. `now` installs immediately if nothing is active.
- `stop_active_work: true` with `now` authorizes stopping feature sessions and the chat. Stop dispatch and confirmation share a ten-second budget. Any stop failure or timeout aborts the install and keeps the current build serving. Already-stopped work stays stopped.
- `version`, when set, must equal the discovered latest stable release.

Repository work such as clones, uploads, and origin checks is never
stopped. It refuses the install before staging, after staging, and again
under the closed admission gate. Failed activity detection refuses too.

An identical repeated request returns the existing operation. Changing
`when`, `stop_active_work`, or the target requires cancel and resubmit.

| Status | Code | When |
|---|---|---|
| `400` | `update_consent_required` | `consent` is not `true`. |
| `400` | `bad_request` | Malformed body or invalid field value. |
| `403` | `forbidden` | Policy is `off`. |
| `409` | `update_unsupported_install` | Ineligible installation, or a different operation or target is active. |
| `409` | `update_blocked_active_work` | Work is active and the request does not, or cannot, stop it. |
| `409` | `update_in_progress` | An install already entered stopping or draining. |

Install failures settle on the snapshot as `update_download_failed`,
`update_signature_failed`, or `update_install_failed`, each naming the
target version with a sanitized reason.

### DELETE /api/v1/update/install

Body: `{}`. Cancels the active install and returns `200` with the snapshot
after cleanup. Cancelling when nothing is active succeeds.

| Status | Code | When |
|---|---|---|
| `403` | `forbidden` | Policy is `off`. |
| `409` | `update_in_progress` | The install entered stopping or draining and can no longer be abandoned. |

### Stopping and draining

Once an install starts stopping work or draining, new work is refused with
`503` `update_in_progress` and a `Retry-After` header. This covers chat
turns, prompt and permission replies, and reads that start background work.
Existing stop and completion paths keep settling.

### Event

Every visible snapshot change emits `update.updated` with resource type
`update`. Clients re-GET the snapshot. No-op reads emit nothing.

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

## Capability Gates

Testing-contract rows under Automated Verification, Manual Verification,
Visual Evidence, or Behavioral Evidence may carry
`[agentico capability: <name>]`, where `<name>` is a built-in capability:
`authenticated-browser(<host>)`, `display`, `docker`, or
`network(<host[:port]>)`. The older `[agentico capability: <name>; probe:
<shell>]` form with an explicit probe still works. Built-in probes run
in-process; an unknown name is a contract error routed to plan revision, not a
user prompt. A Visual Evidence or Manual Verification row that names an
external host without a capability is likewise returned for plan revision.

The harness probes declared capabilities before each implementer iteration
starts and again at post-handoff verification. A missing capability opens the
need-user-input gate with actions `WAIVE`, `RETRY_AFTER_AUTH`, and, for
agent-owned evidence rows whose policy forbids substitution,
`ALLOW_SUBSTITUTE`, which sets `allow_substitution: true` on those rows so a
labelled faithful substitute becomes acceptable evidence.

`authenticated-browser(<host>)` first checks server policy: servers exposed on
the network deny signed-in browser state by default, and
`server.capabilities.browser_state: allow|deny` in `config.yaml` overrides
either default. It then reads Playwright storage state from
`<runtime_dir>/capabilities/browser/<host>/storageState.json`, checks cookie
expiry, and makes one HTTP liveness request that fails on a sign-in redirect
or 401/403. On success the row's command environment receives
`AGENTICO_BROWSER_STATE_<HOST>` (host upper-cased, non-alphanumerics as `_`,
e.g. `AGENTICO_BROWSER_STATE_SLACK_COM`) pointing at that file.

CLI:

- `agentico capability-probe <name[(argument)]>` prints available/unavailable
  with the reason and exits 0/1.
- `agentico report-blocker --contract <testing-contract.yaml> --dir
  <iteration_dir> --items <id,id,...> --capability <name> --reason <text>`
  writes the blocker gate (`need-user-input.yaml`, source agent) into the
  iteration directory; the implementer then ends the iteration with `RETRY`
  and the harness pauses on the user gate. Item names shown to the user come
  from the contract, not the agent.

### GET /api/v1/features/{feature_id}/testing-contract

Returns the current roadmap phase's compiled testing contract
(`TestingContractResponse`): `active_run`, `roadmap_phase`, `revision`, and one `items`
entry per row with `item_id`, `source`, `owner`, `repo`, `name`, `command`,
the policy flags (`required`, `allow_substitution`, `allow_blocked`,
`allow_waiver`), the recorded `disposition` when one exists, and declared
`capabilities`. 404 `not_found` when the phase has no contract yet. The
desktop waive dialog reads this to list waivable rows.

### POST /api/v1/features/{feature_id}/actions/testing-contract-waive

Records user-authorized waivers outside the gate. Body:
`{ "item_ids": ["<id>", ...], "reason": "<text>", "active_run": <n>, "roadmap_phase": <n>, "contract_revision": <n> }`.
The three optional integers bind the waiver to the contract the client read;
a mismatch returns 409 `conflict` so a selection never applies to a later
phase or revision. The response
(`TestingContractWaiveResponse`) carries the new `contract_revision` and the
`waived_items`.

## Session Output

Bulk agent output is not delivered through the global event stream.

- Backfill with `GET /api/v1/sessions/{session_id}/output?from=<row_index>`.
- Tail with `GET /api/v1/sessions/{session_id}/output/stream?from=<row_index>`.

Output stream event IDs are transcript row indexes. Reconnect using
`Last-Event-ID` or the next row index from the previous response. The global stream only emits
throttled `session.output.activity` signals for liveness and size.
