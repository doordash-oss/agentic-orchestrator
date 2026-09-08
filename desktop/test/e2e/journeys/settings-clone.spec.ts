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
 * empty remote clones successfully but stays not feature-ready → the
 * unborn success offers Create initial commit / Not now → declining keeps
 * the catalog row's later entry point → explicit initialization creates
 * one empty Agentico commit, preserving the branch and origin without
 * pushing → readiness flips to feature-ready.
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
import { execFile, execFileSync, spawnSync } from 'node:child_process';
import { createWorld, destroyWorld, waitFor } from '../helpers/world';
import type { Page } from '@playwright/test';

const RUN_NAME = `settings-clone-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

function git(dir: string, ...args: string[]): void {
  execFileSync('git', ['-C', dir, ...args], { stdio: 'pipe' });
}

/** Runs git tolerantly: an unborn HEAD (`rev-parse --verify --quiet`) exits 1. */
function gitText(dir: string, ...args: string[]): string {
  const result = spawnSync('git', ['-C', dir, ...args], { encoding: 'utf8' });
  if (result.status === 1 && result.stdout.trim() === '') return '';
  if (result.status !== 0) {
    throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`);
  }
  return result.stdout.trim();
}

/** Probes the in-process HTTP remote without blocking its Node event loop. */
function remoteRefs(url: string): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile('git', ['ls-remote', url], { encoding: 'utf8' }, (error, stdout) => {
      if (error !== null) {
        reject(error);
        return;
      }
      resolve(stdout.trim());
    });
  });
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
  // The pane hosts both the clone and create preparation forms; scope to
  // the clone section so the shared labels stay unambiguous.
  const cloneForm = settings.getByRole('region', { name: 'Clone repositories' });
  // Wait for the readiness-driven root selector before submitting: the
  // clone form is inert until clone-eligible roots load.
  await expect(cloneForm.getByLabel('Destination root')).toHaveValue(workspaceRoot, {
    timeout: 30_000,
  });
  await cloneForm.getByLabel('Repository URL').fill(remoteUrl);
  await cloneForm.getByLabel('Folder name').fill(destination);
  await expect(cloneForm.getByRole('button', { name: 'Clone repository' })).toBeEnabled();
  await cloneForm.getByRole('button', { name: 'Clone repository' }).click();
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
      await waitFor(
        () => fs.existsSync(path.join(destination, '.git', 'HEAD')),
        'published clone filesystem state',
        30_000,
      );
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

      transcript.section('The unborn success offers both actions; Not now mutates nothing');
      const offer = emptyOp.first().locator('[data-initialize-offer]');
      await expect(offer).toBeVisible();
      await expect(offer).toContainText(/one empty local commit/i);
      const unbornDir = path.join(world.workspaceRoot, 'empty-clone');
      const branchBefore = execFileSync(
        'git',
        ['-C', unbornDir, 'symbolic-ref', '--short', 'HEAD'],
        { encoding: 'utf8' },
      ).trim();
      await offer.getByRole('button', { name: 'Not now' }).click();
      await expect(emptyOp.first().locator('[data-initialize-offer]')).toHaveCount(0);
      expect(gitText(unbornDir, 'rev-parse', '--verify', '--quiet', 'HEAD')).toBe('');
      transcript.step('Not now hid the offer without creating a commit');

      transcript.section('The catalog row keeps the later entry point and initializes on demand');
      const repositoriesRegion = reopened.getByRole('region', { name: 'Repositories' });
      const emptyRow = repositoriesRegion.locator('li[data-repo-key="empty-clone"]');
      await expect(emptyRow).toBeVisible({ timeout: 30_000 });
      await expect(emptyRow).toContainText(/No commits yet/);
      await emptyRow.getByRole('button', { name: /Create initial commit…/ }).click();
      const rowOffer = emptyRow.locator('[data-initialize-offer]');
      await expect(rowOffer).toBeVisible();
      await expect(rowOffer).toContainText(/one empty local commit/i);
      await rowOffer.getByRole('button', { name: 'Create initial commit', exact: true }).click();
      await expect(
        repositoriesRegion.getByText(/Initialized empty-clone at/, { exact: false }),
      ).toBeVisible({ timeout: 30_000 });

      transcript.section(
        'Real git evidence: one Agentico commit, preserved branch and origin, no push',
      );
      expect(
        execFileSync('git', ['-C', unbornDir, 'rev-list', '--count', 'HEAD'], {
          encoding: 'utf8',
        }).trim(),
      ).toBe('1');
      const identityFormat = '%an <%ae> | %cn <%ce> | %s';
      expect(
        execFileSync('git', ['-C', unbornDir, 'log', '-1', `--format=${identityFormat}`], {
          encoding: 'utf8',
        }).trim(),
      ).toBe('Agentico <agentico@localhost> | Agentico <agentico@localhost> | Initial commit');
      const branchAfter = execFileSync(
        'git',
        ['-C', unbornDir, 'symbolic-ref', '--short', 'HEAD'],
        { encoding: 'utf8' },
      ).trim();
      expect(branchAfter).toBe(branchBefore);
      const emptyOriginUrl = execFileSync('git', ['-C', unbornDir, 'remote', 'get-url', 'origin'], {
        encoding: 'utf8',
      }).trim();
      expect(emptyOriginUrl).toBe(empty.url);
      // Nothing was pushed: the served remote still advertises no refs.
      const advertised = await remoteRefs(empty.url);
      expect(advertised).toBe('');
      transcript.step(
        'explicit initialization created exactly one empty Agentico commit, preserved the branch and origin, and pushed nothing',
      );

      transcript.section('Readiness flips to feature-ready under the same catalog key');
      const readinessAfterInitialize = await reopened.evaluate(() =>
        window.agentico.getReadiness(),
      );
      const initializedRepo = readinessAfterInitialize.repositories.find(
        (repo) => repo.name === 'empty-clone',
      );
      expect(initializedRepo).toBeDefined();
      expect(initializedRepo?.valid).toBe(true);
      expect(initializedRepo?.featureReady).toBe(true);
      expect(initializedRepo?.identity).toEqual(emptyRepo?.identity);
      transcript.step('the initialized repository is feature-ready under its current key');
      await evidenceShot(handle, 'settings-clone-initialized', reopened);

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
