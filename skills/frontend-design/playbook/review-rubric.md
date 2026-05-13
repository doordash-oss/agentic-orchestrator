# UI Review Rubric

Use this file before presenting, shipping, or reviewing UI work.

## 1. Purpose

- Can a new user tell the primary task within a few seconds?
- Is there one clearly dominant next action?
- Does the screen explain itself without requiring prior memory?

## 2. Visual Structure

- Does typography establish a readable scale with clear roles?
- Is spacing consistent enough that grouping feels intentional?
- Are emphasis, accent color, and motion used sparingly enough to remain
  meaningful?
- On large screens, does the composition add value instead of just filling
  space?

## 3. Interaction

- Is navigation stable and predictable?
- Are forms, errors, and feedback easy to recover from?
- Are advanced options progressively disclosed rather than mixed into the
  default path?
- Is the confirmation state clear after important actions?

## 4. Functional Wiring

This section is a hard rejection gate, not a judgment call. Aesthetic and interaction quality cannot redeem a screen whose controls do nothing.

For every interactive control on the screen — buttons with `onClick`, form submits, menu items, command-palette entries, keyboard shortcuts bound to actions, native menu commands — confirm:

- Does the handler exist as a real backend call? Reject if it is `undefined`, `() => {}`, `console.log`, `// TODO`, `panic("TODO")`, or a stub returning canned data.
- Does the handler reach a backend mutation, not just client-side state? A control whose only effect is closing a modal or changing the route, when the user expected a persistent action, is unwired.
- Does at least one test drive the rendered control and exercise the *real* handler — not a bridge mock that captures arguments? Mock-only coverage on a primary user journey is a rejection.
- Are routed controls accounted for? If the control navigates to another screen whose own controls are unwired, the journey is a cul-de-sac and the inventory chain is broken.

## 5. Accessibility

- Are contrast, focus, keyboard access, and target sizes strong enough?
- Can meaning survive without color, hover, or animation?
- Does the semantic structure match the visual structure?
- Does the UI hold up under large text, zoom, reduced motion, and assistive tech?

## 6. Platform Fit

- Does the UI feel native to its surface?
- Are web/mobile/desktop/terminal conventions respected?
- Is custom chrome solving a real problem, or just ignoring the platform?

## 7. Beauty

- Does the interface have a clear point of view?
- Is that point of view expressed through hierarchy, rhythm, and material
  choices rather than decoration alone?
- Would removing the styling still leave a strong product structure?

If two concerns conflict, choose clarity, accessibility, and platform fit before novelty. Functional wiring is non-negotiable: a screen that fails section 4 cannot pass the rubric, regardless of how it scores elsewhere.
