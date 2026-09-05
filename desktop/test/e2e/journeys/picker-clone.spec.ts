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
 * Journey — picker clone lifecycle against the packaged app and the real
 * bundled server:
 *
 * creation sheet → nested clone view (shared form) → clone from a
 * controlled local dumb-HTTP git remote → authoritative Succeeded with a
 * durable publication identity → the published repository is adopted into
 * exactly the initiating draft (selected once, focus restored, filter
 * cleared, draft values kept) → readiness/catalog carry the real identity
 * → an empty remote publishes but stays unborn (visible, unselected) →
 * Escape closes only the clone view → reduced motion is honored.
 */
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { expect, test, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createWorld, destroyWorld } from '../helpers/world';

const RUN_NAME = `picker-clone-${
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

async function cloneFromPicker(
  page: Page,
  remoteUrl: string,
  destination: string,
  workspaceRoot: string,
): Promise<void> {
  const dialog = page.getByRole('dialog', { name: 'Clone a repository' });
  await expect(dialog).toBeVisible();
  // The readiness-driven root selector loads before the form is usable.
  await expect(dialog.getByLabel('Destination root')).toHaveValue(workspaceRoot, {
    timeout: 30_000,
  });
  await dialog.getByLabel('Repository URL').fill(remoteUrl);
  await dialog.getByLabel('Folder name').fill(destination);
  await expect(dialog.getByRole('button', { name: 'Clone repository' })).toBeEnabled();
  await dialog.getByRole('button', { name: 'Clone repository' }).click();
}

test('picker clone: real remote, identity-safe adoption, unborn result', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    RUN_NAME,
    'Picker clone lifecycle (packaged app, real bundled server, controlled git remote)',
  );
  const world = createWorld('picker-clone', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const populated = await serveGitRemote(world.root, 'populated', 2);
  const empty = await serveGitRemote(world.root, 'empty', 0);
  let handle: AppHandle | null = null;

  try {
    transcript.section('Launch and open the creation sheet');
    handle = await launchApp(world, testInfo, { traceName: 'picker-clone' });
    const page = handle.page;
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    // Reduced motion is honored by the nested clone view.
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await page.getByRole('button', { name: 'New feature' }).click();
    const sheet = page.getByRole('dialog', { name: 'New feature' });
    await expect(sheet).toBeVisible();
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    const cloneDialogReduced = page.getByRole('dialog', { name: 'Clone a repository' });
    await expect(cloneDialogReduced).toBeVisible();
    const animation = await cloneDialogReduced.evaluate(
      (node) => getComputedStyle(node).animationName,
    );
    expect(animation).toBe('none');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    transcript.step('clone view renders without motion under prefers-reduced-motion');

    // Escape closes only the clone view; the draft stays open.
    await page.keyboard.press('Escape');
    await expect(cloneDialogReduced).not.toBeVisible();
    await expect(sheet).toBeVisible();
    transcript.step('Escape closed the clone view without discarding the draft');

    // Clone a usable repository from the controlled remote. A local clone
    // can complete before the operation view ever renders, so the
    // authoritative outcome is the adoption itself: the view closes and the
    // published repository is selected in the initiating draft.
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    await cloneFromPicker(page, populated.url, 'widget', world.workspaceRoot);
    try {
      await expect(cloneDialogReduced).not.toBeVisible({ timeout: 120_000 });
    } catch (cloneFailure) {
      const failing = await page.evaluate(() =>
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
    transcript.step('usable clone completed and the nested view closed through adoption');
    const widgetRow = sheet.locator('.creation-sheet__row', { hasText: 'widget' });
    await expect(widgetRow).toBeVisible();
    const widgetCheckbox = widgetRow.getByRole('checkbox');
    await expect(widgetCheckbox).toBeChecked({ timeout: 30_000 });
    await expect
      .poll(async () => page.evaluate(() => document.activeElement?.className ?? ''), {
        timeout: 30_000,
      })
      .toContain('creation-sheet__row-control');
    expect(await widgetCheckbox.evaluate((node) => node === document.activeElement)).toBe(true);
    await expect(sheet.getByText('Cloned widget and selected it.')).toBeVisible();
    transcript.step('usable result adopted into its initiating draft with row focus');

    // Authoritative identity: the durable publication identity matches the
    // live catalog and the real filesystem.
    const readiness = await page.evaluate(() => window.agentico.getReadiness());
    const widget = readiness.repositories.find((repo) => repo.name === 'widget');
    expect(widget).toBeDefined();
    expect(widget?.valid).toBe(true);
    expect(widget?.featureReady).toBe(true);
    expect(widget?.identity).toBeDefined();
    const operations = await page.evaluate(() => window.agentico.listCloneOperations());
    const published = operations.operations.find((op) => op.destination === 'widget')?.published;
    expect(published).toBeDefined();
    expect(published?.identity).toBeDefined();
    expect(published?.identity).toEqual(widget?.identity);
    const destination = path.join(world.workspaceRoot, 'widget');
    expect(fs.existsSync(path.join(destination, '.git', 'HEAD'))).toBe(true);
    const logCount = execFileSync('git', ['-C', destination, 'rev-list', '--count', 'HEAD'], {
      encoding: 'utf8',
    }).trim();
    expect(logCount).toBe('2');
    transcript.step('publication identity matches the real catalog and HEAD');

    // Draft values survive the adoption: navigate on and come back.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await sheet.getByLabel('Name').fill('Picker clone feature');
    await sheet.getByRole('button', { name: 'Back' }).click();
    await expect(widgetCheckbox).toBeChecked();
    await expect(sheet.getByLabel('Name')).not.toBeVisible();
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await expect(sheet.getByLabel('Name')).toHaveValue('Picker clone feature');
    transcript.step('adoption preserved every other draft value');

    // An empty remote publishes but stays unborn: visible, never selected.
    await sheet.getByRole('button', { name: 'Back' }).click();
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    await cloneFromPicker(page, empty.url, 'empty-clone', world.workspaceRoot);
    const emptyOperation = page.locator('.creation-clone__operation', {
      hasText: 'empty-clone',
    });
    await expect(emptyOperation.first()).toBeVisible({ timeout: 30_000 });
    await expect(emptyOperation.first()).toContainText('Succeeded', { timeout: 120_000 });
    await expect(emptyOperation.first()).toContainText(/no commits yet/);
    await expect(cloneDialogReduced).toBeVisible();
    const readinessAfterEmpty = await page.evaluate(() => window.agentico.getReadiness());
    const emptyRepo = readinessAfterEmpty.repositories.find((repo) => repo.name === 'empty-clone');
    expect(emptyRepo?.valid).toBe(true);
    expect(emptyRepo?.featureReady).toBe(false);
    const emptyRow = sheet.locator('.creation-sheet__row', { hasText: 'empty-clone' });
    await expect(emptyRow).toBeVisible({ timeout: 30_000 });
    await expect(emptyRow.getByRole('checkbox')).toBeDisabled();
    await expect(emptyRow.getByRole('checkbox')).not.toBeChecked();
    transcript.step('unborn result stays visible with guidance and is never selected');
    await evidenceShot(handle, 'picker-clone-unborn', page);

    await closeApp(handle);
    handle = null;
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => undefined);
    }
    populated.close();
    empty.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
