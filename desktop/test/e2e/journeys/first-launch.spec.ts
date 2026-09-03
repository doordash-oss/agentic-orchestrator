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
 * Journey 1 — first launch, full creation flow against the packaged
 * app and the real bundled server:
 *
 * clean state → connection shell (app-owned launch) → wizard blocks creation
 * (provider unauthenticated) → external remediation (stub flips to
 * authenticated) → Check again → Home → creation's Where step adopts a
 * non-repo folder via the mocked native picker → initialization consent →
 * repo discovered and selected → create feature → ordered setup tasks →
 * Ready to start with nothing started.
 *
 * Native-picker mocking: dialog.showOpenDialog is replaced inside the
 * running main process via Playwright's ElectronApplication.evaluate — the
 * packaged binary is untouched and no test-only dialog code ships.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  evidenceShotBothThemes,
  launchApp,
  mockDirectoryPicker,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { tailText } from '../helpers/runtime';
import {
  createPlainFolder,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  setStubAuthenticated,
  waitFor,
} from '../helpers/world';

/**
 * Platform- (and install-variant-) qualified evidence name: macOS runs the
 * unpacked DMG payload; Linux runs cover the unpacked dir plus the AppImage
 * and installed deb via AGENTICO_E2E_VARIANT (see test/e2e/linux/).
 */
const RUN_NAME = `first-launch-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

test(
  'first launch: provider-gated creation reaches Ready to start',
  { tag: '@smoke' },
  async ({}, testInfo) => {
    const transcript = new Transcript(
      RUN_NAME,
      'Journey 1 — first launch creation (packaged app, real bundled server)',
    );
    const world = createWorld('first-launch', { auth: { loggedIn: false }, authDelaySeconds: 6 });
    const demoApp = createPlainFolder(world, 'demo-app');
    transcript.section('World');
    transcript.step(`isolated world at \`${world.root}\` (HOME, userData, runtime dir, workspace)`);
    transcript.step(
      'runtime config redirects providers.claude.cli to a stub reporting installed but NOT authenticated; codex/opencode point at nonexistent paths',
    );
    transcript.codeBlock('config.yaml', fs.readFileSync(world.configPath, 'utf8'));

    let handle: AppHandle | null = null;
    try {
      transcript.section('Launch: connection shell, app-owned server');
      handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
      transcript.step(
        'launched the verified unpacked packaged app via Playwright _electron.launch',
      );

      // The stub delays its first auth probe, holding the gateway in
      // wait-health long enough to capture the connection shell.
      const shell = handle.page.getByLabel('Agentico connection');
      await expect(shell).toBeVisible();
      await expect(
        handle.page.locator('.shell-card__status-label[data-status="waiting-health"]'),
      ).toBeVisible({ timeout: 30_000 });
      await evidenceShotBothThemes(handle, 'connection-shell');
      transcript.step(
        'connection shell visible in waiting-health stage (app-owned launch); captured light+dark',
      );
      fs.rmSync(world.authDelayPath, { force: true }); // later probes answer instantly

      transcript.section('Wizard blocks creation while the provider is unauthenticated');
      await expect(handle.page.getByRole('heading', { name: 'Set up Agentico' })).toBeVisible({
        timeout: 60_000,
      });
      const claudeRow = handle.page.locator('.provider-row', { hasText: 'claude' });
      await expect(claudeRow).toContainText('Needs attention');
      await expect(claudeRow).toContainText('not signed in');
      await expect(claudeRow).toContainText("Run 'claude auth login' or set ANTHROPIC_API_KEY");
      await evidenceShotBothThemes(handle, 'wizard-providers');

      const readiness = await handle.page.evaluate(() => window.agentico.getReadiness());
      expect(readiness.ready).toBe(false);
      transcript.json('GET /api/v1/readiness (via IPC) while unauthenticated', readiness);

      // The server itself refuses creation while not ready — not just the UI.
      const rejection = await handle.page.evaluate(() =>
        window.agentico
          .createFeature({
            name: 'premature',
            description: '',
            repoKeys: ['none'],
            useCurrentBranch: false,
          })
          .then(() => 'created')
          .catch((err: unknown) => String(err)),
      );
      expect(rejection).toContain('not_ready');
      transcript.step(`feature creation rejected while not ready: \`${rejection}\``);

      // Narrow/wide layout evidence with a real keyboard focus ring.
      await setWindowSize(handle, 500, 760);
      await focusButtonByKeyboard(handle, 'Check again');
      await evidenceShot(handle, 'first-launch-narrow');
      await setWindowSize(handle, 1400, 900);
      await evidenceShot(handle, 'first-launch-wide');
      await setWindowSize(handle, 1080, 720);
      transcript.step(
        'captured narrow (500px, keyboard focus ring on Check again) and wide (1400px) wizard layouts',
      );

      transcript.section('External remediation, then Check again');
      setStubAuthenticated(world, true);
      transcript.step(
        'flipped the stub auth-state file to authenticated (stands in for the user completing `claude auth login` in their own terminal)',
      );
      const app = handle;
      await app.page.getByRole('button', { name: /Check again/ }).click();
      await expect(app.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 30_000,
      });
      transcript.step(
        'the only readiness gates are provider and model availability: one refresh satisfied both and setup handed straight to Home — no workspace or repository step in between',
      );

      transcript.section('Where: adopt a folder, consent to initialization, select the repository');
      await mockDirectoryPicker(app, demoApp);
      const consent = app.page.getByRole('dialog', { name: 'Initialize a new repository?' });

      transcript.section('Create the feature (name/description/repo/branch)');
      transcript.section('Ordered setup tasks → Ready to start');
      const cockpit = await createFeatureViaForm(app, {
        name: 'Tracer Bullet',
        description: 'First packaged end-to-end feature creation.',
        repoPatterns: [/demo-app/],
        waitForReady: true,
        beforeRepoSelect: async () => {
          // A clean install has no repositories, so Where leads with the picker.
          await expect(
            app.page.getByRole('heading', { name: 'Add your first repository' }),
          ).toBeVisible();
          await app.page.getByRole('button', { name: 'Browse for folder' }).click();
          await app.page.getByRole('button', { name: 'Use this folder' }).click();
          await expect(app.page.getByText(/holds no git repository yet/i)).toBeVisible();
          await app.page.getByRole('button', { name: /Initialize it as a repository/ }).click();
          await expect(consent).toBeVisible();
          await expect(consent).toContainText(demoApp);
          await evidenceShot(app, 'repo-init-consent-light');
          // The modal scrim blocks the theme switcher, so cancel (the folder
          // choice is kept), flip the theme, and reopen the same consent dialog.
          await consent.getByRole('button', { name: 'Cancel' }).click();
          await setTheme(app, 'dark');
          await app.page.getByRole('button', { name: /Initialize it as a repository/ }).click();
          await expect(consent).toBeVisible();
          await evidenceShot(app, 'repo-init-consent-dark');
          await consent.getByRole('button', { name: 'Initialize repository' }).click();
          // One unambiguous discovery selects itself, so Where is already valid.
          await expect(app.page.getByRole('checkbox', { name: /demo-app/ })).toBeChecked({
            timeout: 30_000,
          });
          await setTheme(app, 'light');
          transcript.step(
            'consented; the server initialized the repository (git init + initial empty commit), rediscovered the workspace, and the single discovery selected itself',
          );
        },
      });
      await expect(cockpit.getByText('Ready to start')).toBeVisible();
      await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeVisible();
      await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled();
      await expect(cockpit.getByText("Starting isn't available in this version yet.")).toHaveCount(
        0,
      );
      await evidenceShotBothThemes(handle, 'ready-to-start');

      const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
      expect(features).toHaveLength(1);
      const feature = await handle.page.evaluate(
        (id) => window.agentico.getFeature(id),
        features[0]!.id,
      );
      transcript.json('authoritative feature snapshot (via IPC)', feature);
      expect(feature.setup?.status).toBe('done');
      const startAction = feature.actions.find((action) => action.id === 'start');
      expect(startAction?.enabled).toBe(true);

      transcript.section('Nothing started: no orchestration, no sessions');
      // The action catalogue authorizes start but nothing invoked it; the
      // state dir must contain no session or run-log material for the feature.
      expect(['created', 'ready']).toContain(feature.status.toLowerCase());
      const sessionEntries = findEntries(world.stateDir, /session/i);
      expect(sessionEntries).toEqual([]);
      transcript.step(
        `feature status \`${feature.status}\`, start enabled but never invoked; no session files under the state dir`,
      );

      transcript.section('Ownership and graceful shutdown (app-owned)');
      const discovery = readDiscovery(world);
      expect(discovery).not.toBeNull();
      const serverPid = discovery!.pid;
      const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
      expect(connection.status).toBe('ready');
      expect(connection.ownership).toBe('app-owned');
      transcript.json('connection state (via IPC)', connection);
      transcript.json('discovery record (auth_token redacted)', {
        ...discovery,
        auth_token: '[redacted]',
      });

      const logText = persistAppLogs(handle, 'first-launch-app');
      if (discovery!.auth_token !== undefined && discovery!.auth_token !== '') {
        expect(logText).not.toContain(discovery!.auth_token);
      }
      const logExcerpt = nonemptyLogExcerpt(logText, 30);
      transcript.codeBlock('app/server log excerpt (redacted at source)', logExcerpt);

      await closeApp(handle);
      await waitFor(
        () => !processAlive(serverPid),
        `app-owned server ${serverPid} to be reaped`,
        15_000,
      );
      transcript.step(
        `app quit gracefully; app-owned server pid ${serverPid} received SIGTERM and was reaped (process no longer alive)`,
      );
      assertNoLeakedProcesses(world);
      transcript.step('no processes referencing the journey world remain');
      transcript.write(testInfo);

      // Ownership evidence doc: contribute the app-owned shutdown proof
      // (journeys 3 and 4 contribute the external-server sections).
      const ownership = new Transcript(
        'ownership-compatibility',
        'App-owned graceful shutdown (journey 1 teardown)',
        { append: true },
      );
      ownership.step(
        `app-owned server pid ${serverPid} (from the discovery record) was alive while connected ` +
          'and no longer exists after app quit — the bounded SIGTERM→reap path ran',
      );
      ownership.step(`ownership reported over IPC while connected: \`${connection.ownership}\``);
      ownership.codeBlock(
        'app/server log tail at shutdown (redacted at source)',
        nonemptyLogExcerpt(logText, 15),
      );
      ownership.write(testInfo);
    } finally {
      if (handle !== null) {
        await closeApp(handle).catch(() => {});
      }
      assertNoLeakedProcesses(world);
      destroyWorld(world);
    }
  },
);

function nonemptyLogExcerpt(logText: string, lines: number): string {
  const excerpt = tailText(logText, lines).trim();
  return excerpt === ''
    ? '[no app/server process output was emitted; PID lifecycle assertions verified shutdown]'
    : excerpt;
}

/** Tabs from the document body until the named button owns focus. */
async function focusButtonByKeyboard(handle: AppHandle, label: string): Promise<void> {
  await handle.page.locator('body').click({ position: { x: 4, y: 4 } });
  for (let i = 0; i < 40; i += 1) {
    await handle.page.keyboard.press('Tab');
    const focused = await handle.page.evaluate(() =>
      document.activeElement instanceof HTMLElement ? document.activeElement.textContent : null,
    );
    if (focused !== null && focused.trim() === label) {
      return;
    }
  }
  throw new Error(`could not reach the "${label}" button with Tab`);
}

/** Recursive listing of entries whose name matches the pattern. */
function findEntries(root: string, pattern: RegExp): string[] {
  const matches: string[] = [];
  const walk = (dir: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (pattern.test(entry.name)) {
        matches.push(full);
      }
      if (entry.isDirectory()) {
        walk(full);
      }
    }
  };
  walk(root);
  return matches;
}
