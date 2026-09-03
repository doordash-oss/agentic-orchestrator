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
import { writeSignedUpdateFixture } from '../helpers/update-fixtures';

test(
  'update validation rejects malformed metadata, signature tamper, downgrades, and prereleases',
  { tag: '@smoke' },
  async ({}, testInfo) => {
    test.setTimeout(180_000);
    const transcript = new Transcript(
      'distribution-update-validation',
      'Packaged update metadata validation',
    );

    await runValidationCase(
      testInfo,
      transcript,
      'malformed schema',
      () => 'E_UPDATE_CHECK_FAILED',
      (world) => writeSignedUpdateFixture(world.root, { malformedFeed: true }),
    );
    await runValidationCase(
      testInfo,
      transcript,
      'altered signed release envelope',
      () => 'E_UPDATE_SIGNATURE_FAILED',
      (world) =>
        writeSignedUpdateFixture(world.root, {
          servedEnvelopeBytes: Buffer.from('{"tampered":true}\n'),
          packageText: 'package bytes',
        }),
    );
    await runValidationCase(
      testInfo,
      transcript,
      'downgrade with prerelease ignored',
      () => 'E_UPDATE_CHECK_FAILED',
      (world) =>
        writeSignedUpdateFixture(world.root, {
          tag: 'v0.0.9',
          includePrerelease: true,
          packageText: 'old package bytes',
        }),
    );
    await runValidationCase(
      testInfo,
      transcript,
      'prerelease-only feed',
      () => 'E_UPDATE_CHECK_FAILED',
      (world) =>
        writeSignedUpdateFixture(world.root, {
          onlyPrerelease: true,
          packageText: 'prerelease package bytes',
        }),
    );
    transcript.write(testInfo);
  },
);

async function runValidationCase(
  testInfo: TestInfo,
  transcript: Transcript,
  label: string,
  expectedCode: () => string,
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
    // The failed state carries the canonical catalog error for its stage.
    expect(state.error?.code).toBe(expectedCode());
    expect(state.error?.class).toBe('blocking');
    expect(state.error?.title.length ?? 0).toBeGreaterThan(0);
    transcript.json(label, state);
    persistAppLogs(handle, `distribution-update-validation-${label.replaceAll(/\W+/g, '-')}`);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
}
