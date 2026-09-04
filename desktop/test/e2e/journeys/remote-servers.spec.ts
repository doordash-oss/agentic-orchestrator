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
 * Remote server journeys (packaged app): the harness spawns the bundled
 * server as a test-owned child with an ISOLATED state dir deliberately
 * outside the app's registry knowledge — the server runs with its own HOME,
 * so nothing it publishes ever lands in the registry directory the app
 * scans, and its runtime state dir can never match the duplicate-local
 * guard. The only knowledge the app ever gets of it is the connection
 * string, assembled from its per-runtime discovery record
 * (`agentico://<token>@<host>:<port>?name=…`, grammar in
 * internal/server/connectstring.go).
 *
 * [agentico capability: macOS Keychain availability; probe: `security list-keychains`]
 * The add flow persists the bearer token through Electron's safeStorage
 * (the real backend). When the host has no OS keychain the app deliberately
 * persists NOTHING and reports the session-only outcome; every journey
 * below detects that through the main process's own
 * `safeStorage.isEncryptionAvailable()` and either asserts the session-only
 * surface (journeys a/b) or ends with a clearly-logged skip (journey d).
 *
 *   (a) add       — paste string in Settings → Servers, probe, save,
 *                   auto-switch; the workspace remounts against the remote
 *   (b) persist   — relaunch: silent reconnect via the stored token;
 *                   settings.json bytes stay token-free
 *   (c) failures  — wrong token / dead host / malformed string each surface
 *                   their distinct inline error and persist nothing
 *   (d) switch    — local↔remote both directions; per-server selection and
 *                   workspace truth restore (server-switching.spec.ts style)
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { expect, test, type Page, type TestInfo } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import { tailText } from '../helpers/runtime';
import {
  createRepo,
  createWorld,
  destroyWorld,
  minimalEnv,
  waitFor,
  type DiscoveryRecord,
  type JourneyWorld,
} from '../helpers/world';

const AUTH = { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' } as const;
const REMOTE_NAME = 'fossa-remote';

// --- the test-owned remote server ---------------------------------------------

interface RemoteTestServer {
  name: string;
  runtimeDir: string;
  stateDir: string;
  port: number;
  token: string;
  connectionString: string;
  proc: ChildProcess;
  logs: string[];
}

/** Reserves a free loopback port for the test-owned --listen server. */
async function freeLoopbackPort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address();
      probe.close(() => {
        if (address === null || typeof address === 'string') {
          reject(new Error('could not reserve a loopback port'));
          return;
        }
        resolve(address.port);
      });
    });
  });
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

/** Mirrors internal/server/connectstring.go's ConnectionStringFromBaseURL. */
function connectionStringFor(record: DiscoveryRecord, name: string): string {
  if (record.auth_token === undefined || record.auth_token === '') {
    throw new Error('the test-owned server published no auth token');
  }
  const host = new URL(record.base_url).host;
  return `agentico://${record.auth_token}@${host}?name=${encodeURIComponent(name)}`;
}

/**
 * Starts the bundled server so that NOTHING about it reaches the app's
 * registry knowledge: its state dir lives outside the app's state-dir
 * conventions, and its own HOME keeps its registry entry out of the
 * `<world.home>/.agentic-orchestrator/servers` directory the app scans.
 */
async function startRemoteServer(world: JourneyWorld, name: string): Promise<RemoteTestServer> {
  const runtimeDir = path.join(world.root, `remote-${name}`);
  const homeDir = path.join(world.root, `remote-${name}-home`);
  const stateDir = path.join(runtimeDir, 'features');
  const configPath = path.join(runtimeDir, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.mkdirSync(homeDir, { recursive: true });
  fs.copyFileSync(world.configPath, configPath);
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
  return {
    name,
    runtimeDir,
    stateDir,
    port,
    token: record.auth_token,
    connectionString: connectionStringFor(record, name),
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

// --- app-side facts -----------------------------------------------------------

interface RegistryEntry {
  runtime: { runtime_dir: string };
}

/** The registry directory the APP scans (its own HOME). */
function readAppRegistry(world: JourneyWorld): RegistryEntry[] {
  const dir = path.join(world.home, '.agentic-orchestrator', 'servers');
  let names: string[];
  try {
    names = fs.readdirSync(dir).filter((name) => name.endsWith('.json'));
  } catch {
    return [];
  }
  return names.map(
    (name) => JSON.parse(fs.readFileSync(path.join(dir, name), 'utf8')) as RegistryEntry,
  );
}

async function connectionState(handle: AppHandle) {
  return handle.page.evaluate(() => window.agentico.getConnectionStatus());
}

/** The real safeStorage backend, probed through the main process itself. */
async function keychainAvailable(handle: AppHandle): Promise<boolean> {
  return handle.app.evaluate(({ safeStorage }) => safeStorage.isEncryptionAvailable());
}

function noteCapabilitySkip(testInfo: TestInfo, transcript: Transcript, journey: string): void {
  const note =
    `SKIP (capability): no OS keychain on this host — the ${journey} journey's ` +
    'keychain-backed assertions were replaced by the documented alternate path ' +
    '[agentico capability: macOS Keychain; probe: security list-keychains]';
  transcript.step(note);
  testInfo.annotations.push({ type: 'capability', description: note });
}

/** Pastes `value` into the Servers pane add field and probes. */
async function pasteAndProbe(settings: Page, value: string): Promise<void> {
  const pasteField = settings.getByRole('textbox', { name: /add a remote server/i });
  await pasteField.fill(value);
  await settings.getByRole('button', { name: 'Probe and connect' }).click();
}

function addPaneAlert(settings: Page) {
  return settings.locator('.settings-panel__server-add').getByRole('alert');
}

// --- (a) primary: paste, probe, save, auto-switch, remount ---------------------

test('remote add: paste the connection string, probe, save, auto-switch, remount', async ({}, testInfo) => {
  const transcript = new Transcript(
    'remote-servers-add',
    'Remote add via connection string (packaged)',
  );
  const world = createWorld('remote-servers-add', { auth: AUTH, presetWorkspaceRoot: true });
  createRepo(world, 'remote-lab', { commit: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start the isolated test-owned server (its own HOME, its own state dir)');
    remote = await startRemoteServer(world, REMOTE_NAME);
    // Nothing it publishes may reach the registry the app will scan.
    expect(readAppRegistry(world)).toEqual([]);
    transcript.step(
      `remote listening on 127.0.0.1:${String(remote.port)}, outside the app registry`,
    );

    transcript.section('Launch the app: it spawns its default local child');
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-add-launch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const localState = await connectionState(handle);
    expect(localState.status).toBe('ready');
    expect(localState.ownership).toBe('app-owned');
    const localName = localState.serverName ?? '';
    expect(localName).not.toBe('');
    // The only registry entry now is the app-owned child's — never the remote's.
    const registry = readAppRegistry(world);
    expect(registry).toHaveLength(1);
    expect(registry[0]!.runtime.runtime_dir).not.toBe(remote.runtimeDir);
    transcript.step('the remote server never entered the app registry (isolation holds)');

    transcript.section('Settings → Servers: paste the string, probe and connect');
    const keychain = await keychainAvailable(handle);
    transcript.step(`OS keychain available (safeStorage): ${String(keychain)}`);
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await pasteAndProbe(settings, remote.connectionString);

    if (!keychain) {
      // Documented skip-alternate: session-only semantics, nothing persists.
      await expect(settings.getByText(/OS keychain on this machine is unavailable/)).toBeVisible({
        timeout: 60_000,
      });
      const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
      expect(prefs.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
      expect((await connectionState(handle)).serverName).toBe(localName);
      transcript.step('session-only outcome asserted; nothing persisted, no switch');
      noteCapabilitySkip(testInfo, transcript, 'add');
      persistAppLogs(handle, 'remote-servers-add-app');
      transcript.write(testInfo);
      return;
    }

    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await evidenceShot(handle, 'remote-servers-added', settings);

    transcript.section('The workspace auto-switches and remounts against the remote server');
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the auto-switch to the remote server',
      60_000,
    );
    const switched = await connectionState(handle);
    expect(switched.status).toBe('ready');
    expect(switched.ownership).toBe('external');
    // The remote's own truth: the local child's feature-free workspace remounts
    // as the remounted remote workspace (still feature-free, but remote-keyed).
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(handle.page.locator('.sidebar__footer')).toContainText(REMOTE_NAME);

    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    const remoteEntry = prefs.servers.known.find((entry) => entry.kind === 'remote');
    expect(remoteEntry).toBeDefined();
    expect(remoteEntry!.name).toBe(REMOTE_NAME);
    expect(prefs.servers.lastUsed).toBe(remoteEntry!.serverKey);
    expect(switched.serverKey).toBe(remoteEntry!.serverKey);
    // The stored token never crosses an IPC-visible surface.
    expect(JSON.stringify(prefs)).not.toContain(remote.token);
    expect(JSON.stringify(switched)).not.toContain(remote.token);

    transcript.section('The Servers pane and the footer carry the remote locality badge');
    const remoteRow = settings.locator('.settings-panel__server[data-kind="remote"]');
    await expect(remoteRow).toHaveCount(1);
    await expect(remoteRow.locator('.settings-panel__server-kind')).toHaveText('Remote');
    await expect(remoteRow.locator('.settings-panel__server-status')).toHaveText('Connected', {
      timeout: 60_000,
    });
    await handle.page.getByRole('button', { name: `${REMOTE_NAME} — switch server` }).click();
    const remoteOption = handle.page.getByRole('option', { name: `${REMOTE_NAME} — Connected` });
    await expect(remoteOption).toBeVisible({ timeout: 30_000 });
    await expect(remoteOption.locator('.settings-panel__server-kind')).toHaveText('Remote');
    await expect(
      handle.page.getByRole('option', { name: new RegExp(`${localName} at .+ — Available`) }),
    ).toBeVisible();
    await handle.page.keyboard.press('Escape');
    transcript.step('footer popover marks the remote entry with the Remote badge');
    await evidenceShot(handle, 'remote-servers-switched');

    transcript.section('Token at rest: settings.json bytes stay token-free');
    const settingsBytes = fs.readFileSync(path.join(world.userData, 'settings.json'), 'utf8');
    expect(settingsBytes).not.toContain(remote.token);
    expect(settingsBytes).toContain(remoteEntry!.serverKey);
    transcript.step(
      'settings.json references the serverKey only; the token lives in the keychain blob',
    );

    persistAppLogs(handle, 'remote-servers-add-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (b) persistence: silent reconnect, token-free settings -------------------

test('remote persistence: relaunch silently reconnects; no token file bytes leak', async ({}, testInfo) => {
  test.setTimeout(360_000); // two full packaged launches against one world
  const transcript = new Transcript(
    'remote-servers-persistence',
    'Remote persistence and token-at-rest isolation (packaged)',
  );
  const world = createWorld('remote-servers-persistence', {
    auth: AUTH,
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'remote-lab', { commit: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  const settingsFile = path.join(world.userData, 'settings.json');
  const tokenStoreFile = path.join(world.userData, 'remote-tokens.json');
  try {
    transcript.section('First launch: add the remote server');
    remote = await startRemoteServer(world, REMOTE_NAME);
    expect(readAppRegistry(world)).toEqual([]);
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-persistence-first' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const keychain = await keychainAvailable(handle);
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await pasteAndProbe(settings, remote.connectionString);

    if (!keychain) {
      // Documented skip-alternate: nothing is persisted, and the relaunch
      // below proves the remote is genuinely gone from the next session.
      await expect(settings.getByText(/OS keychain on this machine is unavailable/)).toBeVisible({
        timeout: 60_000,
      });
      expect(fs.existsSync(tokenStoreFile)).toBe(false);
      noteCapabilitySkip(testInfo, transcript, 'persistence');
      persistAppLogs(handle, 'remote-servers-persistence-first-app');
      await closeApp(handle);
      handle = null;
      handle = await launchApp(world, testInfo, { traceName: 'remote-servers-persistence-second' });
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 90_000,
      });
      const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
      expect(prefs.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
      expect((await connectionState(handle)).ownership).toBe('app-owned');
      transcript.step('relaunch after session-only add: no remote entry, no reconnect');
      persistAppLogs(handle, 'remote-servers-persistence-second-app');
      transcript.write(testInfo);
      return;
    }

    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the auto-switch to the remote server',
      60_000,
    );
    const firstSwitched = await connectionState(handle);
    const remoteKey = firstSwitched.serverKey;

    transcript.section('Quit and relaunch: the last-used remote reconnects silently');
    persistAppLogs(handle, 'remote-servers-persistence-first-app');
    await closeApp(handle);
    handle = null;
    // The test-owned server outlives the app (ownership `external`).
    expect(remote.proc.exitCode).toBeNull();

    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-persistence-relaunch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    // No startup picker appeared on the way: the stored token attached directly.
    await expect(
      handle.page.getByRole('listbox', { name: /running agentico servers/i }),
    ).toHaveCount(0);
    const reconnected = await connectionState(handle);
    expect(reconnected.status).toBe('ready');
    expect(reconnected.ownership).toBe('external');
    expect(reconnected.serverName).toBe(REMOTE_NAME);
    expect(reconnected.serverKey).toBe(remoteKey);
    await expect(handle.page.locator('.sidebar__footer')).toContainText(REMOTE_NAME);
    transcript.step('silent reconnect to the last-used remote through the stored token');

    transcript.section('Token-at-rest isolation: settings.json and the blob stay token-free');
    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(prefs.servers.lastUsed).toBe(remoteKey);
    expect(JSON.stringify(prefs)).not.toContain(remote.token);
    const settingsBytes = fs.readFileSync(settingsFile, 'utf8');
    expect(settingsBytes).not.toContain(remote.token);
    // The blob exists and carries ciphertext only.
    const blobBytes = fs.readFileSync(tokenStoreFile, 'utf8');
    expect(blobBytes).not.toContain(remote.token);
    expect(blobBytes).toContain(remoteKey);
    transcript.step('settings.json and remote-tokens.json bytes contain no token material');
    await evidenceShot(handle, 'remote-servers-reconnected');

    persistAppLogs(handle, 'remote-servers-persistence-relaunch-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (c) failure matrix: wrong token, dead host, malformed string --------------

test('remote add failures: distinct inline errors, nothing persisted, nothing survives relaunch', async ({}, testInfo) => {
  test.setTimeout(360_000); // includes one relaunch for the persistence assertion
  const transcript = new Transcript(
    'remote-servers-failures',
    'Remote add failure matrix (packaged)',
  );
  const world = createWorld('remote-servers-failures', { auth: AUTH, presetWorkspaceRoot: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('A healthy remote exists, but every add attempt is bad');
    remote = await startRemoteServer(world, REMOTE_NAME);
    expect(readAppRegistry(world)).toEqual([]);
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-failures-launch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    const badPort = await freeLoopbackPort();
    const wrongToken = (remote.token.startsWith('A') ? 'B' : 'A') + remote.token.slice(1);
    // Titles are the desktop catalog's authored ErrorSurface titles.
    const cases: { label: string; connectionString: string; title: string }[] = [
      {
        label: 'malformed scheme',
        connectionString: 'https://not-a-token@127.0.0.1:1',
        title: 'The connection string could not be parsed',
      },
      {
        label: 'dead host',
        connectionString: `agentico://${remote.token}@127.0.0.1:${String(badPort)}?name=ghost-remote`,
        title: 'The server could not be reached',
      },
      {
        label: 'wrong token',
        connectionString: remote.connectionString.replace(remote.token, wrongToken),
        title: 'The token was rejected',
      },
    ];
    for (const candidate of cases) {
      transcript.section(`Failure: ${candidate.label}`);
      await pasteAndProbe(settings, candidate.connectionString);
      await expect(addPaneAlert(settings)).toContainText(candidate.title, { timeout: 60_000 });
      transcript.step(`distinct inline error: "${candidate.title}"`);
    }
    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(prefs.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
    expect(fs.existsSync(path.join(world.userData, 'remote-tokens.json'))).toBe(false);
    transcript.step('no settings entry and no token blob after any failure');
    await evidenceShot(handle, 'remote-servers-failures', settings);

    transcript.section('Relaunch: the bad adds are gone');
    persistAppLogs(handle, 'remote-servers-failures-first-app');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-failures-relaunch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const afterRelaunch = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(afterRelaunch.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
    transcript.step('nothing persisted across the relaunch');

    persistAppLogs(handle, 'remote-servers-failures-relaunch-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (d) switch: local↔remote with per-server context restored -----------------

const SWITCH_FEATURE = 'Remote Switching Anchor Fixture';

test('local↔remote switching: per-server selection and workspace truth restore', async ({}, testInfo) => {
  const transcript = new Transcript(
    'remote-servers-switch',
    'Local↔remote switching via the footer popover (packaged)',
  );
  const world = createWorld('remote-servers-switch', { auth: AUTH, presetWorkspaceRoot: true });
  createRepo(world, 'switch-lab', { commit: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('Local child plus the isolated remote');
    remote = await startRemoteServer(world, REMOTE_NAME);
    expect(readAppRegistry(world)).toEqual([]);
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-switch-launch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const localState = await connectionState(handle);
    expect(localState.ownership).toBe('app-owned');
    const localName = localState.serverName ?? '';
    expect(localName).not.toBe('');

    transcript.section('Add the remote (auto-switches to it)');
    if (!(await keychainAvailable(handle))) {
      noteCapabilitySkip(testInfo, transcript, 'switch');
      persistAppLogs(handle, 'remote-servers-switch-app');
      transcript.write(testInfo);
      return;
    }
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await pasteAndProbe(settings, remote.connectionString);
    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the auto-switch to the remote server',
      60_000,
    );
    const onRemote = await connectionState(handle);
    expect(onRemote.status).toBe('ready');
    const remoteKey = onRemote.serverKey;

    transcript.section('Remote truth: create a feature that the local server cannot see');
    const featureId = await handle.page.evaluate(
      async (name) =>
        (
          await window.agentico.createFeature({
            name,
            description: 'Selection anchor for the local↔remote switching journey.',
            repoKeys: ['switch-lab'],
            useCurrentBranch: false,
          })
        ).featureId,
      SWITCH_FEATURE,
    );
    await handle.page.getByRole('option', { name: new RegExp(SWITCH_FEATURE) }).click();
    const remotePrefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(remotePrefs.shell.featureByServer[remoteKey!]).toBe(featureId);
    expect(JSON.stringify(remotePrefs)).not.toContain(remote.token);

    transcript.section('Switch remote→local via the footer popover: local context restored');
    await handle.page.getByRole('button', { name: `${REMOTE_NAME} — switch server` }).click();
    await expect(handle.page.getByRole('listbox', { name: 'Servers' })).toBeVisible({
      timeout: 30_000,
    });
    const remoteRowCurrent = handle.page.getByRole('option', {
      name: `${REMOTE_NAME} — Connected`,
    });
    await expect(remoteRowCurrent).toBeVisible({ timeout: 30_000 });
    await expect(remoteRowCurrent.locator('.settings-panel__server-kind')).toHaveText('Remote');
    await handle.page
      .getByRole('option', { name: new RegExp(`${localName} at .+ — Available`) })
      .click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === localName,
      'the switch back to the local server',
      60_000,
    );
    const backOnLocal = await connectionState(handle);
    expect(backOnLocal.status).toBe('ready');
    expect(backOnLocal.ownership).toBe('app-owned');
    // The local server never heard of the remote feature; no selection was
    // recorded for it, so the shell lands on Overview — no stale cursor.
    await expect(handle.page.getByRole('option', { name: new RegExp(SWITCH_FEATURE) })).toHaveCount(
      0,
    );
    await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
      { timeout: 60_000 },
    );
    transcript.step('remote→local: remote feature absent, own (empty) selection restored');

    transcript.section('Switch local→remote: the remote selection restores');
    await handle.page.getByRole('button', { name: `${localName} — switch server` }).click();
    await handle.page.getByRole('option', { name: `${REMOTE_NAME} — Available` }).click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the re-attach to the remote server',
      60_000,
    );
    await expect(
      handle.page.getByRole('option', { name: new RegExp(SWITCH_FEATURE) }),
    ).toHaveAttribute('aria-selected', 'true', { timeout: 60_000 });
    const restoredPrefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(restoredPrefs.shell.featureByServer[remoteKey!]).toBe(featureId);
    expect(restoredPrefs.servers.lastUsed).toBe(remoteKey);
    transcript.step('local→remote: feature list, selection, and lastUsed all restored');
    await evidenceShot(handle, 'remote-servers-switch-back');

    persistAppLogs(handle, 'remote-servers-switch-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (e) cold start: the connection-string deep link launched the app ----------

test('remote cold start: a connection-string deep link attaches without spawning the local child', async ({}, testInfo) => {
  const transcript = new Transcript(
    'remote-servers-cold-start',
    'Cold-start connection-string deep link (packaged)',
  );
  const world = createWorld('remote-servers-cold-start', { auth: AUTH, presetWorkspaceRoot: true });
  createRepo(world, 'remote-lab', { commit: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start the isolated test-owned server (its own HOME, its own state dir)');
    remote = await startRemoteServer(world, REMOTE_NAME);
    expect(readAppRegistry(world)).toEqual([]);
    transcript.step(
      `remote listening on 127.0.0.1:${String(remote.port)}, outside the app registry`,
    );

    transcript.section('Launch the app with the connection string as its protocol argument');
    // The OS delivers a protocol launch as argv (Windows/Linux) or as a
    // pre-ready open-url event that the app buffers (macOS); both resolve
    // through the same cold-start route, exercised here via argv.
    handle = await launchApp(world, testInfo, {
      traceName: 'remote-servers-cold-start-launch',
      args: [remote.connectionString],
    });
    const keychain = await keychainAvailable(handle);
    transcript.step(`OS keychain available (safeStorage): ${String(keychain)}`);

    if (!keychain) {
      // Documented skip-alternate: without an OS keychain the add resolves
      // session-only — nothing persists and the app does not switch — and
      // the cold-start path must still never fall back to spawning the child.
      await waitFor(
        async () => {
          const state = await connectionState(handle!);
          return state.status === 'error' || state.status === 'ready';
        },
        'the cold-start attempt to settle',
        90_000,
      );
      const settled = await connectionState(handle);
      expect(settled.serverName ?? null).not.toBe(REMOTE_NAME);
      expect(JSON.stringify(settled)).not.toContain(remote.token);
      expect(readAppRegistry(world)).toEqual([]);
      const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
      expect(prefs.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
      transcript.step('session-only outcome: no switch, nothing persisted, no local child spawned');
      noteCapabilitySkip(testInfo, transcript, 'cold-start');
      persistAppLogs(handle, 'remote-servers-cold-start-app');
      transcript.write(testInfo);
      return;
    }

    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the cold-start attach to the remote server',
      90_000,
    );
    const attached = await connectionState(handle);
    expect(attached.status).toBe('ready');
    expect(attached.ownership).toBe('external');
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(handle.page.locator('.sidebar__footer')).toContainText(REMOTE_NAME);
    expect(JSON.stringify(attached)).not.toContain(remote.token);
    transcript.step('the app attached to the linked server directly');

    transcript.section('The bundled local child was never spawned');
    // A spawned child publishes itself in the app registry; a remote attach
    // records only a known-servers entry.
    expect(readAppRegistry(world)).toEqual([]);
    transcript.step('app registry stays empty: no app-owned server exists');

    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    const remoteEntry = prefs.servers.known.find((entry) => entry.kind === 'remote');
    expect(remoteEntry).toBeDefined();
    expect(remoteEntry!.name).toBe(REMOTE_NAME);
    expect(prefs.servers.lastUsed).toBe(remoteEntry!.serverKey);
    expect(JSON.stringify(prefs)).not.toContain(remote.token);
    transcript.step('the remote is persisted as last-used, token-free');

    persistAppLogs(handle, 'remote-servers-cold-start-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (f) cold start: the link is unusable → visible failure, nothing spawned ---

test('remote cold start: an unusable link fails startup visibly instead of spawning the local child', async ({}, testInfo) => {
  const transcript = new Transcript(
    'remote-servers-cold-start-unusable',
    'Cold-start deep link to an unreachable server (packaged)',
  );
  const world = createWorld('remote-servers-cold-start-unusable', {
    auth: AUTH,
    presetWorkspaceRoot: true,
  });
  let handle: AppHandle | null = null;
  try {
    transcript.section('Reserve a loopback port nobody listens on');
    const port = await freeLoopbackPort();
    const link = `agentico://not-a-real-token@127.0.0.1:${String(port)}?name=ghost`;
    transcript.step(`link targets 127.0.0.1:${String(port)} with no server behind it`);

    transcript.section('Launch the app from that link');
    handle = await launchApp(world, testInfo, {
      traceName: 'remote-servers-cold-start-unusable-launch',
      args: [link],
    });
    const shell = handle.page.getByLabel('Agentico connection');
    await expect(shell).toBeVisible();
    await expect(handle.page.locator('.shell-card__status-label[data-status="error"]')).toBeVisible(
      { timeout: 60_000 },
    );
    await expect(shell).toContainText('E_REMOTE_UNREACHABLE');
    await expect(shell).toContainText('The server this launch was linked to could not be added.');
    await expect(shell).toContainText('launched from a server link');
    const buttons = await shell.getByRole('button').allTextContents();
    // Retry re-attempts the link; the bundled runtime is only ever an
    // explicit choice, never an automatic fallback.
    expect(buttons.filter((label) => label !== 'Explain in chat')).toEqual([
      'Retry',
      'Start bundled runtime',
    ]);
    await evidenceShot(handle, 'remote-servers-cold-start-unusable');
    transcript.step(
      'startup failed on the connection surface with the pipeline error, Retry, and the escape hatch',
    );

    transcript.section('The bundled runtime was never substituted for the linked server');
    const state = await connectionState(handle);
    expect(state.status).toBe('error');
    expect(state.ownership).toBe('none');
    expect(readAppRegistry(world)).toEqual([]);
    expect(JSON.stringify(state)).not.toContain('not-a-real-token');
    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(prefs.servers.known.filter((entry) => entry.kind === 'remote')).toEqual([]);
    transcript.step('no app-owned server, nothing persisted');

    transcript.section('Retry re-attempts the link, not the standard startup');
    await shell.getByRole('button', { name: 'Retry' }).click();
    await expect(handle.page.locator('.shell-card__status-label[data-status="error"]')).toBeVisible(
      { timeout: 60_000 },
    );
    await expect(shell).toContainText('E_REMOTE_UNREACHABLE');
    expect(readAppRegistry(world)).toEqual([]);
    transcript.step('still failed, still nothing spawned');

    transcript.section('Start bundled runtime is the explicit way out');
    await shell.getByRole('button', { name: 'Start bundled runtime' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const local = await connectionState(handle);
    expect(local.status).toBe('ready');
    expect(local.ownership).toBe('app-owned');
    expect(local.status === 'ready' && local.kind).toBe('local');
    expect(readAppRegistry(world)).toHaveLength(1);
    expect(readAppRegistry(world)[0]!.runtime.runtime_dir).toBe(local.connectedRuntimeDir);
    transcript.step('the bundled runtime started on request: one app-owned registry entry');

    persistAppLogs(handle, 'remote-servers-cold-start-unusable-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// --- (g) this machine: back to the bundled runtime after a remote took over ---

test('this machine: Settings starts the bundled runtime after a relaunch attached to the remote', async ({}, testInfo) => {
  test.setTimeout(360_000); // two full packaged launches against one world
  const transcript = new Transcript(
    'remote-servers-this-machine',
    'Settings → Servers "This machine" Start action after a remote relaunch (packaged)',
  );
  const world = createWorld('remote-servers-this-machine', {
    auth: AUTH,
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'remote-lab', { commit: true });
  let remote: RemoteTestServer | null = null;
  let handle: AppHandle | null = null;
  const machineRow = (settings: Page) =>
    settings.locator('.settings-panel__server[data-bundled="true"]');
  try {
    transcript.section('First launch: the bundled runtime, then add the remote from Settings');
    remote = await startRemoteServer(world, REMOTE_NAME);
    handle = await launchApp(world, testInfo, { traceName: 'remote-servers-this-machine-first' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const localName = (await connectionState(handle)).serverName ?? '';
    expect(localName).not.toBe('');
    if (!(await keychainAvailable(handle))) {
      noteCapabilitySkip(testInfo, transcript, 'this-machine');
      persistAppLogs(handle, 'remote-servers-this-machine-first-app');
      transcript.write(testInfo);
      return;
    }
    let settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await expect(machineRow(settings)).toHaveCount(1);
    await expect(machineRow(settings).locator('.settings-panel__server-status')).toHaveText(
      'Connected',
      { timeout: 30_000 },
    );
    await pasteAndProbe(settings, remote.connectionString);
    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the auto-switch to the remote server',
      60_000,
    );
    const remoteKey = (await connectionState(handle)).serverKey;
    // The app-owned child survives the switch-away, so the row reads Running.
    await expect(machineRow(settings).locator('.settings-panel__server-status')).toHaveText(
      'Running',
      { timeout: 30_000 },
    );
    await expect(
      machineRow(settings).getByRole('button', { name: 'Switch to This machine' }),
    ).toBeVisible();
    transcript.step('remote added and current; This machine reads Running with a Switch action');

    transcript.section('Relaunch: the last-used remote wins and no local child is spawned');
    persistAppLogs(handle, 'remote-servers-this-machine-first-app');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, {
      traceName: 'remote-servers-this-machine-relaunch',
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const reconnected = await connectionState(handle);
    expect(reconnected.serverName).toBe(REMOTE_NAME);
    expect(reconnected.ownership).toBe('external');

    transcript.section('Settings → Servers: This machine is Not running; Start it');
    settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await expect(machineRow(settings)).toHaveCount(1);
    await expect(machineRow(settings).locator('.settings-panel__server-status')).toHaveText(
      'Not running',
      { timeout: 30_000 },
    );
    // Exactly one row for the machine: the persisted local entry folded in,
    // no second "Unreachable" row for the same runtime.
    await expect(settings.locator('.settings-panel__server[data-kind="local"]')).toHaveCount(1);
    await expect(settings.getByText('Unreachable')).toHaveCount(0);
    // The list snapshot came from a registry scan, which pruned the dead entry.
    expect(readAppRegistry(world)).toEqual([]);
    await evidenceShot(handle, 'remote-servers-this-machine-not-running');
    await machineRow(settings).getByRole('button', { name: 'Start This machine' }).click();
    await waitFor(
      async () => {
        const state = await connectionState(handle!);
        return state.status === 'ready' && state.ownership === 'app-owned';
      },
      'the bundled runtime to start and take the connection',
      90_000,
    );
    const onLocal = await connectionState(handle);
    expect(onLocal.status === 'ready' && onLocal.kind).toBe('local');
    expect(onLocal.serverName).toBe(localName);
    const registry = readAppRegistry(world);
    expect(registry).toHaveLength(1);
    expect(registry[0]!.runtime.runtime_dir).toBe(onLocal.connectedRuntimeDir);
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(machineRow(settings).locator('.settings-panel__server-status')).toHaveText(
      'Connected',
      { timeout: 30_000 },
    );
    const prefs = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(prefs.servers.known.find((entry) => entry.serverKey === remoteKey)?.kind).toBe('remote');
    expect(prefs.servers.lastUsed).toBe(onLocal.serverKey);
    expect(JSON.stringify(prefs)).not.toContain(remote.token);
    transcript.step(
      'app-owned local started: one registry entry, remote still known, local last-used',
    );

    transcript.section('Back to the remote through the footer popover');
    await handle.page.getByRole('button', { name: `${localName} — switch server` }).click();
    await handle.page.getByRole('option', { name: `${REMOTE_NAME} — Available` }).click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === REMOTE_NAME,
      'the switch back to the remote server',
      60_000,
    );
    expect((await connectionState(handle)).ownership).toBe('external');
    await expect(machineRow(settings).locator('.settings-panel__server-status')).toHaveText(
      'Running',
      { timeout: 30_000 },
    );
    transcript.step('remote current again; the started child survives as Running');

    persistAppLogs(handle, 'remote-servers-this-machine-relaunch-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (remote !== null) {
      await stopRemoteServer(remote);
      if (remote.logs.length > 0) {
        transcript.codeBlock('remote server stderr tail', tailText(remote.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
