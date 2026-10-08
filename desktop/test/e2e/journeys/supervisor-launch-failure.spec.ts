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
 * Supervisor launch failure against the packaged app and the real bundled
 * server: while the world's failure sentinel exists the provider stub exits
 * before its handshake, so a send fails the launch. The message returns to
 * the composer, the transcript gains the error marker row, the status line
 * reads "Supervisor failed — Retry" and the dock shows the error card with
 * Retry — driven by state, so it survives a reload. With the sentinel gone,
 * Retry re-sends the composer text and the reply arrives under a new
 * generation.
 */
import fs from 'node:fs';
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  supervisorStubReply,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_NAME = 'supervisor-launch-failure';
const CHIP_LABEL = 'Claude Haiku · Default';
const MESSAGE = 'Summarize the open features.';

test('supervisor launch failure: the draft returns, the card offers Retry, and Retry relaunches', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Supervisor launch failure and Retry');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'failure-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(supervisorPage(page)).toBeVisible({ timeout: 60_000 });
    await chooseHaiku(page);
    transcript.step('app launched and Claude Haiku is chosen');

    transcript.section('With the sentinel present the launch fails before its handshake');
    fs.writeFileSync(world.supervisorLaunchFailurePath, '');
    await composer(page).fill(MESSAGE);
    await sendButton(page).click();
    await waitForProviderLog(world, 'launch-failed');
    await expect(status(page)).toHaveText('Supervisor failed — Retry', { timeout: 60_000 });
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'failed');
    await expect(composer(page)).toHaveValue(MESSAGE);
    await expect(card(page)).toHaveCount(1);
    await expect(card(page)).toContainText('supervisor_launch_failed');
    await expect(retry(page)).toBeEnabled();
    await expect(conversation(page).getByText(/^Supervisor failed to start · /)).toBeVisible();
    // Nothing was committed for the failed send.
    await expect(conversation(page)).not.toContainText(MESSAGE);
    const failed = await supervisorState(page);
    expect(failed.lifecycle).toBe('failed');
    expect(failed.failure?.code).toBe('supervisor_launch_failed');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(0);
    await evidenceShot(handle, 'supervisor-launch-failure-card');
    transcript.json('failed state', {
      generation: failed.generation,
      lifecycle: failed.lifecycle,
      failure: failed.failure,
    });

    transcript.section('The card is state-driven: it survives a reload, Retry waits for text');
    await page.reload();
    await expect(supervisorPage(page)).toBeVisible({ timeout: 60_000 });
    await expect(status(page)).toHaveText('Supervisor failed — Retry');
    await expect(card(page)).toHaveCount(1);
    await expect(composer(page)).toHaveValue('');
    await expect(retry(page)).toHaveCount(0);
    await expect(card(page)).toContainText('Type a message to retry.');
    await expect(conversation(page).getByText(/^Supervisor failed to start · /)).toBeVisible();
    transcript.step('after a reload the card stays, with Retry waiting for composer text');

    transcript.section('With the sentinel gone, Retry re-sends the composer text');
    fs.rmSync(world.supervisorLaunchFailurePath);
    await composer(page).fill(MESSAGE);
    await expect(retry(page)).toBeEnabled();
    await retry(page).click();
    await expect(conversation(page)).toContainText(supervisorStubReply(1), { timeout: 60_000 });
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'idle', { timeout: 30_000 });
    await expect(status(page)).toHaveText('Ready');
    await expect(card(page)).toHaveCount(0);
    await expect(composer(page)).toHaveValue('');
    await expect(conversation(page)).toContainText(MESSAGE);
    const recovered = await supervisorState(page);
    expect(recovered.generation).toBeGreaterThan(failed.generation);
    expect(recovered.failure).toBeUndefined();
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    await evidenceShot(handle, 'supervisor-launch-failure-recovered');
    transcript.step(
      `Retry relaunched: generation ${failed.generation} → ${recovered.generation}, reply arrived`,
    );
    transcript.json('provider log', readProviderLog(world).split('\n'));
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function supervisorPage(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor', exact: true });
}

function conversation(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor conversation' });
}

function dock(page: Page): Locator {
  return supervisorPage(page).locator('.supervisor-page__dock');
}

function composer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

function sendButton(page: Page): Locator {
  return dock(page).getByRole('button', { name: 'Send', exact: true });
}

/** The launch-failure card in the dock (the canonical error surface). */
function card(page: Page): Locator {
  return dock(page).getByRole('alert');
}

function retry(page: Page): Locator {
  return card(page).getByRole('button', { name: 'Retry', exact: true });
}

function status(page: Page): Locator {
  return page.getByTestId('supervisor-status');
}

async function chooseHaiku(page: Page): Promise<void> {
  const chip = page.getByTestId('supervisor-model-chip');
  await chip.click();
  const popover = page.getByRole('region', { name: 'Harness and model' });
  await popover.getByRole('group', { name: 'Claude' }).getByText('Haiku', { exact: true }).click();
  await expect(chip).toHaveAccessibleName(CHIP_LABEL);
  await page.keyboard.press('Escape');
  await expect(popover).toHaveCount(0);
}

function supervisorState(page: Page) {
  return page.evaluate(() => window.agentico.getSupervisorState());
}

function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}

async function waitForProviderLog(world: JourneyWorld, needle: string): Promise<void> {
  await waitFor(
    () => readProviderLog(world).includes(needle),
    `provider log to contain ${needle}`,
    30_000,
  );
}
