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
 * The supervisor tracer bullet against the packaged app and the real bundled
 * server: the pinned Supervisor row, the harness-and-model chip and its
 * persistence, streamed turns over one long-lived provider process, an inline
 * permission request mirrored in the inbox, and Stop interrupting a held turn
 * without ending the process. The provider CLI is the only stub
 * (`supervisorProvider`), and it counts every real session it serves.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
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
  SUPERVISOR_E2E_MARKERS,
  SUPERVISOR_E2E_PERMISSION_COMMAND,
  supervisorStubReply,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_NAME = 'supervisor-first-run';
const CHIP_LABEL = 'Claude Haiku · Default';
const DETOUR_FEATURE = 'Detour feature';
/** The reconciled supervisor skill: the core, its references and the guide index. */
const SUPERVISOR_SKILL_FILES = [
  'SKILL.md',
  'api-reference.md',
  'recipes.md',
  'environment.md',
  path.join('user-guide', 'index.md'),
];

test('supervisor first run: choose a model, converse, approve inline, and stop a held turn', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Supervisor first-run tracer bullet journey');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'supervisor-lab', { commit: true });
  // A stale skill tree from before the rename, which reconciliation removes.
  const staleChatSkill = path.join(world.runtimeDir, 'skills', 'chat');
  fs.mkdirSync(staleChatSkill, { recursive: true });
  fs.writeFileSync(path.join(staleChatSkill, 'SKILL.md'), '# chat\n');
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('The bundled server reconciled the supervisor skill tree');
    // Reconciliation runs before the server reports ready: the core, its
    // three references and the user-guide index are on disk, and the
    // retired `chat` skill directory seeded before launch is gone.
    const skillsDir = path.join(world.runtimeDir, 'skills');
    for (const file of SUPERVISOR_SKILL_FILES) {
      expect(fs.existsSync(path.join(skillsDir, 'supervisor', file)), file).toBe(true);
    }
    expect(fs.existsSync(staleChatSkill)).toBe(false);
    transcript.json('supervisor skill files', SUPERVISOR_SKILL_FILES);

    transcript.section('The pinned Supervisor row is home: first, selected and showing on launch');
    const rows = page.getByRole('listbox', { name: 'Features' }).getByRole('option');
    await expect(rows.nth(0)).toHaveAccessibleName('Supervisor');
    await expect(rows.nth(0)).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByRole('option', { name: 'Overview' })).toHaveCount(0);
    // The sidebar footer no longer carries a separate Ask chip.
    await expect(
      page.locator('.sidebar__footer').getByRole('button', { name: /^Ask/ }),
    ).toHaveCount(0);
    await expect(supervisorPage(page)).toBeVisible();
    await expect(page.locator('.toolbar__title-name')).toHaveText('Supervisor');
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible();
    await expect(conversation(page)).toContainText(/Good (?:morning|afternoon|evening)\./);
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'stopped');
    transcript.step('Supervisor row is first and selected, and shows the empty conversation');

    transcript.section('Without settings Send is disabled behind the choose-a-model placeholder');
    await expect(composer(page)).toHaveAttribute(
      'placeholder',
      'Choose a harness and model to start',
    );
    await expect(sendButton(page)).toBeDisabled();
    await expect(chip(page)).toHaveAccessibleName('Choose harness and model');
    await evidenceShot(handle, 'supervisor-first-run-unset');

    transcript.section('The chip commits Claude and a model immediately');
    await chip(page).click();
    const popover = page.getByRole('region', { name: 'Harness and model' });
    await expect(popover).toBeVisible();
    const claude = popover.getByRole('group', { name: 'Claude' });
    await expect(claude.getByRole('radio', { name: 'Sonnet' })).toBeVisible();
    await claude.getByText('Haiku', { exact: true }).click();
    await expect(chip(page)).toHaveAccessibleName(CHIP_LABEL);
    await expect(claude.getByRole('radio', { name: 'Haiku' })).toBeChecked();
    await expect(popover.getByRole('group', { name: 'Effort' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(popover).toHaveCount(0);
    await expect(composer(page)).toHaveAttribute('placeholder', 'Message the supervisor');
    const committed = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(committed.settings).toMatchObject({ harness: 'claude', model: 'haiku', effort: '' });
    expect(committed.lifecycle).toBe('stopped');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(0);
    transcript.json('committed supervisor settings', committed.settings);

    transcript.section('The committed choice survives a reload, which lands back on Supervisor');
    await page.reload();
    await expect(page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    await expect(chip(page)).toHaveAccessibleName(CHIP_LABEL);
    transcript.step(`chip still reads "${CHIP_LABEL}" after a reload`);

    await captureSupervisorDeltas(page);

    transcript.section('A first message launches the supervisor and streams its reply');
    await sendMessage(page, 'Hello supervisor, what is running?');
    await expect(conversation(page)).toContainText(supervisorStubReply(1), { timeout: 60_000 });
    await expectReady(page);
    await expect(conversation(page)).toContainText('Hello supervisor, what is running?');
    expect(await capturedDeltaText(page)).toContain(supervisorStubReply(1));
    await expect(chip(page)).toBeEnabled();
    const firstState = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(firstState.sessionId).toMatch(/^__supervisor__\.[0-9a-f-]+\.\d+$/);
    expect(firstState.lastTurnOutcome).toBe('completed');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    await evidenceShot(handle, 'supervisor-first-run-reply');
    transcript.step(`streamed "${supervisorStubReply(1)}" and returned to Ready`);

    transcript.section('A second message reuses the same provider process');
    await sendMessage(page, 'And a follow-up question.');
    await expect(conversation(page)).toContainText(supervisorStubReply(2), { timeout: 30_000 });
    await expectReady(page);
    const secondState = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(secondState.sessionId).toBe(firstState.sessionId);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    transcript.step('second reply arrived on the same session; one provider invocation total');

    transcript.section('A Bash request surfaces inline and in the inbox, then is approved inline');
    await sendMessage(page, `Run the checks ${SUPERVISOR_E2E_MARKERS.permission}`);
    const card = conversation(page).getByText('Permission request', { exact: true });
    await expect(card).toBeVisible({ timeout: 30_000 });
    await expect(conversation(page)).toContainText(SUPERVISOR_E2E_PERMISSION_COMMAND);
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'waiting_permission');
    // A pending request hides Working; Stop stays live.
    await expect(status(page)).toHaveText('Waiting for your response…');
    await expect(composer(page)).toHaveAttribute(
      'placeholder',
      'Respond to the pending request above to continue',
    );
    await expect(stopButton(page)).toBeVisible();
    await waitForProviderLog(world, 'pending:supervisor-perm-3');
    await evidenceShot(handle, 'supervisor-first-run-permission');

    // The inbox carries the same request under the Supervisor context and
    // routes back to the Supervisor page from anywhere: open a feature (the
    // only other page there is) while the request is pending. The feature is
    // never started, so the provider still serves exactly one session.
    await createFeatureViaForm(handle, {
      name: DETOUR_FEATURE,
      repoPatterns: [/supervisor-lab/],
    });
    await expect(page.locator('.toolbar__title-name')).toHaveText(DETOUR_FEATURE);
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'false');
    const inbox = await openInbox(page);
    const inboxItem = inbox.getByRole('button', { name: /Supervisor/ });
    await expect(inboxItem).toHaveCount(1);
    await expect(inboxItem).toContainText('Supervisor');
    await evidenceShot(handle, 'supervisor-first-run-inbox');
    await inboxItem.click();
    await expect(page.getByRole('complementary', { name: 'Attention inbox' })).toHaveCount(0);
    await expect(page.locator('.toolbar__title-name')).toHaveText('Supervisor');
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(card).toBeVisible();

    await conversation(page).getByRole('button', { name: 'Allow once', exact: true }).click();
    // One line in place of the card; the transcript's request summary is the
    // redacted projection, never the raw command.
    const verdict = conversation(page).getByText(/^Allowed Bash/);
    await expect(verdict).toHaveCount(1, { timeout: 30_000 });
    await expect(verdict).toBeVisible();
    await expect(conversation(page)).not.toContainText(SUPERVISOR_E2E_PERMISSION_COMMAND);
    await expect(card).toHaveCount(0);
    await expect(conversation(page)).toContainText(supervisorStubReply(3), { timeout: 30_000 });
    await expectReady(page);
    await waitForProviderLog(world, 'response:supervisor-perm-3:');
    expect(readProviderLog(world)).toMatch(/response:supervisor-perm-3:.*"behavior":"allow"/);
    await waitFor(
      async () =>
        (await page.evaluate(() => window.agentico.getAttention())).items.every(
          (item) => item.id !== 'supervisor-perm-3',
        ),
      'the answered supervisor request to leave the inbox',
    );
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    await evidenceShot(handle, 'supervisor-first-run-verdict');
    transcript.step('approved inline: one-line verdict, completed turn, inbox cleared');

    transcript.section('Stop interrupts a held turn and leaves the process idle');
    await sendMessage(page, `Watch the build ${SUPERVISOR_E2E_MARKERS.hold}`);
    await waitForProviderLog(world, 'holding:4');
    await expect(status(page)).toHaveAttribute('data-lifecycle', 'running');
    await expect(status(page)).toHaveText('Working…');
    await expect(stopButton(page)).toBeEnabled();
    await expect(conversation(page).getByText(/elapsed/)).toBeVisible();
    await evidenceShot(handle, 'supervisor-first-run-working');
    await stopButton(page).click();
    await waitForProviderLog(world, 'interrupted:4');
    await expectReady(page);
    await expect(sendButton(page)).toBeVisible();
    const stoppedState = await page.evaluate(() => window.agentico.getSupervisorState());
    expect(stoppedState.lastTurnOutcome).toBe('interrupted');
    expect(stoppedState.sessionId).toBe(firstState.sessionId);
    expect(stoppedState.lifecycle).toBe('idle');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    await evidenceShot(handle, 'supervisor-first-run-stopped');
    transcript.step('Stop returned the supervisor to Ready; the provider ran exactly once');

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

/** The pinned first sidebar row; exact so a feature row naming the supervisor never matches. */
function supervisorRow(page: Page): Locator {
  return page.getByRole('option', { name: 'Supervisor', exact: true });
}

function supervisorPage(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor', exact: true });
}

function conversation(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor conversation' });
}

function composer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

/** The composer's own controls, outside the transcript (a question card has its own Send). */
function dock(page: Page): Locator {
  return supervisorPage(page).locator('.supervisor-page__dock');
}

function sendButton(page: Page): Locator {
  return dock(page).getByRole('button', { name: 'Send', exact: true });
}

function stopButton(page: Page): Locator {
  return dock(page).getByRole('button', { name: 'Stop', exact: true });
}

function chip(page: Page): Locator {
  return page.getByTestId('supervisor-model-chip');
}

function status(page: Page): Locator {
  return page.getByTestId('supervisor-status');
}

async function expectReady(page: Page): Promise<void> {
  await expect(status(page)).toHaveAttribute('data-lifecycle', 'idle', { timeout: 30_000 });
  await expect(status(page)).toHaveText('Ready');
}

async function sendMessage(page: Page, text: string): Promise<void> {
  await composer(page).fill(text);
  await expect(sendButton(page)).toBeEnabled();
  await sendButton(page).click();
}

async function openInbox(page: Page): Promise<Locator> {
  const inbox = page.getByRole('complementary', { name: 'Attention inbox' });
  if ((await inbox.count()) === 0) {
    await page.getByRole('button', { name: /Attention inbox, \d+ pending/ }).click();
  }
  await expect(inbox).toBeVisible();
  return inbox;
}

/** Records every streamed delta the renderer receives, to prove the reply streamed. */
async function captureSupervisorDeltas(page: Page): Promise<void> {
  await page.evaluate(() => {
    const global = window as typeof window & { __supervisorDeltas?: string[] };
    global.__supervisorDeltas = [];
    window.agentico.onSupervisorEvent((event) => {
      if (event.type === 'delta') global.__supervisorDeltas?.push(event.delta.text);
    });
  });
}

async function capturedDeltaText(page: Page): Promise<string> {
  return page.evaluate(() => {
    const global = window as typeof window & { __supervisorDeltas?: string[] };
    return (global.__supervisorDeltas ?? []).join('');
  });
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
