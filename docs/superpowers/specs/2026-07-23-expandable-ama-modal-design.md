# Expandable AMA Modal

## Goal

Let an operator temporarily expand the AMA chat into a near-full-window surface
when the bottom dock does not provide enough room, without losing chat state or
opening a separate operating-system window.

## Existing Behavior

The AMA surface has compact and expanded dock modes. The expanded dock shares
the bottom of the application window with the active feature workspace and is
limited to roughly half the viewport height. The renderer already owns the
transcript subscription, composer draft, image attachments, pending AMA
questions, notices, and end-session confirmation in `AmaDock`.

The application also has an established near-full-window modal pattern for the
live agent preview, including accessible dialog semantics and keyboard
dismissal.

## Design

Add an `Expand AMA` control to the AMA header. It opens a near-full-window modal
overlay inside Agentico. This is a presentation change only: the modal uses the
same renderer-owned AMA state and event handlers as the dock.

The modal contains:

- a header with AMA status, `End AMA`, and `Close expanded AMA` controls;
- the live transcript and any pending AMA questions in the flexible body; and
- the existing image attachments and message composer pinned at the bottom.

Only one copy of the interactive AMA body and composer is rendered while the
modal is open. Closing the modal restores the dock presentation with the
transcript position, unsent draft, attachments, notices, and active stream
intact. Expansion does not change or persist the user's compact/expanded dock
preference.

The modal should reuse the visual proportions and layering conventions of the
existing live agent preview. It is not a separate Electron `BrowserWindow`.

## State and Data Flow

`AmaDock` retains ownership of all chat and attention state. A local,
non-persisted boolean selects whether the shared AMA surface is rendered in the
dock or in the modal overlay. Opening and closing the modal must not recreate
the output subscription or reload the transcript.

The expand trigger is available from the AMA header. Opening moves focus into
the modal. Closing by its button, Escape, or the backdrop returns focus to the
expand trigger. Existing submit, paste, attachment removal, attention response,
and end-session paths remain unchanged.

## Responsive Behavior

- The modal fills most of the application viewport while leaving a visible
  margin around the overlay.
- The transcript receives the available height and scrolls internally.
- The composer remains visible at the bottom.
- On short or narrow windows, the modal contracts to the viewport rather than
  overflowing it.

## Accessibility

The expanded surface is an `aria-modal` dialog labelled `Expanded AMA`.
Keyboard focus stays within the modal while it is open. Escape and the explicit
close control dismiss it, and focus returns to the expand trigger. The hidden
dock does not leave duplicate transcript, composer, or control landmarks in the
accessibility tree.

## Error Handling

Expansion adds no new server or IPC operation. Existing transcript, submission,
attention, and end-session errors continue to render through the current AMA
notice and alert paths. A presentation error cannot interrupt the underlying
AMA session.

## Verification

Renderer regression coverage will prove that:

1. the expand control opens a labelled modal containing the AMA transcript and
   composer;
2. the expanded surface can be closed by its explicit control and Escape;
3. an unsent draft survives an open-and-close cycle;
4. focus returns to the expand trigger; and
5. the transcript and composer are not duplicated while the modal is open.

Run the focused AMA renderer tests, the desktop renderer static/build checks,
and the repository Fast suite (`make test-fast`) before handoff.
