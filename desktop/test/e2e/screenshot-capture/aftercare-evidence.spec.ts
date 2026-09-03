/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { expect, test } from '@playwright/test';
import { capture, skipWithoutEvidenceDir } from './evidence-capture';

test('aftercare visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

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
