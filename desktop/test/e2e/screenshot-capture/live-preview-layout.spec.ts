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
