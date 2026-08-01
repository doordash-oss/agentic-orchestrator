import { expect, test, type Page } from '@playwright/test';

type LiveScene = 'run-gauge' | 'run-gauge-reviewing' | 'run-gauge-verifying';

interface LayoutSection {
  name: string;
  top: number;
  bottom: number;
  left: number;
  right: number;
}

interface ScrollMeasurements {
  clientHeight: number;
  scrollHeight: number;
  overflowY: string;
  movedScrollTop: number;
}

interface LayoutMeasurements {
  scene: LiveScene;
  viewportHeight: number;
  previewHeight: number;
  previewContainerHeight: number;
  previewGridTemplateRows: string;
  frameGridRow: string;
  transcript: ScrollMeasurements | null;
  roster: ScrollMeasurements | null;
  surfaceClientHeight: number;
  surfaceScrollHeight: number;
  surfaceTop: number;
  surfaceBottom: number;
  inspectionMinHeight: number;
  reviewSummary: LayoutSection | null;
  sections: LayoutSection[];
}

async function measureLayout(
  page: Page,
  scene: LiveScene,
  height: number,
): Promise<LayoutMeasurements> {
  await page.setViewportSize({ width: 1440, height });
  await page.goto(`http://localhost:9871/?scene=${scene}&theme=dark`);
  await expect(page.locator('.current-inspection__metrics')).toBeVisible();
  if (scene === 'run-gauge-verifying') {
    await expect(page.locator('.live-preview__verification-log > *')).toHaveCount(3);
  } else {
    await expect(page.locator('.conversation__scroll > *')).toHaveCount(5);
    await expect(page.locator('.live-preview__roster [role="tab"]')).toHaveCount(3);
  }
  return page.evaluate(
    async ({ measuredScene, measuredHeight }) => {
      const requireElement = (selector: string, label: string): HTMLElement => {
        const element = document.querySelector(selector);
        if (!(element instanceof HTMLElement)) throw new Error(`${label} missing`);
        return element;
      };
      const toSection = (name: string, element: HTMLElement): LayoutSection => {
        const rect = element.getBoundingClientRect();
        return { name, top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right };
      };
      const measureScroll = (element: HTMLElement): ScrollMeasurements => {
        const initialScrollTop = element.scrollTop;
        const initialScrollBehavior = element.style.scrollBehavior;
        element.style.scrollBehavior = 'auto';
        element.scrollTop = 0;
        element.scrollTop = element.scrollHeight;
        const movedScrollTop = element.scrollTop;
        element.scrollTop = initialScrollTop;
        element.style.scrollBehavior = initialScrollBehavior;
        return {
          clientHeight: element.clientHeight,
          scrollHeight: element.scrollHeight,
          overflowY: getComputedStyle(element).overflowY,
          movedScrollTop,
        };
      };

      const surface = requireElement('.cockpit__surface--live', 'live surface');
      const inspection = requireElement('.current-inspection', 'current inspection');
      const inspectionHeader = requireElement('.current-inspection__header', 'inspection header');
      const roadmap = requireElement('.roadmap-gauge', 'roadmap gauge');
      const previewContainer = requireElement('.current-inspection__preview', 'preview container');
      const preview = requireElement('.live-preview__frame', 'live preview');
      const transcript = document.querySelector('.conversation__scroll');
      const roster = document.querySelector('.live-preview__roster');
      const activity = document.querySelector('.current-inspection__activity');
      const metrics = requireElement('.current-inspection__metrics', 'metrics');
      const reviewSummaryElement = document.querySelector('.review-gate');
      const surfaceRect = surface.getBoundingClientRect();
      const reviewSummary =
        reviewSummaryElement instanceof HTMLElement
          ? toSection('review summary', reviewSummaryElement)
          : null;
      const sections = [
        toSection('inspection header', inspectionHeader),
        toSection('roadmap gauge', roadmap),
        toSection('live frame', preview),
        ...(activity instanceof HTMLElement ? [toSection('activity', activity)] : []),
        toSection('metrics', metrics),
        ...(reviewSummary === null ? [] : [reviewSummary]),
      ];
      return {
        scene: measuredScene,
        viewportHeight: measuredHeight,
        previewHeight: preview.getBoundingClientRect().height,
        previewContainerHeight: previewContainer.getBoundingClientRect().height,
        previewGridTemplateRows: getComputedStyle(previewContainer).gridTemplateRows,
        frameGridRow: getComputedStyle(preview).gridRow,
        transcript: transcript instanceof HTMLElement ? measureScroll(transcript) : null,
        roster: roster instanceof HTMLElement ? measureScroll(roster) : null,
        surfaceClientHeight: surface.clientHeight,
        surfaceScrollHeight: surface.scrollHeight,
        surfaceTop: surfaceRect.top,
        surfaceBottom: surfaceRect.bottom,
        inspectionMinHeight: Number.parseFloat(getComputedStyle(inspection).minHeight),
        reviewSummary,
        sections,
      };
    },
    { measuredScene: scene, measuredHeight: height },
  );
}

function expectOrderedSections(layout: LayoutMeasurements, contained: boolean): void {
  for (const [index, section] of layout.sections.entries()) {
    const context = `${layout.scene} at ${layout.viewportHeight}px (container=${layout.previewContainerHeight.toFixed(1)}, rows=${layout.previewGridTemplateRows}, frame-row=${layout.frameGridRow}): ${layout.sections
      .map(
        ({ name, top, bottom, left, right }) =>
          `${name}=(${left.toFixed(1)},${top.toFixed(1)})-(${right.toFixed(1)},${bottom.toFixed(1)})`,
      )
      .join(', ')}`;
    if (contained) {
      expect(section.top, context).toBeGreaterThanOrEqual(layout.surfaceTop);
      expect(section.bottom, context).toBeLessThanOrEqual(layout.surfaceBottom);
    }
    if (index > 0) {
      const previous = layout.sections[index - 1]!;
      const separated =
        section.top >= previous.bottom ||
        section.left >= previous.right ||
        section.right <= previous.left;
      expect(separated, context).toBe(true);
    }
  }
}

test('all collapsed live phases fit ordinary surfaces and summaries reduce preview height', async ({
  page,
}) => {
  const heights = [600, 900, 1200] as const;
  const byScene = new Map<LiveScene, LayoutMeasurements[]>();
  for (const scene of ['run-gauge', 'run-gauge-reviewing', 'run-gauge-verifying'] as const) {
    const layouts: LayoutMeasurements[] = [];
    for (const height of heights) {
      layouts.push(await measureLayout(page, scene, height));
    }
    byScene.set(scene, layouts);
  }

  for (const [scene, layouts] of byScene) {
    const context = `${scene}: ${layouts
      .map(
        (layout) =>
          `${layout.viewportHeight}px frame=${layout.previewHeight.toFixed(1)} container=${layout.previewContainerHeight.toFixed(1)} rows=${layout.previewGridTemplateRows} frame-row=${layout.frameGridRow}`,
      )
      .join(', ')}`;
    expect(layouts[2]!.previewHeight, context).toBeGreaterThan(layouts[1]!.previewHeight + 200);
    expect(layouts[1]!.previewHeight, context).toBeGreaterThan(layouts[0]!.previewHeight);
    for (const layout of layouts) {
      expect(layout.inspectionMinHeight).toBe(320);
      expect(layout.surfaceScrollHeight).toBeLessThanOrEqual(layout.surfaceClientHeight);
      expectOrderedSections(layout, true);
    }
  }

  const active = byScene.get('run-gauge')!;
  const reviewing = byScene.get('run-gauge-reviewing')!;
  const verifying = byScene.get('run-gauge-verifying')!;
  for (const [index] of heights.entries()) {
    expect(reviewing[index]!.previewHeight).toBeLessThanOrEqual(active[index]!.previewHeight);
    expect(reviewing[index]!.reviewSummary).toBeNull();
    expect(verifying[index]!.reviewSummary).toBeNull();
  }
  expect(active[0]!.reviewSummary).toBeNull();
  expect(active[0]!.sections.map(({ name }) => name)).toEqual([
    'inspection header',
    'roadmap gauge',
    'live frame',
    'activity',
    'metrics',
  ]);
  expect(reviewing[0]!.sections.map(({ name }) => name)).toEqual([
    'inspection header',
    'roadmap gauge',
    'live frame',
    'activity',
    'metrics',
  ]);
  expect(verifying[0]!.sections.map(({ name }) => name)).toEqual([
    'inspection header',
    'roadmap gauge',
    'live frame',
    'metrics',
  ]);
});

test('surface owns scrolling only below the complete-panel floor', async ({ page }) => {
  const belowFloor = await measureLayout(page, 'run-gauge', 480);

  expect(belowFloor.surfaceClientHeight).toBeLessThan(320);
  expect(belowFloor.inspectionMinHeight).toBe(320);
  expect(belowFloor.surfaceScrollHeight).toBeGreaterThan(belowFloor.surfaceClientHeight);
  expectOrderedSections(belowFloor, false);
});

test('expanded artifact content floats without changing the live surface scroll owner', async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('http://localhost:9871/?scene=run-gauge&theme=dark');
  await page.getByRole('button', { name: 'Files' }).click();
  await page
    .getByRole('button', { name: /Open artifact/ })
    .first()
    .click();
  await expect(page.getByRole('dialog', { name: /artifact/ })).toBeVisible();

  const overflow = await page.locator('.cockpit__surface--live').evaluate((surface) => ({
    clientHeight: surface.clientHeight,
    scrollHeight: surface.scrollHeight,
  }));
  expect(overflow.scrollHeight).toBeLessThanOrEqual(overflow.clientHeight);
});

test('transcript accepts wheel and focused keyboard scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 600 });
  await page.goto('http://localhost:9871/?scene=run-gauge&theme=dark');
  const transcript = page.getByRole('region', { name: 'Live agent transcript' });
  await expect(transcript).toBeVisible();
  await expect(transcript).toHaveAttribute('tabindex', '0');

  await transcript.focus();
  await expect(transcript).toBeFocused();
  await transcript.press('Home');
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBe(0);

  await transcript.hover();
  await page.mouse.wheel(0, 200);
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);

  await transcript.focus();
  await transcript.press('Home');
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBe(0);
  await transcript.press('PageDown');
  await expect.poll(() => transcript.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
});

test('cycle chrome stays visible while the live transcript owns overflow', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('http://localhost:9871/?scene=post-cycle-rebase&theme=dark');

  const cycle = page.getByRole('region', { name: 'Rebase cycle' });
  const header = cycle.locator('.cycle-workspace__header');
  const transcript = page.getByRole('region', { name: 'Live agent transcript' });
  const footer = cycle.locator('.current-inspection__metrics');
  await expect(header).toBeVisible();
  await expect(transcript).toBeVisible();
  await expect(footer).toBeVisible();

  const layout = await page.evaluate(() => {
    const requireElement = (selector: string): HTMLElement => {
      const element = document.querySelector(selector);
      if (!(element instanceof HTMLElement)) throw new Error(`${selector} missing`);
      return element;
    };
    const main = requireElement('.cycle-workspace__main');
    const headerElement = requireElement('.cycle-workspace__header');
    const transcriptElement = requireElement('.conversation__scroll');
    const footerElement = requireElement('.current-inspection__metrics');
    const mainRect = main.getBoundingClientRect();
    const headerRect = headerElement.getBoundingClientRect();
    const footerRect = footerElement.getBoundingClientRect();
    transcriptElement.style.scrollBehavior = 'auto';
    transcriptElement.scrollTop = transcriptElement.scrollHeight;
    return {
      mainClientHeight: main.clientHeight,
      mainScrollHeight: main.scrollHeight,
      headerTop: headerRect.top,
      mainTop: mainRect.top,
      footerBottom: footerRect.bottom,
      mainBottom: mainRect.bottom,
      transcriptClientHeight: transcriptElement.clientHeight,
      transcriptScrollHeight: transcriptElement.scrollHeight,
      transcriptScrollTop: transcriptElement.scrollTop,
    };
  });

  expect(layout.mainScrollHeight).toBeLessThanOrEqual(layout.mainClientHeight);
  expect(layout.headerTop).toBeGreaterThanOrEqual(layout.mainTop);
  expect(layout.footerBottom).toBeLessThanOrEqual(layout.mainBottom);
  expect(layout.transcriptScrollHeight).toBeGreaterThan(layout.transcriptClientHeight);
  expect(layout.transcriptScrollTop).toBeGreaterThan(0);
});

test('narrow cycle windows keep the cycle main bounded above the facts rail', async ({ page }) => {
  await page.setViewportSize({ width: 760, height: 600 });
  await page.goto('http://localhost:9871/?scene=post-cycle-rebase&theme=dark');
  await expect(page.getByRole('region', { name: 'Live agent transcript' })).toBeVisible();

  const layout = await page.evaluate(() => {
    const requireElement = (selector: string): HTMLElement => {
      const element = document.querySelector(selector);
      if (!(element instanceof HTMLElement)) throw new Error(`${selector} missing`);
      return element;
    };
    const cycle = requireElement('.cycle-workspace');
    const main = requireElement('.cycle-workspace__main');
    const header = requireElement('.cycle-workspace__header');
    const transcript = requireElement('.conversation__scroll');
    const footer = requireElement('.current-inspection__metrics');
    const mainRect = main.getBoundingClientRect();
    transcript.style.scrollBehavior = 'auto';
    transcript.scrollTop = transcript.scrollHeight;
    return {
      cycleClientHeight: cycle.clientHeight,
      cycleScrollHeight: cycle.scrollHeight,
      mainClientHeight: main.clientHeight,
      mainScrollHeight: main.scrollHeight,
      headerTop: header.getBoundingClientRect().top,
      mainTop: mainRect.top,
      footerBottom: footer.getBoundingClientRect().bottom,
      mainBottom: mainRect.bottom,
      transcriptScrollTop: transcript.scrollTop,
    };
  });

  expect(layout.cycleScrollHeight).toBeLessThanOrEqual(layout.cycleClientHeight);
  expect(layout.mainScrollHeight).toBeLessThanOrEqual(layout.mainClientHeight);
  expect(layout.headerTop).toBeGreaterThanOrEqual(layout.mainTop);
  expect(layout.footerBottom).toBeLessThanOrEqual(layout.mainBottom);
  expect(layout.transcriptScrollTop).toBeGreaterThan(0);
});

test('aftercare keeps its runway and compact feature facts legible at rest', async ({ page }) => {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 760, height: 900 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto('http://localhost:9871/?scene=aftercare&theme=dark');

    const desk = page.getByRole('region', { name: 'Feature aftercare' });
    await expect(desk).toBeVisible();
    await expect(page.getByRole('tablist', { name: 'Stage view' })).toHaveCount(0);
    await expect(page.getByLabel('Feature pipeline')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Run record' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Choose one focused action' })).toBeVisible();
    await expect(page.getByRole('complementary', { name: 'Feature facts' })).toBeVisible();
    await expect(page.getByText('Waiting for the agent to respond…')).toHaveCount(0);
    await expect(page.getByRole('button', { name: /Prepare rebase/ })).toBeVisible();
    await expect(page.getByRole('button', { name: /Check comments/ })).toBeVisible();
    await expect(page.getByRole('button', { name: /Plan refactor/ })).toBeVisible();

    const dimensions = await page.evaluate(() => ({
      documentWidth: document.documentElement.scrollWidth,
      viewportWidth: document.documentElement.clientWidth,
      deskWidth: document.querySelector('.aftercare-workspace')?.getBoundingClientRect().width ?? 0,
    }));
    expect(dimensions.documentWidth).toBeLessThanOrEqual(dimensions.viewportWidth);
    expect(dimensions.deskWidth).toBeGreaterThan(0);
  }
});
