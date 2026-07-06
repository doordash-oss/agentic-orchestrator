# Review Comments TUI Redesign

Date: 2026-07-06
Status: Draft for user review

## Context

Pressing `g` on a published feature currently opens a bordered viewport that renders
all fetched GitHub PR review comments as one plain text stream. The result is hard
to scan: there is no structured queue, comment text and diff hunks overflow, the
centered border wastes space, scrolling is unclear, and the only obvious next action
is buried in footer text.

The affected surfaces are:

- `internal/tui/review_comments.go`: classic TUI review-comments preview.
- `internal/tui/api_app.go`: API/dashboard review-comments modal preview.
- `internal/tui/keys.go` and generated keybinding docs: action hints.

## Goals

- Make review comments readable in a terminal without requiring users to inspect
  every comment before launching automation.
- Keep `Shift+A` as an immediate "address all pending comments" action from the
  review-comments screen.
- Add optional curation for users who want to exclude comments before starting.
- Use the full terminal width and height instead of a narrow centered box.
- Reuse the same presentation model where practical for the classic TUI and API
  preview panel.
- Keep the implementation at the TUI model layer and preserve the existing review
  comments lifecycle contract.

## Non-Goals

- Do not change how comments are fetched from GitHub.
- Do not change the review-comments agent skill or resolution JSON contract.
- Do not add per-comment manual replies from this screen.
- Do not require mouse support.
- Do not build a browser or web frontend.

## Chosen UX

Use a browse-first split view.

The screen opens directly into a review-comments workspace with four regions:

1. Header: feature name, repo name, pending count, included count, and filter state.
2. Left queue: one row per comment, with included/excluded state, location, author,
   comment type, and a short body preview.
3. Right detail pane: selected comment metadata, full reviewer body, and formatted
   diff context.
4. Sticky footer: the action bar and navigation hints.

There is no pane focus mode. The queue is always the selector, and the detail pane
always follows the selected queue item.

## Keyboard Model

- `j/k` and `up/down`: move selected comment in the queue.
- `PgUp/PgDn`: scroll the selected comment detail pane.
- `Home/End`: jump to first or last visible comment.
- `/`: enter filter mode for author, file path, comment body, and type.
- `esc`: leave filter mode; when not filtering, go back to the feature detail view.
- `space`: include or exclude the selected comment from the curated batch.
- `enter`: start review-comments for the currently included comments.
- `Shift+A`: start review-comments for all pending comments immediately, ignoring
  any exclusions.
- `q`: quit, matching existing app behavior.

The footer should make the fast path impossible to miss:

```text
[Shift+A] Address all 12   [enter] Address included 10   [space] Include/exclude   [/] Filter   [esc] Back
```

When no comments are excluded, `enter` and `Shift+A` both address the full set, but
the labels still distinguish "included" from "all" so the behavior remains clear
after curation.

## Visual Treatment

The screen should look like an operational terminal tool, not a document dump.

- Remove the narrow centered border around the whole comment stream.
- Use full-width layout with small margins, matching existing TUI surfaces.
- Use the existing palette and styles from `internal/tui/styles.go`.
- Keep cards/panels to functional surfaces only: queue and detail.
- Highlight selected queue row with `SelectedRowStyle`.
- Use clear symbols for included/excluded state, but keep ASCII-safe fallbacks if
  existing terminal tests require stable text.
- Render diff hunks with lightweight syntax cues:
  - removed lines in error/red style,
  - added lines in success/green style,
  - hunk headers and unchanged context in muted style.
- Wrap long comment body lines and diff lines to the detail pane width.

## Component Design

Introduce a reusable normalized comment presentation model inside `internal/tui`.

Suggested units:

- `reviewCommentItem`: normalized fields used by the UI (`ID`, `Type`, `Repo`,
  `Path`, `Line`, `Author`, `Body`, `DiffHunk`, `CreatedAt`).
- `reviewCommentsBrowserModel`: selection, include/exclude state, filter text,
  queue viewport, detail viewport, dimensions, and normalized comments.
- Renderer helpers for queue rows, detail metadata, comment body, and diff hunks.
- Adapter helpers from `git.ReviewComment` and `server.ReviewCommentDTO` into
  `reviewCommentItem`.

The classic `ReviewCommentsModel` should become a thin wrapper around the browser
model. The API preview panel should either reuse the same renderer directly or
share the same normalized row/detail helpers so both surfaces stop drifting.

## Data Flow

Classic TUI:

1. Parent fetches PR comments as it does today.
2. `NewReviewCommentsModel` normalizes comments and initializes all comments as
   included.
3. User navigates, filters, and optionally excludes comments.
4. `Shift+A` emits the existing auto-address action with the full original comment
   set.
5. `enter` emits an action with only included comments.

API/dashboard preview:

1. `review_comments_fetch` returns `server.ReviewCommentDTO` values.
2. The API panel normalizes DTOs for rendering.
3. `Shift+A` starts with the full fetched comment set.
4. `enter`, if supported in the API panel, starts with the included subset.

Subset launch is required for the classic TUI. If wiring subset launch into the
API preview is too large for the first implementation, the API panel should still
share the browse-first presentation, and `enter` can be omitted there until the
mutation path supports it cleanly.

## Empty, Loading, and Error States

- Empty: show a concise success-style message: "No pending review comments for
  this PR." Footer should show `[esc] Back`.
- Filter with no matches: keep the filter visible and show "No comments match
  `<query>`"; `esc` clears the filter.
- All comments excluded: disable `enter` with a warning-style status line:
  "No comments included. Press space to include one, or Shift+A to address all."
- Missing diff hunk: show the reviewer body and a muted "No diff context available"
  line instead of leaving blank space.
- Very narrow terminals: collapse to a single-column reader with queue summary at
  top, but keep `Shift+A`, `enter`, `space`, and `/` semantics unchanged.

## Testing

Add model-layer tests under `internal/tui`, following the repo's TUI test guidance.

Minimum coverage:

- Initial render shows header, queue rows, selected detail, footer actions, and
  the `Shift+A` address-all fast path.
- `j/k` or `up/down` changes selection and updates detail content.
- `PgUp/PgDn` scrolls detail content without changing selection.
- `space` toggles included state and updates included count.
- `enter` uses included comments; `Shift+A` uses all comments.
- `/` filters by body, path, author, and type; `esc` clears filter before leaving.
- Empty, missing diff, all-excluded, and narrow-width states render useful text.
- API preview render uses the same normalized presentation for DTO-backed comments.

Run the fast suite before handoff:

```bash
make test-fast
```

Because this touches TUI behavior, also run:

```bash
go test ./test/e2e/... -count=1 -race
```

If observer wiring is untouched, the TUI observability tier can be skipped with
that reason in the PR verification note.

## Rollout Notes

This is a contained TUI redesign. It should not affect GitHub fetching, lifecycle
state, review-comments iteration directories, or the agent-side resolution flow.
The risky areas are keybinding conflicts, terminal width calculations, and keeping
classic TUI and API preview behavior consistent.
