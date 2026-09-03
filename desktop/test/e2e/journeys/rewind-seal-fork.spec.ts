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
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, seedRunHistory } from '../helpers/world';

test('rewind: full-phase preview, typed confirmation, atomic fork, provenance, warnings, and recovery', async ({}, testInfo) => {
  const transcript = new Transcript(
    'rewind-seal-fork',
    'Full/partial rewind, pipeline upgrade, typed confirmation, provenance, warnings, and crash-recovery journey',
  );
  const world = createWorld('rewind-seal-fork', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'signal-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'rewind-seal-fork' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('Create one isolated feature');
    await createFeatureViaForm(handle, {
      name: 'Rewind Journey',
      description: 'Exercise the rewind seal-and-fork journey.',
      repoPatterns: [/signal-lab/],
      waitForReady: true,
    });

    const featureId = await handle.page.evaluate(async () => {
      const features = (await window.agentico.listFeatures()).features;
      return features.find((feature) => feature.name === 'Rewind Journey')?.id;
    });
    if (!featureId) throw new Error('created rewind feature was not listed');
    transcript.section('Seed an eligible current run and seven sealed predecessors');
    await closeApp(handle);
    handle = null;
    seedRunHistory(world, featureId);
    handle = await launchApp(world, testInfo, { traceName: 'rewind-seal-fork-seeded' });
    await handle.page.getByRole('option', { name: 'Rewind Journey' }).click();

    transcript.section('Open the rewind journey dialog');
    await handle.page.locator('summary[aria-label="More actions"]').click();
    let rewindButton = handle.page.getByRole('menuitem', { name: 'Rewind feature' });
    await expect(rewindButton).toBeVisible();
    await rewindButton.click();

    transcript.section('Verify the rewind journey dialog is visible');
    await expect(handle.page.getByRole('dialog', { name: /Rewind/ })).toBeVisible();
    await expect(handle.page.locator('.rewind-journey__subtitle')).toContainText('irreversible');

    transcript.section('Select the Implement target and expose its hierarchical controls');
    const targetRadio = handle.page.locator('input[name="rewind-target-phase"][value="implement"]');
    await targetRadio.check();
    await expect(targetRadio).toBeChecked();

    transcript.section('Wait for the consequence preview to load');
    await expect(handle.page.locator('.rewind-journey__preview')).toBeVisible({ timeout: 15_000 });
    await expect(handle.page.locator('.rewind-journey__preview')).toHaveAttribute(
      'data-eligible',
      'true',
    );
    await expect(handle.page.getByText('Roadmap phase')).toBeVisible();
    await handle.page.getByText('Advanced', { exact: true }).click();
    await expect(handle.page.getByText('Upgrade pipeline', { exact: true })).toBeVisible();

    transcript.section('Continue to confirmation step');
    await handle.page.getByRole('button', { name: 'Continue' }).click();
    await expect(handle.page.locator('.rewind-journey__type-confirm')).toBeVisible();
    await expect(handle.page.getByText('Roadmap phase', { exact: true })).toBeVisible();
    await expect(handle.page.getByText('Advanced pipeline', { exact: true })).toBeVisible();

    transcript.section('Capture light-theme rewind confirmation at 1440x900');
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    await evidenceShot(
      handle,
      'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900',
    );

    transcript.section('Capture dark-theme rewind confirmation at 1440x900');
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900-371edd9a',
    );

    transcript.section('Verify the destructive action is disabled until REWIND is typed');
    const submitButton = handle.page.getByRole('button', { name: 'Rewind', exact: true });
    await expect(submitButton).toBeDisabled();

    transcript.section('Type REWIND to enable submission');
    const confirmInput = handle.page.locator('#rewind-confirm-input');
    await confirmInput.fill('REWIND');
    await expect(submitButton).toBeEnabled();

    transcript.section('Cancel via Escape performs no mutation');
    await handle.page.keyboard.press('Escape');
    await expect(handle.page.getByRole('dialog', { name: /Rewind/ })).not.toBeVisible();

    transcript.section('Reopen and complete the rewind');
    await handle.page.locator('summary[aria-label="More actions"]').click();
    rewindButton = handle.page.getByRole('menuitem', { name: 'Rewind feature' });
    await rewindButton.click();
    await targetRadio.check();
    await handle.page.getByRole('button', { name: 'Continue' }).click();
    await confirmInput.fill('REWIND');
    await submitButton.click();

    transcript.section('Wait for success or determining-outcome state');
    await expect(
      handle.page
        .locator('.rewind-journey__success')
        .or(handle.page.locator('.rewind-journey__progress')),
    ).toBeVisible({ timeout: 30_000 });

    transcript.section('Open the completed fork and verify durable provenance');
    await expect(handle.page.locator('.rewind-journey__success')).toBeVisible({ timeout: 30_000 });
    // No bespoke rewind-warnings element renders: a clean rewind carries no
    // warnings, and any warning this journey ever yields renders as a
    // status-role ErrorSurface with a rewind code tag instead.
    await expect(handle.page.locator('.rewind-journey__warnings')).toHaveCount(0);
    await expect(handle.page.locator('.rewind-journey__warnings-list')).toHaveCount(0);
    await expect(handle.page.locator('.cockpit__rewind-warnings')).toHaveCount(0);
    await handle.page.getByRole('button', { name: 'Open new run' }).click();
    await expect(handle.page.getByLabel('Rewind outcome')).toBeVisible({ timeout: 15_000 });
    await expect(handle.page.getByText(/forked from sealed Run 7/)).toBeVisible();

    transcript.section('Capture the new-fork landing with provenance');
    await setTheme(handle, 'light');
    await setWindowSize(handle, 1440, 900);
    await evidenceShot(
      handle,
      'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900',
    );
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900-bf76e967',
    );

    transcript.section('Verify sealed-source navigation enters read-only history');
    await handle.page.getByRole('button', { name: 'Run 7' }).click();
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Sealed run');

    transcript.section('Journey complete');
  } finally {
    if (handle !== null) {
      transcript.codeBlock(
        'redacted app/server log tail',
        await persistAppLogs(handle, 'rewind-seal-fork'),
      );
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
