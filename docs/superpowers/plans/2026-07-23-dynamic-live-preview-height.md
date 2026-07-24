# Dynamic Live Preview Height Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the current-run live preview consume available cockpit height while keeping its activity, metrics, review summary, and collapsed resource controls visible in the viewport.

**Architecture:** Preserve the existing cockpit's single-scroll-owner design. Make the current inspection fill the live surface, turn its preview wrapper into the only flexible child, and let the transcript frame grow and shrink inside that wrapper; unbounded expanded resources continue to overflow through the existing live-surface scrollbar.

**Tech Stack:** React 19, TypeScript, CSS Grid/Flexbox, Playwright, Vitest, Electron Vite

## Global Constraints

- The normal collapsed inspection must fit the visible cockpit stage without requiring the stage to scroll.
- The preview must absorb unused height and shrink before metrics or resource controls are pushed below the fold.
- The preview must retain a usable minimum height; short windows may fall back to the existing stage scrollbar.
- Expanded artifacts, logs, and artifact content remain allowed to extend the inspection.
- Preserve document order, focus order, controls, and the existing full-screen preview.
- Do not disturb the current run-metric changes already present in `CurrentRunInspection.tsx` and `FeatureCockpit.tsx`.

---

### Task 1: Make the Live Preview the Flexible Inspection Row

**Files:**
- Create: `desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts`
- Modify: `desktop/test/e2e/screenshot-capture/capture.tsx:199-236`
- Modify: `desktop/src/renderer/src/styles/app.css:3513-3525`
- Modify: `desktop/src/renderer/src/styles/app.css:8198-8206`
- Modify: `desktop/src/renderer/src/styles/app.css:8402-8410`

**Interfaces:**
- Consumes: the existing `.cockpit__surface--live`, `.current-inspection`, `.current-inspection__preview`, `.live-preview__frame`, `.current-inspection__metrics`, and `.current-inspection__resources` DOM classes.
- Produces: a height chain in which `.current-inspection__preview` has `flex: 1 1 auto`, and `.live-preview__frame` consumes that wrapper's remaining height.

- [ ] **Step 1: Add a browser layout contract that fails against the fixed 360px preview**

Update `RunGaugeScene` in `desktop/test/e2e/screenshot-capture/capture.tsx` so the fixture uses the production height-owning hierarchy:

```tsx
<div
  className="workspace-shell__content"
  style={{
    height: '100vh',
    display: 'flex',
    flexDirection: 'column',
    overflow: 'hidden',
    padding: 'var(--space-4)',
  }}
>
  <div className="cockpit" aria-label="Feature cockpit" style={{ flex: 1 }}>
    <div className="cockpit__content">
      <main className="cockpit__stage">
        <div className="cockpit__surface cockpit__surface--live">
          <CurrentRunInspection
            featureId="abcd1234ef567890"
            runNumber={8}
            currentPhase={atRest || finalReview ? 'Final Review' : 'Implement'}
            featureStatus={
              atRest ? 'CodeReady' : finalReview ? 'FinalReviewing' : 'Implementing'
            }
            currentRoadmapPhase={atRest ? 12 : 2}
            totalRoadmapPhases={atRest ? 12 : finalReview ? 2 : 5}
            {...(atRest || finalReview
              ? {}
              : { currentIteration: 3, phaseStatus: 'implementing' })}
            reviewGate={{
              reviewingGate: false,
              reviewFixing: false,
              validatingPlan: false,
              validatorStatuses: {},
            }}
            shouldStream={false}
          />
        </div>
      </main>
    </div>
  </div>
</div>
```

Create `desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts`:

```ts
import { expect, test, type Page } from '@playwright/test';

interface LayoutMeasurements {
  previewHeight: number;
  metricsBottom: number;
  resourcesBottom: number;
  viewportHeight: number;
}

async function measureLayout(page: Page, height: number): Promise<LayoutMeasurements> {
  await page.setViewportSize({ width: 1440, height });
  await page.goto('http://localhost:9871/?scene=run-gauge&theme=dark');
  await expect(page.locator('.current-inspection__metrics')).toBeVisible();
  return page.evaluate(() => {
    const preview = document.querySelector('.live-preview__frame');
    const metrics = document.querySelector('.current-inspection__metrics');
    const resources = document.querySelector('.current-inspection__resources');
    if (!(preview instanceof HTMLElement)) throw new Error('live preview missing');
    if (!(metrics instanceof HTMLElement)) throw new Error('metrics missing');
    if (!(resources instanceof HTMLElement)) throw new Error('resources missing');
    return {
      previewHeight: preview.getBoundingClientRect().height,
      metricsBottom: metrics.getBoundingClientRect().bottom,
      resourcesBottom: resources.getBoundingClientRect().bottom,
      viewportHeight: window.innerHeight,
    };
  });
}

test('live preview absorbs extra stage height without pushing metrics below the viewport', async ({
  page,
}) => {
  const compact = await measureLayout(page, 900);
  const tall = await measureLayout(page, 1200);

  expect(tall.previewHeight).toBeGreaterThan(compact.previewHeight + 200);
  expect(tall.previewHeight).toBeGreaterThan(360);
  expect(compact.metricsBottom).toBeLessThanOrEqual(compact.viewportHeight);
  expect(compact.resourcesBottom).toBeLessThanOrEqual(compact.viewportHeight);
  expect(tall.metricsBottom).toBeLessThanOrEqual(tall.viewportHeight);
  expect(tall.resourcesBottom).toBeLessThanOrEqual(tall.viewportHeight);
});
```

- [ ] **Step 2: Run the layout contract and confirm the fixed-height implementation fails**

Run:

```bash
cd desktop
npx playwright test --config test/e2e/screenshot-capture/playwright.config.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

Expected: FAIL because the tall and compact `.live-preview__frame` measurements both remain capped at approximately `360px`.

- [ ] **Step 3: Implement the height chain and flexible preview**

In `desktop/src/renderer/src/styles/app.css`, extend the live surface and current inspection rules:

```css
.cockpit__surface--live .current-inspection {
  flex: 1 0 auto;
  min-height: 100%;
  margin-block: 0;
}

.current-inspection {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin-block: var(--space-4);
  padding: var(--space-4);
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius);
  background: color-mix(in srgb, var(--color-panel) 92%, var(--color-signal));
}

.current-inspection__preview {
  display: flex;
  flex: 1 1 auto;
  flex-direction: column;
  min-height: 0;
}

.live-preview__frame {
  display: grid;
  flex: 1 1 auto;
  grid-template-rows: auto minmax(0, 1fr);
  min-height: 220px;
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius);
  background: var(--color-bg);
  overflow: hidden;
}
```

Remove the former `height: min(360px, 48vh)` declaration from `.live-preview__frame`. The `220px` minimum is the short-window usability floor; after that point `.cockpit__surface--live` remains the scroll owner.

- [ ] **Step 4: Run the layout contract and confirm the dynamic behavior passes**

Run:

```bash
cd desktop
npx playwright test --config test/e2e/screenshot-capture/playwright.config.ts test/e2e/screenshot-capture/live-preview-layout.spec.ts
```

Expected: PASS; the 1200px viewport produces a preview at least 200px taller than the 900px viewport, and metrics/resources remain within both viewports.

- [ ] **Step 5: Run focused renderer regression checks**

Run:

```bash
cd desktop
npx vitest run src/renderer/src/features/CurrentRunInspection.test.tsx src/renderer/src/features/FeatureCockpit.test.tsx
npm run typecheck
npm run build
```

Expected: all commands exit `0`.

- [ ] **Step 6: Run the mandatory handoff gate**

From the repository root, run:

```bash
make test-fast
```

Expected: the Fast suite passes.

- [ ] **Step 7: Review the diff and commit the implementation**

Run:

```bash
git diff --check
git diff -- desktop/src/renderer/src/styles/app.css desktop/test/e2e/screenshot-capture/capture.tsx desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts
git add desktop/src/renderer/src/styles/app.css desktop/test/e2e/screenshot-capture/capture.tsx desktop/test/e2e/screenshot-capture/live-preview-layout.spec.ts
git commit -m "Keep live run context visible at every window height"
```

Expected: one focused implementation commit containing only the layout CSS, the production-shaped visual fixture, and its browser contract.
