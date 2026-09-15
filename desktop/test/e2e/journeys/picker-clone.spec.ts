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
 * the unborn success offers Create initial commit / Not now → explicit
 * initialization adopts the same identity into the still-open draft with
 * one empty Agentico commit (branch and origin preserved, nothing pushed)
 * → declining keeps the row's later opt-in, which adopts after the view
 * closes → Escape closes only the clone view → reduced motion is honored.
 */
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { execFile, execFileSync, spawnSync } from 'node:child_process';
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
  transferGate: Promise<void> = Promise.resolve(),
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
    void transferGate.then(() => res.end(fs.readFileSync(target)));
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
  let releaseTransfer = () => {};
  const transferGate = new Promise<void>((resolve) => {
    releaseTransfer = resolve;
  });
  const populated = await serveGitRemote(world.root, 'populated', 2, transferGate);
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

    // Hold the remote response until the active progress presentation is verified.
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    await cloneFromPicker(page, populated.url, 'widget', world.workspaceRoot);
    const progress = cloneDialogReduced.getByRole('progressbar', {
      name: 'Clone progress for widget',
    });
    await expect(progress).toBeVisible();
    await expect(progress).not.toHaveAttribute('aria-valuenow');
    await expect(progress.locator('span')).toHaveCSS('animation-name', 'clone-progress');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    await expect(progress.locator('span')).toHaveCSS('animation-name', 'none');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    releaseTransfer();
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
    await expect(progress).toHaveCount(0);
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

    // The clone-success offer initializes the unborn clone and adopts it
    // into exactly the still-open initiating draft.
    const offer = emptyOperation.first().locator('[data-initialize-offer]');
    await expect(offer).toBeVisible();
    await expect(offer).toContainText(/one empty local commit/i);
    const unbornDir = path.join(world.workspaceRoot, 'empty-clone');
    const branchBefore = spawnSync('git', ['-C', unbornDir, 'symbolic-ref', '--short', 'HEAD'], {
      encoding: 'utf8',
    }).stdout.trim();
    await offer.getByRole('button', { name: 'Create initial commit', exact: true }).click();

    await expect(cloneDialogReduced).not.toBeVisible({ timeout: 30_000 });
    const adoptedRow = sheet.locator('.creation-sheet__row', { hasText: 'empty-clone' });
    const adoptedCheckbox = adoptedRow.getByRole('checkbox');
    await expect(adoptedCheckbox).toBeChecked({ timeout: 30_000 });
    await expect
      .poll(async () => page.evaluate(() => document.activeElement?.className ?? ''), {
        timeout: 30_000,
      })
      .toContain('creation-sheet__row-control');
    await expect(sheet.getByText('Initialized empty-clone and selected it.')).toBeVisible();
    // The earlier adoption and every draft value survived.
    await expect(widgetCheckbox).toBeChecked();
    transcript.step('explicit initialization adopted into the initiating draft with row focus');

    // Real git evidence: one empty Agentico commit, preserved branch and
    // origin, nothing pushed.
    expect(
      execFileSync('git', ['-C', unbornDir, 'rev-list', '--count', 'HEAD'], {
        encoding: 'utf8',
      }).trim(),
    ).toBe('1');
    expect(
      execFileSync('git', ['-C', unbornDir, 'log', '-1', '--format=%an <%ae> | %cn <%ce> | %s'], {
        encoding: 'utf8',
      }).trim(),
    ).toBe('Agentico <agentico@localhost> | Agentico <agentico@localhost> | Initial commit');
    const branchAfter = spawnSync('git', ['-C', unbornDir, 'symbolic-ref', '--short', 'HEAD'], {
      encoding: 'utf8',
    }).stdout.trim();
    expect(branchAfter).toBe(branchBefore);
    expect(
      execFileSync('git', ['-C', unbornDir, 'remote', 'get-url', 'origin'], {
        encoding: 'utf8',
      }).trim(),
    ).toBe(empty.url);
    expect(await remoteRefs(empty.url)).toBe('');
    const readinessAfterInitialize = await page.evaluate(() => window.agentico.getReadiness());
    const initializedRepo = readinessAfterInitialize.repositories.find(
      (repo) => repo.name === 'empty-clone',
    );
    expect(initializedRepo?.valid).toBe(true);
    expect(initializedRepo?.featureReady).toBe(true);
    expect(initializedRepo?.identity).toEqual(emptyRepo?.identity);
    transcript.step(
      'initialization created one empty Agentico commit, preserved branch and origin, pushed nothing',
    );

    // Later opt-in after declining: another unborn clone, decline the
    // offer, close the view, then initialize from the row action.
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    await cloneFromPicker(page, empty.url, 'declined-clone', world.workspaceRoot);
    const declinedOperation = page.locator('.creation-clone__operation', {
      hasText: 'declined-clone',
    });
    await expect(declinedOperation.first()).toContainText('Succeeded', { timeout: 120_000 });
    const declinedOffer = declinedOperation.first().locator('[data-initialize-offer]');
    await expect(declinedOffer).toBeVisible();
    await declinedOffer.getByRole('button', { name: 'Not now' }).click();
    await expect(declinedOperation.first().locator('[data-initialize-offer]')).toHaveCount(0);
    await page.getByRole('button', { name: 'Close' }).click();
    await expect(cloneDialogReduced).not.toBeVisible();
    const declinedRow = sheet.locator('.creation-sheet__row-item', {
      hasText: 'declined-clone',
    });
    await expect(declinedRow).toBeVisible({ timeout: 30_000 });
    await expect(declinedRow.getByRole('checkbox')).toBeDisabled();
    await declinedRow.getByRole('button', { name: /Create initial commit…/ }).click();
    const rowOffer = declinedRow.locator('[data-initialize-offer]');
    await expect(rowOffer).toBeVisible();
    await expect(rowOffer).toContainText(/one empty local commit/i);
    await rowOffer.getByRole('button', { name: 'Create initial commit', exact: true }).click();
    const declinedCheckbox = declinedRow.getByRole('checkbox');
    await expect(declinedCheckbox).toBeChecked({ timeout: 30_000 });
    await expect
      .poll(async () => page.evaluate(() => document.activeElement?.className ?? ''), {
        timeout: 30_000,
      })
      .toContain('creation-sheet__row-control');
    await expect(sheet.getByText('Initialized declined-clone and selected it.')).toBeVisible();
    transcript.step('later opt-in after declining adopted without cloning again');

    await closeApp(handle);
    handle = null;
  } finally {
    releaseTransfer();
    if (handle !== null) {
      await closeApp(handle).catch(() => undefined);
    }
    populated.close();
    empty.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
