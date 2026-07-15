# Desktop Parity Matrix

Frozen inventory of every TUI capability/outcome the desktop app must reach
parity with, captured at Phase 1 Task 1. Sources: `docs/keybindings.md`
(generated 2026-07-14), `README.md`, and a read-only scan of `internal/tui/`.

Rules:

- Rows are **frozen**: later phases update `Status` and `Automated evidence`
  but may not silently drop or reword a capability. Additions require a new
  row, not an edit.
- `Status` is `delivered` only when the automated evidence exists and passes;
  everything else stays `pending`. Gaps are never waived by omission.
- "Contract" names the authoritative server surface from `api/openapi.yaml`
  (paths as of Task 1) or the desktop IPC channel from
  `desktop/src/shared/ipc.ts`. `contract pending` marks capabilities whose
  REST surface is still being extended.
- Platform scope is `macOS+Linux` unless a platform-specific behavior is
  called out.

## Foundation (delivered by Task 1)

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Connection shell: branded startup surface showing the connect lifecycle (resolve runtime → discover → attach → authenticate → ready) with status detail, error + retry | Implicit: terminal launch banner / welcome screen (`internal/tui/welcome.go`) | Instrument card with PhaseSpine rail, live status region, error panel with Retry | IPC `agentico:connection:get-status`, `agentico:connection:retry`, `agentico:connection:changed`; driven by the runtime gateway over `GET /api/v1/health` + `GET /api/v1/readiness` | macOS+Linux | `desktop/src/renderer/src/components/ConnectionShell.test.tsx`, `desktop/src/renderer/src/components/PhaseSpine.test.tsx`, `desktop/src/main/__tests__/runtimeGateway.test.ts` | delivered (real gateway, Task 3) |
| Attach to a compatible externally owned server: owner-only loopback discovery validation, explicit compatibility declaration check (API/schema series + runtime policy, never API major alone), incompatible servers hard-block with guidance and are never stopped | Implicit: `PrepareDiscovery` launcher handshake (`cmd/agentico/main.go`, `internal/server/discovery.go`) | Runtime gateway attach path with `external` ownership label in the shell | `GET /api/v1/health` (`compatibility` declaration in `api/openapi.yaml`); discovery record `.agentico-server.json` | macOS+Linux | `desktop/src/main/__tests__/runtimeGateway.test.ts`, `desktop/src/main/__tests__/gatewayDiscovery.test.ts`, `desktop/src/main/__tests__/gatewayCompatibility.test.ts`, `internal/server/compatibility_test.go` | delivered (Task 3) |
| Launch the bundled matched server: resource resolution across macOS/Linux packaged layouts (spaces, non-ASCII, read-only roots), argv-array spawn without shell interpolation, bounded wait for health + authenticated readiness, app-owned status without exposing the bearer token | `agentico` default launcher spawns `agentico server` (`cmd/agentico/main.go`) | Runtime gateway launch path with `app-owned` ownership label; failures land in retryable shell states (`resources-missing`, `launch-failed`, `crashed`) | discovery record + `GET /api/v1/health`, `GET /api/v1/readiness` | macOS+Linux | `desktop/src/main/__tests__/gatewayResources.test.ts`, `desktop/src/main/__tests__/runtimeGateway.test.ts`, `desktop/src/main/__tests__/gatewayServerProcess.test.ts`, `desktop/test/security/gateway.test.ts` | delivered (Task 3) |
| Ownership-aware shutdown: app quit gracefully stops only the app-owned child (SIGTERM, bounded, then SIGKILL, reaped); externally owned servers survive app exit and are never signalled | TUI owned-server shutdown on quit (`waitForOwnedServerShutdownOrStop`, `cmd/agentico/main.go`) | `before-quit` hook awaiting `RuntimeGateway.shutdown()` | local supervision (no REST contract; SIGTERM or `POST /api/v1/shutdown`) | macOS+Linux | `desktop/src/main/__tests__/runtimeGateway.test.ts` (supervision suite), `desktop/src/main/__tests__/gatewayServerProcess.test.ts`, `desktop/test/security/gateway.test.ts` | delivered (Task 3) |
| App-local settings: runtime selection, window state persistence | N/A (terminal geometry; config file edited via `Shift+E`) | Owner-only (0600) schema-versioned `settings.json`, atomic replace, corrupt-file recovery | IPC `agentico:settings:get` / `agentico:settings:update` | macOS+Linux | `desktop/src/main/__tests__/settings.test.ts`, `desktop/test/security/ipcHandlers.test.ts` | delivered |
| Theme: light / dark / system with OS-follow | N/A (terminal palette) | Radiogroup switcher; nativeTheme + CSS custom properties; persisted preference | IPC `agentico:theme:get` / `agentico:theme:set` | macOS+Linux | `desktop/src/renderer/src/App.test.tsx`, `desktop/src/main/__tests__/theme.test.ts` | delivered |

## Dashboard and navigation

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Feature dashboard grouped by status (In Progress / Published / Completed) with attention indicators | Dashboard list; `↑/k`, `↓/j`, `tab` panel switch (`internal/tui/dashboard.go`, `attention.go`) | Dashboard view with grouped feature cards and attention badges, keyboard navigable | `GET /api/v1/features`; live updates via `GET /api/v1/events` (SSE) | macOS+Linux | — | pending |
| Feature detail overview | `enter` to expand, `o` overview (`internal/tui/detail.go`) | Detail pane/route with lifecycle overview and repo progress | `GET /api/v1/features/{feature_id}` | macOS+Linux | — | pending |
| Live event stream keeping all views current (seq/epoch resume) | Implicit (Bubble Tea event loop) | Main-process SSE client with `Last-Event-ID` resume, pushed over validated IPC events | `GET /api/v1/events` (SSE envelope: `seq`, `epoch`, resource version) | macOS+Linux | — | pending |
| Welcome / first-run screen | `internal/tui/welcome.go` | First-run panel in the shell window | local (no server contract) | macOS+Linux | — | pending |
| Help / keybinding reference | `?` help view (`internal/tui/help.go`, `docs/keybindings.md`) | Shortcuts overlay + menu items with platform accelerators | local | macOS+Linux | — | pending |
| Quit / force quit | `q`, `ctrl+c` | Window close / app quit; owned-runtime shutdown prompt | `POST /api/v1/shutdown` (owned runtime only) | macOS+Linux | — | pending |

## Feature creation

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| New-feature wizard: What → Where → Pipeline → Review | `n`, wizard keys (`enter`, `shift+tab`, `tab`, `↑/↓`, `←/→`) (`internal/tui/wizard.go`) | Multi-step modal wizard with the same four steps | `POST /api/v1/features` | macOS+Linux | — | pending |
| Attach images to feature description | `ctrl+v` paste image (`internal/tui/clipboard.go`) | Paste/drop image into description editor | `POST /api/v1/features` (attachment payload) | macOS+Linux | — | pending |
| Attach files via picker | `@` file picker (`internal/tui/filepicker.go`, `fileindex.go`, `autocomplete.go`) | File picker with fuzzy autocomplete | `POST /api/v1/features` (attachment payload) | macOS+Linux | — | pending |
| Repo selection incl. browsing new directories and creating repos on the fly | Wizard Where step (`internal/tui/dirpicker.go`, `workspace_manager.go`, `repos_block.go`) | Repo multi-select with directory browser and init-new-repo | `POST /api/v1/workspace/repositories/init`; `GET /api/v1/config/runtime` (known repos) | macOS+Linux | — | pending |
| Pipeline profile choice (Medium / Large / Moonshot) with gate options | Wizard Pipeline step (`internal/tui/phase_catalog.go`) | Profile cards with gate summary | `POST /api/v1/features` (pipeline field) | macOS+Linux | — | pending |
| Review step: risk level, per-phase models, checkpoints, exit criteria | Wizard Review step; `←/→` cycle model | Review form with model pickers and checkpoint toggles | `GET /api/v1/catalog/models`; `POST /api/v1/features` | macOS+Linux | — | pending |
| Skill picker for feature scoping | `internal/tui/skillpicker.go` | Skill selector in wizard | `POST /api/v1/features` (skills field) | macOS+Linux | — | pending |

## Run control and supervision

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Watch active work live | `a` watch (`internal/tui/attach.go`) | Live session pane streaming agent output | `GET /api/v1/sessions`, `GET /api/v1/sessions/{session_id}/output/stream` (SSE), `/transcript` | macOS+Linux | — | pending |
| Session transcript scrollback | Watch view scrollback | Virtualized transcript view with resume-by-index | `GET /api/v1/sessions/{session_id}/transcript` | macOS+Linux | — | pending |
| Stop watching without stopping the agent | `esc`, `ctrl+]`, `ctrl+x` | Close/blur live pane; agent keeps running | local (view-only) | macOS+Linux | — | pending |
| Answer agent questions (ask-user) | `a` becomes Answer (`internal/tui/attach_askuser.go`, `need_user_input.go`, `question_picker.go`) | Inline question form in live pane | `GET /api/v1/prompts`, `POST /api/v1/prompts/ask-user/answer`, action `need-user-input` | macOS+Linux | — | pending |
| Answer agent help requests | `h` (`internal/tui/help_queue.go`) | Help-request inbox + reply editor | `POST /api/v1/prompts/help/send` | macOS+Linux | — | pending |
| Approve pending permissions | `y` (`internal/tui/chat_permissions.go`) | Permission prompt card with Approve | `GET /api/v1/permissions`, `POST /api/v1/permissions/answer` | macOS+Linux | — | pending |
| Approve & remember permissions | `Shift+A` | Approve-and-remember control on the same card | `POST /api/v1/permissions/answer` (remember flag) | macOS+Linux | — | pending |
| Resume all interrupted features | `Shift+R` | Bulk resume action on dashboard | `POST /api/v1/features/{feature_id}/actions/resume` (per feature) | macOS+Linux | — | pending |
| Stop a running feature | `s` | Stop button with confirmation | `POST /api/v1/features/{feature_id}/actions/pause-stop` | macOS+Linux | — | pending |
| Restart current phase | `r` | Restart control on detail view | `POST /api/v1/features/{feature_id}/actions/restart` | macOS+Linux | — | pending |
| Rewind to an earlier phase | `ctrl+r` (`internal/tui/detail.go` rewind picker) | Phase picker on the PhaseSpine → rewind | `POST /api/v1/features/{feature_id}/actions/rewind` | macOS+Linux | — | pending |
| Retry a failed step | (surfaced on failure) | Retry control on failure card | `POST /api/v1/features/{feature_id}/actions/retry` | macOS+Linux | — | pending |
| Live Preview / logs | `l` (`internal/tui/live_preview.go`) | Live preview pane; log viewer per run | `GET /api/v1/features/{feature_id}/live-preview`, `GET .../runs/{run_number}/logs/{log_id}` | macOS+Linux | — | pending |
| Run artifacts browsing (plans, roadmaps, Q&A, diffs) | Detail views (`internal/tui/artifact_review.go`, `roadmap_helpers.go`, `markdown` renderer) | Artifact browser with markdown rendering | `GET .../runs/{run_number}/artifacts`, `GET .../artifacts/{artifact_id}` | macOS+Linux | — | pending |
| View diff | `v` | Diff viewer (per repo) | `GET .../runs/{run_number}/artifacts` (diff artifact) | macOS+Linux | — | pending |
| Desktop notifications for attention events | Terminal bell / title (`internal/tui/notify.go`; `mute_feature_input` config) | OS notifications with per-feature mute honoring config | SSE `GET /api/v1/events` + `GET /api/v1/config/runtime` (notifications) | macOS+Linux (platform notification APIs) | — | pending |

## Review and publish

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Checkpoint reviews (inquiry, research, design, roadmap, phase plan, manual publish) | `a` becomes Review (`internal/tui/artifact_review.go`, `review_viewport.go`) | Review workspace: artifact, draft feedback, approve/request-changes | `GET /api/v1/features/{feature_id}/reviews`, `PUT .../reviews/{review_id}/draft`, `POST .../reviews/{review_id}/decision` | macOS+Linux | — | pending |
| Publish flow: diff review → commit log → PR description → confirm | `p` (`internal/tui/publish.go`) | Guided publish stepper | `POST /api/v1/features/{feature_id}/actions/publish` (+ subactions `description`, `fetch`, `finish`, `restart`) | macOS+Linux | — | pending |
| Rebase on main | `b` | Rebase action (code-ready/published) | `POST /api/v1/features/{feature_id}/actions/rebase` | macOS+Linux | — | pending |
| Merge to base branch (local repos) | `Shift+M` | Merge action with confirmation | `POST /api/v1/features/{feature_id}/actions/merge` | macOS+Linux | — | pending |
| Refactor implementation with a prompt | `Shift+F` | Refactor prompt dialog | `POST /api/v1/features/{feature_id}/actions/refactor` | macOS+Linux | — | pending |
| View and resolve PR review comments | `g` (`internal/tui/review_comments.go`, `review_messages.go`) | Review-comments inbox with resolve flow | `POST /api/v1/features/{feature_id}/actions/review-comments`, `review-decision` | macOS+Linux | — | pending |
| Mark feature as done | `Shift+D` | Done action on detail view | contract pending (no REST action in current spec) | macOS+Linux | — | pending |
| Clean worktree | `c` | Cleanup action after completion | contract pending (no REST action in current spec) | macOS+Linux | — | pending |
| Delete feature | `d` + confirmation | Delete with confirmation dialog | contract pending (no REST delete in current spec) | macOS+Linux | — | pending |
| Confirmation prompts (y/Y confirm, any other key cancels) | Confirmations overlay | Modal confirm dialogs, keyboard accessible | local | macOS+Linux | — | pending |

## Configuration, chat, and recovery

| Capability | TUI interaction | Planned desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Edit feature config | `e` (`internal/tui/configeditor.go`, `editconfig.go`) | Structured config editor per feature | `GET/PUT /api/v1/features/{feature_id}/config` | macOS+Linux | — | pending |
| Edit workspace/runtime config | `Shift+E` | Workspace config editor (repos, defaults, notifications) | `GET/PUT /api/v1/config/runtime` | macOS+Linux | — | pending |
| Model overrides per phase | Wizard/Review + config editor | Model override editor backed by catalog | `GET /api/v1/catalog/models`; feature config | macOS+Linux | — | pending |
| Ask Me Anything (read-only AI chat) | `/` (`internal/tui/chat.go`, `chat_events.go`, `api_chat_adapter.go`) | Chat panel streaming a read-only session | `POST /api/v1/prompts/chat/start` + session output stream | macOS+Linux | — | pending |
| Provider readiness / prerequisite checks | Launch-time checks; wizard gating | Readiness panel with refresh | `GET /api/v1/readiness`, `POST /api/v1/readiness/refresh` | macOS+Linux | — | pending |
| Session recovery (orphaned sessions from a crashed/other instance; Kill or Resume) | Session Recovery screen (AGENTS.md; `session*.pid` scan) | Recovery dialog at connect time | `GET /api/v1/recovery`, `POST /api/v1/recovery/actions` | macOS+Linux | — | pending |
| Keyboard-first operation (every action reachable without pointer) | All keybindings (`internal/tui/keys.go`) | Full keyboard map + focus management, platform accelerators | local | macOS+Linux | partially: focus/keyboard tests in Task 1 shell (`ConnectionShell.test.tsx`) | pending |
