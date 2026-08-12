# Observability and Fleet Telemetry

Agentic Orchestrator writes observability data under each feature directory in
the configured state directory.

`observability.events` and `observability.otel_enabled` are independent.
`events` controls the local per-feature JSONL and summary artifacts described
below. `otel_enabled` controls workflow traces, fleet metrics, and durable wide
events. Setting an endpoint alone does not enable telemetry.

## Outcome definitions

- **Output ready** is the first transition to `CodeReady`.
- **Delivered** is a transition to `Published` or `Done`.
- **Agentico failure** is terminal `Failed`, classified by `failure_type`.
- `Interrupted`, `NeedUserInput`, rewind, deletion/abandonment, and delivery are
  distinct outcomes and are not counted as failures.
- Quality evidence consists of plan validation, validator, verification item,
  automatic review, and final review outcomes.

Agentico does not observe GitHub merge state, external CI, deployment health,
or production correctness.

## OTel resource identity

All three OTel signals carry `service.name`, `service.version`,
`service.instance.id`, `agentico.build.revision`,
`agentico.installation.id`, and `agentico.telemetry.schema.version=1`.
`otel_service_name` overrides `service.name`; distribution resource attributes
from standard OTel environment configuration are otherwise retained. The
instance ID is random per process. The stable installation ID is stored at
`<runtime-dir>/telemetry/installation-id` with mode `0600` (its parent is
`0700`). Installation and instance identity are resource metadata, never
metric labels.

Personal identity, organizational ownership, username, hostname, machine ID, state/config/workspace
paths, and repository names are never resource attributes. Repository names
may appear in wide events only.

## Fleet metrics

Counters use `{event}` unless a more specific count unit is shown. Histograms
use explicit bucket views; durations are seconds, token usage is `{token}`,
cost is `USD`, sizes are bytes, and output quantities use `{file}`, `{line}` or
`{commit}`. Gauges are sampled by the 60-second periodic reader. Set
`OTEL_METRIC_EXPORT_INTERVAL` in milliseconds to override that interval.

| Type | Instruments |
| --- | --- |
| Gauge | `agentico.feature.active`, `agentico.telemetry.outbox.pending`, `agentico.telemetry.outbox.bytes`, `agentico.telemetry.outbox.oldest.age` |
| Counter | `agentico.runtime.startup.count`, `agentico.telemetry.export.failure.count`, `agentico.telemetry.dropped.count`, `agentico.feature.created.count`, `agentico.feature.transition.count`, `agentico.feature.milestone.count`, `agentico.feature.run.terminal.count`, `agentico.feature.rewind.count`, `agentico.feature.repository.count`, `agentico.phase.iteration.count`, `agentico.session.outcome.count`, `agentico.session.token.usage`, `agentico.session.turn.count`, `agentico.session.retry.count`, `agentico.session.truncation.count`, `agentico.session.context_handoff.count`, `agentico.review.outcome.count`, `agentico.validation.outcome.count`, `agentico.verification.item.count`, `agentico.publish.outcome.count`, `agentico.interaction.request.count`, `agentico.automatic_review.outcome.count`, `agentico.automatic_review.unavailable.count`, `agentico.recovery.action.count`, `agentico.session.critical_message_dropped.count`, `http.server.request.count` |
| Histogram | `agentico.feature.run.duration`, `agentico.feature.run.time_to_code_ready`, `agentico.feature.run.time_to_delivery`, `agentico.feature.output.files_changed`, `agentico.feature.output.lines_added`, `agentico.feature.output.lines_deleted`, `agentico.feature.output.commit.count`, `agentico.phase.duration`, `agentico.session.duration`, `agentico.session.api.duration`, `agentico.session.cost`, `agentico.review.duration`, `agentico.validation.duration`, `agentico.validator.duration`, `agentico.interaction.wait.duration`, `agentico.automatic_review.duration`, `agentico.publish.duration`, `http.server.request.duration`, `http.server.request.body.size`, `http.server.response.body.size` |

Allowed metric dimensions are bounded enums: pipeline, feature kind, risk,
phase, status/outcome, failure type, provider, catalog-normalized model,
effort, review/validator/verification type, interaction kind/decision,
automatic/manual, publish mode, token type, HTTP method, normalized route, and
HTTP status. Unknown or unbounded provider/model values become `other`.
Feature/session IDs, repository names, paths, URLs, commands, questions,
answers, and errors are forbidden metric dimensions. Provider CLI sessions are
not reported as `gen_ai.client` calls because they aggregate multiple internal
model requests.

Health, CORS preflight, SSE, and session output-stream routes are excluded from
HTTP metrics. Dynamic feature, session, review, repository, run, artifact, and
log segments are normalized to their OpenAPI template names.

## Durable wide events

Every typed local event is also exported as a short span named
`agentico.event.<event_type>`, including `phase.started`, `phase.completed`,
`phase.failed`, `session.started`, `session.ended`, `iteration.started`,
`iteration.ended`, `review.started`, `review.completed`, `validation.started`,
`validation.completed`, `validator.started`, `validator.completed`,
`permission.requested`, `permission.resolved`, `question.asked`,
`question.answered`, `automatic_review.completed`,
`automatic_review.unavailable`, `context.file_read`,
`context.handoff_triggered`, `context.large_output`, `recovery.scanned`,
`recovery.action`, `agent.task_started`, `agent.task_progress`,
`agent.task_ended`, `feature.started`, `feature.completed`, `feature.failed`,
`feature.interrupted`, `feature.config_changed`, and `feature.rewound`.

Store-authoritative events add `runtime.started`, `runtime.stopped`,
`feature.created`, `feature.state_changed`, `feature.deleted`,
`feature.run_started`, `feature.run_sealed`, `feature.output_ready`,
`feature.delivered`, `verification.item_completed`, and
`feature.output_stats_collected`. Manual review gates add
`review_gate.requested` and `review_gate.resolved`; authoritative persisted
phase timing adds `phase.duration_recorded`. `publish.completed`,
`publish.failed`, and `telemetry.data_loss` surface delivery and telemetry-loss
outcomes.

Every wide span has `agentico.analytics=true`, a UUID-like
`agentico.event.id` deduplication key, the original timestamp, stable feature
trace ID, unique span ID, run number, and typed `agentico.*` attributes.
Common strings cover event type, feature/run/phase/status, pipeline/risk,
parent/child kind, provider/model/effort, outcome and failure type. Numeric
fields cover duration, cost, tokens, context, turns, repository count, review
and validation evidence, wait time, and output counts. Repository names and
bounded rich context can be present here; they never become metric labels.

Rich strings are credential-redacted with the permission-audit vocabulary,
absolute username-bearing paths are replaced with `<user-path>`, each rich
field is limited to 4 KiB, and a record is limited to 32 KiB. Truncation is
marked with the original byte count. Full prompts, system instructions,
transcripts, raw model results, source files, diffs, environment variables,
config contents, and authentication material are never captured.

### Outbox guarantees

Sanitized records are fsynced into 4 MiB append-only JSONL segments under
`<runtime-dir>/telemetry/outbox/` (`0700` directory, `0600` files) before the
emitting call returns. Batches are limited to 256 records and 1 MiB. An atomic
cursor advances only after OTLP trace export succeeds; fully acknowledged
segments are deleted. Delivery retries with jittered exponential backoff from
one second to five minutes and is at least once. A crash after collector
acceptance but before cursor advancement can duplicate a record; consumers
must deduplicate by `agentico.event.id`.

Unacknowledged data is retained for seven days or 256 MiB. Oldest sealed
segments are evicted when either bound is exceeded and loss is surfaced in
`agentico.telemetry.dropped.count`. Disabling OTel leaves the outbox untouched;
re-enabling it resumes draining. Telemetry persistence/export failure never
fails a feature workflow. Metrics are best effort; wide events are replayable.

Collectors must retain `agentico.event.*` spans unsampled. Sampling them at
the collector defeats the durable analytics guarantee. Existing `agentic.*`
workflow-trace attributes remain compatibility aliases for one release;
`agentico.*` is canonical.

## Event Log

`events.jsonl` is an append-only JSONL stream. Every line is an event envelope
with the common fields defined by `internal/observe.Event`, including
`event_type`, `feature_id`, optional `phase`, optional `data`, and optional
`run_number`.

### Setup Lifecycle Events

Worktree setup, retry, and reconciliation diagnostics emit setup lifecycle
events before any agent phase starts:

- `setup.started`
- `setup.progress`
- `setup.completed`
- `setup.failed`

These are pre-phase events and do not carry a phase name. Do not confuse them
with agent phase telemetry, which is emitted after setup and uses the pipeline
phase context.

Common event fields and `data` keys:

- `feature_id`: feature identifier associated with the setup work.
- `run_number`: active run number for the setup work.
- `attempt`: setup attempt number.
- `setup_log`: setup log path or message.
- `setup_task`: setup task name.
- `setup_kind`: setup operation kind.
- `setup_status`: setup status value.
- `repo_name`: repository name being prepared.
- `path`: worktree or repository path.
- `branch`: branch used for the setup operation.
- `error`: failure details when setup fails.

### `feature.rewound`

Successful rewinds emit a dedicated `feature.rewound` event after the sealed
run is forked and the fresh run is active.

Common `data` keys:

- `rewind_scope`: `full_phase` or `partial_roadmap_phase`.
- `target_phase`: requested pipeline phase directory name.
- `effective_target_phase`: target phase after lifecycle escalation rules.
- `source_run`: sealed predecessor run number.
- `new_run`: fresh active run number.
- `carried_phases`: copied phase artifact directories, when any were carried.
- `backup_branches`: backup branch names keyed by repo, when any were created.

Partial Implement rewinds add:

- `roadmap_phase`: selected roadmap phase number.
- `total_roadmap_phases`: roadmap phase count when known.
- `preserved_roadmap_phases`: human-readable phase range preserved.
- `redone_roadmap_phase`: human-readable selected phase label.
- `discarded_roadmap_phases`: human-readable downstream phase range discarded.

Full phase rewinds omit `roadmap_phase` and the roadmap range labels.

## Feature Summary

`observe-summary.yaml` is rebuilt from active-run events and durable feature
state. The `sealed_runs` section lists sealed rewind history in ascending
`run_number` order.

Each sealed run summary may include:

- `run_number`
- `sealed_at`
- `seal_reason`
- `rewind_target`
- `rewind_roadmap_phase`
- `duration_ms`
- `cost_usd`

`rewind_roadmap_phase` is additive and appears only for sealed runs produced by
partial Implement rewinds. Full rewinds, non-Implement rewinds, legacy run
files, malformed run files, and active runs omit it.

## Run State

`runs/run-NNN/run.yaml` persists rewind audit metadata on sealed predecessor
runs:

- `rewind_target`: pipeline phase selected for rewind.
- `rewind_roadmap_phase`: selected roadmap phase for partial Implement rewinds.
- `backup_branches`: backup branch names keyed by repo.

Legacy `run.yaml` files without `rewind_roadmap_phase` continue to load without
migration.
