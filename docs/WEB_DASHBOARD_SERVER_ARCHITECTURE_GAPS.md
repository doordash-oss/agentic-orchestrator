# Web Dashboard Server Architecture Gaps

Running tracker for functionality that does not yet work, is degraded, or is only approximated while the existing Tailwind Web UI is wired to the PR #63 `agentico server` REST/SSE architecture.

The intent is to keep the UI shape stable and make missing server/API capability explicit so we can decide which gaps should be closed in the server, which should be handled in the client, and which legacy behavior can be dropped.

## Known Functional Gaps

| Area | Current Behavior | Impact | Likely Fix |
| --- | --- | --- | --- |
| Diff preview | `api.diff()` currently throws `not_available` because PR #63 does not expose the old feature diff endpoint. | Publish wizard cannot show the pre-publish diff preview. | Add a bounded diff/read endpoint to `agentico server`, or redesign publish preview around run artifacts if diff is intentionally omitted. |
| Manual publish mark | `publishMark()` is a client no-op because there is no direct PR URL mark endpoint in PR #63. | Manual "mark published" behavior from the legacy UI is lost. | Add a mutation for recording a PR URL / publish result, or remove the UI action if publish must always be server-owned. |
| Commit uncommitted changes | The old `publishCommitUncommitted()` action has no direct PR #63 equivalent. The adapter currently calls publish-description generation, which is not functionally equivalent. | Any UI flow expecting "commit local uncommitted changes before publish" will not do the old operation. | Add a dedicated commit-uncommitted action or update the publish flow to use the new action catalog explicitly. |
| Session attach live stream | The old `/api/sessions/:id/ws` live WebSocket is gone. The drawer now polls `/api/v1/sessions/:id/transcript`. | Attach drawer is read-only-ish and delayed; no live push, no clear done event, less faithful tool/control rendering. | Either add SSE/session-specific live transcript events or fully redesign the drawer around REST transcript windows plus global SSE invalidation. |
| Sending help/chat to a session | Old `POST /api/sessions/:id/message` is replaced by `/api/v1/prompts/help/send`. | The text box is no longer true interactive session stdin/chat; it sends a help response if the server can route it. | Decide whether attach drawer should support chat. If yes, use `/api/v1/prompts/chat/start` or add a session-scoped message endpoint. |
| Stopping a session from the drawer | `sessionStop()` is currently a no-op; PR #63 exposes feature pause/stop actions, not a session stop endpoint. | The drawer stop button does not stop the underlying session. | Add session stop mutation or change the button to feature pause/stop with clear labeling. |
| Logs index | PR #63 exposes bounded run log content by log id (`session`, `phase`, `observe`) but not the legacy phase/iteration log index. | Logs modal shows approximated choices from run numbers, not actual per-phase/per-iteration availability. Some selections may 404. | Add `/runs/:n/logs` index metadata or adapt the UI to the new fixed log-id model. |
| Artifact list semantics | PR #63 artifact list is per run. The adapter always uses active run and derives `pending_review_phase` from artifacts. | Review modal may miss artifacts from other runs or choose the wrong default review artifact. | Drive artifact selection from `active_run_detail.pending_review_phase` and expose/select run number in the UI. |
| Review comments fetch | PR #63 uses a POST action requiring a repo. The legacy UI expected a read endpoint returning all comments. The adapter fans out across repo statuses and suppresses failed repos. | Opening the comments modal can perform side effects/fetch work and may hide repo-specific failures. | Add a read snapshot endpoint for cached comments, or update UI to request a repo/mode and show action state. |
| Rewind two-step flow | Legacy UI has `rewind()` preview and `rewindProceed()`. PR #63 currently has a single rewind action path. | Preview warnings/effective phase behavior may not match the legacy modal. | Align UI to PR #63 action catalog, or add an explicit preview/proceed API pair. |
| Rebase force push | The adapter sends `{ force_push: true }` to the rebase action, but PR #63 `RebaseActionRequest` does not define `force_push`. | Force-push button is likely ineffective. | Add a force-push mutation/action if the server should own this, or remove/hide the button under PR #63. |
| Tweak commit step | `tweakCommit()` is currently a client-side placeholder returning `{ had_changes: true }`; PR #63 has start and finish actions, not the old commit action. | Tweak completion flow can claim changes were committed when the server did not do that step. | Add commit/check action or simplify the UI to match PR #63 tweak lifecycle. |
| Feature summary fields | PR #63 summaries do not expose all legacy fields (`risk_level`, `inquireness`, `tags`, `summary`, pending counts). | Filtering/chips/totals can be incomplete compared with legacy Web UI. | Extend read model where the data is still relevant, or remove legacy-only UI decorations. |
| Runtime config shape | Client currently adapts `/config/runtime` and `/catalog/models`, with hardcoded enums for pipeline/risk/inquireness. | Wizard options may drift from server-supported values. | Add enum/options metadata to runtime config or model catalog. |
| Permission / Ask User detail | The adapter maps prompt/permission snapshots into legacy queue shapes, but detailed question payloads are not fully wired into the old drawer flow. | Complex `AskUserQuestion` prompts may render or submit incorrectly. | Build native PR #63 prompt/permission panels instead of routing through legacy session drawer assumptions. |
| Event semantics | PR #63 SSE kinds are normalized into legacy WebSocket event names for the existing Activity panel. | Activity categories/summaries can be vague or inaccurate. | Update Activity panel to render PR #63 SSE event kinds directly. |

## Confirmed Working / Intended

| Area | Notes |
| --- | --- |
| Health | Uses `GET /api/v1/health` and adapts owner version/start time into the existing top bar. |
| Feature list | Uses `GET /api/v1/features` and preserves the existing Tailwind list layout. |
| Feature detail basics | Uses `GET /api/v1/features/:id` plus sessions/prompts/permissions snapshots for the existing detail view. |
| Create/start/stop/delete basics | Uses PR #63 trusted mutations under `/api/v1/features` and `/api/v1/features/:id/actions/*`. |
| Global activity connection | Uses `EventSource("/api/v1/events")` and keeps the existing right-rail UI alive through an adapter. |
| Dev proxy | Vite proxies `/api/v1` to `AGENTICO_SERVER_URL`, defaulting to `http://127.0.0.1:7878`. |

## Follow-Up Principle

Prefer removing adapter shims as PR #63 server coverage improves. The long-term Web UI should consume PR #63 DTOs and action catalog directly, with this document shrinking as gaps are resolved.
