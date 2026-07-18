import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';
import fs from 'node:fs';

const EVIDENCE_DIR = process.env['AGENTICO_EVIDENCE_DIR'] ?? '';

function evidencePath(name: string): string {
  if (EVIDENCE_DIR === '') {
    throw new Error('AGENTICO_EVIDENCE_DIR must be set');
  }
  // These are deterministic component fixtures, not packaged-journey evidence.
  // Keep their filenames distinct so they can never overwrite contract captures.
  return path.join(EVIDENCE_DIR, 'screenshots', `mock-${name}.png`);
}

function ensureDir(filePath: string): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
}

async function capture(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  width: number,
  height: number,
  fileName: string,
  waitFor: string,
  preScreenshot?: (page: Page) => Promise<void>,
): Promise<void> {
  await page.goto(`http://localhost:9871/?scene=${scene}`);
  await page.evaluate((t) => {
    document.documentElement.dataset['theme'] = t;
  }, theme);
  await page.setViewportSize({ width, height });
  await expect(page.locator(waitFor)).toBeVisible({ timeout: 15_000 });
  if (preScreenshot) {
    await preScreenshot(page);
  }
  const target = evidencePath(fileName);
  ensureDir(target);
  await page.screenshot({ path: target, fullPage: false });
}

test('capture all visual evidence screenshots', async ({ page }) => {
  test.setTimeout(120_000);

  await capture(
    page,
    'archive',
    'light',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-spine-and-histo-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'archive',
    'dark',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-spine-and-histo-1440x900-6658c389',
    '.archive-mode__band',
  );

  await capture(
    page,
    'pinned',
    'light',
    1440,
    900,
    'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'pinned',
    'dark',
    1440,
    900,
    'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900-a7472731',
    '.archive-mode__band',
  );

  await capture(
    page,
    'rewind-confirm',
    'light',
    1440,
    900,
    'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900',
    '.rewind-journey__backdrop',
    async (p) => {
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'rewind-confirm',
    'dark',
    1440,
    900,
    'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900-371edd9a',
    '.rewind-journey__backdrop',
    async (p) => {
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'fork',
    'light',
    1440,
    900,
    'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900',
    '.rewind-journey__backdrop',
    async (p) => {
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
      const input = p.locator('#rewind-confirm-input');
      await input.fill('REWIND');
      await expect(p.locator('.rewind-journey__submit')).toBeEnabled({ timeout: 5_000 });
      await p.locator('.rewind-journey__submit').click();
      await expect(p.locator('.rewind-journey__success')).toBeVisible({ timeout: 15_000 });
    },
  );

  await capture(
    page,
    'fork',
    'dark',
    1440,
    900,
    'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900-bf76e967',
    '.rewind-journey__backdrop',
    async (p) => {
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
      const input = p.locator('#rewind-confirm-input');
      await input.fill('REWIND');
      await expect(p.locator('.rewind-journey__submit')).toBeEnabled({ timeout: 5_000 });
      await p.locator('.rewind-journey__submit').click();
      await expect(p.locator('.rewind-journey__success')).toBeVisible({ timeout: 15_000 });
    },
  );

  await capture(
    page,
    'constrained',
    'light',
    760,
    900,
    'archive-selector-and-return-to-current-control-in-constrained-layout-light-theme-760x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'constrained',
    'dark',
    760,
    900,
    'archive-selector-and-return-to-current-control-in-constrained-layout-dark-theme-760x900',
    '.archive-mode__band',
  );
});
