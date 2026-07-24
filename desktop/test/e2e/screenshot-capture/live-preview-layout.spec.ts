import { expect, test, type Page } from '@playwright/test';

interface LayoutSection {
  name: string;
  top: number;
  bottom: number;
}

interface ScrollMeasurements {
  clientHeight: number;
  scrollHeight: number;
  overflowY: string;
  movedScrollTop: number;
}

interface LayoutMeasurements {
  previewHeight: number;
  transcript: ScrollMeasurements;
  roster: ScrollMeasurements;
  surfaceClientHeight: number;
  surfaceScrollHeight: number;
  surfaceTop: number;
  surfaceBottom: number;
  reviewSummary: LayoutSection | null;
  sections: LayoutSection[];
}

async function measureLayout(page: Page, height: number): Promise<LayoutMeasurements> {
  await page.setViewportSize({ width: 1440, height });
  await page.goto('http://localhost:9871/?scene=run-gauge&theme=dark');
  await expect(page.locator('.current-inspection__metrics')).toBeVisible();
  await expect(page.locator('.conversation__scroll > *')).toHaveCount(5);
  await expect(page.locator('.live-preview__roster [role="tab"]')).toHaveCount(3);
  return page.evaluate(async () => {
    const requireElement = (selector: string, label: string): HTMLElement => {
      const element = document.querySelector(selector);
      if (!(element instanceof HTMLElement)) throw new Error(`${label} missing`);
      return element;
    };
    const toSection = (name: string, element: HTMLElement): LayoutSection => {
      const rect = element.getBoundingClientRect();
      return { name, top: rect.top, bottom: rect.bottom };
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
    const inspectionHeader = requireElement('.current-inspection__header', 'inspection header');
    const roadmap = requireElement('.roadmap-gauge', 'roadmap gauge');
    const preview = requireElement('.live-preview__frame', 'live preview');
    const transcript = requireElement('.conversation__scroll', 'transcript');
    const roster = requireElement('.live-preview__roster', 'live roster');
    const activity = requireElement('.current-inspection__activity', 'activity');
    const metrics = requireElement('.current-inspection__metrics', 'metrics');
    const reviewSummaryElement = document.querySelector('.review-gate');
    const resources = requireElement('.current-inspection__resources', 'resources');
    const surfaceRect = surface.getBoundingClientRect();
    const reviewSummary =
      reviewSummaryElement instanceof HTMLElement
        ? toSection('review summary', reviewSummaryElement)
        : null;
    const sections = [
      toSection('inspection header', inspectionHeader),
      toSection('roadmap gauge', roadmap),
      toSection('live frame', preview),
      toSection('activity', activity),
      toSection('metrics', metrics),
      ...(reviewSummary === null ? [] : [reviewSummary]),
      toSection('resources', resources),
    ];
    return {
      previewHeight: preview.getBoundingClientRect().height,
      transcript: measureScroll(transcript),
      roster: measureScroll(roster),
      surfaceClientHeight: surface.clientHeight,
      surfaceScrollHeight: surface.scrollHeight,
      surfaceTop: surfaceRect.top,
      surfaceBottom: surfaceRect.bottom,
      reviewSummary,
      sections,
    };
  });
}

test('live preview absorbs extra stage height without overflowing the live surface', async ({
  page,
}) => {
  const short = await measureLayout(page, 600);
  const compact = await measureLayout(page, 900);
  const tall = await measureLayout(page, 1200);

  expect(tall.previewHeight).toBeGreaterThan(compact.previewHeight + 200);
  expect(compact.previewHeight).toBeGreaterThan(short.previewHeight);
  for (const layout of [short, compact, tall]) {
    expect(layout.surfaceScrollHeight).toBeLessThanOrEqual(layout.surfaceClientHeight);
    expect(['auto', 'scroll']).toContain(layout.transcript.overflowY);
    expect(['auto', 'scroll']).toContain(layout.roster.overflowY);
    for (const [index, section] of layout.sections.entries()) {
      expect(section.top).toBeGreaterThanOrEqual(layout.surfaceTop);
      expect(section.bottom).toBeLessThanOrEqual(layout.surfaceBottom);
      if (index > 0) {
        expect(section.top).toBeGreaterThanOrEqual(layout.sections[index - 1]!.bottom);
      }
    }
  }

  for (const layout of [short, compact]) {
    expect(layout.transcript.scrollHeight).toBeGreaterThan(layout.transcript.clientHeight);
    expect(layout.transcript.movedScrollTop).toBeGreaterThan(0);
  }
  expect(tall.transcript.scrollHeight).toBeLessThanOrEqual(tall.transcript.clientHeight);
  expect(tall.transcript.movedScrollTop).toBe(0);

  expect(short.roster.scrollHeight).toBeGreaterThan(short.roster.clientHeight);
  expect(short.roster.movedScrollTop).toBeGreaterThan(0);
  // At 900 and 1200 px the three fixture tabs fit without scrolling, while
  // overflow-y remains ready to own roster scrolling if the cohort grows.
  for (const layout of [compact, tall]) {
    expect(layout.roster.scrollHeight).toBeLessThanOrEqual(layout.roster.clientHeight);
    expect(layout.roster.movedScrollTop).toBe(0);
  }

  // The active run-gauge fixture is not reviewing or verifying, so
  // ReviewGateSummary returns null and the collapsed sequence skips it.
  expect(short.reviewSummary).toBeNull();
  expect(short.sections.map(({ name }) => name)).toEqual([
    'inspection header',
    'roadmap gauge',
    'live frame',
    'activity',
    'metrics',
    'resources',
  ]);
});
