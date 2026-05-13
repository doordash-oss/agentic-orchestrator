# Terminal and TUI Design

Terminal UIs have fewer materials than graphical UIs, so hierarchy and
restraint matter more.

## 1. Show State and Next Actions

- The screen should quickly answer: where am I, what is selected, what
  changed, and what can I do next.
- Keep status, selection, and key next steps visible without scrolling away
  from the main task.

## 2. Keyboard-First Means Fully Operable

- Every core action must work without a pointer.
- Use predictable navigation patterns and a universal escape path such as `q`,
  `Esc`, or `Ctrl-C`.
- Keep keybinding help visible or one step away.

## 3. Favor Recognition Over Recall

- Use help footers, inline hints, command palettes, and stable labels.
- Surface shortcuts contextually instead of burying them in docs.

## 4. Build Hierarchy With Text Materials

- In text UIs, hierarchy comes from spacing, alignment, headings, indentation,
  borders, grouping, and stable placement.
- Use color to reinforce these cues, not replace them.

## 5. Feedback Without Disruption

- Progress, loading, success, and error states should appear quickly and
  plainly.
- Do not steal focus for low-severity updates.
- Put the most actionable part of an error where the eye will find it fast.

## 6. Degrade Gracefully

- Handle narrow widths, remote sessions, non-TTY output, `TERM=dumb`, limited
  color depth, and disabled color.
- Respect `NO_COLOR` or equivalent controls.
- Disable non-essential animation when the environment cannot render it well.

## 7. Safety and Scriptability

- Keep destructive actions explicit.
- Offer dry-run or preview modes where they make sense.
- Separate human-readable output from machine-readable modes when needed.
- Exit codes and plain output are part of good UX in terminal tools.

## Good Models

- `git status` for state plus next actions
- help footers or keybinding summaries for discoverability
- command palettes for expert speed without hurting beginner usability
