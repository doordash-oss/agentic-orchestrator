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
 * Journey — explain in chat, end to end against the packaged app and real
 * bundled server with the supervisor provider stub:
 *
 * a harness and model chosen on the Supervisor chip → seeded
 * iteration_budget_exhausted failure → cockpit card → "Explain in chat" →
 * the Supervisor page is selected with the templated question drafted into
 * its focused composer and nothing sent (no provider invocation, empty
 * transcript) → Send → the stub's reply arrives over exactly one provider
 * invocation, the hidden error-context bundle reached the provider's wire,
 * and the committed user record holds only the visible question.
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
import { setFeatureStatus } from '../helpers/seed';
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  SUPERVISOR_E2E_HIDDEN_CONTEXT_HEADING,
  supervisorStubReply,
  type JourneyWorld,
} from '../helpers/world';

const CHIP_LABEL = 'Claude Haiku · Default';

test('explain in chat drafts the templated question into the Supervisor composer unsent', async ({}, testInfo) => {
  const transcript = new Transcript(
    'explain-in-chat',
    'Seeded failure → Explain in chat → unsent Supervisor draft → Send with hidden context',
  );
  const world = createWorld('explain-in-chat', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(
    `committed repository discovered from the preset workspace root: \`${alpha}\`, supervisor provider stub armed`,
  );

  let handle: AppHandle | null = null;
  try {
    transcript.section('Choose the supervisor model, then create the feature');
    handle = await launchApp(world, testInfo, { traceName: 'explain-in-chat-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(supervisorPage(handle.page)).toBeVisible();
    await chooseModel(handle.page);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(0);
    transcript.step(`the Supervisor chip reads "${CHIP_LABEL}"; no provider session yet`);

    const featureName = 'Explain Target';
    await createFeatureViaForm(handle, {
      name: featureName,
      repoPatterns: [/alpha/],
    });
    const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
    expect(features).toHaveLength(1);
    const featureId = features[0]!.id;
    await expect(handle.page.getByLabel(`Feature ${featureName}`)).toBeVisible();
    persistAppLogs(handle, 'explain-in-chat-first-run');
    await closeApp(handle);
    handle = null;

    transcript.section('Seed a Failed feature with an iteration_budget_exhausted record');
    setFeatureStatus(world.stateDir, featureId, 'Failed');
    const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
    let featureYaml = fs.readFileSync(featurePath, 'utf8');
    featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
    featureYaml = upsertYamlScalar(featureYaml, 'max_iterations', '5');
    fs.writeFileSync(featurePath, featureYaml);
    const runPath = path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml');
    const runYaml = replaceTopLevelBlock(fs.readFileSync(runPath, 'utf8'), 'failure', [
      'failure:',
      '  code: iteration_budget_exhausted',
      '  context:',
      '    phase:',
      '      name: implement',
      '      iteration: 3',
      '  diagnostics: phase hit the configured iteration ceiling',
    ]);
    fs.writeFileSync(runPath, runYaml);
    transcript.step('seeded Failed@implement with an iteration_budget_exhausted record');

    transcript.section('Relaunch; Explain in chat drafts into the Supervisor composer');
    handle = await launchApp(world, testInfo, { traceName: 'explain-in-chat-relaunch' });
    const page = handle.page;
    const cockpit = page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });

    const failureCard = cockpit.getByRole('alert');
    await expect(failureCard).toBeVisible({ timeout: 60_000 });
    await expect(failureCard.locator('.error-surface__code')).toHaveText(
      'iteration_budget_exhausted',
    );
    await expect(failureCard.locator('.error-surface__title')).toHaveText(
      'Iteration budget exhausted',
    );
    await failureCard.getByRole('button', { name: 'Explain in chat' }).click();

    const question = `Explain the "Iteration budget exhausted" error (iteration_budget_exhausted) on ${featureName} and what I should do next.`;
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    await expect(page.locator('.toolbar__title-name')).toHaveText('Supervisor');
    await expect(composer(page)).toHaveValue(question);
    await expect(composer(page)).toBeFocused();
    await expect(chip(page)).toHaveAccessibleName(CHIP_LABEL);
    await expect(sendButton(page)).toBeEnabled();
    transcript.step('the Supervisor page shows the templated question in its focused composer');

    // Nothing was sent: no provider session, an empty conversation.
    await expect(conversation(page)).toContainText('Start a conversation with the supervisor.');
    const unsent = await page.evaluate(() => window.agentico.getSupervisorTranscript({}));
    expect(unsent.items).toHaveLength(0);
    expect((await page.evaluate(() => window.agentico.getSupervisorState())).lifecycle).toBe(
      'stopped',
    );
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(0);
    await evidenceShot(handle, 'explain-in-chat-draft');
    transcript.step('nothing was sent: no provider invocation and an empty transcript');

    transcript.section('Send delivers the question with its hidden context');
    await sendButton(page).click();
    await expect(conversation(page)).toContainText(supervisorStubReply(1), { timeout: 60_000 });
    await expect(conversation(page)).toContainText(question);
    await expect(composer(page)).toHaveValue('');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    expect(readProviderLog(world)).toContain('hidden-context:1');
    transcript.step('the stub replied over one provider invocation; the bundle reached its wire');

    const sent = await page.evaluate(() => window.agentico.getSupervisorTranscript({}));
    const userTexts = sent.items
      .filter((record) => record.kind === 'user')
      .map((record) => record.messages.map((message) => message.text ?? '').join(''));
    expect(userTexts).toEqual([question]);
    for (const record of sent.items) {
      for (const message of record.messages) {
        expect(message.text ?? '').not.toContain(SUPERVISOR_E2E_HIDDEN_CONTEXT_HEADING);
      }
    }
    await evidenceShot(handle, 'explain-in-chat-reply');
    transcript.step('the committed user record holds only the visible question');

    transcript.json('provider log', readProviderLog(world).split('\n'));
    persistAppLogs(handle, 'explain-in-chat-second-run');
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

/** Commits Claude Haiku through the Supervisor chip and closes the popover. */
async function chooseModel(page: Page): Promise<void> {
  await chip(page).click();
  const popover = page.getByRole('region', { name: 'Harness and model' });
  await expect(popover).toBeVisible();
  await popover.getByRole('group', { name: 'Claude' }).getByText('Haiku', { exact: true }).click();
  await expect(chip(page)).toHaveAccessibleName(CHIP_LABEL);
  await page.keyboard.press('Escape');
  await expect(popover).toHaveCount(0);
}

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

function sendButton(page: Page): Locator {
  return supervisorPage(page)
    .locator('.supervisor-page__dock')
    .getByRole('button', { name: 'Send', exact: true });
}

function chip(page: Page): Locator {
  return page.getByTestId('supervisor-model-chip');
}

function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}
