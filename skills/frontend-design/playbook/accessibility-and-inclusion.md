# Accessibility and Inclusion

Accessibility is part of what makes an interface feel polished. Interfaces
feel cheaper when text is hard to read, focus disappears, targets are tiny, or
motion is exhausting.

## 1. Contrast and Redundancy

- Meet at least `4.5:1` contrast for normal text and `3:1` for large text and
  essential UI indicators.
- Treat focus rings, selected states, charts, and status badges as
  contrast-sensitive UI, not decorative afterthoughts.
- Pair color with text, iconography, shape, or position.

## 2. Keyboard and Focus

- Every core action should be keyboard reachable.
- Focus order should follow the visual and semantic story of the screen.
- Focus indicators must remain obvious on every theme and background.
- When dialogs or popovers close, return focus to a sensible element.

## 3. Structure and Semantics

- Use real headings, labels, landmarks, lists, and native controls when
  available.
- Make the semantic structure mirror the visual structure.
- Status, success, and error messages should be exposed programmatically, not
  only visually.

## 4. Motion and User Settings

- Respect reduced-motion preferences.
- Verify the UI still works with larger text, zoom, high contrast, and screen
  readers.
- Avoid gesture-only, hover-only, or motion-only interactions for core tasks.

## 5. Target Size and Spacing

- Treat `44x44 pt` and `48dp` targets as practical floors for comfortable
  touch interaction.
- Spacing between adjacent targets matters as much as the target itself.
- Small visual icons can still sit inside generous hit areas.

## 6. Plain Language

- Use labels and action text that describe purpose directly.
- Write errors and instructions for comprehension, not cleverness.
- Link text should explain destination or action without relying on surrounding
  context.

## Never Ship

- placeholder-only labels
- color-only state changes
- hidden or low-contrast focus rings
- tiny icon buttons packed edge to edge
- animation that is required to understand success, error, or selection
