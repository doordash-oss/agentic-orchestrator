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
 * The supervisor operating Agentico, seen from the sidebar: the stub
 * provider (launched by the real supervisor launcher, so it holds the
 * bundled binary as AGENTICO_BIN and the world's runtime directory as
 * AGENTICO_RUNTIME_DIR) creates, configures and starts a feature through the
 * packaged binary's `agentico api` helper, and the sidebar follows the live
 * stream those mutations publish — a new At rest row, then the same row in
 * Running — while the Supervisor row stays selected and nothing reloads.
 * The bearer token from the discovery file must surface nowhere the
 * conversation reaches.
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
import { worldProcessPIDs } from '../helpers/processes';
import { requireDiscovery } from '../helpers/runtime';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  SUPERVISOR_E2E_HELPER_LOG_PREFIX,
  SUPERVISOR_E2E_MARKERS,
  SUPERVISOR_E2E_OPERATE_CONFIG,
  SUPERVISOR_E2E_OPERATE_STARTED_REPLY,
  supervisorOperateCreatedReply,
  supervisorOperateCreateMarker,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_NAME = 'supervisor-helper-sidebar';
const CHIP_LABEL = 'Claude Haiku · Default';
const FEATURE_NAME = 'Helper Sidebar Feature';
/** Set on the renderer's window before the first turn; a reload would clear it. */
const NO_RELOAD_MARK = '__supervisorHelperSidebarNoReload';

test('supervisor helper sidebar: helper-created feature appears At rest, then moves to Running, live', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Supervisor operates a feature through the helper');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
    workflowProvider: true,
  });
  createRepo(world, 'helper-lab', { commit: true });
  let handle: AppHandle | null = null;
  let token = '';

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    token = requireDiscovery(world).auth_token ?? '';
    expect(token, 'the bundled server publishes a bearer token').not.toBe('');
    transcript.step('app launched against the bundled server; discovery token captured');

    transcript.section('Supervisor is home and a model is chosen');
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    await expect(featureRows(page)).toHaveCount(0);
    await chip(page).click();
    const popover = page.getByRole('region', { name: 'Harness and model' });
    await popover
      .getByRole('group', { name: 'Claude' })
      .getByText('Haiku', { exact: true })
      .click();
    await expect(chip(page)).toHaveAccessibleName(CHIP_LABEL);
    await page.keyboard.press('Escape');
    await expect(popover).toHaveCount(0);
    await page.evaluate((mark) => {
      (window as unknown as Record<string, unknown>)[mark] = true;
    }, NO_RELOAD_MARK);
    transcript.step(`chip reads "${CHIP_LABEL}"`);

    transcript.section('The stub creates and configures a feature through the helper');
    await sendMessage(page, `Set up a feature ${supervisorOperateCreateMarker(FEATURE_NAME)}`);
    await expect(conversation(page)).toContainText(supervisorOperateCreatedReply(FEATURE_NAME), {
      timeout: 60_000,
    });
    await expectReady(page);
    const featureId = operatedFeatureId(world);
    expect(featureId, 'the create helper call returned a feature id').not.toBe('');
    expect(helperExits(world)).toEqual(['1:0', '2:0', '3:0', '4:0']);

    const atRestRow = lane(page, 'At rest').getByRole('option', { name: FEATURE_NAME });
    await expect(atRestRow).toBeVisible({ timeout: 30_000 });
    await expect(atRestRow.locator('.sidebar__row-subline')).toHaveText('Created');
    // The row's identity is the feature id, so the Running row below is this row.
    const rowId = await atRestRow.getAttribute('id');
    expect(rowId).toBe(`sidebar-row-${featureId}`);
    await expect(featureRows(page)).toHaveCount(1);
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    expect(await notReloaded(page)).toBe(true);
    const created = await page.evaluate((id) => window.agentico.getFeature(id), featureId);
    expect(created.name).toBe(FEATURE_NAME);
    await evidenceShot(handle, 'supervisor-helper-sidebar-at-rest');
    transcript.step(`"${FEATURE_NAME}" (${featureId}) appeared At rest with the Created sub-line`);

    transcript.section('The stub starts the feature; the same row moves to Running');
    await sendMessage(page, `Start it now ${SUPERVISOR_E2E_MARKERS.operateStart}`);
    await expect(conversation(page)).toContainText(SUPERVISOR_E2E_OPERATE_STARTED_REPLY, {
      timeout: 60_000,
    });
    await expectReady(page);
    expect(helperExits(world)).toEqual(['1:0', '2:0', '3:0', '4:0', '5:0']);
    const runningRow = lane(page, 'Running').getByRole('option', { name: FEATURE_NAME });
    await expect(runningRow).toBeVisible({ timeout: 60_000 });
    await expect(lane(page, 'At rest').getByRole('option', { name: FEATURE_NAME })).toHaveCount(0);
    await expect(featureRows(page)).toHaveCount(1);
    await expect(runningRow).toHaveAttribute('id', rowId!);
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    expect(await notReloaded(page)).toBe(true);
    // One supervisor session plus the one workflow session the start launched.
    await waitFor(
      () => providerInvocationCount(world.providerInvocationLog) === 2,
      'the started feature to launch exactly one workflow session',
      60_000,
    );
    await evidenceShot(handle, 'supervisor-helper-sidebar-running');
    transcript.step('the row left At rest and appears in Running; Supervisor stayed selected');

    transcript.section('The conversation carries the helper tool records');
    const page1 = await page.evaluate(() =>
      window.agentico.getSupervisorTranscript({ limit: 200 }),
    );
    const toolUses = page1.items.filter((record) => record.kind === 'tool_use');
    const toolResults = page1.items.filter((record) => record.kind === 'tool_result');
    expect(toolUses).toHaveLength(5);
    expect(toolResults).toHaveLength(5);
    expect(toolUses.flatMap((record) => record.messages.map((row) => row.tool))).toEqual([
      'Bash',
      'Bash',
      'Bash',
      'Bash',
      'Bash',
    ]);
    // Each operate turn folds its helper calls into an expandable history.
    // The separate live indicator can also show the latest completed turn.
    const history = conversation(page).locator('.conversation__activity-history');
    await expect(history.locator('summary')).toHaveText(['4 activity steps', '1 activity step']);
    for (const group of await history.all()) await group.locator('summary').click();
    await expect(history.getByRole('listitem')).toHaveCount(5);
    for (const step of await history.getByRole('listitem').all()) {
      await expect(step).toBeVisible();
      await expect(step).toContainText(/bash/i);
    }
    transcript.json(
      'supervisor transcript kinds',
      page1.items.map((record) => record.kind),
    );

    transcript.section('The invocation log holds the helper commands');
    const helper = '"$AGENTICO_BIN" api';
    expect(helperCommands(world)).toEqual([
      `${helper} POST /api/v1/features '{"name":"${FEATURE_NAME}"}'`,
      `${helper} POST /api/v1/features/${featureId}/actions/setup '{}'`,
      `${helper} POST /api/v1/features/${featureId}/config '${JSON.stringify(SUPERVISOR_E2E_OPERATE_CONFIG)}'`,
      `${helper} GET /api/v1/features/${featureId}`,
      `${helper} POST /api/v1/features/${featureId}/actions/start '{}'`,
    ]);
    transcript.json('helper commands', helperCommands(world));

    transcript.section('The bearer token appears nowhere the conversation reaches');
    const rendered = await conversation(page).innerText();
    expect(rendered).toContain(supervisorOperateCreatedReply(FEATURE_NAME));
    expect(rendered).not.toContain(token);
    expect(readProviderLog(world)).not.toContain(token);
    const transcripts = durableTranscripts(world);
    expect(transcripts.length, 'one durable supervisor transcript').toBe(1);
    const durable = fs.readFileSync(transcripts[0]!, 'utf8');
    // The durable transcript is the one the helper records landed in.
    expect(durable).toContain(`/api/v1/features/${featureId}/actions/start`);
    expect(durable).not.toContain(token);
    const leaking = supervisorFiles(world).filter((file) => fs.readFileSync(file).includes(token));
    expect(leaking, 'supervisor conversation files carrying the token').toEqual([]);
    transcript.step('token absent from the conversation, invocation log and durable transcript');

    transcript.section('Selecting the row opens the helper-created feature');
    await runningRow.click();
    await expect(page.getByLabel(`Feature ${FEATURE_NAME}`)).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('.toolbar__title-name')).toHaveText(FEATURE_NAME);
    await expect(runningRow).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'false');
    await evidenceShot(handle, 'supervisor-helper-sidebar-feature');
    transcript.step(`the feature page for ${featureId} is open`);

    transcript.json('provider log', readProviderLog(world).split('\n'));
  } finally {
    if (handle !== null) {
      const logs = persistAppLogs(handle, RUN_NAME);
      if (token !== '') expect(logs).not.toContain(token);
      transcript.write(testInfo);
      await closeApp(handle).catch(() => {});
    }
    // The started feature's workflow stub waits for an interrupt; reap any
    // provider process the app's shutdown left behind.
    cleanupFixtureProcesses(world.root);
    await waitFor(
      () => worldProcessPIDs(world.root).length === 0,
      'packaged app, server, and provider processes to exit',
      20_000,
    );
    assertNoLeakedProcesses(world);
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

function featuresListbox(page: Page): Locator {
  return page.getByRole('listbox', { name: 'Features' });
}

/** Feature rows only: every option inside a lane group. */
function featureRows(page: Page): Locator {
  return featuresListbox(page).getByRole('group').getByRole('option');
}

function lane(page: Page, name: string): Locator {
  return featuresListbox(page).getByRole('group', { name, exact: true });
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

async function notReloaded(page: Page): Promise<boolean> {
  return page.evaluate(
    (mark) => (window as unknown as Record<string, unknown>)[mark] === true,
    NO_RELOAD_MARK,
  );
}

function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}

function providerLogValues(world: JourneyWorld, prefix: string): string[] {
  return readProviderLog(world)
    .split('\n')
    .filter((line) => line.startsWith(prefix))
    .map((line) => line.slice(prefix.length));
}

function helperCommands(world: JourneyWorld): string[] {
  return providerLogValues(world, SUPERVISOR_E2E_HELPER_LOG_PREFIX);
}

/** `<call>:<exit code>` for every helper call so far. */
function helperExits(world: JourneyWorld): string[] {
  return providerLogValues(world, 'helper-exit:');
}

function operatedFeatureId(world: JourneyWorld): string {
  return providerLogValues(world, 'operate-feature:').at(-1) ?? '';
}

/** Every file under the server's supervisor state: transcripts, indexes, generation logs. */
function supervisorFiles(world: JourneyWorld): string[] {
  const root = path.join(world.stateDir, 'supervisor');
  return (fs.readdirSync(root, { recursive: true, withFileTypes: true }) as fs.Dirent[])
    .filter((entry) => entry.isFile())
    .map((entry) => path.join(entry.parentPath, entry.name));
}

function durableTranscripts(world: JourneyWorld): string[] {
  return supervisorFiles(world).filter(
    (file) =>
      path.basename(file) === 'transcript.jsonl' &&
      path.basename(path.dirname(path.dirname(file))) === 'conversations',
  );
}

function cleanupFixtureProcesses(worldRoot: string): void {
  for (const pid of worldProcessPIDs(worldRoot)) {
    try {
      process.kill(-pid, 'SIGKILL');
    } catch {
      try {
        process.kill(pid, 'SIGKILL');
      } catch {
        // The process exited between enumeration and teardown.
      }
    }
  }
}
