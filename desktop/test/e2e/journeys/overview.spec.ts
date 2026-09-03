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

/**
 * The Overview pane's Bench redesign against the packaged app: the
 * headline/sub-line masthead, and the lane-grouped rows' Answer (waiting
 * lane) vs Open (every other lane) actions.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, waitFor } from '../helpers/world';

type AttentionItems = Awaited<ReturnType<Window['agentico']['getAttention']>>['items'];
type AttentionItem = AttentionItems[number];

test('overview: headline, Answer on a waiting row, and Open on a resting row', async ({}, testInfo) => {
  const transcript = new Transcript('overview', 'Overview lane rows: headline, Answer, and Open');
  const world = createWorld('overview', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    attentionProvider: true,
  });
  createRepo(world, 'overview-lab', { commit: true });

  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'overview' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('An at-rest feature renders an Open row');
    await createFeatureViaForm(handle, {
      name: 'Evidence Resting Fixture',
      repoPatterns: [/overview-lab/],
      waitForReady: true,
    });
    await handle.page.getByRole('option', { name: 'Overview' }).click();

    const restingRow = handle.page.getByRole('listitem').filter({
      has: handle.page.locator('.overview-row__name', { hasText: 'Evidence Resting Fixture' }),
    });
    await expect(restingRow).toBeVisible({ timeout: 15_000 });
    await expect(restingRow.getByRole('button', { name: 'Open' })).toBeVisible();
    transcript.step('the at-rest feature shows an Open action in its lane row');

    await restingRow.getByRole('button', { name: 'Open' }).click();
    await expect(handle.page.getByLabel('Feature Evidence Resting Fixture')).toBeVisible({
      timeout: 15_000,
    });
    transcript.step('clicking Open mounted the feature cockpit');

    transcript.section('A feature with a pending prompt renders an Answer row that jumps to it');
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await createFeatureViaForm(handle, {
      name: 'Evidence Waiting Fixture',
      repoPatterns: [/overview-lab/],
      waitForReady: true,
    });
    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();
    await waitForAttentionItem(handle.page, 'perm-allow-once');

    await handle.page.getByRole('option', { name: 'Overview' }).click();
    const waitingRow = handle.page.getByRole('listitem').filter({
      has: handle.page.locator('.overview-row__name', { hasText: 'Evidence Waiting Fixture' }),
    });
    await expect(waitingRow).toBeVisible({ timeout: 15_000 });
    const answerButton = waitingRow.getByRole('button', { name: 'Answer' });
    await expect(answerButton).toBeVisible();
    transcript.step('the waiting-lane row shows an Answer action instead of Open');

    await answerButton.click();
    await expect(handle.page.getByLabel('Feature Evidence Waiting Fixture')).toBeVisible({
      timeout: 15_000,
    });
    const inlineAttention = handle.page.getByRole('region', { name: 'Agent request' });
    await expect(inlineAttention).toBeVisible({ timeout: 15_000 });
    transcript.step('Answer opened the feature and surfaced its pending permission prompt');

    persistAppLogs(handle, 'overview');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function waitForAttentionItem(page: Page, id: string): Promise<AttentionItem> {
  let found: AttentionItem | undefined;
  await waitFor(
    async () => {
      const snapshot = await page.evaluate(() => window.agentico.getAttention());
      found = snapshot.items.find((item) => item.id === id);
      return found !== undefined;
    },
    'matching attention snapshot',
    60_000,
  );
  if (found === undefined) throw new Error('matching attention item was not returned');
  return found;
}
