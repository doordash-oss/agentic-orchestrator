import fs from 'node:fs';
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
import { seedVerificationNeedUserInputGate } from '../helpers/verificationGateFixture';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

type AttentionItems = Awaited<ReturnType<Window['agentico']['getAttention']>>['items'];

test('packaged phase rail: a hold opens with the paused dot and tooltip, then resolves back to Elapsed/Cost/Context', async ({}, testInfo) => {
  const transcript = new Transcript(
    'phase-rail',
    'Rail hold journey — Paused substitution and dot on a NEED_USER_INPUT gate, Elapsed/Cost/Context restored after retry',
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

    transcript.section('Create a feature and seed a NEED_USER_INPUT gate on it');
    await createFeatureViaForm(handle, {
      name: 'Phase Rail Hold Journey',
      description:
        'Exercises the rail hold->answer->restore contract against the real bundled server.',
      repoPatterns: [/phase-rail-lab/],
    });
    const feature = await waitForFeatureNamed(handle.page, 'Phase Rail Hold Journey');
    const gatePath = seedVerificationNeedUserInputGate(world, feature.id, 'phase-rail-lab');

    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'phase-rail-seeded' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, feature.id);
    const cockpit = handle.page.getByLabel('Feature Phase Rail Hold Journey');
    const gateDialog = handle.page.getByRole('dialog', { name: 'Verification needs your input' });
    await expect(gateDialog).toBeVisible();
    await gateDialog.getByRole('button', { name: 'Answer later' }).click();
    await expect(gateDialog).toHaveCount(0);

    transcript.section(
      'The current segment turns attention-colored with a dot and a Paused readout',
    );
    const rail = cockpit.locator('.phase-rail');
    await expect(rail).toBeVisible({ timeout: 30_000 });
    const dot = rail.locator('.phase-rail__dot');
    await expect(dot).toBeVisible({ timeout: 30_000 });
    await expect(dot).toHaveAttribute('title', /^Held( <1m|\d+[mhd])? for your answer$/);
    await expect(rail.locator('.phase-rail__segment[data-held="true"]')).toHaveCount(1);
    const trioLabels = rail.locator('.phase-rail__trio-entry dt');
    await expect(trioLabels.first()).toHaveText('Paused');
    await expect(rail.locator('.phase-rail__trio-entry[data-attention="true"]')).toHaveCount(1);
    await evidenceShot(handle, 'phase-rail-held-paused');

    transcript.section('Retrying verification closes the hold and restores the plain trio');
    await handle.page.getByRole('button', { name: /Attention inbox, \d+ pending/ }).click();
    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).toBeVisible();
    await inbox.getByRole('button', { name: /Input gate/ }).click();
    await expect(gateDialog).toBeVisible();
    await gateDialog
      .getByRole('radio', { name: /I've granted access — retry verification/ })
      .click();
    await gateDialog.getByRole('button', { name: 'Retry verification' }).click();
    await waitForAttentionMissing(handle.page, feature.id, 'gate');
    await waitForProviderLog(world, 'session');

    await expect(dot).toHaveCount(0);
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
    await evidenceShot(handle, 'phase-rail-restored-after-retry');
    transcript.step(
      'rail hold opened with the dot/tooltip and Paused readout, then resolved after retrying verification',
    );

    persistAppLogs(handle, 'phase-rail-app-server');
    transcript.codeBlock('seeded need-user-input gate', fs.readFileSync(gatePath, 'utf8'));
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
      const features = await page.evaluate(() => window.agentico.listFeatures());
      found = features.find((feature) => feature.name === name);
      return found !== undefined;
    },
    `feature named ${name}`,
    30_000,
  );
  return found!;
}

async function waitForAttentionGate(page: Page, featureId: string): Promise<void> {
  await waitForAttention(
    page,
    (items) => items.some((item) => item.kind === 'gate' && item.featureId === featureId),
    60_000,
  );
}

async function waitForAttentionMissing(
  page: Page,
  featureId: string,
  kind: AttentionItems[number]['kind'],
): Promise<void> {
  await waitForAttention(
    page,
    (items) =>
      !items.some(
        (item) => item.kind === kind && item.kind !== 'recovery' && item.featureId === featureId,
      ),
    30_000,
  );
}

async function waitForAttention(
  page: Page,
  predicate: (items: AttentionItems) => boolean,
  timeoutMs: number,
): Promise<void> {
  await waitFor(
    async () => {
      try {
        const snapshot = await page.evaluate(() => window.agentico.getAttention());
        return predicate(snapshot.items);
      } catch (error) {
        if (error instanceof Error && error.message.includes('E_NOT_CONNECTED')) return false;
        throw error;
      }
    },
    'matching attention snapshot',
    timeoutMs,
  );
}

async function waitForProviderLog(world: JourneyWorld, needle: string): Promise<void> {
  await waitFor(
    () =>
      fs.existsSync(world.providerInvocationLog) &&
      fs.readFileSync(world.providerInvocationLog, 'utf8').includes(needle),
    `provider log entry ${needle}`,
    30_000,
  );
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
