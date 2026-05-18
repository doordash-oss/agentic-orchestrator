---
name: frontend-design
description: Create distinctive, production-grade frontend interfaces with high design quality. Use this skill when the user asks to build web components, pages, or applications. Generates creative, polished code that avoids generic AI aesthetics.
topics: UI, UX, TUI, frontend, design, components, widgets, layout, styling, dashboard, visual, Bubbletea, lipgloss, CSS, React
---

This skill guides creation of distinctive, production-grade frontend interfaces that avoid generic "AI slop" aesthetics. Implement real working code with exceptional attention to aesthetic details and creative choices.

The user provides frontend requirements: a component, page, application, or interface to build. They may include context about the purpose, audience, or technical constraints.

## Playbook (Research-Grounded Reference)

Before designing, **consult the playbook in [playbook/index.md](playbook/index.md)**. It is the authoritative, research-backed foundation for this skill — grounded in official design systems and usability/accessibility research, not framework taste. Creative boldness (below) must rest on these fundamentals; they are complements, not alternatives.

Start with [playbook/index.md](playbook/index.md) to pick files by surface and phase. Minimum reads per phase:

| Phase | Read |
|-------|------|
| Design / Research | `foundations.md`, `interaction-and-trust.md`, `platforms-and-adaptation.md`, `accessibility-and-inclusion.md` |
| Plan | `foundations.md`, `accessibility-and-inclusion.md`, plus the surface-specific file (`platforms-and-adaptation.md` or `terminal-and-tui.md`) |
| Implement | `foundations.md`, `accessibility-and-inclusion.md`, plus `interaction-and-trust.md` or `terminal-and-tui.md` as the surface demands |
| Review | `review-rubric.md` plus every file relevant to the implemented surface |

Always read `accessibility-and-inclusion.md` — contrast, focus, semantics, targets, reduced motion, and multimodal feedback are non-negotiable. For terminal/TUI work (e.g. Bubbletea, lipgloss), `terminal-and-tui.md` is required. Cite `sources.md` when the user asks for rationale or references.

## Design Thinking

Before coding, understand the context and commit to a BOLD aesthetic direction:
- **Purpose**: What problem does this interface solve? Who uses it?
- **Tone**: Pick an extreme: brutally minimal, maximalist chaos, retro-futuristic, organic/natural, luxury/refined, playful/toy-like, editorial/magazine, brutalist/raw, art deco/geometric, soft/pastel, industrial/utilitarian, etc. There are so many flavors to choose from. Use these for inspiration but design one that is true to the aesthetic direction.
- **Constraints**: Technical requirements (framework, performance, accessibility).
- **Differentiation**: What makes this UNFORGETTABLE? What's the one thing someone will remember?

**CRITICAL**: Choose a clear conceptual direction and execute it with precision. Bold maximalism and refined minimalism both work - the key is intentionality, not intensity.

Then implement working code (HTML/CSS/JS, React, Vue, etc.) that is:
- Production-grade and functional
- Visually striking and memorable
- Cohesive with a clear aesthetic point-of-view
- Meticulously refined in every detail

## Frontend Aesthetics Guidelines

Focus on:
- **Typography**: Choose fonts that are beautiful, unique, and interesting. Avoid generic fonts like Arial and Inter; opt instead for distinctive choices that elevate the frontend's aesthetics; unexpected, characterful font choices. Pair a distinctive display font with a refined body font.
- **Color & Theme**: Commit to a cohesive aesthetic. Use CSS variables for consistency. Dominant colors with sharp accents outperform timid, evenly-distributed palettes.
- **Motion**: Use animations for effects and micro-interactions. Prioritize CSS-only solutions for HTML. Use Motion library for React when available. Focus on high-impact moments: one well-orchestrated page load with staggered reveals (animation-delay) creates more delight than scattered micro-interactions. Use scroll-triggering and hover states that surprise.
- **Spatial Composition**: Unexpected layouts. Asymmetry. Overlap. Diagonal flow. Grid-breaking elements. Generous negative space OR controlled density.
- **Backgrounds & Visual Details**: Create atmosphere and depth rather than defaulting to solid colors. Add contextual effects and textures that match the overall aesthetic. Apply creative forms like gradient meshes, noise textures, geometric patterns, layered transparencies, dramatic shadows, decorative borders, custom cursors, and grain overlays.

NEVER use generic AI-generated aesthetics like overused font families (Inter, Roboto, Arial, system fonts), cliched color schemes (particularly purple gradients on white backgrounds), predictable layouts and component patterns, and cookie-cutter design that lacks context-specific character.

Interpret creatively and make unexpected choices that feel genuinely designed for the context. No design should be the same. Vary between light and dark themes, different fonts, different aesthetics. NEVER converge on common choices (Space Grotesk, for example) across generations.

**IMPORTANT**: Match implementation complexity to the aesthetic vision. Maximalist designs need elaborate code with extensive animations and effects. Minimalist or refined designs need restraint, precision, and careful attention to spacing, typography, and subtle details. Elegance comes from executing the vision well.

## Functional Inventory

For every screen this skill produces or modifies, list each interactive control, the backend handler it invokes, and the test that exercises that handler without a mock. A screen can pass diff review, type-check, lint, and screenshot review while shipping `onClick` handlers that do nothing — the inventory is what makes that visible at design time.

- **Interactive control** — anything whose user-activation should cause an observable on-disk or backend effect: buttons, form submits, menu items, command-palette entries, keyboard shortcuts, drag targets, modal confirms. Pure-navigation routing isn't a control unless the destination auto-fires a mutation.
- **Handler** — the function that actually performs the user-task (IPC bridge call, HTTP request, orchestrator method, file-system mutation). Not the local state setter, not the navigation call, not a debounce wrapper.
- **Non-mock test** — drives the rendered control as a user would *and* exercises the real handler. A bridge mock that records arguments isn't sufficient: the bridge could be missing the method entirely and the mock test would still pass.

Reject inventory entries whose handler resolves to `undefined`, `() => {}`, `// TODO`, `panic("TODO")`, a stub returning canned data, a chain ending in client-side state, or a navigation onto a screen whose own controls are unwired.

In a phase plan, the inventory lives in the relevant task's `#### What to build` / `#### Acceptance criteria` and is reflected in `## Success Criteria`.

## Before Shipping

Run the output through [playbook/review-rubric.md](playbook/review-rubric.md). Beautiful and bold is not enough — the rubric verifies the primary task is obvious, the scan path is intentional, feedback is proportional, and accessibility/platform fit are built in, not bolted on.

Remember: Claude is capable of extraordinary creative work. Don't hold back, show what can truly be created when thinking outside the box and committing fully to a distinctive vision — grounded in the playbook's fundamentals.

## Visual Evidence Loop (implementation ↔ review)

Text-only diffs cannot answer "does this look right" — that's why features in this skill's domain routinely ship as generic-looking output: the reviewer has nothing to judge against. Close the loop by producing visual evidence in the implementation phase and consuming it in the review phase.

### When implementing UI changes

If the current iteration adds or modifies user-facing UI — React/Vue/Svelte/Solid/Angular components, HTML/CSS, native-window views (Wails/Tauri/Electron/SwiftUI/Qt), TUI screens (Bubbletea, lipgloss, ink), generated dashboards, slide decks, emailers, anything where layout and style matter — capture the rendered state into the `screenshots/` subdirectory that the iteration's user prompt identifies.

- **Scope**: one full-viewport PNG per top-level screen or meaningfully distinct state (loading / loaded / error / empty / interactive variant). Don't boil the ocean; the reviewer needs judgment material, not regression coverage.
- **Tooling**: use whatever the repo already has. Playwright's `toHaveScreenshot()` / `page.screenshot()` is the common case for web apps; Puppeteer, Storybook snapshots, `wails dev` + OS-level capture (`screencapture` on macOS, `import` on Linux/X, `xvfb-run` under headless CI), Percy, Chromatic, a hand-rolled helper — all fine if that's what the repo uses. If the repo has no rendering tooling, document that in `progress.md` with the plan for introducing it; don't silently skip.
- **Naming**: name files after the screen/state they depict (`dashboard.png`, `wizard-step2-error.png`, `settings-dark.png`), not the iteration number — the iteration directory already carries that context.
- **Storage**: commit the PNGs only if the repo's convention keeps visual baselines in git (Percy/Chromatic users typically don't; visual-regression-only users sometimes do). Default: leave untracked — they exist for review-time judgment, not long-term storage.

### When reviewing UI changes

The implementer deposits screenshots into `<iterDir>/screenshots/`. As the reviewer, your obligation is to consume them.

1. **Diff touches UI code** (any of `.tsx`, `.jsx`, `.vue`, `.svelte`, `.css`, `.scss`, `.sass`, `.html`, Bubbletea/lipgloss/ink Go modules, UI-generating Rust/Python if the repo uses those for rendering, etc.):
   - **Screenshots present**: Read each image via tool-use. Judge whether the rendered state matches the design commitments — colors, typography, layout, spacing, contrast, state variants, overall aesthetic — against the design direction, any user-attached mockups (the "Visual References" section of the prompt), and the phase plan's UI exit criteria. Cite what you saw in your feedback: *"the primary button in `dashboard.png` renders as `#7c3aed` but the design committed to an OKLCH accent in the `0.68 0.12 280` neighborhood"* is feedback a diff-only reviewer cannot produce.
   - **Screenshots absent or empty directory**: emit `## Verdict\nCHANGES_REQUESTED` in your `review-feedback.md` with a Critical finding — *"no visual evidence for a UI-touching iteration."* You cannot approve UI work you cannot see. Ask specifically for screenshots of each affected screen into `<iterDir>/screenshots/` on the next iteration, naming which harness the repo has available.

2. **Diff does not touch UI code**: the directory's absence is expected. Do not raise a finding.

Apply the dimensions from [playbook/review-rubric.md](playbook/review-rubric.md) when judging: primary task obvious, scan path intentional, feedback proportional, accessibility and platform fit built in not bolted on. If the design committed to a distinctive aesthetic direction, the rubric is the yardstick for whether the implementation actually landed on that direction or drifted toward generic defaults.

The goal is not exhaustive visual regression — one screenshot per meaningfully distinct screen/state is enough for a judgment call. The failure mode this closes: features that pass code review, tests, and lint, and ship a UI the user hates.

## Behavioral Evidence Loop (implementation ↔ review)

Visual evidence answers *does it look right*. It does not answer *does it actually work*. Compile + screenshots + unit tests can all pass on a binary whose Create button has no handler. Close that gap by capturing one driven execution per primary user journey.

### When implementing

If this iteration adds or modifies a primary user journey (any user action expecting an observable on-disk or backend effect — create, start, save, publish, delete, attach, approve, cancel, etc.), capture one driven execution per journey into `<iterDir>/behaviors/`. Use whatever driver the repo has (Playwright trace, AppleScript log, headless harness output, HTTP smoke transcript, CLI session capture with the resulting filesystem diff). Each artifact must show both the input the test drove and the observable effect that followed — launch or render alone is not evidence. Name files after the journey, not the iteration. If no user-mutation surface is touched, the directory's absence is expected.

### When reviewing

Read what's under `<iterDir>/behaviors/` and confirm each artifact shows the journey *completing* (input + effect), not just launching. If the diff touches a user-mutation surface (entrypoints, wizards, submit handlers, top-level commands, IPC bridge methods that front a mutation) and the directory is missing, empty, or only captures launch/render, emit `## Verdict\nCHANGES_REQUESTED` with a Critical finding. Compile + screenshots + unit tests are not a substitute for an observed user-task.
