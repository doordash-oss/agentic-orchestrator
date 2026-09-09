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
 * Journey — repository preparation (Create + root selection) against the
 * packaged app and the real bundled server:
 *
 * (a) picker Create from zero roots: choose a folder, persist it as a root,
 *     create a named child repository with explicit consent, and adopt it
 *     into the same draft (one empty main commit, Agentico identity, no
 *     origin) — exactly the Phase 4 promise, with real git evidence;
 * (b) Settings Create against a chooser-added root, the root persisting
 *     across relaunch, and surviving a controlled clone failure;
 * (c) remote Create/Clone use only the remote's configured roots: no
 *     chooser, no desktop path transmitted, direct root-mutation IPC
 *     refused, and the other server's directories and config untouched;
 * (d) a server switch while root selection is pending is suppressed: the
 *     stale sequence never writes either server's configuration.
 */
import { execFile, execFileSync, spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { expect, test, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  mockDirectoryPicker,
  openSettings,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import {
  createWorld,
  destroyWorld,
  minimalEnv,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_PREFIX = `repository-preparation-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;
const REMOTE_NAME = 'prep-remote';

interface WorldHandle {
  world: JourneyWorld;
  handle: AppHandle | null;
}

function gitText(dir: string, ...args: string[]): string {
  return execFileSync('git', ['-C', dir, ...args], {
    encoding: 'utf8',
    env: { ...minimalEnv(), GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  }).trim();
}

/** Probes the in-process HTTP remote without blocking its Node event loop. */
function remoteRefs(url: string): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(
      'git',
      ['ls-remote', url],
      {
        encoding: 'utf8',
        env: { ...minimalEnv(), GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
      },
      (error, stdout) => {
        if (error !== null) {
          reject(error);
          return;
        }
        resolve(stdout.trim());
      },
    );
  });
}

/** Proves the promised shape of a created repository with real git. */
function assertCreatedRepository(dir: string): void {
  expect(gitText(dir, 'symbolic-ref', '--short', 'HEAD')).toBe('main');
  expect(gitText(dir, 'rev-list', '--count', 'HEAD')).toBe('1');
  expect(gitText(dir, 'log', '-1', '--format=%an <%ae>')).toBe('Agentico <agentico@localhost>');
  expect(gitText(dir, 'log', '-1', '--format=%cn <%ce>')).toBe('Agentico <agentico@localhost>');
  expect(gitText(dir, 'show', '--name-only', '--format=', 'HEAD')).toBe('');
  expect(gitText(dir, 'remote')).toBe('');
  expect(fs.existsSync(path.join(dir, '.git', 'HEAD'))).toBe(true);
}

/** A real git repository served over controlled local dumb HTTP. */
async function serveGitRemote(
  tmsDir: string,
  name: string,
  commits: number,
): Promise<{ url: string; close: () => void }> {
  const src = path.join(tmsDir, `${name}-src`);
  fs.mkdirSync(src, { recursive: true });
  execFileSync('git', ['-C', src, 'init', '--initial-branch=main'], { stdio: 'pipe' });
  for (let i = 0; i < commits; i += 1) {
    fs.writeFileSync(path.join(src, `file-${i}.txt`), `data ${i}\n`);
    execFileSync('git', ['-C', src, 'add', '.'], { stdio: 'pipe' });
    execFileSync(
      'git',
      [
        '-C',
        src,
        '-c',
        'user.name=e2e',
        '-c',
        'user.email=e2e@example.invalid',
        'commit',
        '-m',
        `commit ${i}`,
      ],
      { stdio: 'pipe' },
    );
  }
  const bare = path.join(tmsDir, `${name}.git`);
  execFileSync('git', ['-C', src, 'clone', '--bare', src, bare], { stdio: 'pipe' });
  execFileSync('git', ['-C', bare, 'update-server-info'], { stdio: 'pipe' });
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
  return { url: `http://127.0.0.1:${String(port)}/${name}.git`, close: () => server.close() };
}

/** A delayed native-picker mock, so a test can act while a choice is pending. */
async function mockDelayedDirectoryPicker(
  handle: AppHandle,
  directory: string,
  delayMs: number,
): Promise<void> {
  await handle.app.evaluate(
    ({ dialog }, payload) => {
      dialog.showOpenDialog = (async () => {
        await new Promise((resolve) => setTimeout(resolve, payload.delayMs));
        return { canceled: false, filePaths: [payload.dir], bookmarks: [] };
      }) as typeof dialog.showOpenDialog;
    },
    { dir: directory, delayMs },
  );
}

/**
 * Builds `remote-behind` inside the given workspace root: a repository whose
 * local main is two commits behind a real bare origin, so selecting it must
 * produce a behind origin comparison on whichever server owns that root.
 */
function pairRemoteBehindOrigin(world: JourneyWorld, workspaceRoot: string): void {
  const dir = path.join(workspaceRoot, 'remote-behind');
  fs.mkdirSync(dir, { recursive: true });
  gitText(dir, 'init', '--initial-branch=main');
  gitText(
    dir,
    '-c',
    'user.name=e2e',
    '-c',
    'user.email=e2e@example.invalid',
    'commit',
    '--allow-empty',
    '-m',
    'initial',
  );
  const bare = path.join(world.root, 'remote-behind-origin.git');
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare');
  gitText(dir, 'remote', 'add', 'origin', bare);
  gitText(dir, 'push', '-u', 'origin', 'main');
  const writer = path.join(world.root, 'remote-behind-writer');
  gitText(world.root, 'clone', bare, writer);
  for (const message of ['remote one', 'remote two']) {
    gitText(
      writer,
      '-c',
      'user.name=e2e',
      '-c',
      'user.email=e2e@example.invalid',
      'commit',
      '--allow-empty',
      '-m',
      message,
    );
  }
  gitText(writer, 'push', 'origin', 'main');
}

// --- test-owned remote server -------------------------------------------------

interface RemoteTestServer {
  name: string;
  root: string;
  configPath: string;
  runtimeDir: string;
  port: number;
  connectionString: string;
  proc: ChildProcess;
  logs: string[];
}

interface DiscoveryRecord {
  base_url: string;
  auth_token?: string;
}

function discoveryAt(runtimeDir: string): DiscoveryRecord | null {
  try {
    return JSON.parse(
      fs.readFileSync(path.join(runtimeDir, '.agentico-server.json'), 'utf8'),
    ) as DiscoveryRecord;
  } catch {
    return null;
  }
}

async function freeLoopbackPort(): Promise<number> {
  return new Promise((resolve) => {
    const probe = net.createServer();
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address();
      const port = typeof address === 'object' && address !== null ? address.port : 0;
      probe.close(() => resolve(port));
    });
  });
}

/**
 * Starts the bundled server with its own HOME, state dir and workspace
 * root, so nothing it owns reaches the app-owned local server's world.
 */
async function startPrepRemoteServer(world: JourneyWorld): Promise<RemoteTestServer> {
  const name = REMOTE_NAME;
  const runtimeDir = path.join(world.root, 'prep-remote');
  const homeDir = path.join(world.root, 'prep-remote-home');
  const stateDir = path.join(runtimeDir, 'features');
  const root = path.join(world.root, 'prep-remote-work');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.mkdirSync(homeDir, { recursive: true });
  fs.mkdirSync(root, { recursive: true });
  const configPath = path.join(runtimeDir, 'config.yaml');
  const missing = path.join(world.stubDir, 'missing');
  fs.writeFileSync(
    configPath,
    [
      'providers:',
      '  claude:',
      `    cli: ${world.claudeStub}`,
      '  codex:',
      `    cli: ${path.join(missing, 'codex')}`,
      '  opencode:',
      `    cli: ${path.join(missing, 'opencode')}`,
      'workspace_roots:',
      `  - ${root}`,
      '',
    ].join('\n'),
  );
  const port = await freeLoopbackPort();
  const logs: string[] = [];
  const proc = spawn(
    bundledServerBinary(packagedExecutable()),
    [
      'server',
      '--config',
      configPath,
      '--state-dir',
      stateDir,
      '--name',
      name,
      '--listen',
      `127.0.0.1:${String(port)}`,
    ],
    { env: { ...minimalEnv(world), HOME: homeDir }, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  proc.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  await waitFor(() => discoveryAt(runtimeDir) !== null, `${name} discovery record`, 30_000);
  const record = discoveryAt(runtimeDir)!;
  if (record.auth_token === undefined || record.auth_token === '') {
    throw new Error(`${name} discovery record carries no token`);
  }
  const host = new URL(record.base_url).host;
  return {
    name,
    root,
    configPath,
    runtimeDir,
    port,
    connectionString: `agentico://${record.auth_token}@${host}?name=${encodeURIComponent(name)}`,
    proc,
    logs,
  };
}

async function stopRemoteServer(server: RemoteTestServer): Promise<void> {
  if (server.proc.exitCode !== null || server.proc.signalCode !== null) {
    return;
  }
  server.proc.kill('SIGKILL');
  await waitFor(
    () => server.proc.exitCode !== null || server.proc.signalCode !== null,
    `${server.name} to exit`,
    15_000,
  ).catch(() => {});
}

async function connectionState(handle: AppHandle) {
  return handle.page.evaluate(() => window.agentico.getConnectionStatus());
}

/** The canonical error code of a refused direct IPC call, or 'resolved'. */
async function refusedCode(page: Page, call: string): Promise<string> {
  return page.evaluate(async (source) => {
    try {
      const result = await (0, eval)(`window.agentico.${source}`);
      void result;
      return 'resolved';
    } catch (err) {
      // The preload's canonical error crosses the wire as a
      // sentinel-prefixed message; the attached object does not survive
      // serialization.
      const message = (err as Error).message ?? '';
      if (message.startsWith('E_CANONICAL_ERROR ')) {
        try {
          return (
            (JSON.parse(message.slice('E_CANONICAL_ERROR '.length)) as { code?: string }).code ??
            'uncoded'
          );
        } catch {
          return 'uncoded';
        }
      }
      return 'uncoded';
    }
  }, call);
}

/** Pastes a connection string into the Servers pane and probes it. */
async function addRemoteServer(handle: AppHandle, settings: Page, connectionString: string) {
  await selectSettingsPane(settings, 'Servers');
  const pasteField = settings.getByRole('textbox', { name: /add a remote server/i });
  await pasteField.fill(connectionString);
  await settings.getByRole('button', { name: 'Probe and connect' }).click();
  await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
    timeout: 60_000,
  });
  await waitFor(
    async () => (await connectionState(handle)).serverName === REMOTE_NAME,
    'the auto-switch to the remote server',
    60_000,
  );
}

/** Switches servers through the main window's footer popover. */
async function switchServer(
  handle: AppHandle,
  currentServerName: string,
  optionPattern: RegExp,
): Promise<void> {
  await handle.page.getByRole('button', { name: `${currentServerName} — switch server` }).click();
  await expect(handle.page.getByRole('listbox', { name: 'Servers' })).toBeVisible({
    timeout: 30_000,
  });
  await handle.page.getByRole('option', { name: optionPattern }).click();
}

// --- (a) picker create from zero roots -----------------------------------------

test('picker create: choose a root, create with consent, adopt into the same draft', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    `${RUN_PREFIX}-picker`,
    'Picker repository creation from zero roots (packaged app, real bundled server)',
  );
  // No preset workspace root: the picker itself must choose and persist one.
  const world = createWorld('prep-picker-create', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
  });
  const ctx: WorldHandle = { world, handle: null };
  try {
    transcript.section('Launch with no roots and open the creation sheet');
    ctx.handle = await launchApp(world, testInfo, { traceName: 'prep-picker-create' });
    const page = ctx.handle.page;
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await page.getByRole('button', { name: 'New feature' }).click();
    const sheet = page.getByRole('dialog', { name: 'New feature' });
    await expect(sheet).toBeVisible();

    transcript.section('Nested create view with no roots: the chooser is the way forward');
    await sheet.getByRole('button', { name: /create a repository/i }).click();
    const dialog = page.getByRole('dialog', { name: 'Create a repository' });
    await expect(dialog).toBeVisible();
    // Reduced motion is honored by the nested view.
    await page.emulateMedia({ reducedMotion: 'reduce' });
    expect(await dialog.evaluate((node) => getComputedStyle(node).animationName)).toBe('none');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await expect(dialog.getByText(/No clone-eligible workspace root yet/)).toBeVisible({
      timeout: 30_000,
    });
    await expect(dialog.getByRole('button', { name: 'Create repository' })).toBeDisabled();

    transcript.section('Choose a folder: the root persists before preparation begins');
    await mockDirectoryPicker(ctx.handle, world.workspaceRoot);
    await dialog.getByRole('button', { name: /choose folder/i }).click();
    await expect(dialog.getByLabel('Destination root')).toHaveValue(world.workspaceRoot, {
      timeout: 30_000,
    });
    const readinessWithRoot = await page.evaluate(() => window.agentico.getReadiness());
    expect(readinessWithRoot.workspaceRoots.map((root) => root.path)).toEqual([
      world.workspaceRoot,
    ]);
    transcript.step('the chosen folder persisted and became the selected root');

    transcript.section('Create the named child repository with explicit consent');
    await dialog.getByLabel('Repository folder name').fill('made-fresh');
    await expect(dialog.getByText(/one empty initial commit on the main branch/i)).toBeVisible();
    await dialog.getByRole('checkbox', { name: /initial empty commit/i }).check();
    await dialog.getByRole('button', { name: 'Create repository' }).click();

    await expect(dialog).not.toBeVisible({ timeout: 120_000 });
    const madeRow = sheet.locator('.creation-sheet__row', { hasText: 'made-fresh' });
    await expect(madeRow).toBeVisible({ timeout: 30_000 });
    const madeCheckbox = madeRow.getByRole('checkbox');
    await expect(madeCheckbox).toBeChecked({ timeout: 30_000 });
    await expect
      .poll(async () => page.evaluate(() => document.activeElement?.className ?? ''), {
        timeout: 30_000,
      })
      .toContain('creation-sheet__row-control');
    await expect(sheet.getByText('Created made-fresh and selected it.')).toBeVisible();
    transcript.step('created repository adopted into the initiating draft with row focus');

    // Real git evidence: exactly one empty main commit, Agentico identity,
    // no origin, and a feature-ready catalog entry with a real identity.
    const made = path.join(world.workspaceRoot, 'made-fresh');
    assertCreatedRepository(made);
    const readiness = await page.evaluate(() => window.agentico.getReadiness());
    const entry = readiness.repositories.find((repo) => repo.name === 'made-fresh');
    expect(entry?.valid).toBe(true);
    expect(entry?.featureReady).toBe(true);
    // The server resolves the canonical (symlink-free) path.
    expect(entry?.identity?.path).toBe(fs.realpathSync(made));
    transcript.step('real git evidence: one empty main commit, Agentico identity, no origin');

    // The draft survives the adoption with every other value.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await sheet.getByLabel('Name').fill('Prepared feature');
    await sheet.getByRole('button', { name: 'Back' }).click();
    await expect(madeCheckbox).toBeChecked();
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await expect(sheet.getByLabel('Name')).toHaveValue('Prepared feature');
    transcript.step('draft values preserved through creation and adoption');
    await evidenceShot(ctx.handle, 'prep-picker-create', page);

    await closeApp(ctx.handle);
    ctx.handle = null;
  } finally {
    if (ctx.handle !== null) {
      await closeApp(ctx.handle).catch(() => undefined);
    }
    destroyWorld(world);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  }
});

// --- (b) settings create, relaunch persistence, root survives clone failure ---

test('settings create: chooser-added root persists across relaunch and survives clone failure', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    `${RUN_PREFIX}-settings`,
    'Settings repository creation, root persistence across relaunch (packaged)',
  );
  const world = createWorld('prep-settings-create', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const secondRoot = path.join(world.root, 'second-root');
  fs.mkdirSync(secondRoot, { recursive: true });
  const ctx: WorldHandle = { world, handle: null };
  try {
    transcript.section('Settings create against a chooser-added root');
    ctx.handle = await launchApp(world, testInfo, { traceName: 'prep-settings-create' });
    await expect(ctx.handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const settings = await openSettings(ctx.handle);
    await selectSettingsPane(settings, 'Workspace roots');
    const createForm = settings.locator('section[aria-label="Create repositories"]');
    await expect(createForm).toBeVisible({ timeout: 30_000 });

    await mockDirectoryPicker(ctx.handle, secondRoot);
    await createForm.getByRole('button', { name: /choose folder/i }).click();
    await expect(createForm.getByLabel('Destination root')).toHaveValue(secondRoot, {
      timeout: 30_000,
    });
    // Unrelated roots keep their order; the new root was appended.
    const rootsAfterAdd = await ctx.handle.page.evaluate(() =>
      window.agentico
        .getReadiness()
        .then((snapshot) => snapshot.workspaceRoots.map((root) => root.path)),
    );
    expect(rootsAfterAdd).toEqual([world.workspaceRoot, secondRoot]);
    transcript.step('chooser-added root persisted, appended, and selected');

    await createForm.getByLabel('Repository folder name').fill('settings-made');
    await createForm.getByRole('checkbox', { name: /initial empty commit/i }).check();
    await createForm.getByRole('button', { name: 'Create repository' }).click();
    await expect(settings.getByText(/Created settings-made at .+ on this computer\./)).toBeVisible({
      timeout: 60_000,
    });
    assertCreatedRepository(path.join(secondRoot, 'settings-made'));
    transcript.step('settings create published with the promised repository shape');

    transcript.section('A controlled clone failure retains the saved root');
    const cloneForm = settings.locator('section[aria-label="Clone repositories"]');
    await expect(cloneForm.getByLabel('Destination root')).toHaveValue(world.workspaceRoot, {
      timeout: 30_000,
    });
    await cloneForm.getByLabel('Destination root').selectOption(secondRoot);
    await cloneForm.getByLabel('Repository URL').fill('http://127.0.0.1:9/dead.git');
    await cloneForm.getByLabel('Folder name').fill('never-made');
    await cloneForm.getByRole('button', { name: 'Clone repository' }).click();
    const failedCard = settings.locator('.settings-panel__clone-operation', {
      hasText: 'never-made',
    });
    await expect(failedCard.first()).toContainText('Failed', { timeout: 120_000 });
    const rootsAfterFailure = await ctx.handle.page.evaluate(() =>
      window.agentico
        .getReadiness()
        .then((snapshot) => snapshot.workspaceRoots.map((root) => root.path)),
    );
    expect(rootsAfterFailure).toEqual([world.workspaceRoot, secondRoot]);
    expect(fs.existsSync(path.join(secondRoot, 'never-made'))).toBe(false);
    for (const entry of fs.readdirSync(secondRoot)) {
      expect(entry.startsWith('.agentico-')).toBe(false);
    }
    transcript.step('clone failure retained the saved root and left no residue');

    await closeApp(ctx.handle);
    ctx.handle = null;

    transcript.section('Relaunch: the chooser-added root persists');
    ctx.handle = await launchApp(world, testInfo, { traceName: 'prep-settings-relaunch' });
    await expect(ctx.handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const rootsAfterRelaunch = await ctx.handle.page.evaluate(() =>
      window.agentico
        .getReadiness()
        .then((snapshot) => snapshot.workspaceRoots.map((root) => root.path)),
    );
    expect(rootsAfterRelaunch).toEqual([world.workspaceRoot, secondRoot]);
    transcript.step('chooser-selected root persisted across relaunch');
    await evidenceShot(ctx.handle, 'prep-settings-relaunch');

    await closeApp(ctx.handle);
    ctx.handle = null;
  } finally {
    if (ctx.handle !== null) {
      await closeApp(ctx.handle).catch(() => undefined);
    }
    destroyWorld(world);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  }
});

// --- (c) remote create and clone ----------------------------------------------

test('remote create and clone use only the remote configured roots', async ({}, testInfo) => {
  test.setTimeout(360_000);
  const transcript = new Transcript(
    `${RUN_PREFIX}-remote`,
    'Remote repository preparation uses only administrator-configured roots (packaged)',
  );
  const world = createWorld('prep-remote', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const populated = await serveGitRemote(world.root, 'prep-remote-src', 2);
  const empty = await serveGitRemote(world.root, 'prep-empty-src', 0);
  const remote = await startPrepRemoteServer(world);
  // A repository inside the remote workspace whose origin is two commits
  // ahead: only the selected remote server can see or check it.
  pairRemoteBehindOrigin(world, remote.root);
  const localConfigBefore = fs.readFileSync(world.configPath, 'utf8');
  const remoteConfigBefore = fs.readFileSync(remote.configPath, 'utf8');
  const ctx: WorldHandle = { world, handle: null };
  try {
    transcript.section('Launch locally, then attach and switch to the remote server');
    ctx.handle = await launchApp(world, testInfo, { traceName: 'prep-remote' });
    await expect(ctx.handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const settings = await openSettings(ctx.handle);
    await addRemoteServer(ctx.handle, settings, remote.connectionString);
    const onRemote = await connectionState(ctx.handle);
    expect(onRemote.status).toBe('ready');
    if (onRemote.status === 'ready') {
      expect(onRemote.kind).toBe('remote');
    }
    transcript.step('connected and switched to the remote server');

    transcript.section('Remote roots are the only destinations; desktop mutation is refused');
    const page = ctx.handle.page;
    const remoteRoots = await page.evaluate(() =>
      window.agentico
        .getReadiness()
        .then((snapshot) => snapshot.workspaceRoots.map((root) => root.path)),
    );
    expect(remoteRoots).toEqual([remote.root]);
    expect(await refusedCode(page, 'pickWorkspaceDirectory()')).toBe('E_REQUIRES_LOCAL_SERVER');
    expect(await refusedCode(page, "addWorkspaceRoot('/tmp/never-on-remote')")).toBe(
      'E_REQUIRES_LOCAL_SERVER',
    );
    expect(await refusedCode(page, "removeWorkspaceRoot('/tmp/never-on-remote')")).toBe(
      'E_REQUIRES_LOCAL_SERVER',
    );
    transcript.step('direct chooser and root-mutation IPC refused before any remote request');

    transcript.section('Picker create on the remote: configured root, consent, adoption');
    await page.getByRole('button', { name: 'New feature' }).click();
    const sheet = page.getByRole('dialog', { name: 'New feature' });
    await expect(sheet).toBeVisible();
    await sheet.getByRole('button', { name: /create a repository/i }).click();
    const dialog = page.getByRole('dialog', { name: 'Create a repository' });
    await expect(dialog).toBeVisible();
    // No native chooser on a remote form; the configured root is offered.
    await expect(dialog.getByRole('button', { name: /choose folder/i })).toHaveCount(0);
    await expect(dialog.getByLabel('Destination root')).toHaveValue(remote.root, {
      timeout: 30_000,
    });
    await dialog.getByLabel('Repository folder name').fill('remote-made');
    await dialog.getByRole('checkbox', { name: /initial empty commit/i }).check();
    await dialog.getByRole('button', { name: 'Create repository' }).click();
    await expect(dialog).not.toBeVisible({ timeout: 120_000 });
    const remoteMadeRow = sheet.locator('.creation-sheet__row', { hasText: 'remote-made' });
    await expect(remoteMadeRow.getByRole('checkbox')).toBeChecked({ timeout: 30_000 });
    await expect(sheet.getByText('Created remote-made and selected it.')).toBeVisible();
    // Real git evidence on the remote-owned directory.
    assertCreatedRepository(path.join(remote.root, 'remote-made'));
    transcript.step('remote create published into the remote root and adopted by identity');

    transcript.section('Picker clone on the remote shares the same root presentation');
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    const cloneDialog = page.getByRole('dialog', { name: 'Clone a repository' });
    await expect(cloneDialog).toBeVisible();
    await expect(cloneDialog.getByRole('button', { name: /choose folder/i })).toHaveCount(0);
    await expect(cloneDialog.getByLabel('Destination root')).toHaveValue(remote.root, {
      timeout: 30_000,
    });
    await cloneDialog.getByLabel('Repository URL').fill(populated.url);
    await cloneDialog.getByLabel('Folder name').fill('remote-clone');
    await cloneDialog.getByRole('button', { name: 'Clone repository' }).click();
    await expect(cloneDialog).not.toBeVisible({ timeout: 120_000 });
    const remoteCloneRow = sheet.locator('.creation-sheet__row', { hasText: 'remote-clone' });
    await expect(remoteCloneRow.getByRole('checkbox')).toBeChecked({ timeout: 30_000 });
    expect(gitText(path.join(remote.root, 'remote-clone'), 'rev-list', '--count', 'HEAD')).toBe(
      '2',
    );
    transcript.step('remote clone published into the remote root and adopted');

    transcript.section(
      'Remote empty-remote clone: offer, initialize, adopt — no desktop-path mutation',
    );
    await sheet.getByRole('button', { name: /clone a repository/i }).click();
    await expect(cloneDialog).toBeVisible();
    await cloneDialog.getByLabel('Repository URL').fill(empty.url);
    await cloneDialog.getByLabel('Folder name').fill('remote-empty');
    await cloneDialog.getByRole('button', { name: 'Clone repository' }).click();
    const unbornOperation = page.locator('.creation-clone__operation', {
      hasText: 'remote-empty',
    });
    await expect(unbornOperation.first()).toContainText('Succeeded', { timeout: 120_000 });
    await expect(unbornOperation.first()).toContainText(/no commits yet/);
    const initializeOffer = unbornOperation.first().locator('[data-initialize-offer]');
    await expect(initializeOffer).toBeVisible();
    await expect(initializeOffer).toContainText(/one empty local commit/i);
    await initializeOffer
      .getByRole('button', { name: 'Create initial commit', exact: true })
      .click();
    await expect(cloneDialog).not.toBeVisible({ timeout: 30_000 });
    const remoteEmptyRow = sheet.locator('.creation-sheet__row', { hasText: 'remote-empty' });
    await expect(remoteEmptyRow.getByRole('checkbox')).toBeChecked({ timeout: 30_000 });
    await expect(sheet.getByText('Initialized remote-empty and selected it.')).toBeVisible();
    // Real git evidence in the remote-owned workspace: one empty Agentico
    // commit, origin preserved, nothing pushed.
    const remoteEmptyDir = path.join(remote.root, 'remote-empty');
    expect(gitText(remoteEmptyDir, 'rev-list', '--count', 'HEAD')).toBe('1');
    expect(gitText(remoteEmptyDir, 'log', '-1', '--format=%an <%ae> | %cn <%ce> | %s')).toBe(
      'Agentico <agentico@localhost> | Agentico <agentico@localhost> | Initial commit',
    );
    expect(gitText(remoteEmptyDir, 'remote', 'get-url', 'origin')).toBe(empty.url);
    expect(await remoteRefs(empty.url)).toBe('');
    transcript.step('remote initialization adopted by identity with server-side git evidence only');

    transcript.section('Origin checks execute against the selected server');
    // Deselect the adopted rows so the origin evidence is scoped to the one
    // repository whose origin comparison can only come from the remote.
    await remoteMadeRow.getByRole('checkbox').uncheck();
    await remoteCloneRow.getByRole('checkbox').uncheck();
    await remoteEmptyRow.getByRole('checkbox').uncheck();
    const behindRow = sheet.locator('.creation-sheet__row', { hasText: 'remote-behind' });
    await expect(behindRow).toBeVisible({ timeout: 30_000 });
    await behindRow.getByRole('checkbox').check();
    await expect(
      sheet.getByRole('checkbox', {
        name: /remote-behind.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    transcript.step(
      'the behind comparison was fetched by the selected remote server from its own workspace',
    );

    transcript.section("The other server's directories and configuration are untouched");
    expect(fs.existsSync(path.join(world.workspaceRoot, 'remote-made'))).toBe(false);
    expect(fs.existsSync(path.join(world.workspaceRoot, 'remote-clone'))).toBe(false);
    expect(fs.existsSync(path.join(world.workspaceRoot, 'remote-empty'))).toBe(false);
    expect(fs.existsSync(path.join(world.workspaceRoot, 'remote-behind'))).toBe(false);
    expect(fs.readFileSync(world.configPath, 'utf8')).toBe(localConfigBefore);
    expect(fs.readFileSync(remote.configPath, 'utf8')).toBe(remoteConfigBefore);
    transcript.step('no desktop path was transmitted and neither config changed');
    await evidenceShot(ctx.handle, 'prep-remote', page);

    await closeApp(ctx.handle);
    ctx.handle = null;
  } finally {
    if (ctx.handle !== null) {
      await closeApp(ctx.handle).catch(() => undefined);
    }
    populated.close();
    empty.close();
    await stopRemoteServer(remote);
    destroyWorld(world);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  }
});

// --- (d) server switch during pending root selection --------------------------

test('server switch during pending root selection suppresses the stale sequence', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    `${RUN_PREFIX}-switch`,
    'A server switch while root selection is pending never writes either server (packaged)',
  );
  const world = createWorld('prep-switch', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const pendingRoot = path.join(world.root, 'pending-root');
  fs.mkdirSync(pendingRoot, { recursive: true });
  const remote = await startPrepRemoteServer(world);
  const localConfigBefore = fs.readFileSync(world.configPath, 'utf8');
  const remoteConfigBefore = fs.readFileSync(remote.configPath, 'utf8');
  const ctx: WorldHandle = { world, handle: null };
  try {
    transcript.section('Attach the remote, then return to the local server');
    ctx.handle = await launchApp(world, testInfo, { traceName: 'prep-switch' });
    await expect(ctx.handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const localName = (await connectionState(ctx.handle)).serverName ?? '';
    expect(localName).not.toBe('');
    const settings = await openSettings(ctx.handle);
    await addRemoteServer(ctx.handle, settings, remote.connectionString);
    // Back to local through the footer popover so both servers are known;
    // the popover opens from the main window while Settings stays open.
    await switchServer(ctx.handle, REMOTE_NAME, new RegExp(`${localName} at .+ — Available`));
    await waitFor(
      async () => {
        const state = await connectionState(ctx.handle!);
        return state.status === 'ready' && state.serverName === localName;
      },
      'the switch back to the local server',
      60_000,
    );
    transcript.step('both servers known; back on the local server');

    transcript.section('Root selection pending while the connection flips to the remote');
    // The delayed chooser answers only after the switch has begun.
    await mockDelayedDirectoryPicker(ctx.handle, pendingRoot, 2500);
    // Back on the Workspace-roots pane for the clone form.
    await selectSettingsPane(settings, 'Workspace roots');
    const cloneForm = settings.locator('section[aria-label="Clone repositories"]');
    await expect(cloneForm).toBeVisible({ timeout: 30_000 });
    await cloneForm.getByRole('button', { name: /choose folder/i }).click();
    // Flip to the remote while the choice is still pending; while the app
    // is on the local server the remote row is Available, not Connected.
    await switchServer(ctx.handle, localName, new RegExp(`${REMOTE_NAME} — Available`));
    await waitFor(
      async () => (await connectionState(ctx.handle!)).serverName === REMOTE_NAME,
      'the switch to the remote server while the chooser is pending',
      60_000,
    );
    // Let the stale sequence finish: it must be refused before any write.
    await new Promise((resolve) => setTimeout(resolve, 4000));

    const remoteRoots = await ctx.handle.page.evaluate(() =>
      window.agentico
        .getReadiness()
        .then((snapshot) => snapshot.workspaceRoots.map((root) => root.path)),
    );
    expect(remoteRoots).toEqual([remote.root]);
    expect(fs.readFileSync(world.configPath, 'utf8')).toBe(localConfigBefore);
    expect(fs.readFileSync(remote.configPath, 'utf8')).toBe(remoteConfigBefore);
    expect(fs.existsSync(path.join(remote.root, 'pending-root'))).toBe(false);
    transcript.step('the stale root selection never wrote either server');
    await evidenceShot(ctx.handle, 'prep-switch', settings);

    await closeApp(ctx.handle);
    ctx.handle = null;
  } finally {
    if (ctx.handle !== null) {
      await closeApp(ctx.handle).catch(() => undefined);
    }
    await stopRemoteServer(remote);
    destroyWorld(world);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  }
});
