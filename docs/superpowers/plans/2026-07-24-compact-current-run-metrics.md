# Compact Current Run Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the collapsed live-run card visible in a compact desktop window and show authentic roadmap-phase time, cost, and model names.

**Architecture:** Extend the renderer's pure presentation model to translate the active roadmap phase into the orchestrator's accounting keys and to resolve canonical model IDs through catalogue metadata. Feed that data into `CurrentRunInspection`, then change only the live cockpit height chain so the transcript shrinks and scrolls before the outer surface does.

**Tech Stack:** React 19, TypeScript 5.9, Vitest, Testing Library, CSS Grid/Flexbox, Playwright, Electron Vite

## Global Constraints

- Keep the inspection header, roadmap gauge, metrics, review summary, and collapsed artifact/log controls visible in ordinary compact desktop windows.
- The live agent transcript and roster own internal scrolling; outer scrolling is only a below-minimum fallback or an expanded-resource behavior.
- Use `phase-{N}-plan` for roadmap planning and `phase-{N}-impl` for implementation/review metric lookup.
- Preserve exact, case-insensitive, and final-review metric fallbacks.
- Prefer model catalogue `displayName`; retain the canonical identifier in the tooltip and derive a compact fallback if catalogue metadata is unavailable.
- A failed run-detail or model-catalogue request must not blank the inspection.
- Preserve document order, focus order, controls, and the full-screen preview.
- Every production behavior change follows red-green TDD.
- Run the repository Fast suite before handoff.

---

### Task 1: Resolve Roadmap Metric Keys and Readable Model Names

**Files:**
- Modify: `desktop/src/renderer/src/features/featureView.ts:6,248-266`
- Test: `desktop/src/renderer/src/features/featureView.test.ts:1-22,110-125`

**Interfaces:**
- Consumes: `ModelCatalogue`, `Record<string, number>`, current phase label, optional roadmap phase number, and canonical session model ID.
- Produces: `phaseMetric(byPhase, phase, roadmapPhase?) => number | undefined` and `displayModelName(model, catalogue) => string`.

- [ ] **Step 1: Add failing pure-function tests**

Import `displayModelName` and extend the existing `phaseMetric` tests:

```ts
describe('phaseMetric', () => {
  it('reads the active roadmap implementation and planning accounting keys', () => {
    expect(phaseMetric({ 'phase-5-impl': 760 }, 'Implement', 5)).toBe(760);
    expect(phaseMetric({ 'phase-5-impl': 12.4 }, 'Review', 5)).toBe(12.4);
    expect(phaseMetric({ 'phase-5-plan': 95 }, 'Plan', 5)).toBe(95);
  });

  it('retains phase-name and final-review fallbacks', () => {
    expect(phaseMetric({ Implement: 120, Plan: 30 }, 'Implement')).toBe(120);
    expect(phaseMetric({ implement: 120 }, 'Implement')).toBe(120);
    expect(phaseMetric({ Review: 45 }, 'Final Review')).toBe(45);
    expect(phaseMetric({ Plan: 5 }, 'Implement')).toBeUndefined();
    expect(phaseMetric(undefined, 'Implement', 2)).toBeUndefined();
  });
});

describe('displayModelName', () => {
  const catalogue = {
    providerOrder: ['opencode'],
    providerModels: {
      opencode: [
        {
          id: 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]',
          displayName: 'GLM 5.2 (1.04M)',
        },
      ],
    },
    phaseDefaults: {},
    phaseProviderModels: {},
  };

  it('uses catalogue display metadata for bare and provider-qualified ids', () => {
    const model = 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]';
    expect(displayModelName(model, catalogue)).toBe('GLM 5.2 (1.04M)');
    expect(displayModelName(`opencode:${model}`, catalogue)).toBe('GLM 5.2 (1.04M)');
  });

  it('falls back to the last readable path segment', () => {
    expect(
      displayModelName('portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]', null),
    ).toBe('glm-5p2[1.04M]');
    expect(displayModelName('claude-sonnet-5', null)).toBe('claude-sonnet-5');
  });
});
```

- [ ] **Step 2: Run the pure renderer test and verify RED**

Run:

```bash
cd desktop
npx vitest run --project renderer src/renderer/src/features/featureView.test.ts
```

Expected: FAIL because `displayModelName` is not exported and `phaseMetric` does not accept or resolve roadmap accounting keys.

- [ ] **Step 3: Implement the smallest pure presentation helpers**

Import `ModelCatalogue` in `featureView.ts`, add candidate-key lookup before the existing fallbacks, and add catalogue-aware display formatting:

```ts
export function phaseMetric(
  byPhase: Readonly<Record<string, number>> | undefined,
  phase: string,
  roadmapPhase?: number,
): number | undefined {
  if (byPhase === undefined) return undefined;
  const target = phase.trim().toLocaleLowerCase();
  if (roadmapPhase !== undefined && roadmapPhase > 0) {
    const suffix =
      target === 'plan' || target === 'planning'
        ? 'plan'
        : ['implement', 'implementation', 'review'].includes(target)
          ? 'impl'
          : null;
    if (suffix !== null) {
      const roadmapKey = `phase-${roadmapPhase}-${suffix}`;
      if (roadmapKey in byPhase) return byPhase[roadmapKey];
    }
  }
  if (phase in byPhase) return byPhase[phase];
  for (const [key, value] of Object.entries(byPhase)) {
    if (key.trim().toLocaleLowerCase() === target) return value;
  }
  if (target === 'final review') return phaseMetric(byPhase, 'Review');
  return undefined;
}

export function displayModelName(model: string, catalogue: ModelCatalogue | null): string {
  for (const [provider, models] of Object.entries(catalogue?.providerModels ?? {})) {
    const match = models.find((entry) => model === entry.id || model === `${provider}:${entry.id}`);
    if (match?.displayName !== undefined && match.displayName !== '') return match.displayName;
  }
  const canonical = model.includes(':') ? model.slice(model.indexOf(':') + 1) : model;
  return canonical.split('/').filter(Boolean).at(-1) ?? canonical;
}
```

- [ ] **Step 4: Run the pure renderer test and verify GREEN**

Run:

```bash
cd desktop
npx vitest run --project renderer src/renderer/src/features/featureView.test.ts
```

Expected: PASS with all `featureView` tests green.

- [ ] **Step 5: Commit the presentation model**

```bash
git add desktop/src/renderer/src/features/featureView.ts desktop/src/renderer/src/features/featureView.test.ts
git commit -m "Show authentic roadmap-phase diagnostics"
```

---

### Task 2: Feed Current Run Metrics and Catalogue Metadata into the Preview

**Files:**
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx:1-20,150-250,360-370,500-510,784-830,833-940`
- Test: `desktop/src/renderer/src/features/CurrentRunInspection.test.tsx:307-356`

**Interfaces:**
- Consumes: `phaseMetric` and `displayModelName` from Task 1, `window.agentico.getModelCatalogue()`, and `currentRoadmapPhase`.
- Produces: phase metrics scoped to the active roadmap phase and a short visible model label whose `title` is the canonical model ID.

- [ ] **Step 1: Change the component test to authentic server data**

In the existing current-phase metric test:

```ts
const canonicalModel = 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]';
mock.api.getModelCatalogue.mockResolvedValue({
  providerOrder: ['opencode'],
  providerModels: {
    opencode: [{ id: canonicalModel, displayName: 'GLM 5.2 (1.04M)' }],
  },
  phaseDefaults: {},
  phaseProviderModels: {},
});
```

Set the session model to `canonicalModel`, return:

```ts
timing: { totalSeconds: 3844, byPhase: { 'phase-5-impl': 760 } },
cost: { totalUsd: 21.62, byPhase: { 'phase-5-impl': 12.4 } },
```

Pass `currentRoadmapPhase={5}` and assert:

```ts
expect(await screen.findByText('12m 40s')).toBeInTheDocument();
expect(screen.getByText('$12.40')).toBeInTheDocument();
expect(screen.getByText('GLM 5.2 (1.04M)')).toHaveAttribute('title', canonicalModel);
expect(screen.queryByText(canonicalModel)).not.toBeInTheDocument();
```

- [ ] **Step 2: Run the component test and verify RED**

Run:

```bash
cd desktop
npx vitest run --project renderer src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: FAIL because elapsed/cost render as em dashes and the canonical model ID is still visible.

- [ ] **Step 3: Load catalogue metadata without coupling inspection availability to it**

Add `ModelCatalogue` to the type imports, add:

```ts
const [modelCatalogue, setModelCatalogue] = useState<ModelCatalogue | null>(null);
```

In `refresh`, wrap the catalogue request independently, include it in `Promise.all`, and store it:

```ts
window.agentico.getModelCatalogue().catch(() => null),
```

```ts
if (nextModelCatalogue !== null) setModelCatalogue(nextModelCatalogue);
```

This preserves the existing inspection result when catalogue loading fails.

- [ ] **Step 4: Thread roadmap phase and readable model data through both previews**

Pass `currentRoadmapPhase` and `modelCatalogue` to the inline `PreviewMetrics` and `LivePreviewOverlay`. Extend `PreviewMetrics` with:

```ts
currentRoadmapPhase?: number;
modelCatalogue: ModelCatalogue | null;
```

Resolve values with:

```ts
const phaseSeconds = phaseMetric(
  runDetail?.timing?.byPhase,
  currentPhase,
  currentRoadmapPhase,
);
const phaseUsd = phaseMetric(runDetail?.cost?.byPhase, currentPhase, currentRoadmapPhase);
```

Render the chip as:

```tsx
<span className="current-inspection__model" title={model}>
  {displayModelName(model, modelCatalogue)}
</span>
```

- [ ] **Step 5: Run the component test and verify GREEN**

Run:

```bash
cd desktop
npx vitest run --project renderer src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: PASS, including authentic roadmap keys and catalogue-backed model text.

- [ ] **Step 6: Commit the component integration**

```bash
git add desktop/src/renderer/src/features/CurrentRunInspection.tsx desktop/src/renderer/src/features/CurrentRunInspection.test.tsx
git commit -m "Keep active run metrics tied to their session"
```

---

### Task 3: Keep the Collapsed Inspection Inside a Compact Stage

**Files:**
- Modify: `desktop/src/renderer/src/styles/app.css:3510-3571,8248-8260,8454-8465`
- Test: `desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts:1-60`

**Interfaces:**
- Consumes: existing `.cockpit__surface--live`, `.current-inspection`, `.current-inspection__preview`, `.live-preview__frame`, `.conversation__scroll`, `.current-inspection__metrics`, and `.current-inspection__resources`.
- Produces: a collapsed inspection that fits the surface at 600, 900, and 1200 px viewports while the transcript remains the activity scroll owner.

- [ ] **Step 1: Extend the Playwright measurement to compact desktop height**

Add transcript measurements:

```ts
interface LayoutMeasurements {
  previewHeight: number;
  transcriptClientHeight: number;
  transcriptScrollHeight: number;
  surfaceClientHeight: number;
  surfaceScrollHeight: number;
  surfaceTop: number;
  surfaceBottom: number;
  metricsTop: number;
  metricsBottom: number;
  resourcesTop: number;
  resourcesBottom: number;
}
```

Measure `.conversation__scroll`, then exercise:

```ts
const short = await measureLayout(page, 600);
const compact = await measureLayout(page, 900);
const tall = await measureLayout(page, 1200);

expect(tall.previewHeight).toBeGreaterThan(compact.previewHeight + 200);
expect(compact.previewHeight).toBeGreaterThan(short.previewHeight);
for (const layout of [short, compact, tall]) {
  expect(layout.surfaceScrollHeight).toBeLessThanOrEqual(layout.surfaceClientHeight);
  expect(layout.metricsTop).toBeGreaterThanOrEqual(layout.surfaceTop);
  expect(layout.metricsBottom).toBeLessThanOrEqual(layout.surfaceBottom);
  expect(layout.resourcesTop).toBeGreaterThanOrEqual(layout.surfaceTop);
  expect(layout.resourcesBottom).toBeLessThanOrEqual(layout.surfaceBottom);
  expect(layout.transcriptScrollHeight).toBeGreaterThanOrEqual(layout.transcriptClientHeight);
}
```

- [ ] **Step 2: Run the layout contract and verify RED**

Run:

```bash
cd desktop
npx playwright test --config test/e2e/screenshot-capture/playwright.config.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

Expected: FAIL at the 600 px viewport because `.live-preview__frame` enforces 220 px and `.current-inspection` uses `flex: 1 0 auto; min-height: 100%`, making the live surface scroll.

- [ ] **Step 3: Let the card and preview shrink before outer overflow**

Update the live-surface contract:

```css
.cockpit__surface--live .current-inspection {
  flex: 1 1 100%;
  min-height: 0;
  margin-block: 0;
}
```

Protect expanded content from clipping while the collapsed card fits:

```css
.current-inspection:has(.current-inspection__resource-content),
.current-inspection:has(.current-inspection__content) {
  flex-basis: auto;
  min-height: 100%;
}
```

Set the complete-panel fallback floor and lower only the inline activity
frame's usable floor while retaining the existing scrollable transcript:

```css
.current-inspection {
  min-height: 320px;
}

.live-preview__frame {
  min-height: 96px;
}
```

- [ ] **Step 4: Run the layout contract and verify GREEN**

Run:

```bash
cd desktop
npx playwright test --config test/e2e/screenshot-capture/playwright.config.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

Expected: PASS at all three heights; the surface has no collapsed-state overflow and the short preview is smaller while its transcript remains scrollable.

- [ ] **Step 5: Visually inspect the compact and tall scenes**

Run:

```bash
cd desktop
npx vite --config test/e2e/screenshot-capture/vite.config.ts --host 127.0.0.1 --port 9871
```

While that server remains running, use a second terminal:

```bash
cd desktop
npx playwright screenshot --viewport-size=1440,600 'http://localhost:9871/?scene=run-gauge&theme=dark' /tmp/agentico-run-gauge-compact.png
npx playwright screenshot --viewport-size=1440,1200 'http://localhost:9871/?scene=run-gauge&theme=dark' /tmp/agentico-run-gauge-tall.png
```

Expected: inspect both PNGs and confirm no controls overlap, the activity frame
remains legible, and the model chip does not dominate the metrics row. Delete
the temporary PNGs after inspection; do not add them to Git.

- [ ] **Step 6: Commit the compact layout**

```bash
git add desktop/src/renderer/src/styles/app.css desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts
git commit -m "Keep live activity inside compact cockpit bounds"
```

---

### Task 4: Full Verification and Handoff

**Files:**
- Verify only; no planned production edits.

**Interfaces:**
- Consumes: Tasks 1-3.
- Produces: fresh evidence for renderer correctness, static analysis, build, and the mandatory Fast suite.

- [ ] **Step 1: Run focused renderer and layout regressions**

```bash
cd desktop
npx vitest run --project renderer src/renderer/src/features/featureView.test.ts src/renderer/src/features/CurrentRunInspection.test.tsx
npx playwright test --config test/e2e/screenshot-capture/playwright.config.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

Expected: all tests pass.

- [ ] **Step 2: Run desktop static checks and build**

```bash
cd desktop
npm run check
npm run build
```

Expected: typecheck, lint, formatting, API drift, and Electron Vite build all exit `0`.

- [ ] **Step 3: Run repository static analysis and build**

From the repository root:

```bash
go vet ./...
go build ./...
```

Expected: both commands exit `0`.

- [ ] **Step 4: Run the mandatory Fast suite**

```bash
make test-fast
```

Expected: the Fast suite exits `0` within its normal approximately 30-second target.

- [ ] **Step 5: Review the complete diff**

```bash
git status --short
git diff --check HEAD~3
git diff --stat HEAD~3
```

Expected: only the approved renderer, tests, CSS, design, and implementation-plan files are present; there are no whitespace errors.
