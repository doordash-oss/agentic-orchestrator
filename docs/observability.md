# Observability Artifacts

Agentic Orchestrator writes observability data under each feature directory in
the configured state directory.

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
- `error_code`: stable canonical catalog code when the event carries a failure.
- `error_class`: canonical severity class (`blocking`, `needs_action`, or `warning`).

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

### Slack Integration Events

The Slack notifier emits one event for every successful write it makes to
Slack, and one for every event it had to drop:

- `slack.root_card_updated`: a destination's root card was posted or edited.
- `slack.message_posted`: a message was posted into a card's thread.
- `slack.event_dropped`: a lifecycle event was discarded before delivery.

`data` keys:

- `destination_kind`: recipient kind of the destination written to (`user` or
  `channel`), for the two write events.
- `action`: `posted` when the root card was first created, `edited` when an
  existing card was updated in place, for `slack.root_card_updated`.
- `item_kind`: kind of the delivered item (`progress`), for
  `slack.message_posted`.
- `event_type`: name of the dropped lifecycle event (`feature.started`,
  `phase.completed`, and so on), for `slack.event_dropped`.
- `reason`: why the event was dropped: `queue_overflow` when the bounded intake
  queue was full, or `feature_load_failed` when the feature record could not
  be loaded.

None of these events carries the Slack token, a channel name, or any rendered
message text.

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
