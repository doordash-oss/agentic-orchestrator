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
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { setFeatureStatus } from '../helpers/seed';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

test('packaged phase rail: a hold opens with the paused dot and tooltip, then resolves back to Elapsed/Cost/Context', async ({}, testInfo) => {
  const transcript = new Transcript(
    'phase-rail',
    'Rail hold journey — Paused substitution and dot on an Interrupted feature, Elapsed/Cost/Context restored after Resume',
  );
  const world = createWorld('phase-rail', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'phase-rail-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'phase-rail' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('Create a feature and seed it as Interrupted');
    await createFeatureViaForm(handle, {
      name: 'Phase Rail Hold Journey',
      description:
        'Exercises the rail hold->resume->restore contract against the real bundled server.',
      repoPatterns: [/phase-rail-lab/],
    });
    const feature = await waitForFeatureNamed(handle.page, 'Phase Rail Hold Journey');

    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);
    setFeatureStatus(world.stateDir, feature.id, 'Interrupted');

    handle = await launchApp(world, testInfo, { traceName: 'phase-rail-seeded' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await handle.page.getByRole('option', { name: /Phase Rail Hold Journey/ }).click();
    const cockpit = handle.page.getByLabel('Feature Phase Rail Hold Journey');

    transcript.section(
      'The current segment turns attention-colored with a dot and a Paused readout',
    );
    const rail = cockpit.locator('.phase-rail');
    await expect(rail).toBeVisible({ timeout: 30_000 });
    const dot = rail.locator('.phase-rail__dot');
    await expect(dot).toBeVisible({ timeout: 30_000 });
    await expect(dot).toHaveAttribute('title', /^Held( <1m| \d+[mhd])? for your answer$/);
    await expect(rail.locator('.phase-rail__segment[data-held="true"]')).toHaveCount(1);
    const trioLabels = rail.locator('.phase-rail__trio-entry dt');
    await expect(trioLabels.first()).toHaveText('Paused');
    await expect(rail.locator('.phase-rail__trio-entry[data-attention="true"]')).toHaveCount(1);
    await evidenceShot(handle, 'phase-rail-held-paused');

    transcript.section('Resuming closes the hold and restores the plain trio');
    await handle.page.getByRole('button', { name: 'Resume', exact: true }).click();

    await expect(dot).toHaveCount(0, { timeout: 30_000 });
    await expect(rail.locator('.phase-rail__segment[data-held="true"]')).toHaveCount(0);
    await expect(rail.locator('.phase-rail__trio-entry[data-attention="true"]')).toHaveCount(0);
    await waitFor(
      async () => {
        const labels = await trioLabels.allTextContents();
        return labels.includes('Elapsed') || labels.includes('Cost') || labels.includes('Context');
      },
      'the rail trio to read Elapsed/Cost/Context again after the hold resolves',
      30_000,
    );
    await evidenceShot(handle, 'phase-rail-restored-after-resume');
    transcript.step(
      'rail hold opened with the dot/tooltip and Paused readout, then resolved after Resume',
    );

    persistAppLogs(handle, 'phase-rail-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
});

async function waitForFeatureNamed(
  page: Page,
  name: string,
): Promise<{ id: string; name: string; status: string }> {
  let found: { id: string; name: string; status: string } | undefined;
  await waitFor(
    async () => {
      const features = (await page.evaluate(() => window.agentico.listFeatures())).features;
      found = features.find((feature) => feature.name === name);
      return found !== undefined;
    },
    `feature named ${name}`,
    30_000,
  );
  return found!;
}

async function assertNoLeakedProcessesEventually(world: JourneyWorld): Promise<void> {
  await waitFor(
    () => {
      try {
        assertNoLeakedProcesses(world);
        return true;
      } catch {
        return false;
      }
    },
    `no leaked processes for ${world.root}`,
    15_000,
  );
  assertNoLeakedProcesses(world);
}
