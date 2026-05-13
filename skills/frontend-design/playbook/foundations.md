# Visual Foundations

## 1. Legibility First

- Start with a small semantic type scale: display, title, body, label, caption.
- Use readable default sizes and weights. Thin type is acceptable only when
  size, contrast, and context support it.
- Keep body text boring in the best way: highly readable, stable, and easy to
  scan.
- Reserve expressive type for hero moments, headings, or brand accents.

## 2. Hierarchy Uses Multiple Signals

- Build hierarchy with size, weight, contrast, placement, and surrounding
  space together.
- Make the primary action unmistakable. Secondary actions should look
  secondary.
- Use whitespace to create focus before adding more borders, shadows, or
  color.

## 3. Spacing Is a System

- Pick a base spacing unit and reuse it consistently. A `4px` / `8px` rhythm
  is common because it scales cleanly across components and layouts.
- Align components, text blocks, and icons to shared edges or keylines.
- Group related items with proximity first. Separate unrelated items with
  larger gaps than intra-group gaps.

## 4. Composition Beats Decoration

- Use grids to create rhythm, not just symmetry.
- On larger surfaces, add structure: panes, sidebars, supporting panels, or
  summary/detail layouts.
- Constrain reading measure for long-form content. On wider surfaces, lines
  around `60-75` characters are usually more comfortable than full-width text.
- Avoid "wall of cards" layouts when the task has a natural order or
  hierarchy.

## 5. Color Needs Roles

- Keep most surfaces neutral. Use accent colors to direct attention, not to
  fill every available area.
- Define semantic roles for primary action, link, focus, success, warning,
  error, and selected state.
- Never use color as the only signal for meaning or state.
- Keep brand color intensity high where it helps orientation, low where it
  would flatten hierarchy.

## 6. Motion Must Explain Something

- Use motion to clarify causality, focus, progress, or spatial change.
- Small state changes deserve subtle motion; major transitions can be more
  expressive.
- Non-essential motion must have a reduced-motion fallback.
- If removing the animation makes the interface easier to understand, the
  motion was not helping.

## 7. Tokenize the Design

- Prefer named tokens and scales over one-off values.
- Tokenize spacing, typography, color roles, radius, elevation, focus, and
  state changes.
- Shared tokens preserve brand consistency while still allowing
  surface-specific adaptation.

## Avoid

- placeholder-only hierarchy where everything is medium gray and medium weight
- accent colors used on large surfaces without a reason
- inconsistent padding between similar components
- decorative gradients, shadows, or glass effects that obscure task structure
- animations that exist only to prove that the UI is modern
