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
 * Uncertain Update-from-origin outcomes and their reconciliation
 * (packaged app, real server):
 *   unprovable attempt  → outcome unknown; the settlement read warns that
 *                         the update did not complete; a still-valid local
 *                         source is accepted and pinned exactly
 *   server switch       → the attempt's outcome stays unknown on the
 *                         originating server; the branch really advanced
 *                         behind the switch; returning reconciles it and
 *                         restores acceptance — the other server's sheet
 *                         never sees or settles the record
 */
import { execFileSync, spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import { parseFeatureRepoField, parseFeatureRepos } from '../helpers/completionFixture';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, minimalEnv, waitFor } from '../helpers/world';
import type { JourneyWorld } from '../helpers/world';

const HERMETIC_GIT_ENV = {
  ...minimalEnv(),
  GIT_CONFIG_GLOBAL: '/dev/null',
  GIT_CONFIG_SYSTEM: '/dev/null',
} as const;

function gitText(dir: string, ...args: string[]): string {
  return execFileSync('git', ['-C', dir, ...args], {
    encoding: 'utf8',
    env: HERMETIC_GIT_ENV,
  }).trim();
}

function durableFeatureIds(stateDir: string): string[] {
  return fs
    .readdirSync(stateDir, { withFileTypes: true })
    .filter(
      (entry) =>
        entry.isDirectory() && fs.existsSync(path.join(stateDir, entry.name, 'feature.yaml')),
    )
    .map((entry) => entry.name);
}

/** Reserves an ephemeral port and frees it so nothing can answer there. */
async function pickDeadPort(): Promise<number> {
  const server = net.createServer();
  const listening = new Promise<void>((resolve) => server.once('listening', () => resolve()));
  server.listen(0, '127.0.0.1');
  await listening;
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  server.close();
  if (port === 0) throw new Error('could not reserve a dead port');
  return port;
}

/**
 * Pairs a repository with a real bare origin and leaves the local default
 * branch UNOCCUPIED: the original checkout moves to a `work` branch while a
 * writer clone advances origin/main by two commits. The default-mode source
 * still resolves `main` through origin/HEAD.
 */
function pairUnoccupiedBehindOrigin(
  world: JourneyWorld,
  name: string,
): { repo: string; bare: string } {
  const repo = createRepo(world, name, { commit: true });
  const bare = path.join(world.root, `${name}-origin.git`);
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare', '--initial-branch=main');
  gitText(repo, 'remote', 'add', 'origin', bare);
  gitText(repo, 'push', 'origin', 'main');
  gitText(repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/main');
  gitText(repo, 'checkout', '-b', 'work');
  const writer = path.join(world.root, `${name}-writer`);
  execFileSync('git', ['clone', bare, writer], { stdio: 'pipe', env: HERMETIC_GIT_ENV });
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
  return { repo, bare };
}

/**
 * Pairs a repository with a real bare origin and leaves the default branch
 * HELD BY THE ORIGINAL CHECKOUT: the checkout stays ON `main`, clean, while
 * a writer clone advances origin/main by two commits with real file changes
 * (a rewritten tracked README and a new file extended by the second commit).
 * Current-mode selection resolves the checked-out branch; default mode still
 * resolves `main` through origin/HEAD.
 */
function pairOccupiedBehindOrigin(
  world: JourneyWorld,
  name: string,
): { repo: string; bare: string } {
  const repo = createRepo(world, name, { commit: true });
  const bare = path.join(world.root, `${name}-origin.git`);
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare', '--initial-branch=main');
  gitText(repo, 'remote', 'add', 'origin', bare);
  gitText(repo, 'push', 'origin', 'main');
  gitText(repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/main');
  const writer = path.join(world.root, `${name}-writer`);
  execFileSync('git', ['clone', bare, writer], { stdio: 'pipe', env: HERMETIC_GIT_ENV });
  // Real file changes, not empty commits: the fast-forward must advance the
  // checkout's index and working files, and the assertions compare bytes.
  fs.writeFileSync(path.join(writer, 'README.md'), `# ${name} (advanced by origin)\n`);
  fs.writeFileSync(path.join(writer, 'remote-notes.txt'), 'remote note one\n');
  gitText(writer, 'add', '.');
  gitText(
    writer,
    '-c',
    'user.name=e2e',
    '-c',
    'user.email=e2e@example.invalid',
    'commit',
    '-m',
    'remote one',
  );
  fs.writeFileSync(path.join(writer, 'remote-notes.txt'), 'remote note one\nremote note two\n');
  gitText(writer, 'add', '.');
  gitText(
    writer,
    '-c',
    'user.name=e2e',
    '-c',
    'user.email=e2e@example.invalid',
    'commit',
    '-m',
    'remote two',
  );
  gitText(writer, 'push', 'origin', 'main');
  return { repo, bare };
}

/**
 * A controllable dumb-HTTP origin: serves the world's bare repositories as
 * static files (git falls back to the dumb protocol when the smart
 * info/refs?service=… probe 404s) and can delay the dumb info/refs request
 * as an observable barrier while an update's fetch is in flight.
 */
interface SlowOriginProxy {
  urlFor(name: string): Promise<string>;
  setSlow(slow: boolean): Promise<void>;
  waitForDelayedInfoRefs(): Promise<void>;
  stop(): void;
}

function startSlowOriginProxy(world: JourneyWorld): SlowOriginProxy {
  const script = path.join(world.root, 'slow-origin-proxy.js');
  fs.writeFileSync(
    script,
    `
const http = require('http');
const fs = require('fs');
const path = require('path');
const root = process.argv[2];
let slow = false;
const seen = [];
const srv = http.createServer((req, res) => {
  const url = req.url || '';
  if (url.startsWith('/__control/')) {
    if (url.startsWith('/__control/set?slow=')) {
      slow = url.endsWith('slow=1');
      res.writeHead(204); res.end(); return;
    }
    if (url.startsWith('/__control/seen')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(seen)); return;
    }
    res.writeHead(404); res.end(); return;
  }
  const clean = url.split('?')[0];
  // Smart-probe requests get the plain dumb info/refs file too: git sees a
  // non-smart payload and falls back to the dumb protocol, so every fetch
  // reads exactly one info/refs — delaying it is the observable barrier.
  const delayed = slow && clean.endsWith('/info/refs');
  seen.push({ url: clean, delayed });
  const send = () => {
    const rel = decodeURIComponent(clean).replace(/^\\/+/, '');
    const file = path.join(root, rel);
    fs.stat(file, (err, st) => {
      if (err || !st.isFile()) { res.writeHead(404); res.end('not found'); return; }
      res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
      fs.createReadStream(file).pipe(res);
    });
  };
  if (delayed) setTimeout(send, 2500); else send();
});
srv.listen(0, '127.0.0.1', () => console.log('PORT=' + srv.address().port));
`,
  );
  const proc = spawn(process.execPath, [script, world.root], {
    stdio: ['ignore', 'pipe', 'ignore'],
    env: minimalEnv(),
  });
  let port = 0;
  const ready = new Promise<number>((resolve) => {
    proc.stdout!.on('data', (chunk: Buffer) => {
      const match = /PORT=(\d+)/.exec(chunk.toString());
      if (match !== null && port === 0) {
        port = Number(match[1]);
        resolve(port);
      }
    });
  });
  const base = async () => `http://127.0.0.1:${await ready}`;
  return {
    async urlFor(name: string) {
      return `${await base()}/${name}-origin.git`;
    },
    async setSlow(slow: boolean) {
      const prefix = await base();
      await waitFor(
        async () => {
          const response = await fetch(`${prefix}/__control/set?slow=${slow ? 1 : 0}`);
          return response.ok;
        },
        'the slow-origin proxy to accept control',
        10_000,
      );
    },
    async waitForDelayedInfoRefs() {
      const prefix = await base();
      await waitFor(
        async () => {
          const response = await fetch(`${prefix}/__control/seen`);
          if (!response.ok) return false;
          const entries = (await response.json()) as { delayed: boolean }[];
          return entries.some((entry) => entry.delayed);
        },
        'a delayed dumb info/refs request from the in-flight update',
        30_000,
      );
    },
    stop() {
      if (proc.exitCode === null) proc.kill('SIGKILL');
    },
  };
}

test('an unprovable update outcome is reconciled to a warning and the still-valid source is accepted exactly', async ({}, testInfo) => {
  const world = createWorld('repository-source-reconcile', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairUnoccupiedBehindOrigin(world, 'reconcile-lab');
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the unoccupied source behind origin');
  }
  const deadPort = await pickDeadPort();

  const transcript = new Transcript(
    'repository-source-reconcile',
    'Uncertain source update outcome and reconciliation (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'repository-source-reconcile' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /reconcile-lab/ }).check();
    await expect(
      sheet.getByRole('checkbox', { name: /reconcile-lab.*Origin: 2 commits behind origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'reconcile-lab' });
    transcript.step('the behind row offered Update from origin through the real server');

    // The origin becomes unreachable after the comparison: the update's
    // fresh fetch cannot prove anything, so its result is unprovable.
    gitText(repo, 'remote', 'set-url', 'origin', `http://127.0.0.1:${deadPort}/dead.git`);
    await row.getByRole('button', { name: 'Update from origin' }).click();

    // The settlement read needs no network: it observes the branch locally,
    // reports that the update did not complete, and restores warning-based
    // continuation for the still-valid local source.
    await expect(
      row.getByText(/Reconciled main on .+: the update did not complete, and main is unchanged/),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-reconcile-original-tip-warning');
    transcript.step('the unprovable attempt was reconciled to a no-mutation warning');
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(staleSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');

    // Restoring the origin and checking again refreshes the comparison; the
    // mutation is never retried automatically.
    gitText(repo, 'remote', 'set-url', 'origin', bare);
    await row.getByRole('button', { name: 'Check again' }).click();
    await expect(
      sheet.getByRole('checkbox', { name: /reconcile-lab.*Origin: 2 commits behind origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(staleSha);

    // The warning persists through the review and the still-valid local
    // source is accepted without an acknowledgement checkbox.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Reconciled source continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(/reconcile-lab: main is 2 commits behind origin\/main/),
    ).toBeVisible();
    await expect(
      sheet.getByText(/Reconciled main on .+: the update did not complete/),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Reconciled source continuation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(() => durableFeatureIds(world.stateDir).length === 1, 'the reconcile feature id');
    const featureId = durableFeatureIds(world.stateDir)[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['reconcile-lab']!;
    expect(storedCommits['reconcile-lab']).toBe(staleSha);
    expect(runYaml).toContain(`exact_sha: ${staleSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'branch', '--show-current')).toBe('work');
    transcript.step('the reconciled warning persisted and the source was pinned exactly');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

interface TestServer {
  name: string;
  runtimeDir: string;
  stateDir: string;
  configPath: string;
  proc: ChildProcess;
  logs: string[];
}

interface RegistryEntry {
  name?: string;
  runtime: { runtime_dir: string };
  pid: number;
  base_url: string;
}

function registryDir(world: JourneyWorld): string {
  return path.join(world.home, '.agentic-orchestrator', 'servers');
}

function readRegistry(world: JourneyWorld): RegistryEntry[] {
  let names: string[];
  try {
    names = fs.readdirSync(registryDir(world)).filter((name) => name.endsWith('.json'));
  } catch {
    return [];
  }
  return names.map(
    (name) =>
      JSON.parse(fs.readFileSync(path.join(registryDir(world), name), 'utf-8')) as RegistryEntry,
  );
}

function startTestServer(world: JourneyWorld, name: string, runtimeDir: string): TestServer {
  const runtimePath = path.join(world.root, runtimeDir);
  const stateDir = path.join(runtimePath, 'features');
  const configPath = path.join(runtimePath, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.copyFileSync(world.configPath, configPath);
  const logs: string[] = [];
  const proc = spawn(
    bundledServerBinary(packagedExecutable()),
    ['server', '--config', configPath, '--state-dir', stateDir, '--name', name],
    { env: minimalEnv(world), stdio: ['ignore', 'pipe', 'pipe'] },
  );
  proc.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  return { name, runtimeDir: runtimePath, stateDir, configPath, proc, logs };
}

function discoveryAt(runtimeDir: string): { pid: number; base_url: string } | null {
  try {
    return JSON.parse(fs.readFileSync(path.join(runtimeDir, '.agentico-server.json'), 'utf-8')) as {
      pid: number;
      base_url: string;
    };
  } catch {
    return null;
  }
}

async function connectionState(handle: AppHandle) {
  return handle.page.evaluate(() => window.agentico.getConnectionStatus());
}

async function serverKeyFor(handle: AppHandle, name: string): Promise<string> {
  return handle.page.evaluate((want) => {
    const api = window.agentico as unknown as {
      listServers(): Promise<{ rows: { serverKey: string; name: string | null }[] }>;
    };
    return api.listServers().then((snapshot) => {
      const row = snapshot.rows.find((entry) => entry.name === want);
      if (row === undefined) throw new Error(`server ${want} is not listed`);
      return row.serverKey;
    });
  }, name);
}

test('a mid-update server switch leaves the outcome unknown until returning to the originating server reconciles it', async ({}, testInfo) => {
  const world = createWorld('repository-source-reconcile-switch', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const proxy = startSlowOriginProxy(world);
  const { repo, bare } = pairUnoccupiedBehindOrigin(world, 'reconcile-switch-lab');
  // The dumb-HTTP origin needs the server info files after the last push.
  gitText(bare, 'update-server-info');
  gitText(repo, 'remote', 'set-url', 'origin', await proxy.urlFor('reconcile-switch-lab'));
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the unoccupied source behind origin');
  }

  const transcript = new Transcript(
    'repository-source-reconcile-switch',
    'Uncertain update outcome across a server switch (packaged app, two real servers)',
  );
  const servers: TestServer[] = [];
  let handle: AppHandle | undefined;
  try {
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(alpha, beta);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery', 30_000);
    await waitFor(() => readRegistry(world).length === 2, 'two registry entries', 30_000);

    handle = await launchApp(world, testInfo, { traceName: 'repository-source-reconcile-switch' });
    const app = handle;
    const options = app.page.getByRole('option');
    await expect(options.filter({ hasText: 'alpha' })).toHaveCount(1, { timeout: 60_000 });
    await options.filter({ hasText: 'alpha' }).click();
    await waitFor(
      async () => (await connectionState(app)).serverName === 'alpha',
      'alpha attach',
      60_000,
    );

    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /reconcile-switch-lab/ }).check();
    await expect(
      sheet.getByRole('checkbox', {
        name: /reconcile-switch-lab.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'reconcile-switch-lab' });
    transcript.step('alpha showed the behind comparison through the controllable origin');

    // The barrier: the update's fresh fetch is delayed mid-flight, and the
    // workspace switches to beta before its response can arrive.
    await proxy.setSlow(true);
    await row.getByRole('button', { name: 'Update from origin' }).click();
    await proxy.waitForDelayedInfoRefs();
    const betaKey = await serverKeyFor(app, 'beta');
    await app.page.evaluate((serverKey) => {
      const api = window.agentico as unknown as {
        switchConnectionServer(request: { serverKey: string }): Promise<unknown>;
      };
      return api.switchConnectionServer({ serverKey });
    }, betaKey);
    await waitFor(
      async () => (await connectionState(app)).serverName === 'beta',
      'beta attach mid-update',
      60_000,
    );
    transcript.step('the workspace switched to beta while the update was still in flight');

    // The update completed on alpha behind the switch: the branch really
    // advanced, and only alpha's own reconciliation may report that.
    await waitFor(
      () => gitText(repo, 'rev-parse', 'refs/heads/main') === originSha,
      'the switched-away update to complete on alpha',
      30_000,
    );
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');
    await proxy.setSlow(false);

    // Beta's creation view never sees or settles alpha's uncertainty.
    if ((await app.page.getByRole('dialog', { name: 'New feature' }).count()) === 0) {
      await app.page.getByRole('button', { name: 'New feature' }).click();
    }
    await expect(app.page.getByRole('dialog', { name: 'New feature' })).toBeVisible({
      timeout: 30_000,
    });
    await expect(
      app.page.getByRole('dialog', { name: 'New feature' }).getByText(/result of updating main/),
    ).toHaveCount(0);
    transcript.step("beta's sheet never saw alpha's unknown outcome");

    // Returning to alpha replays the record and reconciles it there: the
    // settlement proves the update completed and restores acceptance.
    const alphaKey = await serverKeyFor(app, 'alpha');
    await app.page.evaluate((serverKey) => {
      const api = window.agentico as unknown as {
        switchConnectionServer(request: { serverKey: string }): Promise<unknown>;
      };
      return api.switchConnectionServer({ serverKey });
    }, alphaKey);
    await waitFor(
      async () => (await connectionState(app)).serverName === 'alpha',
      'alpha re-attach',
      60_000,
    );
    const restored = app.page.getByRole('dialog', { name: 'New feature' });
    await expect(restored).toBeVisible({ timeout: 60_000 });
    await expect(
      restored.getByText(
        new RegExp(
          `Reconciled main on alpha: the update completed \\(now at ${originSha.slice(0, 7)}\\)\\.`,
        ),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-reconcile-switch-target-present');
    transcript.step('returning to alpha reconciled the outcome to the completed update');

    // The reconciled source is acceptable again: submission is restored.
    await restored.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Switch reconciled source');
    await restored.getByRole('button', { name: 'Next: Depth' }).click();
    await restored.getByRole('button', { name: 'Next: Contract' }).click();
    await restored.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await expect(restored.getByRole('button', { name: 'Create', exact: true })).toBeEnabled();
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(originSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');
    transcript.step('the reconciled source restored submission on the originating server');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    for (const server of servers) {
      if (server.proc.exitCode === null) server.proc.kill('SIGKILL');
    }
    proxy.stop();
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('a mid-update explicit bundled-runtime selection leaves the outcome unknown until returning to the originating server reconciles it', async ({}, testInfo) => {
  const world = createWorld('repository-source-reconcile-bundled', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const proxy = startSlowOriginProxy(world);
  const { repo, bare } = pairUnoccupiedBehindOrigin(world, 'reconcile-bundled-lab');
  // The dumb-HTTP origin needs the server info files after the last push.
  gitText(bare, 'update-server-info');
  gitText(repo, 'remote', 'set-url', 'origin', await proxy.urlFor('reconcile-bundled-lab'));
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the unoccupied source behind origin');
  }

  const transcript = new Transcript(
    'repository-source-reconcile-bundled',
    'Uncertain update outcome across an explicit bundled-runtime selection (packaged app, real servers)',
  );
  const servers: TestServer[] = [];
  let handle: AppHandle | undefined;
  try {
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    servers.push(alpha);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery', 30_000);
    await waitFor(() => readRegistry(world).length === 1, 'one registry entry', 30_000);

    handle = await launchApp(world, testInfo, {
      traceName: 'repository-source-reconcile-bundled',
    });
    const app = handle;
    // A single registry entry attaches silently: no startup picker appears,
    // so the journey starts already connected to alpha.
    await expect(app.page.getByRole('listbox', { name: /running agentico servers/i })).toHaveCount(
      0,
    );
    await waitFor(
      async () => {
        const state = await connectionState(app);
        return state.status === 'ready' && state.serverName === 'alpha';
      },
      'alpha attach',
      90_000,
    );

    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /reconcile-bundled-lab/ }).check();
    await expect(
      sheet.getByRole('checkbox', {
        name: /reconcile-bundled-lab.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'reconcile-bundled-lab' });
    transcript.step('alpha showed the behind comparison through the controllable origin');

    // The barrier: the update's fresh fetch is delayed mid-flight, and the
    // user explicitly selects the bundled runtime before its response can
    // arrive — the same transition the Settings "Start This machine" action
    // and the footer switcher's Start row drive.
    await proxy.setSlow(true);
    await row.getByRole('button', { name: 'Update from origin' }).click();
    await proxy.waitForDelayedInfoRefs();
    await app.page.evaluate(() => {
      const api = window.agentico as unknown as {
        startLocalRuntime(): Promise<unknown>;
      };
      return api.startLocalRuntime();
    });
    await waitFor(
      async () => {
        const state = await connectionState(app);
        return state.status === 'ready' && state.ownership === 'app-owned';
      },
      'the bundled runtime to start and take the connection mid-update',
      90_000,
    );
    const onBundled = await connectionState(app);
    expect(onBundled.status === 'ready' && onBundled.kind).toBe('local');
    expect(onBundled.connectedRuntimeDir).not.toBe(alpha.runtimeDir);
    transcript.step('the bundled runtime was selected while the update was still in flight');

    // The update completed on alpha behind the transition: the branch really
    // advanced, and the late response was discarded instead of authorizing
    // the new connection, so only alpha's own reconciliation may report it.
    await waitFor(
      () => gitText(repo, 'rev-parse', 'refs/heads/main') === originSha,
      'the switched-away update to complete on alpha',
      30_000,
    );
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');
    await proxy.setSlow(false);

    // The bundled server's creation view never sees or settles alpha's
    // uncertainty, and the repository is never adopted into its draft.
    if ((await app.page.getByRole('dialog', { name: 'New feature' }).count()) === 0) {
      await app.page.getByRole('button', { name: 'New feature' }).click();
    }
    await expect(app.page.getByRole('dialog', { name: 'New feature' })).toBeVisible({
      timeout: 30_000,
    });
    await expect(
      app.page.getByRole('dialog', { name: 'New feature' }).getByText(/result of updating main/),
    ).toHaveCount(0);
    transcript.step("the bundled server's sheet never saw alpha's unknown outcome");

    // Returning to alpha replays the record and reconciles it there: the
    // settlement proves the update completed and restores acceptance.
    const alphaKey = await serverKeyFor(app, 'alpha');
    await app.page.evaluate((serverKey) => {
      const api = window.agentico as unknown as {
        switchConnectionServer(request: { serverKey: string }): Promise<unknown>;
      };
      return api.switchConnectionServer({ serverKey });
    }, alphaKey);
    await waitFor(
      async () => {
        const state = await connectionState(app);
        return state.status === 'ready' && state.serverName === 'alpha';
      },
      'alpha re-attach',
      90_000,
    );
    const restored = app.page.getByRole('dialog', { name: 'New feature' });
    await expect(restored).toBeVisible({ timeout: 60_000 });
    await expect(
      restored.getByText(
        new RegExp(
          `Reconciled main on alpha: the update completed \\(now at ${originSha.slice(0, 7)}\\)\\.`,
        ),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-reconcile-bundled-target-present');
    transcript.step('returning to alpha reconciled the outcome to the completed update');

    // The reconciled source is acceptable again: submission is restored.
    await restored.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Bundled-switch reconciled source');
    await restored.getByRole('button', { name: 'Next: Depth' }).click();
    await restored.getByRole('button', { name: 'Next: Contract' }).click();
    await restored.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await expect(restored.getByRole('button', { name: 'Create', exact: true })).toBeEnabled();
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(originSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');
    transcript.step('the reconciled source restored submission on the originating server');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    for (const server of servers) {
      if (server.proc.exitCode === null) server.proc.kill('SIGKILL');
    }
    proxy.stop();
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('a mid-update server switch on the original checkout reconciles the completed fast-forward and creation pins the advanced tip', async ({}, testInfo) => {
  const world = createWorld('repository-source-reconcile-original', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const proxy = startSlowOriginProxy(world);
  const { repo, bare } = pairOccupiedBehindOrigin(world, 'reconcile-original-lab');
  // The dumb-HTTP origin needs the server info files after the last push.
  gitText(bare, 'update-server-info');
  gitText(repo, 'remote', 'set-url', 'origin', await proxy.urlFor('reconcile-original-lab'));
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the original checkout behind origin');
  }

  const transcript = new Transcript(
    'repository-source-reconcile-original',
    'Original-checkout update outcome across a server switch (packaged app, two real servers)',
  );
  const servers: TestServer[] = [];
  let handle: AppHandle | undefined;
  try {
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(alpha, beta);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery', 30_000);
    await waitFor(() => readRegistry(world).length === 2, 'two registry entries', 30_000);

    handle = await launchApp(world, testInfo, {
      traceName: 'repository-source-reconcile-original',
    });
    const app = handle;
    const options = app.page.getByRole('option');
    await expect(options.filter({ hasText: 'alpha' })).toHaveCount(1, { timeout: 60_000 });
    await options.filter({ hasText: 'alpha' }).click();
    await waitFor(
      async () => (await connectionState(app)).serverName === 'alpha',
      'alpha attach',
      60_000,
    );

    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /reconcile-original-lab/ }).check();
    await expect(
      sheet.getByRole('checkbox', {
        name: /reconcile-original-lab.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'reconcile-original-lab' });
    // The default-branch source is held only by the original checkout, so the
    // offered action names the checkout's files, not just the ref.
    await expect(
      row.getByText(
        'Advances main and its checked-out files in the original repository on alpha to origin/main.',
      ),
    ).toBeVisible();
    transcript.step('alpha offered the original-checkout update with its impact copy');

    // The barrier: the update's fresh fetch is delayed mid-flight, and the
    // workspace switches to beta before its response can arrive.
    await proxy.setSlow(true);
    await row.getByRole('button', { name: 'Update from origin' }).click();
    await proxy.waitForDelayedInfoRefs();
    const betaKey = await serverKeyFor(app, 'beta');
    await app.page.evaluate((serverKey) => {
      const api = window.agentico as unknown as {
        switchConnectionServer(request: { serverKey: string }): Promise<unknown>;
      };
      return api.switchConnectionServer({ serverKey });
    }, betaKey);
    await waitFor(
      async () => (await connectionState(app)).serverName === 'beta',
      'beta attach mid-update',
      60_000,
    );
    transcript.step(
      'the workspace switched to beta while the original-checkout update was in flight',
    );

    // The update completed on alpha behind the switch: the branch AND the
    // original checkout's HEAD, index, and working files really advanced.
    await waitFor(
      () => gitText(repo, 'rev-parse', 'refs/heads/main') === originSha,
      'the switched-away original-checkout update to complete on alpha',
      30_000,
    );
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(originSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# reconcile-original-lab (advanced by origin)\n',
    );
    expect(fs.readFileSync(path.join(repo, 'remote-notes.txt'), 'utf8')).toBe(
      'remote note one\nremote note two\n',
    );
    expect(gitText(repo, 'ls-files', '--stage', '--', 'README.md')).toContain(
      gitText(repo, 'rev-parse', `${originSha}:README.md`),
    );
    expect(gitText(repo, 'diff', '--stat', originSha)).toBe('');
    await proxy.setSlow(false);
    transcript.step('the update completed on alpha: branch, HEAD, index, and files all advanced');

    // Beta's creation view never sees or settles alpha's uncertainty.
    if ((await app.page.getByRole('dialog', { name: 'New feature' }).count()) === 0) {
      await app.page.getByRole('button', { name: 'New feature' }).click();
    }
    await expect(app.page.getByRole('dialog', { name: 'New feature' })).toBeVisible({
      timeout: 30_000,
    });
    await expect(
      app.page
        .getByRole('dialog', { name: 'New feature' })
        .getByText(/result of updating main and its checked-out files/),
    ).toHaveCount(0);
    transcript.step("beta's sheet never saw alpha's unknown original-checkout outcome");

    // Returning to alpha replays the record and reconciles it there: the
    // settlement proves the whole checkout advanced and restores acceptance.
    const alphaKey = await serverKeyFor(app, 'alpha');
    await app.page.evaluate((serverKey) => {
      const api = window.agentico as unknown as {
        switchConnectionServer(request: { serverKey: string }): Promise<unknown>;
      };
      return api.switchConnectionServer({ serverKey });
    }, alphaKey);
    await waitFor(
      async () => (await connectionState(app)).serverName === 'alpha',
      'alpha re-attach',
      60_000,
    );
    const restored = app.page.getByRole('dialog', { name: 'New feature' });
    await expect(restored).toBeVisible({ timeout: 60_000 });
    await expect(
      restored.getByText(
        new RegExp(
          `Reconciled main on alpha: the update completed \\(now at ${originSha.slice(0, 7)}\\)\\.`,
        ),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-reconcile-original-target-present');
    transcript.step('returning to alpha reconciled the outcome to the completed update');

    // The reconciled source is acceptable again: the refreshed comparison is
    // up to date and submission is restored at the advanced tip.
    await expect(
      restored.getByRole('checkbox', {
        name: /reconcile-original-lab.*Origin: up to date with origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    await restored.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Original checkout reconciled update');
    await restored.getByRole('button', { name: 'Next: Depth' }).click();
    await restored.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      restored.getByText('reconcile-original-lab: main is up to date with origin/main.'),
    ).toBeVisible();
    await expect(
      restored.getByText(/Reconciled main on alpha: the update completed/),
    ).toBeVisible();
    await restored.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await expect(restored.getByRole('button', { name: 'Create', exact: true })).toBeEnabled();
    await restored.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Original checkout reconciled update');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(alpha.stateDir).length === 1,
      'the original-checkout reconciled feature id',
    );
    const featureId = durableFeatureIds(alpha.stateDir)[0]!;
    const featureYaml = fs.readFileSync(
      path.join(alpha.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(alpha.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['reconcile-original-lab']!;
    expect(storedCommits['reconcile-original-lab']).toBe(originSha);
    expect(runYaml).toContain(`exact_sha: ${originSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(originSha);
    // The original repository stays on main at the advanced tip, clean.
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(originSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    transcript.step(
      'the reconciled feature started from the advanced tip with the checkout intact',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    for (const server of servers) {
      if (server.proc.exitCode === null) server.proc.kill('SIGKILL');
    }
    proxy.stop();
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('an unprovable original-checkout update over a dirty checkout settles to a partial-effects warning and continues at the unchanged tip', async ({}, testInfo) => {
  const world = createWorld('repository-source-reconcile-dirty', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairOccupiedBehindOrigin(world, 'reconcile-dirty-lab');
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the original checkout behind origin');
  }
  const deadPort = await pickDeadPort();

  const transcript = new Transcript(
    'repository-source-reconcile-dirty',
    'Dirty original-checkout settlement (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'repository-source-reconcile-dirty' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /reconcile-dirty-lab/ }).check();
    await expect(
      sheet.getByRole('checkbox', {
        name: /reconcile-dirty-lab.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'reconcile-dirty-lab' });
    transcript.step('the behind original-checkout row offered Update from origin');

    // The comparison was observed while the checkout was clean. The checkout
    // is dirtied externally (an unstaged tracked edit) and the origin becomes
    // unreachable before the attempt: the update's fetch cannot prove
    // anything, so its outcome is unknown, and the settlement read — which
    // observes the checkout locally — must report the unchanged tip over a
    // dirty checkout without ever claiming a rollback.
    fs.writeFileSync(path.join(repo, 'README.md'), '# reconcile-dirty-lab (externally edited)\n');
    const dirtyStatus = gitText(repo, 'status', '--porcelain');
    gitText(repo, 'remote', 'set-url', 'origin', `http://127.0.0.1:${deadPort}/dead.git`);
    await row.getByRole('button', { name: 'Update from origin' }).click();

    await expect(
      row.getByText(
        new RegExp(
          `Reconciled main on .+: the update did not complete, and main is unchanged ` +
            `\\(now at ${staleSha.slice(0, 7)}\\), but the original repository's checkout has ` +
            `uncommitted changes that may include partial update effects — resolve them outside ` +
            `Agentico, then check again\\.`,
        ),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-reconcile-dirty-partial-effects');
    transcript.step('the unprovable attempt settled to a partial-effects warning');

    // Real-git evidence: nothing mutated and the dirty bytes are preserved.
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(staleSha);
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/main');
    expect(gitText(repo, 'status', '--porcelain')).toBe(dirtyStatus);
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# reconcile-dirty-lab (externally edited)\n',
    );
    transcript.step('the settlement left the unchanged tip and the dirty bytes intact');

    // Warning-based continuation: the still-valid local source is accepted
    // without an acknowledgement, and the warning persists through review.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Dirty settlement continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(
        'reconcile-dirty-lab: main is 2 commits behind origin/main; the feature will start from the local source.',
      ),
    ).toBeVisible();
    await expect(
      sheet.getByText(/Reconciled main on .+: the update did not complete/),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Dirty settlement continuation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the dirty settlement continuation feature id',
    );
    const featureId = durableFeatureIds(world.stateDir)[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['reconcile-dirty-lab']!;
    expect(storedCommits['reconcile-dirty-lab']).toBe(staleSha);
    expect(runYaml).toContain(`exact_sha: ${staleSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(staleSha);
    // The original repository stays on main at the unchanged tip with its
    // dirty bytes preserved byte-for-byte.
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe(dirtyStatus);
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# reconcile-dirty-lab (externally edited)\n',
    );
    transcript.step('creation continued at the unchanged tip with the dirty bytes preserved');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
