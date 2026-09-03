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

test('cockpit visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();
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
