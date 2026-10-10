# Supervisor Recipes

Each recipe is a sequence of single, bare helper calls. Run them one at a time, read each response before the next call, and finish with the validating read. Take every `{param}` from a response you read in this conversation. Field details are in [api-reference.md](api-reference.md); quoting rules are in [environment.md](environment.md).

## Create and configure a feature

1. Find the repositories and confirm a provider is ready:
   `"$AGENTICO_BIN" api GET /api/v1/readiness`
   Use the names under `workspace.repositories[].name` for `repos`. If the user named a repository that is not listed, say so instead of guessing a close match.
2. If the user asked for specific models, check the catalog:
   `"$AGENTICO_BIN" api GET /api/v1/catalog/models`
3. Create the feature with the user's brief. Put the brief in `description`; choose `pipeline` only if the user did, otherwise let the defaults apply. A unique `idempotency_key` makes a retried call safe:
   `"$AGENTICO_BIN" api POST /api/v1/features '{"name":"Add CSV export","description":"Add a CSV export button to the reports page.","repos":["web-app"],"idempotency_key":"csv-export-1"}'`
   Note the feature ID in the 201 response.
4. Dispatch its worktree setup. Creation only queues setup; until this call the feature stays in setup and cannot start:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/setup '{}'`
5. Validate creation:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
   Expect the feature with its repositories, and its setup running or complete. If setup failed, report `failure` and offer the `setup` action again.
6. To change its config, read the current config first:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/config`
7. Send the whole `current` object back with only the user's change applied (the endpoint replaces the config; an omitted field resets):
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/config '{"models":{"implementation":"opus"},"effort":{},"inquireness":"medium","checkpoints":{"inquiry_review":false,"research_review":false,"design_review":false,"roadmap_review":true,"phase_plan_review":true,"manual_publish":true,"draft_publish":false},"pipeline":"medium","input_notifications":"default","automatic_review_mode":"default"}'`
8. Validate the change:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/config`
   Report the `current` values that changed.

## Start a feature

1. Read the feature and its action catalog:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
   If the `start` action is disabled, report its `disabled_reasons` (setup still running, readiness problems) instead of calling it. Use `resume` for a stopped feature and `retry` for a failed phase.
2. Start it:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/start '{}'`
3. Validate:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
   Report the new `status` and `current_phase`. A session for the feature appears in `"$AGENTICO_BIN" api GET /api/v1/sessions` once its phase launches.

## Answer a question or gate

Relay the question to the user and send only their answer. Never answer on the user's behalf, even when the answer looks obvious.

### A session's question (ask-user)

1. `"$AGENTICO_BIN" api GET /api/v1/prompts`
   Show the user each question and its options.
2. Answer with each question's `index` as the key and the option label verbatim (or free text) as the value:
   `"$AGENTICO_BIN" api POST /api/v1/prompts/ask-user/answer '{"request_id":"{request_id}","session_id":"{session_id}","answers":{"1":"main"}}'`
3. Validate: `"$AGENTICO_BIN" api GET /api/v1/prompts` no longer lists that `request_id`.

### A help request

1. `"$AGENTICO_BIN" api GET /api/v1/prompts`
2. `"$AGENTICO_BIN" api POST /api/v1/prompts/help/send '{"session_id":"{session_id}","message":"Use the staging database for tests."}'`
3. Validate: `"$AGENTICO_BIN" api GET /api/v1/prompts` no longer lists the help request.

### A permission request from a feature session

1. `"$AGENTICO_BIN" api GET /api/v1/permissions`
   Show the user the `tool_name` and `summary`, and ask for a decision.
2. `"$AGENTICO_BIN" api POST /api/v1/permissions/answer '{"request_id":"{request_id}","session_id":"{session_id}","decision":"allow_once"}'`
3. Validate: `"$AGENTICO_BIN" api GET /api/v1/permissions` no longer lists the request.

### A need-user-input gate

1. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
   Read `need_user_input`: the `summary` and each question's `index` and `prompt`. If `verification.allowed_actions` is present the gate is a missing capability; explain the blockers and the allowed actions.
2. Save the user's answers, keyed by question index:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/need-user-input-draft '{"answers":{"1":"Use the existing export service."}}'`
3. Resume the feature:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/actions/need-user-input '{}'`
4. Validate: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}` shows the gate closed (`need_user_input.open` false or absent) and the feature moving again.

### An artifact review gate

1. Open the review session:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/reviews '{}'`
   Summarise the artifact `text` for the user and ask whether to proceed or iterate.
2. If the user wants edits, save them first (use the session's `draft_revision` as `base_revision`):
   `"$AGENTICO_BIN" api PUT /api/v1/features/{feature_id}/reviews/{review_id}/draft '{"base_revision":"{draft_revision}","text":"{edited artifact text}"}'`
3. Submit the decision with the latest `draft_revision`:
   `"$AGENTICO_BIN" api POST /api/v1/features/{feature_id}/reviews/{review_id}/decision '{"decision":"proceed","base_revision":"{draft_revision}"}'`
4. Validate: `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}` shows the gate cleared and the next phase.

## Inspect a run

1. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs`
2. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}`
   Report status, timing and cost.
3. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/sessions`
4. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts`
5. Read the artifact the user cares about:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/artifacts/{artifact_id}`
6. For what a session actually did, read its transcript:
   `"$AGENTICO_BIN" api GET '/api/v1/sessions/{session_id}/transcript?limit=50'`

## Monitor

Prefer one state check. Only watch over time when the user asks you to.

### Monitor a feature on Slack

Load [slack-monitoring.md](slack-monitoring.md). The request includes DM updates for blockers, questions and phase changes, plus listening for the user's replies and acting on them. Use that playbook for setup, correlation, lifetime and confirmation; use the recipes above and [api-reference.md](api-reference.md) for the actual operations.

### One state poll

1. `"$AGENTICO_BIN" api GET /api/v1/features`
2. `"$AGENTICO_BIN" api GET /api/v1/prompts`
3. `"$AGENTICO_BIN" api GET /api/v1/permissions`

Report what needs the user (open questions, gates, permission requests, failures) first, then what is running.

### Tail the events stream

1. Take a snapshot and note its `meta.as_of_seq`:
   `"$AGENTICO_BIN" api GET /api/v1/features`
2. Watch for a bounded window, starting after that sequence:
   `"$AGENTICO_BIN" api --timeout 120s --after 1520 GET /api/v1/events`
   The call exits 0 when the window ends; no output beyond `connected` means nothing changed.
3. For each `lifecycle.updated` or `session.updated` line, re-read the resource it names (for example `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`) and report the change in one line. On `stream.reset`, go back to step 1.
4. To keep watching, run another window with `--after` set to the last `seq` you saw. Stop when the user's condition is met or the user says stop, and tell the user each time something changed. Keep windows to a few minutes so you can report between them, and never fill the gap with `sleep`.

## Explain a failure

1. `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`
   Read `failure`, `errors` and `warnings`: each carries a `code`, `summary` and `remediation`.
2. Find where it happened:
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}`
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/logs`
   `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}/runs/{run_number}/logs/{log_id}`
3. Read the end of the failing session's transcript:
   `"$AGENTICO_BIN" api GET '/api/v1/sessions/{session_id}/transcript?offset=200'`
4. If a harness could not boot (provider CLI missing, unauthenticated, wrong version), confirm with readiness and report it as an environment problem with Agentico's remediation:
   `"$AGENTICO_BIN" api GET /api/v1/readiness`
5. If sessions were orphaned by a restart, show the recovery snapshot and let the user choose `resume` or `kill` per item:
   `"$AGENTICO_BIN" api GET /api/v1/recovery`
6. Explain the cause in plain words, quote the remediation, and offer the next step (`retry`, `resume`, `rewind`). Act only when the user agrees, then validate with `"$AGENTICO_BIN" api GET /api/v1/features/{feature_id}`.
