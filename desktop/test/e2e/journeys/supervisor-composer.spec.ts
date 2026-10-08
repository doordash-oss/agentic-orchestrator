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
 * The finished Supervisor composer against the packaged app, the real
 * bundled server and the supervisorProvider Claude stub: attachments picked
 * through the (stubbed) native picker reach the harness as readable copies
 * under the conversation directory; messages submitted during a held turn
 * queue in the strip, Escape stops the turn without flushing them, and a
 * manual send goes first before the queue drains one turn at a time;
 * "Send now" interrupts and delivers its item; "New conversation" resets an
 * idle conversation at once and confirms first while a turn is held.
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
import {
  createRepo,
  createWorld,
  destroyWorld,
  SUPERVISOR_E2E_MARKERS,
  waitFor,
} from '../helpers/world';

const RUN_NAME = 'supervisor-composer';
const CHIP_LABEL = 'Claude Haiku · Default';
const NOTE_FIRST_LINE = 'Composer journey note first line';
const PICKER_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);

/** Answers the native open dialog by title inside the running main process. */
async function stubPicker(handle: AppHandle, answers: Record<string, string[]>): Promise<void> {
  await handle.app.evaluate(({ dialog }, nextAnswers) => {
    dialog.showOpenDialog = (async (...args: unknown[]) => {
      const options = args[args.length - 1] as { title?: string };
      const filePaths = nextAnswers[options.title ?? ''] ?? [];
      return { canceled: filePaths.length === 0, filePaths, bookmarks: [] };
    }) as typeof dialog.showOpenDialog;
  }, answers);
}

function composer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

function conversation(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor conversation' });
}

function queueStrip(page: Page): Locator {
  return page.getByRole('region', { name: 'Queued messages' });
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

async function submit(page: Page, text: string): Promise<void> {
  await composer(page).fill(text);
  await composer(page).press('Enter');
}

async function expectLifecycle(page: Page, lifecycle: string): Promise<void> {
  await expect(status(page)).toHaveAttribute('data-lifecycle', lifecycle, { timeout: 30_000 });
}

function invocationLog(logPath: string): string {
  return fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : '';
}

/** The visible text positions of `needles` in the transcript, in the order given. */
async function transcriptOrder(page: Page, needles: string[]): Promise<number[]> {
  const text = await conversation(page).innerText();
  return needles.map((needle) => text.indexOf(needle));
}

test('supervisor composer: attachments, the queue, Escape, Send now and New conversation', async ({}, testInfo) => {
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'composer-lab', { commit: true });
  const filesDir = path.join(world.root, 'picked');
  fs.mkdirSync(filesDir, { recursive: true });
  const imagePath = path.join(filesDir, 'diagram.png');
  const notePath = path.join(filesDir, 'notes.txt');
  const moreImages = [2, 3, 4].map((index) =>
    path.join(filesDir, `clipboard-79963618-2eca-44b2-a61a-c39946ffc3b4-${index}.png`),
  );
  moreImages.forEach((image) => fs.writeFileSync(image, PICKER_PNG));
  fs.writeFileSync(imagePath, PICKER_PNG);
  fs.writeFileSync(notePath, `${NOTE_FIRST_LINE}\nsecond line\n`);
  const { hold } = SUPERVISOR_E2E_MARKERS;
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(page.getByTestId('supervisor-model-chip')).toBeVisible({ timeout: 60_000 });
    await chooseHaiku(page);

    // Attachments: an image and a text file reach the harness as copies it can read.
    const attach = page.getByRole('button', { name: 'Attach files or photos' });
    await stubPicker(handle, { 'Choose images': [imagePath, ...moreImages] });
    await attach.click();
    await page.getByRole('menuitem', { name: 'Add photos' }).click();
    const draftAttachments = page.getByRole('list', { name: 'Attached files' });
    await expect(draftAttachments.getByRole('listitem')).toHaveCount(4);
    for (let index = 1; index <= 4; index++) {
      await expect(draftAttachments.getByText(`Image ${index}`, { exact: true })).toBeVisible();
    }
    await expect(draftAttachments).not.toContainText('clipboard-');
    const draft =
      'Compare these screenshots.\nKeep the editor comfortable.\nImage 1 shows the initial state.\nImage 2 shows the expanded activity.\nPlease preserve the writing room.';
    await composer(page).fill(draft);
    await composer(page).focus();
    const previousViewport = await page.evaluate(() => ({
      width: innerWidth,
      height: innerHeight,
    }));
    await page.setViewportSize({ width: 760, height: 800 });
    const geometry = await draftAttachments.evaluate((list) => {
      const tokens = [...list.children].map((item) => item.getBoundingClientRect());
      return {
        tops: tokens.map((token) => token.top),
        height: list.getBoundingClientRect().height,
      };
    });
    expect(Math.max(...geometry.tops) - Math.min(...geometry.tops)).toBeLessThan(2);
    expect(geometry.height).toBeLessThan(42);
    const editorGeometry = await composer(page).evaluate((editor) => ({
      height: editor.clientHeight,
      contentHeight: editor.scrollHeight,
    }));
    expect(editorGeometry.height).toBeGreaterThan(90);
    expect(editorGeometry.contentHeight).toBeLessThanOrEqual(editorGeometry.height + 1);
    await testInfo.attach('compact-composer-attachments', {
      body: await page.locator('.supervisor-composer').screenshot(),
      contentType: 'image/png',
    });
    await evidenceShot(handle, 'supervisor-composer-compact');
    for (const image of moreImages) {
      await draftAttachments
        .getByRole('button', { name: `Remove ${path.basename(image)}` })
        .click();
    }
    await stubPicker(handle, { 'Choose attachments': [notePath] });
    await attach.click();
    const attachMenu = page.getByRole('menu');
    await expect(attachMenu).toBeVisible();
    const menuBounds = await attachMenu.boundingBox();
    expect(menuBounds!.x).toBeGreaterThanOrEqual(0);
    expect(menuBounds!.y).toBeGreaterThanOrEqual(0);
    expect(menuBounds!.x + menuBounds!.width).toBeLessThanOrEqual(760);
    await page.getByRole('menuitem', { name: 'Add files' }).click();
    await page.setViewportSize(previousViewport);
    await submit(page, 'Read the attached files');
    const chips = conversation(page).getByRole('list', { name: 'Attachments' });
    await expect(chips.getByRole('listitem')).toHaveCount(2, { timeout: 30_000 });
    await expect(chips).toContainText('diagram.png');
    await expect(chips).toContainText('notes.txt');
    await expectLifecycle(page, 'idle');
    await waitFor(
      () =>
        /attachment:1:[^\n]*\/attachments\/[^\n]*\.png\n/.test(
          invocationLog(world.providerInvocationLog),
        ) &&
        new RegExp(`attachment-line:1:[^\\n]*/attachments/[^\\n]*\\.txt:${NOTE_FIRST_LINE}`).test(
          invocationLog(world.providerInvocationLog),
        ),
      'the stub read both conversation copies',
    );
    expect(invocationLog(world.providerInvocationLog)).not.toContain(imagePath);
    expect(invocationLog(world.providerInvocationLog)).not.toContain(notePath);

    // Queue: two messages wait while a turn is held; Escape stops the turn
    // and keeps them; a manual send goes first and the queue then drains.
    await submit(page, `Hold this turn ${hold}`);
    await expectLifecycle(page, 'running');
    await submit(page, 'Second message');
    await submit(page, 'Third message');
    await expect(queueStrip(page).getByRole('listitem')).toHaveCount(2);
    expect(invocationLog(world.providerInvocationLog)).not.toMatch(/interrupted:2/);
    await composer(page).focus();
    await page.keyboard.press('Escape');
    await waitFor(
      () => invocationLog(world.providerInvocationLog).includes('interrupted:2'),
      'the stub logged the Escape interrupt',
    );
    await expectLifecycle(page, 'idle');
    await expect(queueStrip(page).getByRole('listitem')).toHaveCount(2);
    await submit(page, 'Fourth message');
    await expect(queueStrip(page)).toHaveCount(0, { timeout: 60_000 });
    await expectLifecycle(page, 'idle');
    await expect(conversation(page)).toContainText('Third message');
    const [fourth, second, third] = await transcriptOrder(page, [
      'Fourth message',
      'Second message',
      'Third message',
    ]);
    expect(fourth).toBeGreaterThan(-1);
    expect(second).toBeGreaterThan(fourth!);
    expect(third).toBeGreaterThan(second!);

    // Send now: interrupts the held turn and delivers the marked item.
    await submit(page, `Hold again ${hold}`);
    await expectLifecycle(page, 'running');
    await submit(page, 'Jump the line');
    await queueStrip(page).getByRole('button', { name: 'Send now' }).click();
    await waitFor(
      () => /interrupted:6/.test(invocationLog(world.providerInvocationLog)),
      'the stub logged the Send now interrupt',
    );
    await expect(queueStrip(page)).toHaveCount(0, { timeout: 30_000 });
    await expect(conversation(page)).toContainText('Jump the line');
    await expectLifecycle(page, 'idle');

    // New conversation while idle: no dialog, an empty transcript, the same chip.
    const before = await page.evaluate(() => window.agentico.getSupervisorState());
    await page.getByRole('button', { name: 'New conversation' }).click();
    await expect(page.getByRole('dialog')).toHaveCount(0);
    await expect(conversation(page)).not.toContainText('Jump the line', { timeout: 30_000 });
    await expect(page.getByTestId('supervisor-model-chip')).toHaveAccessibleName(CHIP_LABEL);
    const after = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(after.conversationId).not.toBe(before.conversationId);
    expect(
      fs.existsSync(
        path.join(world.stateDir, 'supervisor', 'conversations', before.conversationId),
      ),
    ).toBe(true);

    // New conversation during a held turn confirms, then stops the turn.
    await submit(page, `Hold once more ${hold}`);
    await expectLifecycle(page, 'running');
    await page.getByRole('button', { name: 'New conversation' }).click();
    const dialog = page.getByRole('dialog', { name: 'Start a new conversation' });
    await expect(dialog).toContainText('The current turn will be stopped.');
    await dialog.getByRole('button', { name: 'New conversation' }).click();
    // New conversation stops the process as End does (stdin closes), and the
    // new conversation's process counts turns from 1 again.
    await waitFor(
      () => invocationLog(world.providerInvocationLog).includes('hold-ended:1\n'),
      'the stub saw the held turn end',
    );
    const oldTranscript = path.join(
      world.stateDir,
      'supervisor',
      'conversations',
      after.conversationId,
      'transcript.jsonl',
    );
    await waitFor(
      () =>
        fs.existsSync(oldTranscript) &&
        fs
          .readFileSync(oldTranscript, 'utf8')
          .includes('Interrupted when a new conversation was started'),
      'the old transcript marks the cut turn interrupted',
    );
    await expect(conversation(page)).not.toContainText('Hold once more', { timeout: 30_000 });
    await expect(status(page)).not.toHaveAttribute('data-lifecycle', 'running');
    const reset = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(reset.conversationId).not.toBe(after.conversationId);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
