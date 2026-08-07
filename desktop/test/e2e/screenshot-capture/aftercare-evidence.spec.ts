import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';
import fs from 'node:fs';

const EVIDENCE_DIR = process.env['AGENTICO_EVIDENCE_DIR'] ?? '';

function evidencePath(name: string): string {
  return path.join(EVIDENCE_DIR, 'screenshots', `${name}.png`);
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
  fs.mkdirSync(path.dirname(target), { recursive: true });
  await page.screenshot({ path: target, fullPage: false });
}

test('aftercare visual evidence', async ({ page }) => {
  test.skip(EVIDENCE_DIR === '', 'AGENTICO_EVIDENCE_DIR not set');

  // Published with a pull request, a reachable diff, and carried check states.
  for (const theme of ['dark', 'light'] as const) {
    await capture(
      page,
      'aftercare-verified',
      theme,
      1440,
      900,
      theme === 'dark'
        ? 'aftercare-published-feature-with-a-pr-and-diff-available-unnumbered-runway-with-1440x900'
        : 'same-state-light-theme-1440x900',
      '.aftercare-shipped__bar',
    );
  }

  // Code ready with undelivered commits: the runway carries the delivery row
  // and the toolbar's Mark done is prominent-but-blocked with its blocker.
  await capture(
    page,
    'aftercare-codeready',
    'dark',
    1440,
    900,
    'codeready-feature-with-undelivered-commits-runway-delivery-row-with-pending-comm-1440x900',
    '.cockpit__completion-blocker',
  );

  // The inspector pane, opened from the toolbar toggle.
  await capture(
    page,
    'aftercare-inspector',
    'dark',
    1440,
    900,
    'inspector-pane-open-via-the-toolbar-toggle-showing-the-feature-facts-dark-theme-1440x900',
    '.aftercare-workspace',
    async (p) => {
      await p.getByRole('button', { name: 'Toggle inspector' }).click();
      await expect(p.getByRole('region', { name: 'Feature facts' })).toBeVisible();
    },
  );

  // Graceful omission: no PR, no reachable diff.
  await capture(
    page,
    'aftercare-bare',
    'dark',
    1440,
    900,
    'graceful-omission-feature-with-no-pr-and-no-diff-changes-and-pr-rows-absent-veri-1440x900',
    '.aftercare-shipped__placeholder',
    async (p) => {
      await expect(p.getByRole('button', { name: 'View run record' })).toBeVisible();
      await expect(p.getByText('Pull request')).toHaveCount(0);
    },
  );

  // Narrow: the wrap-up menu's reduced verb set. The drawer inspector is a
  // modal presentation whose panel covers the toolbar's menu at this width, so
  // it gets its own frame below rather than a composite that hides one of them.
  await capture(
    page,
    'aftercare-verified',
    'dark',
    800,
    600,
    'narrow-window-wrap-up-menu-with-the-reduced-verb-set-and-the-drawer-inspector-800x600',
    '.aftercare-workspace',
    async (p) => {
      await p.locator('.cockpit__wrapup-summary').click();
      const menu = p.getByRole('menu', { name: 'Wrap up' });
      await expect(menu).toBeVisible();
      await expect(menu.getByRole('menuitem')).toHaveText(['Clean up', 'Mark done']);
    },
  );

  await capture(
    page,
    'aftercare-verified',
    'dark',
    800,
    600,
    'narrow-window-drawer-inspector-800x600',
    '.aftercare-workspace',
    async (p) => {
      await p.getByRole('button', { name: 'Inspector' }).click();
      await expect(p.getByRole('dialog', { name: 'Feature inspector' })).toBeVisible();
    },
  );
});
