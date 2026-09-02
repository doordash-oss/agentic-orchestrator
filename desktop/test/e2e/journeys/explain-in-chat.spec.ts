/**
 * Journey — explain in chat, end to end against the packaged app and real
 * bundled server with the workflow provider stub:
 *
 * seeded iteration_budget_exhausted failure → cockpit card → "Explain in
 * chat" → AMA panel opens and auto-submits the templated question with the
 * run-scoped context reference → the transcript shows the question as the
 * user bubble followed by the stub's assistant reply, the composer stays
 * empty, the stored initial prompt equals the visible question alone, and
 * no transcript row carries the hidden bundle heading → a typed follow-up
 * sends into the same live session.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { setFeatureStatus } from '../helpers/seed';
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const HIDDEN_BUNDLE_HEADING = 'Chat context —';

test('explain in chat submits the templated question with hidden context', async ({}, testInfo) => {
  const transcript = new Transcript(
    'explain-in-chat',
    'Seeded failure → Explain in chat → auto-submitted question with hidden context → live follow-up',
  );
  const world = createWorld('explain-in-chat', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(
    `committed repository discovered from the preset workspace root: \`${alpha}\`, workflow provider stub armed`,
  );

  let handle: AppHandle | null = null;
  try {
    transcript.section('Create the feature; setup completes');
    handle = await launchApp(world, testInfo, { traceName: 'explain-in-chat-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
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

    transcript.section('Relaunch; Explain in chat auto-submits the templated question');
    handle = await launchApp(world, testInfo, { traceName: 'explain-in-chat-relaunch' });
    const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });

    const failureCard = cockpit.getByRole('alert');
    await expect(failureCard).toBeVisible({ timeout: 60_000 });
    await expect(failureCard.locator('.error-surface__code')).toHaveText(
      'iteration_budget_exhausted',
    );
    await expect(failureCard.locator('.error-surface__title')).toHaveText(
      'Iteration budget exhausted',
    );

    const panel = handle.page.getByRole('complementary', { name: 'Ask Agentico' });
    await expect(panel).toHaveCount(0);
    await failureCard.getByRole('button', { name: 'Explain in chat' }).click();
    await expect(panel).toBeVisible({ timeout: 60_000 });

    const question = `Explain the "Iteration budget exhausted" error (iteration_budget_exhausted) on ${featureName} and what I should do next.`;
    const chatTranscript = panel.getByLabel('AMA transcript');
    await expect(chatTranscript).toContainText(question, { timeout: 60_000 });
    await expect(chatTranscript).toContainText(/Backfill ready|Live semantic/, { timeout: 60_000 });
    transcript.step('the AMA panel opened with the templated question and the stub reply');

    const composer = panel.getByRole('textbox', { name: 'Ask Agentico' });
    await expect(composer).toHaveValue('');

    const session = await handle.page.evaluate(() => window.agentico.getSession('__chat__'));
    expect(session.id).toBe('__chat__');
    expect(session.initialPrompt).toBe(question);
    transcript.step('the stored initial prompt is the visible question alone');

    const loaded = await handle.page.evaluate(() =>
      window.agentico.getSessionTranscript({ sessionId: '__chat__', limit: 200 }),
    );
    expect(loaded.messages.length).toBeGreaterThan(0);
    for (const message of loaded.messages) {
      expect(message.text ?? '').not.toContain(HIDDEN_BUNDLE_HEADING);
    }
    transcript.step('no transcript row carries the hidden bundle heading');

    transcript.section('A typed follow-up sends into the same live session');
    await composer.fill('What should I do first?');
    await panel.getByRole('button', { name: 'Send' }).click();
    const followUp = await handle.page.evaluate(() =>
      window.agentico.getSessionTranscript({ sessionId: '__chat__', limit: 200 }),
    );
    expect(followUp.messages.length).toBeGreaterThan(loaded.messages.length);
    expect(
      followUp.messages.some((message) => (message.text ?? '').includes('What should I do first?')),
    ).toBe(true);
    const liveSession = await handle.page.evaluate(() => window.agentico.getSession('__chat__'));
    expect(liveSession.id).toBe('__chat__');
    transcript.step('the follow-up joined the same live chat session');

    persistAppLogs(handle, 'explain-in-chat-second-run');
    await closeApp(handle);
    handle = null;
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
