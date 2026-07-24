# Dynamic Live Preview Height

## Goal

Use the available height in the current-run stage for live agent activity while
keeping the run activity, metrics, review state, and collapsed artifact/log
controls visible without scrolling.

## Design

The current-run inspection becomes a height-constrained grid that fills its
cockpit surface. Its header, roadmap gauge, errors, metrics, review summary, and
resource controls use their intrinsic height. The preview row receives the
remaining height through `minmax(0, 1fr)`.

The live preview frame removes its fixed `min(360px, 48vh)` height and fills the
flexible preview row. It retains a practical minimum height when the surrounding
surface is not height-constrained, so narrow or short layouts remain usable.

The normal collapsed inspection must fit the visible cockpit stage without
requiring the stage to scroll. Expanded artifacts, logs, and artifact content
remain allowed to extend the inspection and use the existing stage scrolling,
because their content is unbounded.

## Responsive Behavior

- In a sufficiently tall desktop window, the preview grows to consume unused
  stage height.
- As the window becomes shorter, the preview shrinks before metrics or resource
  controls are pushed below the fold.
- At the preview's minimum usable height, the existing stage scrollbar becomes
  the fallback rather than compressing the transcript into an unusable strip.
- The existing full-screen preview remains unchanged.

## Accessibility

The change preserves document order, focus order, button behavior, and the
existing full-screen affordance. No content is conditionally hidden to make the
layout fit.

## Testing

- Add a renderer layout contract that covers the height-filling class structure
  or computed style boundary without relying on a specific viewport pixel
  snapshot.
- Run the targeted renderer tests.
- Run the Fast suite (`make test-fast`) before handoff.
- Run the relevant desktop typecheck/build or test script for the renderer
  stylesheet/component change.
