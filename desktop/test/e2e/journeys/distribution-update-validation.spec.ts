import { expect, test, type TestInfo } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, type JourneyWorld } from '../helpers/world';
import { sha256, writeSignedUpdateFixture } from '../helpers/update-fixtures';

test('update validation rejects malformed metadata, signature tamper, downgrades, and prereleases', async ({}, testInfo) => {
  test.setTimeout(180_000);
  const transcript = new Transcript(
    'distribution-update-validation',
    'Packaged update metadata validation',
  );

  await runValidationCase(testInfo, transcript, 'malformed schema', (world) =>
    writeSignedUpdateFixture(world.root, { malformedFeed: true }),
  );
  await runValidationCase(testInfo, transcript, 'altered signed checksum metadata', (world) =>
    writeSignedUpdateFixture(world.root, {
      checksumText: `${'0'.repeat(64)}  Agentico-mac-universal.dmg\n`,
      signatureText: `${sha256(Buffer.from('package bytes'))}  Agentico-mac-universal.dmg\n`,
      packageText: 'package bytes',
    }),
  );
  await runValidationCase(testInfo, transcript, 'downgrade with prerelease ignored', (world) =>
    writeSignedUpdateFixture(world.root, {
      tag: 'v0.0.9',
      includePrerelease: true,
      packageText: 'old package bytes',
    }),
  );
  await runValidationCase(testInfo, transcript, 'prerelease-only feed', (world) =>
    writeSignedUpdateFixture(world.root, {
      onlyPrerelease: true,
      packageText: 'prerelease package bytes',
    }),
  );
  transcript.write(testInfo);
});

async function runValidationCase(
  testInfo: TestInfo,
  transcript: Transcript,
  label: string,
  fixture: (world: JourneyWorld) => string,
): Promise<void> {
  const world = createWorld(`update-validation-${label.replaceAll(/\W+/g, '-')}`, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const fixturePath = fixture(world);
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, {
      traceName: `distribution-update-validation-${label.replaceAll(/\W+/g, '-')}`,
      env: { AGENTICO_UPDATE_FIXTURE: fixturePath },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const state = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(state.status).toBe('failed');
    expect(state.targetVersion).not.toBe('0.2.0');
    transcript.json(label, state);
    persistAppLogs(handle, `distribution-update-validation-${label.replaceAll(/\W+/g, '-')}`);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
}
