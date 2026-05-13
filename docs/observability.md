# Observability Artifacts

Agentic Orchestrator writes observability data under each feature directory in
the configured state directory.

## Event Log

`events.jsonl` is an append-only JSONL stream. Every line is an event envelope
with the common fields defined by `internal/observe.Event`, including
`event_type`, `feature_id`, optional `phase`, optional `data`, and optional
`run_number`.

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
