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

### `feature.layer_boundary`

Roadmap layer boundaries emit `feature.layer_boundary` after the boundary's
single persistence write. Common `data` keys:

- `layer_position`: completed layer's position.
- `layer_title`: completed layer's title.
- `layer_branch`: completed layer's branch.
- `repo_tips`: per-repository tip SHAs the boundary recorded.
- `next_layer_position` / `next_layer_branch`: the next layer when the
  boundary split the worktrees onto its branch.

### `feature.layer_publish`

Each stack layer a publish pass acts on emits one `feature.layer_publish`
event per repository, after the layer's persistence write. Common `data`
keys:

- `repository`: repository the layer belongs to.
- `layer_position`: layer's stack position.
- `layer_title`: layer's title.
- `layer_branch`: layer's branch.
- `pr_url`: the layer's pull request URL, when one exists.
- `pr_state`: recorded pull request state (`open`, `merged`, or `closed`).
- `action`: what the pass did — `created` (new pull request), `pushed`
  (fast-forward push onto an existing pull request's branch), `rewritten`
  (lease-protected force push replacing remote history), `merged` (pull
  request found merged), `blocked` (pull request found closed without
  merge), or `failed` (the layer's publish step failed).

### Repository status events and the layer field

Domain `repo.status_changed` events carry a `layer_position` data key
whenever exactly one stack layer is known: publish emits one event per
changed layer (instead of one per repository), a Final Review fix
relocation names the layer it landed on, and a layer-boundary split names
the next layer. Events that are not layer-scoped omit the key. The SSE
projection renders them as lifecycle updates exactly as before.

## Feature Summary

`observe-summary.yaml` is rebuilt from active-run events and durable feature
state. The `sealed_runs` section lists sealed rewind history in ascending
`run_number` order.

Each repository entry under `repos` carries:

- `status`: `published`, `touched`, `failed`, or `untouched`.
- `pull_requests`: every delivery layer's pull request for the repository,
  each with `position`, `title`, `url`, and `state`. Omitted when the
  repository has no pull requests.
- `cost_usd`, `input_tokens`, `output_tokens`: session-derived aggregates.

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
