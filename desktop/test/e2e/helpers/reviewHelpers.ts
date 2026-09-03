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

import { expect, type Page } from '@playwright/test';
import { evidenceShot, setTheme, setWindowSize, type AppHandle } from './app';
import { waitFor } from './world';

/** Finds a feature id by name from the server's feature list. */
export async function findFeatureId(handle: AppHandle, name: string): Promise<string> {
  let id = '';
  await waitFor(
    async () => {
      const feature = (
        await handle.page.evaluate(() => window.agentico.listFeatures())
      ).features.find((candidate) => candidate.name === name);
      id = feature?.id ?? '';
      return id !== '';
    },
    `feature ${name}`,
    30_000,
  );
  return id;
}

/** Replaces the Monaco editor content by selecting all and typing. */
export async function editMonaco(handle: AppHandle, text: string): Promise<void> {
  const editor = handle.page.getByLabel('Review draft');
  await expect(editor).toBeVisible();
  await editor.click({ force: true });
  await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+A' : 'Control+A');
  await handle.page.keyboard.type(text);
}

/** Captures a screenshot in both light and dark themes, restoring light after. */
export async function captureBoth(handle: AppHandle, light: string, dark: string): Promise<void> {
  await setTheme(handle, 'light');
  await evidenceShot(handle, light);
  await setTheme(handle, 'dark');
  await evidenceShot(handle, dark);
  await setTheme(handle, 'light');
}

/** Sets the window to a contracted size, captures both themes, then restores. */
export async function captureAtSize(
  handle: AppHandle,
  width: number,
  height: number,
  light: string,
  dark: string,
): Promise<void> {
  await setWindowSize(handle, width, height);
  await captureBoth(handle, light, dark);
}

/** Waits until a feature reaches the given status. */
export async function waitForFeatureStatus(
  page: Page,
  featureId: string,
  status: string,
): Promise<void> {
  await waitFor(
    async () =>
      (await page.evaluate((id: string) => window.agentico.getFeature(id), featureId)).status ===
      status,
    `feature ${featureId} status ${status}`,
    30_000,
  );
}

/** Waits until a feature leaves the given status. */
export async function waitForFeatureToLeaveStatus(
  page: Page,
  featureId: string,
  status: string,
): Promise<void> {
  await waitFor(
    async () =>
      (await page.evaluate((id: string) => window.agentico.getFeature(id), featureId)).status !==
      status,
    `feature ${featureId} to leave ${status}`,
    30_000,
  );
}
