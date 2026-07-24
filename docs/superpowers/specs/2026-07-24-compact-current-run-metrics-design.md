# Compact Current Run Metrics

## Goal

Keep the collapsed current-run inspection entirely visible in an ordinary
desktop window, while showing the active roadmap phase's elapsed time, cost,
and a readable model name.

## Layout

The live cockpit surface remains height-constrained. The current-run inspection
fills that surface and gives its remaining height to the live agent activity
frame. The inspection header, roadmap gauge, activity summary, metrics, review
summary, and collapsed artifact/log controls retain their intrinsic height.

The live agent activity frame may shrink below its current 220 px minimum in a
short window. Its transcript and agent roster continue to own their internal
scrolling, so earlier activity stays reachable without making the whole
inspection scroll. A small minimum height protects the frame from collapsing
into an unusable strip; only below that complete-panel minimum does the cockpit
surface fall back to outer scrolling.

Expanded artifacts, logs, and inline file content remain allowed to extend the
inspection and use the cockpit surface's existing overflow behavior.

## Phase Metrics

Metric lookup continues to accept ordinary phase names such as `research` and
`Review`. During roadmap planning or implementation, it also receives the
current roadmap phase number and checks the accounting keys written by the
orchestrator:

- `phase-{N}-plan` while planning a roadmap phase.
- `phase-{N}-impl` while implementing or reviewing that roadmap phase.

Exact and case-insensitive phase-name matching remain fallbacks. Final review
continues to map to the run's `review` entry.

This logic is shared by elapsed-time and cost rendering so the two readings
cannot select different accounting scopes.

## Model Name

The model chip shows the model catalogue's human-readable `displayName` when
the active session model matches a catalogue entry. Matching accepts both bare
catalogue IDs and provider-qualified IDs.

If catalogue metadata is unavailable, the chip derives a compact fallback from
the last meaningful segment of a slash-qualified model ID. The full canonical
identifier remains available in the chip's tooltip.

## Error Handling

Failure to load run detail or model catalogue metadata must not blank the live
inspection. Metrics retain the existing em dash for genuinely unavailable
values, and model display falls back to the compact identifier.

## Accessibility

Document order, keyboard focus order, resource toggles, and the full-screen
preview affordance remain unchanged. The transcript's scroll container stays
keyboard- and pointer-scrollable, and the model chip retains the full identifier
as its accessible tooltip.

## Testing

- Add pure renderer tests for roadmap accounting-key lookup and compact model
  naming.
- Update the current-run component test to use authentic
  `phase-{N}-impl` metric keys and a provider-qualified model.
- Extend the Playwright layout contract to a short desktop viewport and assert
  that the collapsed inspection fits its surface while the activity frame owns
  overflow.
- Run targeted renderer and screenshot-layout tests.
- Run desktop static checks and build.
- Run the Fast suite (`make test-fast`) before handoff.
