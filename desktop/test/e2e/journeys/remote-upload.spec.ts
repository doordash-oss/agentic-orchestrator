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
 * Remote-upload journey (packaged app): attaches to a test-owned loopback
 * server EXACTLY like remote-degradation.spec.ts — own HOME, own state dir,
 * outside the app registry, all traffic through a request-capturing proxy —
 * then proves the byte-upload story end to end:
 *
 *   (1) creation composer staging on a remote connection: the native picker
 *       (dialog.showOpenDialog stubbed in the RUNNING main process, the same
 *       technique as mockDirectoryPicker) stages an image and an attachment,
 *       and a clipboard paste stages a clipboard image — every chip flips
 *       uploading → ready through POST /api/v1/uploads
 *   (2) an unsupported image (.svg) is rejected with the actionable error on
 *       its chip, before any bytes reach the server
 *   (3) the created feature materializes the uploaded bytes inside the REMOTE
 *       server's own state dir (images/, attachments/), the staged sources
 *       are deleted after consumption (single-use), and the create request
 *       carried server references only — never a client local path
 *   (4) an AMA chat message with a pasted image resolves to a server-side
 *       copy under the remote state's chat/ dir, and the chat session's
 *       initial prompt references exactly that server-readable path
 *
 * Drag-and-drop is NOT exercised: webUtils.getPathForFile resolves no path
 * for a synthetic DataTransfer File (the packaged-window limitation
 * remote-degradation.spec.ts documents), so a scripted drop can stage
 * nothing; the composer path it would take is identical to the paste import
 * exercised here.
 *
 * [agentico capability: macOS Keychain availability; probe: `security list-keychains`]
 * The attach persists the bearer token through Electron's safeStorage (the
 * real backend); on hosts without an OS keychain the journey ends with a
 * clearly-logged capability skip, matching the other remote journeys.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { expect, test, type Locator, type TestInfo } from '@playwright/test';
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
const REMOTE_NAME = 'upload-remote';
const REPO = 'upload-lab';
const FEATURE_NAME = 'Remote upload feature';
/** Sentinel: the client-side fixture directory that must NEVER reach the server. */
const FIXTURE_SENTINEL = 'client-upload-fixtures';

/** A real (tiny) PNG so the server's image path manipulates genuine bytes. */
const PICKER_PNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==',
  'base64',
);
const ATTACHMENT_BYTES = Buffer.from('%PDF-1.4 agentico remote-upload e2e fixture\n', 'utf8');

interface RecordedRequest {
  method: string;
  url: string;
  body: string;
}

// --- the test-owned remote server (remote-degradation.spec.ts conventions) ---

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

/** Transparent loopback forwarder in front of the test-owned server. */
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

// --- journey-local helpers -----------------------------------------------------

/**
 * Replaces the native creation-file picker inside the RUNNING main process
 * with deterministic per-title answers ('Choose images' / 'Choose
 * attachments'). Same no-app-code-modified technique as mockDirectoryPicker:
 * Playwright's main-process evaluation against the byte-identical binary.
 * Re-calling swaps the answers for the next pick.
 */
async function stubCreationPicker(
  handle: AppHandle,
  answers: Record<string, string[]>,
): Promise<void> {
  await handle.app.evaluate(({ dialog }, nextAnswers) => {
    const global = globalThis as typeof globalThis & {
      __agenticoUploadPickerAnswers?: Record<string, string[]>;
    };
    global.__agenticoUploadPickerAnswers = nextAnswers;
    dialog.showOpenDialog = (async (...args: unknown[]) => {
      const options = args[args.length - 1] as { title?: string };
      const filePaths = global.__agenticoUploadPickerAnswers?.[options.title ?? ''] ?? [];
      return { canceled: filePaths.length === 0, filePaths, bookmarks: [] };
    }) as typeof dialog.showOpenDialog;
  }, answers);
}

/** Writes a known image onto the OS clipboard through the real main process. */
async function writeClipboardPng(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ clipboard, nativeImage }, encoded) => {
    clipboard.writeImage(nativeImage.createFromBuffer(Buffer.from(encoded, 'base64')));
  }, PICKER_PNG.toString('base64'));
}

/**
 * Pastes an image into a composer the way a ⌘V of clipboard image data does:
 * a synthetic paste event carrying an unpathable File (so the import falls
 * back to the real OS clipboard, which writeClipboardPng just populated).
 */
async function pasteClipboardImage(target: Locator): Promise<void> {
  await target.evaluate((element) => {
    const dataTransfer = new DataTransfer();
    dataTransfer.items.add(new File(['synthetic'], 'pasted.png', { type: 'image/png' }));
    element.dispatchEvent(
      new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: dataTransfer }),
    );
  });
}

/** Waits for a composer chip of `name` to reach a terminal staged state. */
async function expectChip(chips: Locator, name: RegExp | string, state: string): Promise<Locator> {
  const chip = chips.locator(`li[data-state="${state}"]`, { hasText: name });
  await expect(chip).toBeVisible({ timeout: 30_000 });
  return chip;
}

/** Data files still staged on the server (sidecar tombstones don't count). */
function liveStagedRefs(stateDir: string): string[] {
  const dir = path.join(stateDir, 'uploads');
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir).filter((entry) => /^[0-9a-f]{32}$/.test(entry));
}

function noteCapabilitySkip(testInfo: TestInfo, transcript: Transcript): void {
  const note =
    'SKIP (capability): no OS keychain on this host — the remote attach needs ' +
    'safeStorage token persistence ' +
    '[agentico capability: macOS Keychain; probe: security list-keychains]';
  transcript.step(note);
  testInfo.annotations.push({ type: 'capability', description: note });
}

/** Clicks a native menu item by id, the same dispatch path its accelerator uses. */
async function clickNativeMenu(handle: AppHandle, id: string): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow, Menu }, itemId) => {
    const item = Menu.getApplicationMenu()?.getMenuItemById(itemId);
    if (item == null) throw new Error(`menu item ${itemId} missing`);
    item.click(undefined, BrowserWindow.getAllWindows()[0], undefined);
  }, id);
}

// --- the journey ----------------------------------------------------------------

test('remote upload: picker/paste staging, server materialization, cleanup, rejection, no local path leaks', async ({}, testInfo) => {
  test.setTimeout(360_000);
  const transcript = new Transcript('remote-upload', 'Remote upload journey');
  const world = createWorld('remote-upload', {
    auth: AUTH,
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, REPO, { commit: true });
  const fixtureDir = path.join(world.root, FIXTURE_SENTINEL);
  fs.mkdirSync(fixtureDir, { recursive: true });
  const pickerPngPath = path.join(fixtureDir, 'upload-diagram.png');
  const attachmentPath = path.join(fixtureDir, 'upload-spec.pdf');
  const svgPath = path.join(fixtureDir, 'upload-vector.svg');
  fs.writeFileSync(pickerPngPath, PICKER_PNG);
  fs.writeFileSync(attachmentPath, ATTACHMENT_BYTES);
  fs.writeFileSync(svgPath, '<svg xmlns="http://www.w3.org/2000/svg"/>\n');
  const remotePort = await freeLoopbackPort();
  let remote: RemoteTestServer | null = null;
  let proxy: CaptureProxy | null = null;
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start the isolated test-owned server behind the capture proxy');
    remote = await startRemoteServer(world, remotePort);
    proxy = await startCaptureProxy(remotePort);
    const connectionString = `agentico://${remote.token}@127.0.0.1:${String(proxy.port)}?name=${encodeURIComponent(REMOTE_NAME)}`;

    transcript.section('Launch the app and attach remotely through the proxy');
    handle = await launchApp(world, testInfo, { traceName: 'remote-upload-launch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const keychain = await handle.app.evaluate(({ safeStorage }) =>
      safeStorage.isEncryptionAvailable(),
    );
    if (!keychain) {
      noteCapabilitySkip(testInfo, transcript);
      persistAppLogs(handle, 'remote-upload-app');
      transcript.write(testInfo);
      return;
    }
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Servers');
    await settings.getByRole('textbox', { name: /add a remote server/i }).fill(connectionString);
    await settings.getByRole('button', { name: 'Probe and connect' }).click();
    await expect(settings.getByText('Server added; switching to it now.')).toBeVisible({
      timeout: 60_000,
    });
    await waitFor(
      async () => {
        const state = await handle!.page.evaluate(() => window.agentico.getConnectionStatus());
        return state.status === 'ready' && state.kind === 'remote';
      },
      'the auto-switch to the remote server',
      60_000,
    );
    transcript.step('ready connection state carries kind=remote');

    transcript.section('Composer staging: rejection, native picker, clipboard paste');
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible();
    await handle.page.getByRole('checkbox', { name: new RegExp(REPO) }).check();
    await handle.page.getByRole('button', { name: 'Next: Describe' }).click();
    const chips = handle.page.getByLabel('Attached files');
    const attach = handle.page.getByRole('button', { name: 'Attach files or photos' });
    await expect(attach).toBeEnabled();

    // (2) Rejection: an .svg picked as an image fails its chip in place with
    // the actionable copy — and (asserted below) never reaches the wire.
    await stubCreationPicker(handle, { 'Choose images': [svgPath] });
    await attach.click();
    await handle.page.getByRole('menuitem', { name: 'Add photos' }).click();
    const rejected = await expectChip(chips, 'upload-vector.svg', 'failed');
    await expect(rejected).toContainText('Only PNG, JPEG, GIF, or WebP images can be attached.');
    await expect(rejected).toContainText('Choose an image in a supported format.');
    await rejected.getByRole('button', { name: 'Remove upload-vector.svg' }).click();
    transcript.step('unsupported image rejected with actionable failure copy');

    // (1) Native picker: one image and one attachment stage to ready chips.
    await stubCreationPicker(handle, {
      'Choose images': [pickerPngPath],
      'Choose attachments': [attachmentPath],
    });
    await attach.click();
    await handle.page.getByRole('menuitem', { name: 'Add photos' }).click();
    await expectChip(chips, 'upload-diagram.png', 'ready');
    await attach.click();
    await handle.page.getByRole('menuitem', { name: 'Add files' }).click();
    await expectChip(chips, 'upload-spec.pdf', 'ready');

    // Clipboard paste: the composer imports the real OS clipboard image and
    // stages it remotely under its generated clipboard capture name.
    await writeClipboardPng(handle);
    await pasteClipboardImage(handle.page.locator('#feature-description'));
    await expectChip(chips, /clipboard-.*\.png/, 'ready');
    await evidenceShot(handle, 'remote-upload-composer-staged');
    transcript.step('picker image, picker attachment, and pasted image all staged ready');

    transcript.section('Create the feature against the remote server');
    await handle.page.locator('#feature-description').fill('Remote upload coverage.');
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

    transcript.section('Server-side: feature dirs materialize the uploaded bytes');
    const featureDir = path.join(remote.stateDir, feature!.id);
    const imagesDir = path.join(featureDir, 'images');
    expect(fs.readdirSync(imagesDir).sort()).toEqual(['image-1.png', 'image-2.png']);
    expect(fs.readFileSync(path.join(imagesDir, 'image-1.png'))).toEqual(PICKER_PNG);
    const attachments = fs.readdirSync(path.join(featureDir, 'attachments'));
    expect(attachments).toHaveLength(1);
    expect(attachments[0]).toMatch(/^consumed-[0-9a-f]{32}-[0-9a-f]{32}\.pdf$/);
    expect(fs.readFileSync(path.join(featureDir, 'attachments', attachments[0]!))).toEqual(
      ATTACHMENT_BYTES,
    );
    // Consumption is single-use: every staged data file was deleted at commit.
    expect(liveStagedRefs(remote.stateDir)).toEqual([]);
    transcript.step('feature images/attachments landed; staged uploads cleaned after consumption');

    transcript.section('AMA chat with a pasted image resolves to a server-readable file');
    await clickNativeMenu(handle, 'global.ama');
    const panel = handle.page.getByRole('complementary', { name: 'Ask Agentico' });
    await expect(panel).toBeVisible();
    await writeClipboardPng(handle);
    await pasteClipboardImage(panel.getByRole('textbox', { name: 'Ask Agentico' }));
    await expectChip(panel.getByLabel('Attached images'), /clipboard-.*\.png/, 'ready');
    await panel.getByRole('textbox', { name: 'Ask Agentico' }).fill('Describe the uploaded image.');
    await panel.getByRole('button', { name: 'Send' }).click();
    await expect(panel.getByLabel('AMA transcript')).toContainText(/Backfill ready|Live semantic/, {
      timeout: 60_000,
    });

    // The chat copy is durable in the REMOTE state's chat dir, and the
    // session's initial prompt quotes exactly that server-side path.
    const chatDir = path.join(remote.stateDir, 'chat');
    const chatCopies = fs
      .readdirSync(chatDir)
      .filter((entry) => /^consumed-[0-9a-f]{32}-[0-9a-f]{32}\.png$/.test(entry));
    expect(chatCopies).toHaveLength(1);
    const chatImagePath = path.join(chatDir, chatCopies[0]!);
    expect(fs.statSync(chatImagePath).size).toBeGreaterThan(0);
    const chat = await handle.page.evaluate(() => window.agentico.getSession('__chat__'));
    expect(chat.initialPrompt ?? '').toContain(chatImagePath);
    expect(liveStagedRefs(remote.stateDir)).toEqual([]);
    transcript.step('chat prompt references the server-side image copy; staging stays clean');

    // End the chat so the fixture provider exits through its real interrupt
    // path (no leaked stub against this world).
    const end = panel.getByRole('button', { name: 'End session', exact: true });
    await expect(end).toBeVisible();
    await end.click();
    await panel
      .getByRole('group', { name: 'End session confirmation' })
      .getByRole('button', { name: 'End session', exact: true })
      .click();
    await expect(panel).toContainText('AMA ended.');

    transcript.section('Wire invariants: byte uploads only, zero client local paths');
    const requests = proxy.requests;
    const uploads = requests.filter(
      (request) => request.method === 'POST' && request.url.startsWith('/api/v1/uploads?'),
    );
    // Picker image, picker attachment, composer paste, AMA paste. The .svg
    // was rejected client-side and never became a request.
    expect(uploads.map((request) => request.url)).toEqual([
      expect.stringContaining('kind=image&name=upload-diagram.png'),
      expect.stringContaining('kind=attachment&name=upload-spec.pdf'),
      expect.stringMatching(/kind=image&name=clipboard-.*\.png/),
      expect.stringMatching(/kind=image&name=clipboard-.*\.png/),
    ]);
    expect(uploads.map((request) => request.url).join('\n')).not.toContain(FIXTURE_SENTINEL);

    // The create and chat mutations carry server references, never paths.
    const create = requests.find(
      (request) => request.method === 'POST' && request.url === '/api/v1/features',
    );
    expect(create).toBeDefined();
    const createBody = JSON.parse(create!.body) as Record<string, unknown>;
    expect(createBody['image_uploads']).toHaveLength(2);
    expect(createBody['attachment_uploads']).toHaveLength(1);
    // Local-path arrays are sent empty remotely — the references above
    // carry every byte.
    expect(createBody['images']).toEqual([]);
    expect(createBody['attachments']).toEqual([]);
    const chatStart = requests.find(
      (request) => request.method === 'POST' && request.url === '/api/v1/prompts/chat/start',
    );
    expect(chatStart).toBeDefined();
    const chatBody = JSON.parse(chatStart!.body) as Record<string, unknown>;
    expect(chatBody['image_uploads']).toHaveLength(1);
    expect(chatBody['images']).toEqual([]);

    // The negative invariant across EVERY mutation: no client fixture path
    // and no file:// URL ever left the app.
    const leaked = requests.filter(
      (request) =>
        request.body.includes(FIXTURE_SENTINEL) ||
        request.url.includes(FIXTURE_SENTINEL) ||
        request.body.includes('file://'),
    );
    expect(leaked.map((request) => `${request.method} ${request.url}: ${request.body}`)).toEqual(
      [],
    );
    transcript.step(`${String(requests.length)} mutation requests inspected, none leaked`);

    persistAppLogs(handle, 'remote-upload-app');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    if (remote !== null) await stopRemoteServer(remote);
    if (proxy !== null) await proxy.close();
    transcript.write(testInfo);
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
