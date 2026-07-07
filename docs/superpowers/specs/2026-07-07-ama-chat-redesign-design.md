# Ask Me Anything (AMA) Chat Redesign

Date: 2026-07-07
Status: Draft for user review

## Context

The AMA chat (`internal/tui/chat.go`) is a bottom panel opened with `/` from the dashboard.
It renders every question and answer into one accumulated plain-text string and displays it
in a `viewport.Model` above a fixed 3-row `textarea.Model`. This is visibly and functionally
behind the rest of the app:

- Assistant text is never run through the existing `renderMarkdown` (glamour) helper, so code
  blocks, lists, and formatting all render as a raw unstyled wall of text.
- `Enter` always sends immediately; there is no way to insert a literal newline, so multi-line
  questions are impossible to compose.
- When the agent asks a clarifying `AskUserQuestion`, the options are dumped as flat text
  (`"1. A, 2. B"`) and answered by typing free text — there is no selectable picker, and only
  one pending question is tracked at a time (multi-question bundles render but can't be
  navigated).
- User and assistant turns are barely distinguished (a bold "You:" prefix vs. unstyled text),
  so the transcript is hard to scan.
- The panel's height is a fixed function of terminal size only (`chatPanelHeight`, clamped to
  10–18 rows / 35% of height) regardless of whether there's any conversation to show.

The per-feature session view (`internal/tui/attach.go`, `AttachModel`) already solves the first
four problems in production: it renders assistant text through `renderMarkdown`, supports
`shift+enter` to insert a newline with the textarea auto-growing 1→6 rows, and has a full
question-picker (numbered options, cursor highlight, checkboxes for multi-select, confidence/
description lines, a freeform "Type something" row, and navigation across multi-question
bundles with a recap-before-submit step). It has no fullscreen/maximize concept anywhere,
because its panel height is entirely content-driven (no manual size toggle exists in
`internal/tui` today).

## Goals

- Reuse `attach.go`'s proven question-picker and expanding-input interaction patterns in AMA
  instead of inventing new ones, so the two chat surfaces in the app behave consistently.
- Render AMA assistant text through the same markdown pipeline the rest of the app uses.
- Give the AMA panel real turn structure (who said what, at a glance) instead of one
  undifferentiated text blob.
- Let the panel stay small when idle and grow with conversation, with a manual full-screen
  escape hatch for reading long answers or composing long questions.
- Gain multi-question `AskUserQuestion` navigation in AMA as a natural side effect of sharing
  the picker with `attach.go` (today AMA only tracks one pending question at a time).

## Non-Goals

- Do not change the session/attach protocol, SDK message shapes, or how the agent issues
  `AskUserQuestion` tool calls.
- Do not persist AMA chat history across app restarts — it stays session-scoped, same as today.
- Do not add mouse support.
- Do not change `attach.go`'s user-visible behavior or appearance. Its internals move into
  shared helpers; what a user sees in the per-feature session view does not change.
- Do not build a browser or web frontend — this is a terminal application.

## Visual Treatment

Each turn is tagged inline, REPL-style — no message bubbles, no per-turn boxes:

```
[you]  How do I add multi-line input to the AMA chat?

[agent]  Bind shift+enter to insert a literal newline into the
textarea before forwarding to bubbles — attach.go already does
this:

    case key.Matches(msg, key.WithKeys("shift+enter")):
        m.preExpandInput()
        ...

Then grow the box height as lines accumulate, capped at 6 rows.
```

- `[you]` reuses `attach.go`'s existing tag convention exactly: brand purple (`colorBrand`),
  bold. Only the tag is bold/colored — the question text itself renders in normal foreground,
  not shouted in bold purple end-to-end as it is today.
- `[agent]` is new: teal (`colorActive`), chosen because it's unused for "someone is talking"
  semantics elsewhere (unlike `colorInfo`/blue, already used for in-progress status icons in
  `styles.go`'s `statusIcon`/`phaseIcon`).
- Assistant text renders through `renderMarkdown` (the same glamour-backed helper `attach.go`
  uses), so code blocks, lists, and emphasis render properly.

**Signature element:** the `[agent]` tag doubles as the turn's live status indicator instead of
a separate "Thinking…" line. While responding, the tag shows an animated spinner frame in place
of the static glyph, with the tool-use/thinking snippet muted beside it:

```
[⠹ agent]  Using Read...
```

It settles back to `[agent]` the instant real text starts streaming, and flips to a red variant
on error. Cancelled turns render as a muted `[cancelled]` tag rather than raw appended text.

**Question picker** — ported visually from `attach.go`'s existing picker, rendered inline in the
scrollback instead of a separate panel:

```
[agent]  Which environment should we deploy to?

  1. staging
❯ 2. production
     Rolls out to all live traffic
  3. Type something.
  ─────────────────────────────
↑/↓ navigate · enter select
```

Multi-select adds the same `[ ]`/`[x]` checkboxes `attach.go` uses, with the same footer hint
convention (`space toggle · enter submit · ↑/↓ navigate`). Multi-question bundles get the same
question-to-question navigation and recap-before-submit step `attach.go` already has.

**Auto-picked answers** (answered out-of-band without the interactive picker) get the same
`[auto-picked, confidence: 0.87]`-style warning-accented treatment `attach.go` already renders
for its transcript.

## Sizing and the Expand Mechanic

The panel is compact by default and grows with content, mirroring `attach.go`'s own philosophy
of automatic, content-driven sizing (it has no manual size-toggle key anywhere):

- Empty chat: just the placeholder line and a 1-row input — well under today's flat 10-row
  floor, which applies regardless of whether there's anything to show.
- As turns accumulate, docked height grows up to the existing cap (18 rows / 35% of terminal
  height) — this ceiling is unchanged from today.
- `ctrl+g` toggles full-screen: the chat panel takes the entire terminal and the dashboard is
  not rendered at all. This is a new key — there is no existing fullscreen/maximize convention
  anywhere in `internal/tui` to conflict with, and `ctrl+g` doesn't collide with any binding
  used by `textarea.Model`'s default (emacs-style) keymap.

Returning from full-screen reuses `esc`'s existing step-back behavior with one new branch
inserted ahead of today's logic, so every `esc` press steps back exactly one level:

1. If full-screen → drop to docked (chat stays open; if it was responding, it keeps responding).
2. Else if responding → background the panel (existing behavior: session keeps running).
3. Else if input is non-empty → clear the input (existing behavior).
4. Else → close the panel (existing behavior).

## Keyboard Model

- `enter`: send the current input, or (while a question is pending) select the focused option.
- `shift+enter`: insert a literal newline; textarea grows up to 6 rows (same binding and growth
  behavior as `attach.go`).
- `up`/`down` (also `k`/`j`): navigate picker options when a question is pending; otherwise
  scroll history. The picker owns the keyboard while a question is pending (same as
  `attach.go`'s "swallow all other keys during multi-choice"), so single-letter `j`/`k` are safe
  reuses there — they are not bound outside of an active question, since the textarea has focus
  and would otherwise just type them.
- `space`: toggle inclusion for the focused option on a multi-select question.
- `left`/`right` (also `h`/`l`): move between questions in a multi-question bundle.
- `ctrl+g`: toggle full-screen.
- `esc`: step back one level per the hierarchy above.
- `ctrl+c`: cancel the in-flight turn (unchanged from today).

Footer hints switch contextually, matching `attach.go`'s existing convention: normal mode shows
`[enter] Send · [shift+enter] Newline · [esc] Close · [ctrl+g] Full screen`; an active
single-select question shows `↑/↓ navigate · enter select`; multi-select shows
`space toggle · enter submit · ↑/↓ navigate`; responding shows `[esc] Background · [ctrl+c] Cancel`.

## Component Design

Introduce two shared pieces in `internal/tui`, extracted from their current inline homes in
`attach.go`:

- **Expanding-input helper**: generalizes `preExpandInput`/`syncInputHeight`/`maxInputLines`
  (currently duplicated ad hoc at three call sites in `attach.go`) into one reusable piece
  wrapping a `textarea.Model` plus its current/max height. `attach.go`'s three call sites and
  the new `chat.go` call site all use it instead of copy-pasted logic.
- **Question picker**: generalizes the option-navigation state and render/update logic
  currently inline in `attach.go`'s `Update()` and `renderQuestion()`, driven by `attach.go`'s
  existing `askUserQuestion`/`askUserOption`/`pendingAskBundle` types (already shared package-
  level types, already used by both files' parsing today). Both `attach.go` and `chat.go` hold
  an instance of this picker and delegate rendering/key-handling to it while a question bundle
  is active.

`attach.go` is refactored to call these two shared pieces instead of its inline versions. Its
user-visible behavior does not change; `attach_test.go` is the regression harness for this move.

`chat.go` changes:

- `history string` / `partialText string` become a `[]chatTurn` list (role, text, in-progress
  flag) so each turn can be tagged, colored, and markdown-rendered independently instead of one
  continuously concatenated string.
- `chatPanelHeight(totalHeight int)` (a free function of total height only) becomes a method on
  `ChatModel`, mirroring `AttachModel`'s own `chatPanelHeight()` method, so it can factor in
  turn count and the new `fullscreen bool` field.
- New `fullscreen bool` field on `ChatModel`; `View()` branches on it to render at full
  width/height with no border-title suffix change needed beyond reflecting the mode.

`api_app.go` changes: when `m.chat.fullscreen` is true, skip rendering the dashboard entirely
and give the chat model the full terminal dimensions; otherwise call the new content-aware
height method instead of today's fixed function of total height only.

## Data Flow

Unchanged: sending a message still goes through `SendUserMessage`/`StartSession`; streaming
assistant text still arrives via `AttachCh` batches; `AskUserQuestion` control requests are
still parsed with the existing `parseAskUserQuestions`.

What changes is how the result is stored and displayed:

- Streaming text updates the last turn in `[]chatTurn` in place (in-progress flag set) instead
  of replacing a separate `partialText` string; on the terminal `Result` message it's committed
  as a finished turn, matching today's partial→final commit semantics.
- An incoming `AskUserQuestion` control request activates the shared picker against the parsed
  bundle (mirroring `attach.go`'s `activateAskUserQuestions`) instead of appending flat text and
  tracking a single `*llm.ControlRequestMessage`. This is what gains AMA multi-question
  navigation and the recap-before-submit step for free.
- Auto-picked answers (already-answered-elsewhere questions) render with the same warning-
  accented `[auto-picked, confidence: X]` tag `attach.go` already produces for its transcript.

## Empty, Loading, and Error States

- Empty: placeholder text, unchanged in wording, in a much smaller panel than today's fixed
  10-row floor.
- Responding: `[⠹ agent]` spinner-tag with a muted tool-use/thinking snippet beside it, replaced
  in place by streamed text as it arrives.
- Cancelled (`ctrl+c`): a muted `[cancelled]` tag turn instead of raw text appended to history.
- Session/turn error: an `[agent]` turn rendered in the red error variant instead of a bare
  `ErrorStyle`-rendered line with no tag.
- Narrow terminal: the picker's side-by-side option-preview pane collapses to stacked-below
  under tight width, reusing `attach.go`'s existing width guard rather than new logic.
- Full-screen on a narrow terminal: no special-casing beyond what full width/height already
  implies; the same narrow-mode content rules apply as in docked mode.

## Testing

Per `AGENTS.md`, TUI package tests stay at the model layer — drive `ChatModel.Update`/`View`
and the new shared helpers directly, no `teatest`/terminal-lifecycle flows in the fast suite.

Minimum coverage:

- Turn rendering: `[you]`/`[agent]` tags, colors, and markdown rendering per turn.
- `shift+enter` inserts a newline and grows the input up to 6 rows without sending; plain
  `enter` still sends.
- Picker navigation (`up`/`down`, `space` for multi-select, `enter` to confirm) matches
  `attach.go`'s behavior for both single-select and multi-select bundles, including a
  multi-question bundle with `left`/`right` navigation and the recap step.
- `ctrl+g` toggles full-screen; `esc` steps down exactly one level at a time through
  full-screen → docked → clear-input → close, including while responding.
- Auto-picked answers render the same `[auto-picked, confidence: X]` treatment as `attach.go`.
- `attach.go` regression: existing `attach_test.go` coverage continues to pass unchanged after
  its picker/input logic is delegated to the new shared helpers.

Run before handoff:

```bash
make test-fast
go vet ./...
go build ./...
```

Because this touches TUI and session-lifecycle behavior, also run:

```bash
go test ./test/e2e/... -count=1 -race
```

The TUI observability gate can be skipped with that reason in the PR verification note unless
observer wiring is touched.

## Rollout Notes

This is a contained TUI redesign plus one shared-code extraction. It should not affect the
session/attach protocol, how the agent issues `AskUserQuestion`, or `attach.go`'s user-visible
behavior. The main risk areas are the `attach.go` extraction regressing existing behavior (why
its own test suite is the regression harness) and the dashboard/chat height composition in
`api_app.go` when toggling full-screen.
