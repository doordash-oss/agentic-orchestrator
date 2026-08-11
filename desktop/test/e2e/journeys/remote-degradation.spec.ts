/**
 * Remote-degradation journey (packaged app): attaches to a test-owned
 * loopback server EXACTLY like remote-servers.spec.ts — own HOME, own state
 * dir, outside the app registry — then proves every same-filesystem feature
 * degrades with an explanation instead of silently misbehaving:
 *
 *   (1) creation composer gating: no native browse dialog, typed path entry,
 *       attach affordances disabled with the shared explanation, a dropped
 *       file intercepted with a notice, '@'-mention explains instead of
 *       searching
 *   (2) the create→run→complete cycle runs against the remote-profile
 *       server, and the completion surface swaps Reveal in Finder for Copy
 *       Path, copying the server-reported worktree path to the clipboard
 *   (3) workspace-root text entry surfaces the server's own 4xx validation
 *       inline and accepts a valid path
 *   (4) the negative invariant: a request-capturing proxy between the app
 *       and the server records every mutation body — none may carry a
 *       local staged-artifact path
 *   (5) switching back to the local server restores every affordance
 *
 * [agentico capability: macOS Keychain availability; probe: `security list-keychains`]
 * The attach persists the bearer token through Electron's safeStorage (the
 * real backend); on hosts without an OS keychain the journey ends with a
 * clearly-logged capability skip, matching the remote-servers journeys.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { expect, test, type TestInfo } from '@playwright/test';
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
import {
  clearRunFailures,
  parseFeatureRepos,
  replaceTopLevelBlock,
  upsertYamlScalar,
  writeWorktreeChange,
} from '../helpers/completionFixture';
import { Transcript } from '../helpers/transcript';
import {
  createPlainFolder,
  createRepo,
  createWorld,
  destroyWorld,
  minimalEnv,
  waitFor,
  type DiscoveryRecord,
  type JourneyWorld,
} from '../helpers/world';

const AUTH = { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' } as const;
const REMOTE_NAME = 'degradation-remote';
const REPO = 'degradation-lab';
const FEATURE_NAME = 'Remote degradation feature';
/** Sentinel: a staged-artifact name that must NEVER reach the server. */
const DROP_SENTINEL = 'remote-degradation-drop-sentinel.png';

interface RecordedRequest {
  method: string;
  url: string;
  body: string;
}

// --- the test-owned remote server (remote-servers.spec.ts conventions) --------

interface RemoteTestServer {
  runtimeDir: string;
  stateDir: string;
  port: number;
  token: string;
  proc: ChildProcess;
}

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

/**
 * (Re)starts the test-owned server on the reserved port. On restart the
 * state dir already exists from the first boot, so seeded completion state
 * survives; its own HOME keeps it out of the app's registry forever.
 */
async function startRemoteServer(world: JourneyWorld, port: number): Promise<RemoteTestServer> {
  const runtimeDir = path.join(world.root, `remote-${REMOTE_NAME}`);
  const homeDir = path.join(world.root, `remote-${REMOTE_NAME}-home`);
  const stateDir = path.join(runtimeDir, 'features');
  const configPath = path.join(runtimeDir, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.mkdirSync(homeDir, { recursive: true });
  if (!fs.existsSync(configPath)) fs.copyFileSync(world.configPath, configPath);
  const proc = spawn(
    bundledServerBinary(packagedExecutable()),
    [
      'server',
      '--config',
      configPath,
      '--state-dir',
      stateDir,
      '--name',
      REMOTE_NAME,
      '--listen',
      `127.0.0.1:${String(port)}`,
    ],
    { env: { ...minimalEnv(world), HOME: homeDir }, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  proc.stdout?.on('data', () => {});
  proc.stderr?.on('data', () => {});
  await waitFor(() => discoveryAt(runtimeDir) !== null, `${REMOTE_NAME} discovery record`, 30_000);
  const record = discoveryAt(runtimeDir)!;
  if (record.auth_token === undefined || record.auth_token === '') {
    throw new Error(`${REMOTE_NAME} discovery record carries no token`);
  }
  return { runtimeDir, stateDir, port, token: record.auth_token, proc };
}

async function stopRemoteServer(server: RemoteTestServer): Promise<void> {
  if (server.proc.exitCode !== null || server.proc.signalCode !== null) return;
  server.proc.kill('SIGKILL');
  await waitFor(
    () => server.proc.exitCode !== null || server.proc.signalCode !== null,
    'remote server exit',
    15_000,
  ).catch(() => {});
}

// --- the request-capturing proxy the app attaches through ----------------------

interface CaptureProxy {
  port: number;
  requests: RecordedRequest[];
  close(): Promise<void>;
}

/**
 * Transparent loopback forwarder in front of the test-owned server. The app
 * attaches through it, so it sees every mutation the app ever sends the
 * server — the harness-level half of the no-local-paths-leak invariant.
 */
async function startCaptureProxy(targetPort: number): Promise<CaptureProxy> {
  const requests: RecordedRequest[] = [];
  const proxy = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk: Buffer) => chunks.push(chunk));
    req.on('end', () => {
      const body = Buffer.concat(chunks);
      const method = req.method ?? 'GET';
      const url = req.url ?? '/';
      if (method === 'POST' || method === 'PATCH' || method === 'PUT') {
        requests.push({ method, url, body: body.toString('utf8') });
      }
      const headers = { ...req.headers };
      delete headers['transfer-encoding'];
      headers.host = `127.0.0.1:${String(targetPort)}`;
      headers['content-length'] = String(body.length);
      const upstream = http.request(
        { host: '127.0.0.1', port: targetPort, path: url, method, headers },
        (upstreamResponse) => {
          res.writeHead(upstreamResponse.statusCode ?? 502, upstreamResponse.headers);
          upstreamResponse.pipe(res);
        },
      );
      upstream.on('error', () => {
        if (!res.headersSent) res.writeHead(502);
        res.end();
      });
      upstream.write(body);
      upstream.end();
    });
  });
  const port = await freeLoopbackPort();
  await new Promise<void>((resolve, reject) => {
    proxy.once('error', reject);
    proxy.listen(port, '127.0.0.1', resolve);
  });
  return {
    port,
    requests,
    close: () =>
      new Promise<void>((resolve) => {
        proxy.close(() => resolve());
      }),
  };
}

/** Asserts the ready-state locality signal; the union only carries it when ready. */
function assertKind(
  state: Awaited<ReturnType<typeof window.agentico.getConnectionStatus>>,
  expected: 'local' | 'remote',
): void {
  expect(state.status).toBe('ready');
  if (state.status !== 'ready') throw new Error('expected a ready connection');
  expect(state.kind).toBe(expected);
}
function assertNoLocalPathPayloads(requests: RecordedRequest[]): void {
  const leaked = requests.filter(
    (request) => request.body.includes(DROP_SENTINEL) || request.body.includes('file://'),
  );
  expect(leaked.map((request) => `${request.method} ${request.url}: ${request.body}`)).toEqual([]);
}

// --- completion seeding on the remote server's OWN state dir ------------------

/**
 * Seeds a completed feature into the remote server's state dir — the
 * completionFixture conventions applied to `remoteStateDir` directly, while
 * the server is stopped (seed.ts requires a stopped server).
 */
function seedRemoteCompletion(remoteStateDir: string, featureId: string): string {
  const featurePath = path.join(remoteStateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const worktree = parseFeatureRepos(featureYaml)[REPO];
  if (worktree === undefined) throw new Error('degradation fixture worktree missing');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'CodeReady');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '3');
  fs.writeFileSync(featurePath, featureYaml);

  const activeRun = featureYaml.match(/^active_run:\s*(\d+)/m)?.[1] ?? '1';
  const runPath = path.join(
    remoteStateDir,
    featureId,
    'runs',
    `run-${activeRun.padStart(3, '0')}`,
    'run.yaml',
  );
  let runYaml = clearRunFailures(fs.readFileSync(runPath, 'utf8'));
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    `  ${REPO}:`,
    '    touched: true',
  ]);
  fs.writeFileSync(runPath, runYaml);
  writeWorktreeChange(worktree, 'README.md', `# ${REPO}\nremote-degradation completion change\n`);
  return worktree;
}

function noteCapabilitySkip(testInfo: TestInfo, transcript: Transcript): void {
  const note =
    'SKIP (capability): no OS keychain on this host — the remote attach needs ' +
    'safeStorage token persistence ' +
    '[agentico capability: macOS Keychain; probe: security list-keychains]';
  transcript.step(note);
  testInfo.annotations.push({ type: 'capability', description: note });
}

// --- the journey ----------------------------------------------------------------

test('remote degradation: gated affordances, copy-path completion, server-validated roots, no local path leaks', async ({}, testInfo) => {
  test.setTimeout(360_000);
  const transcript = new Transcript('remote-degradation', 'Remote-aware feature degradation');
  const world = createWorld('remote-degradation', { auth: AUTH, presetWorkspaceRoot: true });
  createRepo(world, REPO, { commit: true });
  const extraRoot = createPlainFolder(world, 'degradation-extra-root');
  const remotePort = await freeLoopbackPort();
  let remote: RemoteTestServer | null = null;
  let proxy: CaptureProxy | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start the isolated test-owned server behind the capture proxy');
    remote = await startRemoteServer(world, remotePort);
    proxy = await startCaptureProxy(remotePort);
    // Mirrors internal/server/connectstring.go's ConnectionStringFromBaseURL,
    // with the proxy's host:port substituted so the app talks through it.
    const connectionString = `agentico://${remote.token}@127.0.0.1:${String(proxy.port)}?name=${encodeURIComponent(REMOTE_NAME)}`;
    transcript.step(
      `remote on :${String(remotePort)}; the app attaches through capture proxy :${String(proxy.port)}`,
    );

    transcript.section('Launch the app and attach remotely through the proxy');
    handle = await launchApp(world, testInfo, { traceName: 'remote-degradation-launch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const keychain = await handle.app.evaluate(({ safeStorage }) =>
      safeStorage.isEncryptionAvailable(),
    );
    if (!keychain) {
      noteCapabilitySkip(testInfo, transcript);
      persistAppLogs(handle, 'remote-degradation-app');
      transcript.write(testInfo);
      return;
    }
    const localState = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    assertKind(localState, 'local');
    const localName = localState.serverName ?? '';
    expect(localName).not.toBe('');

    let settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await settings.getByRole('textbox', { name: /add a remote server/i }).fill(connectionString);
    await settings.getByRole('button', { name: 'Probe and connect' }).click();
    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await waitFor(
      async () =>
        (await handle!.page.evaluate(() => window.agentico.getConnectionStatus())).serverName ===
        REMOTE_NAME,
      'the auto-switch to the remote server',
      60_000,
    );
    const remoteState = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(remoteState.status).toBe('ready');
    assertKind(remoteState, 'remote');
    transcript.step('ready connection state carries kind=remote');

    transcript.section('Composer gating: browse, typed paths, attach, drop, @-mention');
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible();

    // No native browse dialog remotely: server-side typed entry replaces it.
    await expect(handle.page.getByRole('button', { name: 'Browse for folder' })).toHaveCount(0);
    await expect(handle.page.getByLabel('Folder path on the server')).toBeVisible();

    await handle.page.getByRole('checkbox', { name: new RegExp(REPO) }).check();
    await handle.page.getByRole('button', { name: 'Next: Describe' }).click();

    // The attach affordance stays enabled: files now stage via server upload.
    const attach = handle.page.getByRole('button', { name: 'Attach files or photos' });
    await expect(attach).toBeEnabled();
    await expect(handle.page.locator('.composer__hint')).toContainText(
      /files upload to the server/,
    );
    await evidenceShot(handle, 'remote-degradation-composer-gated');

    // A synthetic dropped file resolves no local path for the preload
    // (webUtils), so nothing stages; real drops stage upload chips.
    await handle.page.evaluate((sentinel) => {
      const composer = document.querySelector('.composer');
      if (composer === null) throw new Error('composer not mounted');
      const dataTransfer = new DataTransfer();
      dataTransfer.items.add(new File(['sentinel-bytes'], sentinel, { type: 'image/png' }));
      composer.dispatchEvent(
        new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer }),
      );
    }, DROP_SENTINEL);
    await expect(handle.page.getByLabel('Attached files')).toHaveCount(0);

    // The @-mention popover still opens and explains instead of searching.
    await handle.page.locator('#feature-description').fill('Look at @read');
    const mention = handle.page.getByRole('listbox', { name: 'Repository files' });
    await expect(mention).toBeVisible({ timeout: 15_000 });
    await expect(mention).toContainText(/Repository file search requires a local server/);
    // Clearing the trigger closes the popover; Escape belongs to the sheet.
    await handle.page.locator('#feature-description').fill('');
    await expect(mention).toHaveCount(0);
    transcript.step('browse/attach/drop/mention all degrade with the shared explanation');

    transcript.section('Create the feature against the remote server and run it through setup');
    await handle.page.locator('#feature-description').fill('Remote degradation coverage.');
    await handle.page.locator('#feature-name').fill(FEATURE_NAME);
    await handle.page.getByRole('button', { name: 'Next: Depth' }).click();
    await handle.page.getByRole('button', { name: 'Next: Contract' }).click();
    await handle.page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await handle.page
      .getByRole('button', { name: 'Create', exact: true })
      .click({ timeout: 2_000 })
      .catch(() => undefined);
    const cockpit = handle.page.getByLabel(`Feature ${FEATURE_NAME}`);
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start')).toBeVisible({ timeout: 90_000 });
    const feature = (await handle.page.evaluate(() => window.agentico.listFeatures())).find(
      (candidate) => candidate.name === FEATURE_NAME,
    );
    expect(feature).toBeDefined();
    transcript.step('feature created and set up entirely on the remote server');

    transcript.section('Workspace-root text entry: server 4xx inline, valid path saves');
    settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Workspace roots');
    const rootField = settings.getByLabel('Folder path on the server');
    await expect(rootField).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Add workspace root' })).toHaveCount(0);
    await rootField.fill('/definitely/not/a/real/root');
    await settings.getByRole('button', { name: 'Add root' }).click();
    await expect(settings.getByRole('alert')).toContainText('/definitely/not/a/real/root', {
      timeout: 30_000,
    });
    await evidenceShot(handle, 'remote-degradation-root-rejected', settings);
    await rootField.fill(extraRoot);
    await settings.getByRole('button', { name: 'Add root' }).click();
    await expect(settings.getByText(extraRoot)).toBeVisible({ timeout: 30_000 });
    transcript.step('server 4xx named the bad path inline; the valid path saved');

    transcript.section('Seed completion on the remote server while it is stopped, then restart');
    await stopRemoteServer(remote);
    const worktree = seedRemoteCompletion(remote.stateDir, feature!.id);
    remote = await startRemoteServer(world, remotePort);
    await waitFor(
      async () => {
        try {
          const response = await fetch(
            `http://127.0.0.1:${String(remotePort)}/api/v1/features/${feature!.id}`,
            { headers: { authorization: `Bearer ${remote!.token}` } },
          );
          return response.status === 200;
        } catch {
          return false;
        }
      },
      'the restarted remote server serving the seeded feature',
      30_000,
    );
    // The token lives in main-process memory for the whole session: detach to
    // the local server, then re-attach the restarted remote (journey d's flow).
    await handle.page.getByRole('button', { name: `${REMOTE_NAME} — switch server` }).click();
    await handle.page
      .getByRole('option', { name: new RegExp(`${localName} at .+ — Available`) })
      .click();
    await waitFor(
      async () =>
        (await handle!.page.evaluate(() => window.agentico.getConnectionStatus())).status ===
          'ready' &&
        (
          (await handle!.page.evaluate(() => window.agentico.getConnectionStatus())) as {
            kind?: string;
          }
        ).kind === 'local',
      'the detach to the local server',
      60_000,
    );
    await handle.page.getByRole('button', { name: `${localName} — switch server` }).click();
    const remoteOption = handle.page.getByRole('option', {
      name: new RegExp(`${REMOTE_NAME} — Available`),
    });
    await expect(remoteOption).toBeVisible({ timeout: 60_000 });
    await remoteOption.click();
    await waitFor(
      async () => {
        const state = await handle!.page.evaluate(() => window.agentico.getConnectionStatus());
        return state.status === 'ready' && state.kind === 'remote';
      },
      'the re-attach to the restarted remote server',
      60_000,
    );
    transcript.step('re-attached to the restarted remote server; kind flipped remote again');

    transcript.section('Copy Path replaces Reveal in Finder and lands on the clipboard');
    await handle.page.getByRole('option', { name: FEATURE_NAME }).click();
    const completed = handle.page.getByLabel(`Feature ${FEATURE_NAME}`);
    await expect(completed).toBeVisible({ timeout: 60_000 });
    await completed.getByRole('button', { name: 'View changes', exact: true }).click();
    const changesModal = handle.page.getByRole('dialog', { name: 'Feature changes' });
    const changes = changesModal.getByRole('region', { name: 'Changes' });
    await expect(changes).toBeVisible({ timeout: 60_000 });
    await changes.getByRole('tab', { name: new RegExp(REPO) }).click();
    await expect(changes.getByText('Inspecting')).toBeVisible();
    await expect(changes.getByRole('button', { name: 'Reveal in Finder' })).toHaveCount(0);
    // navigator.clipboard.writeText requires a focused document: the window
    // must be OS-active exactly the way it is when a human presses Copy.
    await handle.page.bringToFront();
    await changesModal.getByRole('button', { name: 'Copy Path' }).click();
    await expect(changes.getByRole('status')).toContainText(
      `on the server host, not this machine: ${worktree}`,
      { timeout: 30_000 },
    );
    const clipboardText = await handle.app.evaluate(({ clipboard }) => clipboard.readText());
    expect(clipboardText).toBe(worktree);
    await evidenceShot(handle, 'remote-degradation-copy-path');
    await changesModal.getByRole('button', { name: 'Close' }).click();
    await expect(changesModal).toHaveCount(0);
    transcript.step('Copy Path wrote the server-reported worktree path to the OS clipboard');

    transcript.section('Negative invariant: zero local staged-artifact paths reached the server');
    assertNoLocalPathPayloads(proxy.requests);
    transcript.step(`${String(proxy.requests.length)} mutation requests inspected, none leaked`);

    transcript.section('Switching back to the local server restores every affordance');
    await handle.page.getByRole('button', { name: `${REMOTE_NAME} — switch server` }).click();
    await handle.page
      .getByRole('option', { name: new RegExp(`${localName} at .+ — Available`) })
      .click();
    await waitFor(
      async () =>
        (await handle!.page.evaluate(() => window.agentico.getConnectionStatus())).status ===
          'ready' &&
        (
          (await handle!.page.evaluate(() => window.agentico.getConnectionStatus())) as {
            kind?: string;
          }
        ).kind === 'local',
      'the switch back to the local server',
      60_000,
    );
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Browse for folder' })).toBeVisible();
    await expect(handle.page.getByLabel('Folder path on the server')).toHaveCount(0);
    await handle.page.getByRole('button', { name: 'Cancel' }).click();
    transcript.step('local affordances restored; no gating leaks into the local profile');

    persistAppLogs(handle, 'remote-degradation-app');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    if (remote !== null) await stopRemoteServer(remote);
    if (proxy !== null) await proxy.close();
    transcript.write(testInfo);
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
