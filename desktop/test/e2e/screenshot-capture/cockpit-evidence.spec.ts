import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';
import fs from 'node:fs';

const EVIDENCE_DIR = process.env['AGENTICO_EVIDENCE_DIR'] ?? '';

function evidencePath(name: string): string {
  return path.join(EVIDENCE_DIR, 'screenshots', `${name}.png`);
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
  await page.setViewportSize({ width, height });
  await page.goto(`http://localhost:9871/?scene=${scene}&theme=${theme}`);
  await page.evaluate((t) => {
    document.documentElement.dataset['theme'] = t;
  }, theme);
  await expect(page.locator(waitFor).first()).toBeVisible({ timeout: 15_000 });
  if (preScreenshot) await preScreenshot(page);
  const target = evidencePath(fileName);
  ensureDir(target);
  await page.screenshot({ path: target, fullPage: false });
}

test('cockpit visual evidence', async ({ page }) => {
  test.skip(EVIDENCE_DIR === '', 'AGENTICO_EVIDENCE_DIR not set');
  await capture(
    page,
    'cockpit-redesign-live',
    'dark',
    1440,
    900,
    'live-run-cockpit-implement-phase-four-segment-stage-bar-with-run-popup-multi-rep-1440x900',
    '.cockpit__segmented',
  );

  await capture(
    page,
    'cockpit-redesign-live',
    'light',
    1440,
    900,
    'live-run-cockpit-same-state-light-theme-1440x900',
    '.cockpit__segmented',
  );

  await capture(
    page,
    'cockpit-redesign-review12',
    'dark',
    1440,
    900,
    'review-phase-with-twelve-axes-strip-overflowing-with-hidden-scrollbar-failed-axi-1440x900',
    '.live-preview__strip-tally',
  );

  await capture(
    page,
    'cockpit-redesign-popup',
    'dark',
    1440,
    900,
    'run-iteration-popup-open-current-run-plus-sealed-runs-listed-newest-first-dark-t-1440x900',
    '.cockpit__run-switcher-menu',
  );

  await capture(
    page,
    'cockpit-redesign-sealed',
    'dark',
    1440,
    900,
    'sealed-run-selected-popup-label-run-n-sealed-segments-disabled-restyled-read-onl-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'cockpit-redesign-verification',
    'dark',
    1440,
    900,
    'verification-inline-tick-events-in-the-reading-column-with-no-separate-verificat-1440x900',
    '.conversation__verification-tick',
  );

  await capture(
    page,
    'cockpit-redesign-live',
    'dark',
    1440,
    900,
    'toolbar-trailing-zone-during-a-run-status-chip-contextual-stop-overflow-inspecto-1440x900',
    '.toolbar__cockpit-actions',
    async (p) => {
      await p.locator('.cockpit__overflow-summary').click();
      await expect(p.locator('.cockpit__overflow-menu')).toBeVisible();
    },
  );

  await capture(
    page,
    'cockpit-redesign-live',
    'dark',
    800,
    600,
    'narrow-window-cohort-strip-scrolling-with-the-tally-still-pinned-and-toolbar-act-800x600',
    '.live-preview__strip-tally',
  );
});
