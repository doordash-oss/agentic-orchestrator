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
 * Restart resilience against the packaged app and the real bundled server:
 * a turn that has committed partial text is cut by SIGKILLing the bundled
 * server; the app relaunches it, and without a renderer reload the
 * Supervisor page shows the conversation paused, the "Interrupted before
 * restart" row and the "Interrupted" footer on the partial reply. The next
 * message rebuilds Claude's native session under the world's isolated home
 * and resumes it (`--resume <id>`); a second restart-and-send cycle resumes
 * the same native id. The provider CLI is the only stub (`supervisorProvider`).
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { requireDiscovery, waitForNewServer } from '../helpers/runtime';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  SUPERVISOR_E2E_MARKERS,
  supervisorStubPartialReply,
  supervisorStubReply,
  supervisorStubResumedReply,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_NAME = 'supervisor-restart-resume';
const CHIP_LABEL = 'Claude Haiku · Default';
const PAUSED = 'Paused — interrupted before restart. Send a message to continue.';

test('supervisor restart: a cut turn reads paused, and the next message resumes the same native session', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Supervisor restart and native-session resume');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'restart-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(supervisorPage(page)).toBeVisible({ timeout: 60_000 });
    transcript.step('app launched on the Supervisor page');

    transcript.section('Choose a model and complete one turn');
    await chooseHaiku(page);
    await sendMessage(page, 'Remember the codename Juniper.');
    await expect(conversation(page)).toContainText(supervisorStubReply(1), { timeout: 60_000 });
    await expectReady(page);
    transcript.step(`first turn answered "${supervisorStubReply(1)}"`);

    transcript.section('A second turn commits partial text and holds');
    await sendMessage(page, `Audit the build ${SUPERVISOR_E2E_MARKERS.partialHold}`);
    await waitForProviderLog(world, 'partial-holding:2');
    await expect(conversation(page)).toContainText(supervisorStubPartialReply(2), {
      timeout: 30_000,
    });
    await expect(status(page)).toHaveText('Working…');
    const beforeRestart = await supervisorState(page);
    transcript.json('state before the restart', pick(beforeRestart));

    transcript.section('SIGKILL the bundled server mid-turn; the app relaunches it');
    const first = await killBundledServer(world);
    transcript.step(`server pid ${first.killed} relaunched as pid ${first.next}`);

    transcript.section('Without a reload the conversation reads paused');
    await expect(status(page)).toHaveText(PAUSED, { timeout: 60_000 });
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'stopped');
    await expect(status(page)).toHaveAttribute('data-tone', 'paused');
    await expect(
      conversation(page).getByText('Interrupted before restart', { exact: true }),
    ).toBeVisible();
    const partialRow = conversation(page)
      .locator('article')
      .filter({ hasText: supervisorStubPartialReply(2) });
    await expect(partialRow.getByText('Interrupted', { exact: true })).toBeVisible();
    const paused = await supervisorState(page);
    expect(paused).toMatchObject({
      lifecycle: 'stopped',
      lastTurnOutcome: 'interrupted',
      interruptedBy: 'shutdown',
      pendingRequests: [],
    });
    await evidenceShot(handle, 'supervisor-restart-paused');
    transcript.json('state after the restart', pick(paused));

    transcript.section('The next message rebuilds the native session and resumes it');
    await sendMessage(page, 'What was the codename?');
    // Two user prompts preceded the restart: the completed turn and the cut one.
    await expect(conversation(page)).toContainText(supervisorStubResumedReply(2), {
      timeout: 60_000,
    });
    await expectReady(page);
    const firstResume = await waitForResume(world, 1);
    const sessionFiles = rebuiltSessionFiles(world);
    expect(sessionFiles).toHaveLength(1);
    expect(path.basename(sessionFiles[0]!, '.jsonl')).toBe(firstResume);
    expect(readProviderLog(world)).toContain('history:2');
    const resumed = await supervisorState(page);
    expect(resumed.generation).toBeGreaterThan(beforeRestart.generation);
    await evidenceShot(handle, 'supervisor-restart-resumed');
    transcript.json('resumed native session', { id: firstResume, file: sessionFiles[0] });

    transcript.section('A second restart and send resumes the same native id');
    const second = await killBundledServer(world);
    transcript.step(`server pid ${second.killed} relaunched as pid ${second.next}`);
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'stopped', { timeout: 60_000 });
    // Idle when the server went down: nothing was cut, so the page rests.
    await expect(status(page)).toHaveText('Ready');
    await sendMessage(page, 'One more question.');
    await expect(conversation(page)).toContainText(supervisorStubResumedReply(3), {
      timeout: 60_000,
    });
    await expectReady(page);
    const secondResume = await waitForResume(world, 2);
    expect(secondResume).toBe(firstResume);
    expect(rebuiltSessionFiles(world)).toEqual(sessionFiles);
    const final = await supervisorState(page);
    expect(final.generation).toBeGreaterThan(resumed.generation);
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

function composer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

function sendButton(page: Page): Locator {
  return supervisorPage(page)
    .locator('.supervisor-page__dock')
    .getByRole('button', { name: 'Send', exact: true });
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

async function expectReady(page: Page): Promise<void> {
  await expect(status(page)).toHaveAttribute('data-lifecycle', 'idle', { timeout: 30_000 });
  await expect(status(page)).toHaveText('Ready');
}

async function sendMessage(page: Page, text: string): Promise<void> {
  await composer(page).fill(text);
  await expect(sendButton(page)).toBeEnabled({ timeout: 30_000 });
  await sendButton(page).click();
}

function supervisorState(page: Page) {
  return page.evaluate(() => window.agentico.getSupervisorState());
}

function pick(state: Awaited<ReturnType<typeof supervisorState>>) {
  return {
    generation: state.generation,
    lifecycle: state.lifecycle,
    lastTurnOutcome: state.lastTurnOutcome,
    interruptedBy: state.interruptedBy,
  };
}

/** SIGKILLs the bundled server from its discovery record and waits for the app's relaunch. */
async function killBundledServer(world: JourneyWorld): Promise<{ killed: number; next: number }> {
  const { pid } = requireDiscovery(world);
  process.kill(pid, 'SIGKILL');
  await waitFor(() => !processAlive(pid), `bundled server ${pid} to exit`, 15_000);
  const next = await waitForNewServer(world, pid);
  return { killed: pid, next: next.pid };
}

/** Every native session file the server rebuilt under the world's isolated Claude home. */
function rebuiltSessionFiles(world: JourneyWorld): string[] {
  const projects = path.join(world.home, '.claude', 'projects');
  if (!fs.existsSync(projects)) return [];
  return fs
    .readdirSync(projects)
    .flatMap((dir) =>
      fs
        .readdirSync(path.join(projects, dir))
        .filter((file) => file.endsWith('.jsonl'))
        .map((file) => path.join(projects, dir, file)),
    )
    .sort();
}

/** The id of the `count`-th `resume:<id>` line the stub logged. */
async function waitForResume(world: JourneyWorld, count: number): Promise<string> {
  const resumes = (): string[] =>
    readProviderLog(world)
      .split('\n')
      .filter((line) => line.startsWith('resume:'))
      .map((line) => line.slice('resume:'.length));
  await waitFor(() => resumes().length >= count, `resume line ${count}`, 30_000);
  expect(readProviderLog(world)).not.toContain('resume-file-missing:');
  return resumes()[count - 1]!;
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
