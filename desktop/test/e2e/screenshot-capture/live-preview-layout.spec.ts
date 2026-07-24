import { expect, test, type Page } from '@playwright/test';

interface LayoutMeasurements {
  previewHeight: number;
  surfaceClientHeight: number;
  surfaceScrollHeight: number;
  surfaceTop: number;
  surfaceBottom: number;
  metricsTop: number;
  metricsBottom: number;
  resourcesTop: number;
  resourcesBottom: number;
}

async function measureLayout(page: Page, height: number): Promise<LayoutMeasurements> {
  await page.setViewportSize({ width: 1440, height });
  await page.goto('http://localhost:9871/?scene=run-gauge&theme=dark');
  await expect(page.locator('.current-inspection__metrics')).toBeVisible();
  return page.evaluate(() => {
    const preview = document.querySelector('.live-preview__frame');
    const surface = document.querySelector('.cockpit__surface--live');
    const metrics = document.querySelector('.current-inspection__metrics');
    const resources = document.querySelector('.current-inspection__resources');
    if (!(preview instanceof HTMLElement)) throw new Error('live preview missing');
    if (!(surface instanceof HTMLElement)) throw new Error('live surface missing');
    if (!(metrics instanceof HTMLElement)) throw new Error('metrics missing');
    if (!(resources instanceof HTMLElement)) throw new Error('resources missing');
    const surfaceRect = surface.getBoundingClientRect();
    const metricsRect = metrics.getBoundingClientRect();
    const resourcesRect = resources.getBoundingClientRect();
    return {
      previewHeight: preview.getBoundingClientRect().height,
      surfaceClientHeight: surface.clientHeight,
      surfaceScrollHeight: surface.scrollHeight,
      surfaceTop: surfaceRect.top,
      surfaceBottom: surfaceRect.bottom,
      metricsTop: metricsRect.top,
      metricsBottom: metricsRect.bottom,
      resourcesTop: resourcesRect.top,
      resourcesBottom: resourcesRect.bottom,
    };
  });
}

test('live preview absorbs extra stage height without overflowing the live surface', async ({
  page,
}) => {
  const compact = await measureLayout(page, 900);
  const tall = await measureLayout(page, 1200);

  expect(tall.previewHeight).toBeGreaterThan(compact.previewHeight + 200);
  expect(tall.previewHeight).toBeGreaterThan(360);
  for (const layout of [compact, tall]) {
    expect(layout.surfaceScrollHeight).toBeLessThanOrEqual(layout.surfaceClientHeight);
    expect(layout.metricsTop).toBeGreaterThanOrEqual(layout.surfaceTop);
    expect(layout.metricsBottom).toBeLessThanOrEqual(layout.surfaceBottom);
    expect(layout.resourcesTop).toBeGreaterThanOrEqual(layout.surfaceTop);
    expect(layout.resourcesBottom).toBeLessThanOrEqual(layout.surfaceBottom);
  }
});
