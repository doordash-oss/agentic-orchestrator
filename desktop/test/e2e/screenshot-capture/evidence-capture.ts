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

import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';
import fs from 'node:fs';

/**
 * Shared plumbing for every capture spec. These specs write into the orchestration
 * harness's evidence directory, so they are no-ops on a bare local run — the skip
 * guard lives here so every capture spec inherits the same one rather than
 * reinventing it.
 */

export const EVIDENCE_DIR = process.env['AGENTICO_EVIDENCE_DIR'] ?? '';

/** Call first inside an evidence test: without the harness there is nowhere to write. */
export function skipWithoutEvidenceDir(): void {
  test.skip(EVIDENCE_DIR === '', 'AGENTICO_EVIDENCE_DIR not set');
}

export function evidencePath(name: string): string {
  return path.join(EVIDENCE_DIR, 'screenshots', `${name}.png`);
}

export async function shoot(page: Page, fileName: string): Promise<void> {
  const target = evidencePath(fileName);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  await page.screenshot({ path: target, fullPage: false });
}

export async function openScene(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  width: number,
  height: number,
  waitFor: string,
  dataset: Record<string, string> = {},
): Promise<void> {
  await page.setViewportSize({ width, height });
  await page.goto(`http://localhost:9871/?scene=${scene}&theme=${theme}`);
  await page.evaluate(
    ({ t, extra }) => {
      document.documentElement.dataset['theme'] = t;
      for (const [key, value] of Object.entries(extra)) {
        document.documentElement.dataset[key] = value;
      }
    },
    { t: theme, extra: dataset },
  );
  await expect(page.locator(waitFor).first()).toBeVisible({ timeout: 15_000 });
}

export async function capture(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  width: number,
  height: number,
  fileName: string,
  waitFor: string,
  preScreenshot?: (page: Page) => Promise<void>,
): Promise<void> {
  await openScene(page, scene, theme, width, height, waitFor);
  if (preScreenshot) await preScreenshot(page);
  await shoot(page, fileName);
}
