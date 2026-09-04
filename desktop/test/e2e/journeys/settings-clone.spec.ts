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
 * Journey — Settings clone lifecycle against the packaged app and the real
 * bundled server:
 *
 * preset workspace root → Settings workspace-roots pane → clone from a
 * controlled local dumb-HTTP git remote → authoritative operation snapshot
 * reaches Succeeded → real filesystem/origin/HEAD/publication evidence →
 * closing Settings and reopening rediscovers the operation (no ID known)
 * → readiness reports the published repository as feature-ready → an
 * empty remote clones successfully but stays not feature-ready.
 */
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  openSettings,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { execFileSync } from 'node:child_process';
import { createWorld, destroyWorld, waitFor } from '../helpers/world';
import type { Page } from '@playwright/test';

const RUN_NAME = `settings-clone-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

function git(dir: string, ...args: string[]): void {
  execFileSync('git', ['-C', dir, ...args], { stdio: 'pipe' });
}

/** A real git repository served over controlled local dumb HTTP. */
async function serveGitRemote(
  tmsDir: string,
  name: string,
  commits: number,
): Promise<{ url: string; close: () => void }> {
  const src = path.join(tmsDir, `${name}-src`);
  fs.mkdirSync(src, { recursive: true });
  git(src, 'init', '--initial-branch=main');
  for (let i = 0; i < commits; i += 1) {
    fs.writeFileSync(path.join(src, `file-${i}.txt`), `data ${i}\n`);
    git(src, 'add', '.');
    git(
      src,
      '-c',
      'user.name=e2e',
      '-c',
      'user.email=e2e@example.invalid',
      'commit',
      '-m',
      `commit ${i}`,
    );
  }
  const bare = path.join(tmsDir, `${name}.git`);
  git(src, 'clone', '--bare', src, bare);
  git(bare, 'update-server-info');
  const server = http.createServer((req, res) => {
    // A dumb-HTTP git server serves the repository files; query strings
    // (the smart-protocol probe) resolve to the same file so git falls
    // back to the dumb protocol.
    const rel = decodeURIComponent((req.url ?? '/').split('?')[0] ?? '/').replace(/^\/+/, '');
    const target = path.join(tmsDir, rel);
    if (!target.startsWith(tmsDir) || !fs.existsSync(target) || !fs.statSync(target).isFile()) {
      res.writeHead(404);
      res.end('not found');
      return;
    }
    res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
    res.end(fs.readFileSync(target));
  });
  const listening = new Promise<void>((resolve) => server.once('listening', () => resolve()));
  server.listen(0, '127.0.0.1');
  await listening;
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  if (port === 0) {
    throw new Error('git remote server did not bind a port');
  }
  return { url: `http://127.0.0.1:${port}/${name}.git`, close: () => server.close() };
}

async function cloneFromSettings(
  settings: Page,
  remoteUrl: string,
  destination: string,
  workspaceRoot: string,
): Promise<void> {
  // Wait for the readiness-driven root selector before submitting: the
  // clone form is inert until clone-eligible roots load.
  await expect(settings.getByLabel('Destination root')).toHaveValue(workspaceRoot, {
    timeout: 30_000,
  });
  await settings.getByLabel('Repository URL').fill(remoteUrl);
  await settings.getByLabel('Folder name').fill(destination);
  await expect(settings.getByRole('button', { name: 'Clone repository' })).toBeEnabled();
  await settings.getByRole('button', { name: 'Clone repository' }).click();
}

test(
  'settings clone: real remote, authoritative snapshots, rediscovery, empty remote',
  { tag: '@smoke' },
  async ({}, testInfo) => {
    test.setTimeout(300_000);
    const transcript = new Transcript(
      RUN_NAME,
      'Settings clone lifecycle (packaged app, real bundled server, controlled git remote)',
    );
    const world = createWorld('settings-clone', {
      auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
      presetWorkspaceRoot: true,
    });
    const populated = await serveGitRemote(world.root, 'populated', 2);
    const empty = await serveGitRemote(world.root, 'empty', 0);
    let handle: AppHandle | null = null;

    try {
      transcript.section('Launch and open Settings on the repository pane');
      handle = await launchApp(world, testInfo, { traceName: 'settings-clone' });
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 60_000,
      });
      const settings = await openSettings(handle);
      await expect(settings.locator('section[aria-label="Clone repositories"]')).toBeVisible();

      transcript.section('Clone from the controlled remote through the form');
      await cloneFromSettings(settings, populated.url, 'widget', world.workspaceRoot);
      const operation = settings.locator('.settings-panel__clone-operation', {
        hasText: 'widget',
      });
      await expect(operation.first()).toBeVisible({ timeout: 30_000 });
      try {
        await expect(operation.first()).toContainText('Succeeded', { timeout: 120_000 });
      } catch (cloneFailure) {
        const failing = await settings.evaluate(() =>
          window.agentico.listCloneOperations().then((list) =>
            JSON.stringify(
              list.operations.map((op) => ({
                id: op.id,
                state: op.state,
                destination: op.destination,
                error: op.error ?? null,
                cleanupIssue: op.cleanupIssue ?? null,
              })),
            ),
          ),
        );
        transcript.step(`clone operation diagnostics: ${failing}`);
        throw cloneFailure;
      }
      transcript.step('clone operation reached the authoritative Succeeded snapshot');

      transcript.section('Real filesystem outcomes: history, origin, publication evidence');
      const destination = path.join(world.workspaceRoot, 'widget');
      expect(fs.existsSync(path.join(destination, '.git', 'HEAD'))).toBe(true);
      const logCount = execFileSync('git', ['-C', destination, 'rev-list', '--count', 'HEAD'], {
        encoding: 'utf8',
      }).trim();
      expect(logCount).toBe('2');
      const originUrl = execFileSync('git', ['-C', destination, 'remote', 'get-url', 'origin'], {
        encoding: 'utf8',
      }).trim();
      expect(originUrl).toBe(populated.url);
      expect(fs.existsSync(path.join(destination, '.git', 'agentico-publication.json'))).toBe(true);
      // The hidden staging directory is gone.
      for (const entry of fs.readdirSync(world.workspaceRoot)) {
        expect(entry.startsWith('.agentico-clone-')).toBe(false);
      }
      transcript.step(
        'published repository carries full history, origin, and publication evidence',
      );
      await evidenceShot(handle, 'settings-clone-succeeded', settings);

      transcript.section('Closing Settings and reopening rediscovers the operation');
      await settings.close();
      await waitFor(() => handle!.app.windows().length === 1, 'settings window closed', 30_000);
      const reopened = await openSettings(handle);
      await expect(reopened.locator('section[aria-label="Clone repositories"]')).toBeVisible();
      const rediscovered = reopened.locator('.settings-panel__clone-operation', {
        hasText: 'widget',
      });
      await expect(rediscovered.first()).toBeVisible({ timeout: 30_000 });
      await expect(rediscovered.first()).toContainText('Succeeded');
      transcript.step('reopened Settings rediscovered the succeeded clone without knowing its ID');

      transcript.section('Readiness reports the published repository as feature-ready');
      const readiness = await reopened.evaluate(() => window.agentico.getReadiness());
      const widget = readiness.repositories.find((repo) => repo.name === 'widget');
      expect(widget).toBeDefined();
      expect(widget?.valid).toBe(true);
      expect(widget?.featureReady).toBe(true);
      transcript.step('the published repository is discoverable and feature-ready');

      transcript.section('An empty remote clones successfully but is not feature-ready');
      await cloneFromSettings(reopened, empty.url, 'empty-clone', world.workspaceRoot);
      const emptyOp = reopened.locator('.settings-panel__clone-operation', {
        hasText: 'empty-clone',
      });
      await expect(emptyOp.first()).toBeVisible({ timeout: 30_000 });
      await expect(emptyOp.first()).toContainText('Succeeded', { timeout: 120_000 });
      await expect(emptyOp.first()).toContainText(/no commits yet/);
      const readinessAfterEmpty = await reopened.evaluate(() => window.agentico.getReadiness());
      const emptyRepo = readinessAfterEmpty.repositories.find(
        (repo) => repo.name === 'empty-clone',
      );
      expect(emptyRepo).toBeDefined();
      expect(emptyRepo?.valid).toBe(true);
      expect(emptyRepo?.featureReady).toBe(false);
      transcript.step('empty remote published successfully and stays not feature-ready');
      await evidenceShot(handle, 'settings-clone-empty-remote', reopened);

      await closeApp(handle);
      handle = null;
    } finally {
      populated.close();
      empty.close();
      if (handle !== null) await closeApp(handle).catch(() => {});
      assertNoLeakedProcesses(world);
      destroyWorld(world);
      transcript.write(testInfo);
    }
  },
);
