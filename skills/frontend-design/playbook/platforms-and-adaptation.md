# Platforms and Adaptation

A good multi-surface product feels like the same product everywhere, but each
surface still feels native.

## Shared Rules

- Keep the core model, terminology, and brand consistent across surfaces.
- Adapt layout, navigation, density, and input patterns to each platform.
- Reuse native components before inventing custom chrome.
- Add value on larger surfaces instead of merely stretching compact layouts.

## Web

- Design from a single-column mobile baseline, then add structure as space
  becomes available.
- Support zoom, reflow, keyboard navigation, and semantic structure.
- Do not rely on hover alone for discovery or action access.
- Constrain line length and content width on wide screens.

## Mobile / Compact

- Optimize for reach, safe areas, and short attention windows.
- Keep top-level navigation simple and thumb-friendly.
- Prefer simple gestures and always provide an alternative to gesture-only
  actions.
- Use compact layouts that prioritize the primary task over chrome.

## Desktop / Large-Screen App

- Respect windowing, toolbars, menus, multi-pane layouts, and keyboard-first
  flows.
- Persistent side navigation, split views, and inspector panels are justified
  only when the information architecture supports them.
- Avoid custom window chrome unless you can match the platform as well as the
  platform can.

## Responsive and Multi-Pane Layouts

- Let content reflow, reorganize, or split into panes when more space is
  available.
- Move from bottom navigation or tab bars on compact surfaces to rails,
  sidebars, or drawers on larger ones when appropriate.
- Large screens should show more value: summary plus detail, supporting
  context, bulk actions, or richer navigation.
- Wide layouts should not turn into unreadable rivers of text or aimless card
  galleries.

## Brand Consistency

- Share tokens and semantic roles across surfaces.
- Let platform conventions change the mechanics; keep the product's voice,
  hierarchy, and information model recognizable.
