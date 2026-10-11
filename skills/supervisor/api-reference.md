# Agentico API Reference (Supervisor)

Every operation the supervisor uses, grouped by job. Each entry gives the method and path exactly as `api/openapi.yaml` spells them, the required body fields, and one helper example. Replace each `{param}` with an ID you read from an earlier response; never invent one.

## Conventions

- Call shape: `"$AGENTICO_BIN" api [--timeout <duration>] METHOD /api/v1/... ['<json body>']`. The absolute helper command from your system prompt works the same way.
- Every mutation (`POST`, `PUT`, `PATCH`, `DELETE`) needs a JSON body. Send `'{}'` when the operation takes no fields. Unknown fields are rejected with `bad_request`, so send only the fields listed here.
- `GET` never takes a body. Optional query parameters go on the path; quote such a path in single quotes (`'/api/v1/features/{feature_id}/runs?page=2'`). A path that needs two query parameters contains `&`, so that call asks the user for approval; that is expected.
- Bodies go in single quotes. Keep them free of `;`, `|`, `&`, `<`, `>`, backticks, `$`, real newlines and apostrophes; write those characters as JSON escapes instead (see [environment.md](environment.md)).
- Errors arrive as the canonical envelope: `code`, `summary`, `remediation`, optional `diagnostics`. Report the summary and remediation.
- Snapshots carry `meta.as_of_seq`, the event sequence they reflect; pass it to the events stream with `--after` to see only later changes.
- Stream paths (`GET /api/v1/events` and `GET /api/v1/sessions/{session_id}/output/stream`) require `--timeout`; the helper prints one line per event until the timeout, then exits 0.

## Features and config

- `GET /api/v1/features` — list top-level feature summaries (`id`, `name`, `status`, `current_phase`, `active_run`, `errors`, `active_child`). Child features appear under their parent's `active_child` and `child_history`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features`
- `POST /api/v1/features` — create a feature. Required: `name`. Common optional fields: `description` (the brief, up to 10000 characters), `repos` (repository names from `workspace.repositories[].name` in `GET /api/v1/readiness`), `pipeline` (`medium`, `large`, `moonshot`), `inquireness` (`none`, `medium`, `high`), `risk_level` (`low`, `medium`, `high`), `exit_criteria`, `models`, `checkpoints`, `use_current_branch`, `idempotency_key`. Returns 201 with the new feature's ID; creation only queues worktree setup, so follow it with the `setup` action.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features '{"name":"Add CSV export","description":"Add a CSV export button to the reports page.","repos":["web-app"],"pipeline":"medium"}'`
- `GET /api/v1/features/{feature_id}` — read one feature's detail: status, phase, runs, repositories, gates (`review_gate`, `need_user_input`), `failure`, `errors`, and the `actions` catalog saying which actions are enabled and why others are disabled.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
- `GET /api/v1/features/{feature_id}/config` — read the feature's editable config as `current`, `defaults` and `original`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/config`
- `POST /api/v1/features/{feature_id}/config` — replace the feature's editable config. The body is a whole config, not a patch: start from `current` in the GET response, change only what the user asked, and send every field back: `models`, `effort`, `inquireness`, `checkpoints`, `pipeline`, `input_notifications` (`default`, `enabled`, `muted`), `automatic_review_mode` (`default`, `enabled`, `disabled`, `dangerously_skip_permissions`). Omitted fields reset to empty.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/config '{"models":{"implementation":"opus"},"effort":{},"inquireness":"medium","checkpoints":{"inquiry_review":false,"research_review":false,"design_review":false,"roadmap_review":true,"phase_plan_review":true,"manual_publish":true,"draft_publish":false},"pipeline":"medium","input_notifications":"default","automatic_review_mode":"default"}'`
- `GET /api/v1/features/{feature_id}/live-preview` — compact live preview of what the feature is doing right now.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/live-preview`
- `GET /api/v1/features/{feature_id}/testing-contract` — the current roadmap phase's testing contract: `items` with `item_id`, `name`, `command`, policy flags and any recorded `disposition`. 404 before the phase has a contract.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/testing-contract`
- `GET /api/v1/features/{feature_id}/completion/preflight` — read-only preflight for publish, merge and done: eligible repositories, existing PR URLs, blockers, and the `source_revision` to pass to those actions.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/completion/preflight`
- `GET /api/v1/features/{feature_id}/rewind/preview` — read-only rewind preview. Required query: `target_phase` (`knowledge-base`, `inquire`, `research`, `design`, `plan`, `implement`, `review`, `publish`). Returns valid targets, consequences and the `source_run_number` and `source_revision` to pass to rewind.
  Example: `"$AGENTICO_BIN" api GET '/api/v1/features/{feature_id}/rewind/preview?target_phase=plan'`
- `GET /api/v1/features/{feature_id}/repositories/{repo_name}/diff` — bounded diff of one repository's changes. Optional query: `file_path` for one file's content.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/repositories/{repo_name}/diff`
- `GET /api/v1/features/{feature_id}/repositories/{repo_name}/path` — the repository's worktree path on this machine.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/repositories/{repo_name}/path`

## Feature actions and sub-actions

All actions use `POST /api/v1/features/{feature_id}/actions/{action}`. Check the feature detail's `actions` catalog first: a disabled action lists its `disabled_reasons`, and destructive actions carry an `impact_preview` to show the user before you act.

- `POST /api/v1/features/{feature_id}/actions/start` — start the feature's pipeline. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/start '{}'`
- `POST /api/v1/features/{feature_id}/actions/pause-stop` — stop the running session; the feature can be resumed. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/pause-stop '{}'`
- `POST /api/v1/features/{feature_id}/actions/resume` — resume a stopped or interrupted feature. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/resume '{}'`
- `POST /api/v1/features/{feature_id}/actions/retry` — retry a failed phase. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/retry '{}'`
- `POST /api/v1/features/{feature_id}/actions/restart` — restart a feature that stopped at its iteration limit. Optional: `max_iterations_delta`, `max_plan_iterations_delta` raise those limits.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/restart '{"max_iterations_delta":2}'`
- `POST /api/v1/features/{feature_id}/actions/setup` — dispatch worktree setup for a newly created feature, or retry it after it failed. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/setup '{}'`
- `POST /api/v1/features/{feature_id}/actions/rewind` — rewind to an earlier phase. Required: `target_phase`. Optional: `roadmap_phase`, `upgrade_pipeline`, and `source_run_number` plus `source_revision` from the rewind preview (send them so a stale preview is refused).
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/rewind '{"target_phase":"plan","source_run_number":1,"source_revision":"{source_revision}"}'`
- `POST /api/v1/features/{feature_id}/actions/need-user-input-draft` — save answers to the open need-user-input gate. Required: `answers`, an object keyed by each question's `index` (as a string) from `need_user_input.questions`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/need-user-input-draft '{"answers":{"1":"Use the existing export service."}}'`
- `POST /api/v1/features/{feature_id}/actions/need-user-input` — submit the saved answers and resume the gated feature. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/need-user-input '{}'`
- `POST /api/v1/features/{feature_id}/actions/testing-contract-waive` — waive testing-contract rows the user agreed to waive. Required: `item_ids`, `reason`. Optional: `active_run`, `roadmap_phase`, `contract_revision` from the testing contract you read.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/testing-contract-waive '{"item_ids":["{item_id}"],"reason":"User waived: no display on this machine."}'`
- `POST /api/v1/features/{feature_id}/actions/publish` — publish (open or update pull requests). Optional: `repos`, `title`, `body`, `source_revision` from the completion preflight.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/publish '{"source_revision":"{source_revision}"}'`
- `POST /api/v1/features/{feature_id}/actions/{action}/{subaction}` — run a named sub-action; `{subaction}` is `description` or `fetch`. The publish description sub-action generates a PR title and body. Optional: `repos`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/publish/description '{}'`
- `POST /api/v1/features/{feature_id}/actions/merge` — merge the published pull requests. Optional: `source_revision`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/merge '{"source_revision":"{source_revision}"}'`
- `POST /api/v1/features/{feature_id}/actions/mark-done` — mark the feature done. Optional: `source_revision`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/mark-done '{}'`
- `POST /api/v1/features/{feature_id}/actions/cleanup` — remove the feature's worktrees. Optional: `source_revision`, `target` (`worktrees`).
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/cleanup '{}'`
- `POST /api/v1/features/{feature_id}/actions/delete` — delete the feature (and cascade its children). Show the user the action's `impact_preview` and get explicit confirmation first. Optional: `source_revision`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/delete '{}'`
- `POST /api/v1/features/{feature_id}/actions/discard` — discard an active child feature. Confirm with the user first. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/discard '{}'`
- `POST /api/v1/features/{feature_id}/actions/rebase` — launch a rebase child that reconciles repositories behind their base. Body `{}`; returns 201.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/rebase '{}'`
- `POST /api/v1/features/{feature_id}/actions/refactor` — launch a refactor child from a brief. Required: `name`. Optional: `description`, `pipeline`, `checkpoints`, `models`, `effort`, `risk_level`, `exit_criteria`, `inquireness`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/refactor '{"name":"Extract export service","description":"Move CSV export logic into its own module."}'`

## Reviews and review feedback

Artifact review gates (inquiry, research, design, roadmap, phase plan) pause the feature until a decision is submitted.

- `GET /api/v1/features/{feature_id}/reviews` — read the active review session without reopening it: `review_id`, `artifact_id`, `target_phase`, `text`, `draft_revision`, `can_iterate`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/reviews`
- `POST /api/v1/features/{feature_id}/reviews` — create or reopen the review session for the open gate. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/reviews '{}'`
- `PUT /api/v1/features/{feature_id}/reviews/{review_id}/draft` — replace the draft text (the user's edits or comments on the artifact). Required: `base_revision` (the current `draft_revision`), `text`.
  Example: `"$AGENTICO_BIN" api PUT /api/v1/features/{feature_id}/reviews/{review_id}/draft '{"base_revision":"{draft_revision}","text":"{edited artifact text}"}'`
- `POST /api/v1/features/{feature_id}/reviews/{review_id}/validate` — validate draft text without saving it. Required: `text`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/reviews/{review_id}/validate '{"text":"{edited artifact text}"}'`
- `POST /api/v1/features/{feature_id}/reviews/{review_id}/decision` — commit the draft and decide. Required: `decision` (`proceed` to approve and continue, `iterate` to send the draft back for another pass, only when `can_iterate` is true) and `base_revision`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/reviews/{review_id}/decision '{"decision":"proceed","base_revision":"{draft_revision}"}'`
- `POST /api/v1/features/{feature_id}/actions/review-feedback/fetch` — fetch unaddressed pull-request feedback into a pending draft. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/review-feedback/fetch '{}'`
- `POST /api/v1/features/{feature_id}/actions/review-feedback/selection` — select or deselect feedback items. Required: `expected_revision` (the draft revision), `updates` (a list of `{"stable_ref":..., "selected":true|false}`).
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/review-feedback/selection '{"expected_revision":3,"updates":[{"stable_ref":"{stable_ref}","selected":true}]}'`
- `POST /api/v1/features/{feature_id}/actions/review-feedback` — launch a child feature from the selected feedback. Required: `expected_revision`. Optional: `gate` (turn roadmap and phase-plan review on for parent and child).
  Example: `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/review-feedback '{"expected_revision":3}'`

## Prompts

Feature sessions ask questions (ask-user) and request help; both wait in the pending prompts snapshot.

- `GET /api/v1/prompts` — list pending ask-user and help prompts with `request_id`, `session_id`, `feature_id` and the questions.
  Example: `"$AGENTICO_BIN" api GET /api/v1/prompts`
- `POST /api/v1/prompts/ask-user/answer` — answer an ask-user prompt. Required: `request_id`, `answers` (an object keyed by each question's `index` as a string, from the prompt's `questions`, valued with an array of one-based option indexes or a text answer; every question must be answered; prefer indexes for selections because display labels may be truncated). Optional: `session_id`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/prompts/ask-user/answer '{"request_id":"{request_id}","session_id":"{session_id}","answers":{"1":[1]}}'`

## Help and permission answers

- `POST /api/v1/prompts/help/send` — send a help message to a session that asked for help. Required: `message` and one of `session_id` or `feature_id`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/prompts/help/send '{"session_id":"{session_id}","message":"Use the staging database for tests."}'`
- `GET /api/v1/permissions` — list pending permission requests from feature sessions: `request_id`, `session_id`, `feature_id`, `tool_name`, `summary`, and the remember preview.
  Example: `"$AGENTICO_BIN" api GET /api/v1/permissions`
- `POST /api/v1/permissions/answer` — answer a permission request with the user's decision. Required: `request_id`, `decision` (`allow_once`, `allow_remember`, `deny`, `retry_auto_review`). `allow_remember` also requires `remember_scope` (from the remember preview; empty string means global) and accepts `remember_pattern`. Optional: `session_id`, `auto_approve_scope` (`feature` or `workspace`, not with `deny`). Only answer with the decision the user gave you.
  Example: `"$AGENTICO_BIN" api POST /api/v1/permissions/answer '{"request_id":"{request_id}","session_id":"{session_id}","decision":"allow_once"}'`

## Sessions

- `GET /api/v1/sessions` — list active and recent sessions: `id`, `feature_id`, `run_number`, `phase`, `provider`, `model`, `status`, `turn_state`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/sessions`
- `GET /api/v1/sessions/{session_id}` — read one session's summary.
  Example: `"$AGENTICO_BIN" api GET /api/v1/sessions/{session_id}`

## Transcripts and output streams

- `GET /api/v1/sessions/{session_id}/transcript` — read a bounded page of the session's structured transcript. Optional query: `offset`, `limit`.
  Example: `"$AGENTICO_BIN" api GET '/api/v1/sessions/{session_id}/transcript?limit=50'`
- `GET /api/v1/sessions/{session_id}/output/stream` — tail the session's transcript records as they are written. Requires `--timeout`. Optional query: `from` (the transcript row index to start at).
  Example: `"$AGENTICO_BIN" api --timeout 30s GET '/api/v1/sessions/{session_id}/output/stream?from=120'`

## Runs

- `GET /api/v1/features/{feature_id}/runs` — list the feature's runs, newest first. Optional query: `page`, `page_size`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs`
- `GET /api/v1/features/{feature_id}/runs/{run_number}` — one run's detail: seal, provenance, timing, cost.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}`
- `GET /api/v1/features/{feature_id}/runs/{run_number}/sessions` — the sessions that belong to one run.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/sessions`

## Artifacts and logs

- `GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts` — list a run's artifacts (plans, roadmaps, Q&A, reports) with `artifact_id`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts`
- `GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}` — read a bounded slice of one artifact. Optional query: `offset`, `limit`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}`
- `GET /api/v1/features/{feature_id}/runs/{run_number}/logs` — list a run's logs with `log_id`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/logs`
- `GET /api/v1/features/{feature_id}/runs/{run_number}/logs/{log_id}` — read a bounded slice of one log. Optional query: `offset`, `limit`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/logs/{log_id}`

## Recovery

Recovery lists sessions orphaned by a previous server process.

- `GET /api/v1/recovery` — scan recoverable sessions: a `snapshot_id` and `items`, each with a `key`, `feature_id`, `phase`, `process_alive`, `error`, `log_available`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/recovery`
- `GET /api/v1/recovery/logs` — read one item's redacted log. Required query: `snapshot_id`, `key` (two parameters, so this call asks the user).
  Example: `"$AGENTICO_BIN" api GET '/api/v1/recovery/logs?snapshot_id={snapshot_id}&key={key}'`
- `POST /api/v1/recovery/actions` — apply the user's choice per item. Required: `snapshot_id`, `actions` (an object mapping each item `key` to `resume` or `kill`).
  Example: `"$AGENTICO_BIN" api POST /api/v1/recovery/actions '{"snapshot_id":"{snapshot_id}","actions":{"{key}":"resume"}}'`

## Model catalog

- `GET /api/v1/catalog/models` — list the models each provider offers; use these IDs in `models` fields.
  Example: `"$AGENTICO_BIN" api GET /api/v1/catalog/models`
- `POST /api/v1/catalog/models/refresh` — re-probe one provider and refresh its models. Required: `provider`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/catalog/models/refresh '{"provider":"claude"}'`

## Readiness

- `GET /api/v1/health` — server health, version and launch metadata.
  Example: `"$AGENTICO_BIN" api GET /api/v1/health`
- `GET /api/v1/readiness` — consolidated readiness: providers, workspace roots and `workspace.repositories` (the names `repos` accepts), and per-feature warnings.
  Example: `"$AGENTICO_BIN" api GET /api/v1/readiness`
- `POST /api/v1/readiness/refresh` — re-probe provider readiness now. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/readiness/refresh '{}'`
- `GET /api/v1/readiness/runtime` — runtime readiness without repository inspection (faster).
  Example: `"$AGENTICO_BIN" api GET /api/v1/readiness/runtime`
- `POST /api/v1/readiness/runtime/refresh` — refresh runtime readiness without repository inspection. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/readiness/runtime/refresh '{}'`

## Runtime config

- `GET /api/v1/config/runtime` — read runtime configuration: model and feature defaults, workspace roots, notifications.
  Example: `"$AGENTICO_BIN" api GET /api/v1/config/runtime`
- `PATCH /api/v1/config/runtime` — change runtime configuration. Send only what changes, under `defaults` (for example `models`, `effort`, `pipeline`, `inquireness`, `checkpoints`, `max_iterations`), `workspace_roots` (the complete list; each must be an existing directory) or `notifications`.
  Example: `"$AGENTICO_BIN" api PATCH /api/v1/config/runtime '{"defaults":{"pipeline":"large"}}'`

## Update

- `GET /api/v1/update` — release availability: `status`, `policy`, `current_version`, `latest_version`, active install state, `active_work_summary` (`supervisor_active` while this conversation is starting, running or waiting; `supervisor_waiting` while it waits on a permission or question).
  Example: `"$AGENTICO_BIN" api GET /api/v1/update`
- `POST /api/v1/update/check` — run one release check now. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/update/check '{}'`
- `POST /api/v1/update/install` — install the discovered release, only when the user explicitly asks. Required: `consent` (`true`), `when` (`idle` waits for work to finish, but not for this conversation waiting on the user: the install ends it and its open request reads as interrupted; `now` installs immediately). Optional: `stop_active_work` (with `now`; stops feature sessions and ends this conversation), `version`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/update/install '{"consent":true,"when":"idle"}'`
- `DELETE /api/v1/update/install` — cancel the active install. Body `{}`.
  Example: `"$AGENTICO_BIN" api DELETE /api/v1/update/install '{}'`

## Workspace repositories

Repository operations act on configured workspace roots. Most need explicit user `consent`; never set it without the user's go-ahead.

- `POST /api/v1/workspace/repositories/clone` — start a clone. Required: `remote_url`, `root_path` (a workspace root), `destination` (new folder name), `idempotency_key`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/clone '{"remote_url":"https://github.com/acme/web-app.git","root_path":"/home/me/src","destination":"web-app","idempotency_key":"clone-web-app-1"}'`
- `GET /api/v1/workspace/repositories/clone` — list active and recent clones. Optional query: `limit`, `after`.
  Example: `"$AGENTICO_BIN" api GET /api/v1/workspace/repositories/clone`
- `GET /api/v1/workspace/repositories/clone/{operation_id}` — read one clone operation.
  Example: `"$AGENTICO_BIN" api GET /api/v1/workspace/repositories/clone/{operation_id}`
- `POST /api/v1/workspace/repositories/clone/{operation_id}/cancel` — cancel a clone. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/clone/{operation_id}/cancel '{}'`
- `POST /api/v1/workspace/repositories/clone/{operation_id}/cleanup` — retry cleanup of a clone left cleanup-pending. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/clone/{operation_id}/cleanup '{}'`
- `POST /api/v1/workspace/repositories/clone/{operation_id}/retry` — retry a finished clone attempt. Body `{}`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/clone/{operation_id}/retry '{}'`
- `POST /api/v1/workspace/repositories/create` — create a new feature-ready repository with one empty initial commit. Required: `root_path`, `destination`, `idempotency_key`, `consent`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/create '{"root_path":"/home/me/src","destination":"new-tool","idempotency_key":"create-new-tool-1","consent":true}'`
- `POST /api/v1/workspace/repositories/init` — initialize a git repository at an empty path inside a workspace root. Required: `path`, `consent`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/init '{"path":"/home/me/src/scratch","consent":true}'`
- `POST /api/v1/workspace/repositories/initialize` — create one empty initial commit in an existing clone that has no commits. Required: `repo_key`, `identity` (as read from the repository catalog), `consent`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/initialize '{"repo_key":"{repo_key}","identity":{identity},"consent":true}'`
- `POST /api/v1/workspace/repositories/sources` — resolve the local source each selected repository would use for a feature. Required: `mode` (`default` or `current`), `repositories` (a list of `{"repo_key":..., "identity":...}`).
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/sources '{"mode":"default","repositories":[{"repo_key":"{repo_key}","identity":{identity}}]}'`
- `POST /api/v1/workspace/repositories/origin-status` — compare selected local sources with their origin branches. Required: `mode`, `repositories`. Optional: `refresh` (repository keys to re-check).
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/origin-status '{"mode":"default","repositories":[{"repo_key":"{repo_key}","identity":{identity}}]}'`
- `POST /api/v1/workspace/repositories/update-source` — fast-forward one selected source branch from origin. Required: `repo_key`, `identity`, `mode`, `branch`, `origin_branch`, `expected_local_sha`, `expected_origin_sha`, `checkout_head_ref`, `checkout_head_sha`, all copied from the origin-status result.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/update-source '{"repo_key":"{repo_key}","identity":{identity},"mode":"default","branch":"main","origin_branch":"origin/main","expected_local_sha":"{local_sha}","expected_origin_sha":"{origin_sha}","checkout_head_ref":"{head_ref}","checkout_head_sha":"{head_sha}"}'`
- `POST /api/v1/workspace/repositories/reconcile-source-update` — settle an update-source attempt whose outcome is uncertain. Required: `repo_key`, `identity`, `mode`, `branch`, `origin_branch`, `expected_local_sha`, `expected_origin_sha`.
  Example: `"$AGENTICO_BIN" api POST /api/v1/workspace/repositories/reconcile-source-update '{"repo_key":"{repo_key}","identity":{identity},"mode":"default","branch":"main","origin_branch":"origin/main","expected_local_sha":"{local_sha}","expected_origin_sha":"{origin_sha}"}'`

## Global events stream

- `GET /api/v1/events` — the ordered runtime event stream. Requires `--timeout`; optional `--after <seq>` resumes after a snapshot's `meta.as_of_seq`. Each printed line is one event envelope with `seq`, `kind` (`connected`, `lifecycle.updated`, `session.updated`, `session.output.activity`, `config.updated`, `recovery.updated`, `clone.updated`, `update.updated`, `stream.reset`), and `resource`. Events say what changed, not the new state: re-read the snapshot the event names. On `stream.reset`, re-read your snapshots before trusting later events.
  Example: `"$AGENTICO_BIN" api --timeout 60s --after 1520 GET /api/v1/events`
