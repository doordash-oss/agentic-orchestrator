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
 * Launch/teardown plumbing for the packaged app under Playwright. Every
 * launch records a trace; teardown stops the trace, closes the app through
 * the normal quit path, and asserts the app process actually exited. On
 * evidence runs (AGENTICO_E2E_EVIDENCE_DIR set) the trace is full
 * (screenshots + DOM snapshots), always saved, and copied to the evidence
 * directory; otherwise it is snapshot-only and saved only when the test
 * failed.
 */
import type { ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { _electron as electron, expect } from '@playwright/test';
import type { ElectronApplication, Locator, Page, TestInfo } from '@playwright/test';
import { packagedAppAsar, packagedExecutable, packagedResourcesDir } from './packaged';
import { killProcessTree, worldProcessPIDs } from './processes';
import { minimalEnv, waitFor, type JourneyWorld } from './world';

export interface AppHandle {
  app: ElectronApplication;
  appProcess: ChildProcess;
  page: Page;
  /**
   * The main window's webContents id, recorded while it was the only window.
   * `BrowserWindow.getAllWindows()` is not in creation order, so this is how
   * main-process helpers tell the main window from the Settings window.
   */
  mainWebContentsId: number;
  /** Combined stdout+stderr of the app process (redacted logs, gateway notes). */
  logs: string[];
  world: JourneyWorld;
  traceName: string;
  testInfo: TestInfo;
  closed: boolean;
}

export interface LaunchOptions {
  /** Launch a different executable (e.g. a copy in a read-only root). */
  executablePath?: string;
  /** Trace zip base name; defaults to the journey file name. */
  traceName?: string;
  /** Narrow per-journey environment additions, merged onto the hermetic base. */
  env?: Record<string, string>;
}

export interface CreateFeatureOptions {
  name: string;
  description?: string;
  repoPatterns: RegExp[];
  waitForReady?: boolean;
  /** Runs on the opened Repositories step, before any repository is selected. */
  beforeRepoSelect?(): void | Promise<void>;
  beforeSubmit?(): void | Promise<void>;
}

/** Creates a feature through the focused form and returns its opened cockpit. */
export async function createFeatureViaForm(
  handle: AppHandle,
  {
    name,
    description = '',
    repoPatterns,
    waitForReady = false,
    beforeRepoSelect,
    beforeSubmit,
  }: CreateFeatureOptions,
): Promise<Locator> {
  await handle.page.getByRole('button', { name: 'New feature' }).click();
  await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible();
  await beforeRepoSelect?.();
  for (const repoPattern of repoPatterns) {
    await handle.page.getByRole('checkbox', { name: repoPattern }).check();
  }
  await handle.page.getByRole('button', { name: 'Next: Describe' }).click();
  await handle.page.locator('#feature-name').fill(name);
  if (description !== '') await handle.page.locator('#feature-description').fill(description);
  await handle.page.getByRole('button', { name: 'Next: Depth' }).click();
  await handle.page.getByRole('button', { name: 'Next: Contract' }).click();
  // Journeys own lifecycle explicitly: opt out of the default auto-start so
  // the cockpit lands in the deterministic pre-start state.
  await handle.page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
  await beforeSubmit?.();
  const cockpit = handle.page.getByLabel(`Feature ${name}`);
  // Creation immediately closes the sheet and mounts a cockpit. Some Electron
  // builds keep Playwright's click action pending after that intentional
  // detach, so bound the dispatch and use the authoritative cockpit as the
  // success condition.
  await handle.page
    .getByRole('button', { name: 'Create', exact: true })
    .click({ timeout: 2_000 })
    .catch(() => undefined);
  await expect(cockpit).toBeVisible({ timeout: 30_000 });
  if (waitForReady)
    await expect(cockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
  return cockpit;
}

export async function launchApp(
  world: JourneyWorld,
  testInfo: TestInfo,
  options: LaunchOptions = {},
): Promise<AppHandle> {
  const installExecutablePath = options.executablePath ?? packagedExecutable();
  const resourcesPath = packagedResourcesDir(installExecutablePath);
  const args: string[] = [];
  if (process.platform === 'linux' && process.env['CI'] !== undefined) {
    // GitHub runners have no setuid chrome-sandbox helper for the unpacked
    // app; xvfb provides the display, this provides a runnable renderer.
    args.push('--no-sandbox');
  }
  if (process.env['AGENTICO_E2E_NO_SANDBOX'] === '1') {
    // Harness sandboxed runs (macOS sandbox-exec, restricted CI containers)
    // can fail at electron.launch with "sandbox initialization failed:
    // Operation not permitted" before any test logic runs. An explicit env
    // flag lets the harness environment opt out of the chromium sandbox
    // without relying on platform detection.
    args.push('--no-sandbox');
  }
  args.unshift(packagedAppAsar(installExecutablePath));
  // Concurrent workers exec the same binary; Linux can transiently report
  // ETXTBSY when a spawn races a fork that briefly inherits a write fd
  // (nodejs/node#22811-style), so that one error is retried briefly.
  const app = await retryingEtxtbsy(() =>
    electron.launch({
      args,
      env: {
        ...minimalEnv(world),
        AGENTICO_E2E_RESOURCES_PATH: resourcesPath,
        AGENTICO_E2E_INSTALL_EXECUTABLE: installExecutablePath,
        ...(options.env ?? {}),
      },
      timeout: 60_000,
    }),
  );
  const logs: string[] = [];
  const appProcess = app.process();
  appProcess.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  appProcess.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  // Screenshots multiply the trace's cost and are only consumed by evidence
  // runs; snapshots stay on so a saved failure trace still explains itself.
  await app.context().tracing.start({ screenshots: evidenceDir() !== null, snapshots: true });
  const page = await app.firstWindow({ timeout: 30_000 });
  // Actions (click/check/fill) otherwise inherit the whole test budget, so a
  // control that never becomes actionable — disabled while work is in flight,
  // or never stable while the surface repaints — burns the full 240s and
  // reports a bare "test timeout" with no page snapshot and no saved trace.
  // Bounding them names the stuck action and leaves budget for the evidence.
  page.setDefaultTimeout(Number(process.env['AGENTICO_E2E_ACTION_TIMEOUT'] ?? 60_000));
  // Recorded now, while the main window is provably the only one open.
  const mainWebContentsId = await app.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('the main window vanished during launch');
    return window.webContents.id;
  });
  // OS-level activation steals focus from whoever is using the machine and no
  // journey needs it: CDP input and assertions work without it. Opt back in
  // for one-off debugging with AGENTICO_E2E_ACTIVATE=1.
  if (process.env['AGENTICO_E2E_ACTIVATE'] === '1') {
    await page.bringToFront();
  }
  return {
    app,
    appProcess,
    page,
    mainWebContentsId,
    logs,
    world,
    traceName: options.traceName ?? path.basename(testInfo.file, '.spec.ts'),
    testInfo,
    closed: false,
  };
}

/**
 * Stops tracing (saving the zip when it will be consumed: evidence runs
 * always, other runs only on failure) and quits the app through the regular
 * quit path, asserting the process really exited so a hung shutdown can
 * never pass silently.
 */
export async function closeApp(handle: AppHandle): Promise<void> {
  if (handle.closed) {
    return;
  }
  handle.closed = true;
  try {
    // Bounded: against a wedged app these calls never settle, and an unbounded
    // await here costs the worker its whole teardown budget and loses the trace
    // — the one artifact that would explain the wedge.
    if (evidenceDir() !== null || testLooksFailed(handle.testInfo)) {
      const tracePath = handle.testInfo.outputPath(`${handle.traceName}-trace.zip`);
      await bounded(handle.app.context().tracing.stop({ path: tracePath }), 60_000, 'tracing.stop');
      copyTraceToEvidence(tracePath, `${handle.traceName}-trace.zip`);
    } else {
      await bounded(handle.app.context().tracing.stop(), 60_000, 'tracing.stop');
    }
  } catch {
    // Tracing is evidence, not correctness — never fail teardown on it.
  }
  const appProcess = handle.appProcess;
  // Generous on purpose: without this stub a quit-confirmation dialog blocks the
  // quit outright, so giving up early would *cause* the hang it guards against.
  const stubFailure = await bounded(
    installTeardownQuitDialogAnswer(handle),
    60_000,
    'quit dialog answer',
  ).then(
    () => null,
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  );
  // Generous, because the heaviest journeys quit a server that is still winding
  // down real sessions: only a genuine wedge should trip this, never a loaded
  // runner. The reason is reported rather than swallowed, so the failure below
  // distinguishes "close() never returned" from "the process outlived a
  // returned close()".
  const closeFailure = await bounded(handle.app.close(), 90_000, 'app.close').then(
    () => null,
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  );
  const deadline = Date.now() + 15_000;
  while (appProcess.exitCode === null && appProcess.signalCode === null) {
    if (Date.now() > deadline) {
      // The app's own logs are the only record of what its quit was waiting on.
      persistAppLogs(handle, `${handle.traceName}-hung-quit`);
      killProcessTree(appProcess.pid);
      const causes = [stubFailure, closeFailure].filter((cause) => cause !== null);
      throw new Error(
        causes.length === 0
          ? 'the app process did not exit within 15s of close()'
          : `the app process did not exit within 15s of close(); teardown steps failed first: ${causes.join('; ')}`,
      );
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

export async function installRelaunchProbe(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ app }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoOriginalQuit?: typeof app.quit;
      __agenticoOriginalRelaunch?: typeof app.relaunch;
      __agenticoRelaunchCount?: number;
    };
    global.__agenticoOriginalQuit = global.__agenticoOriginalQuit ?? app.quit.bind(app);
    global.__agenticoOriginalRelaunch = global.__agenticoOriginalRelaunch ?? app.relaunch.bind(app);
    global.__agenticoRelaunchCount = 0;
    app.relaunch = () => {
      global.__agenticoRelaunchCount = (global.__agenticoRelaunchCount ?? 0) + 1;
    };
    app.quit = (() => undefined) as typeof app.quit;
  });
}

export async function restoreRelaunchProbe(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ app }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoOriginalQuit?: typeof app.quit;
      __agenticoOriginalRelaunch?: typeof app.relaunch;
    };
    if (global.__agenticoOriginalQuit !== undefined) {
      app.quit = global.__agenticoOriginalQuit;
      global.__agenticoOriginalQuit = undefined;
    }
    if (global.__agenticoOriginalRelaunch !== undefined) {
      app.relaunch = global.__agenticoOriginalRelaunch;
      global.__agenticoOriginalRelaunch = undefined;
    }
  });
}

export function relaunchCount(handle: AppHandle): Promise<number> {
  return handle.app.evaluate(() => {
    const global = globalThis as typeof globalThis & { __agenticoRelaunchCount?: number };
    return global.__agenticoRelaunchCount ?? 0;
  });
}

async function installTeardownQuitDialogAnswer(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ dialog }) => {
    dialog.showMessageBox = (async (...args: unknown[]) => {
      const options = args[args.length - 1] as { buttons?: string[] } | undefined;
      const buttons = Array.isArray(options?.buttons) ? options.buttons : [];
      const stopWork = buttons.indexOf('Stop Work and Quit');
      if (stopWork >= 0) {
        return { response: stopWork, checkboxChecked: false };
      }
      const quitAnyway = buttons.indexOf('Quit Anyway');
      if (quitAnyway >= 0) {
        return { response: quitAnyway, checkboxChecked: false };
      }
      return { response: 0, checkboxChecked: false };
    }) as typeof dialog.showMessageBox;
  });
}

/**
 * Whether the test has already failed by teardown time. closeApp runs inside
 * the journey's own finally block, before the runner records the in-flight
 * error onto testInfo — a hard expect failure still reads status "passed"
 * here. Its error is already on the failed step, though, so the (private)
 * step tree is scanned as the in-flight failure signal.
 */
function testLooksFailed(testInfo: TestInfo): boolean {
  if (testInfo.status !== testInfo.expectedStatus || testInfo.errors.length > 0) return true;
  type Step = { error?: unknown; steps: Step[] };
  const steps = (testInfo as unknown as { _steps?: Step[] })._steps ?? [];
  const hasError = (list: Step[]): boolean =>
    list.some((step) => step.error !== undefined || hasError(step.steps));
  return hasError(steps);
}

async function retryingEtxtbsy<T>(launch: () => Promise<T>): Promise<T> {
  for (let attempt = 1; ; attempt++) {
    try {
      return await launch();
    } catch (error) {
      if (attempt >= 3 || !(error instanceof Error) || !error.message.includes('ETXTBSY')) {
        throw error;
      }
      await new Promise((resolve) => setTimeout(resolve, 250 * attempt));
    }
  }
}

/** Rejects if `work` has not settled within `ms`, naming the step that stuck. */
async function bounded<T>(work: Promise<T>, ms: number, step: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      work,
      new Promise<never>((_, reject) => {
        timer = setTimeout(() => reject(new Error(`teardown step "${step}" exceeded ${ms}ms`)), ms);
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

/** Writes app logs alongside the test output (and returns the joined text). */
export function persistAppLogs(handle: AppHandle, name: string): string {
  const emitted = handle.logs.join('');
  const joined =
    emitted === ''
      ? '[evidence] The packaged app and bundled server emitted no stdout/stderr lines.\n'
      : emitted;
  const fileName = `${name}.log`;
  fs.writeFileSync(handle.testInfo.outputPath(fileName), joined);
  const dir = evidenceDir();
  if (dir !== null) {
    const target = path.join(dir, 'behaviors');
    fs.mkdirSync(target, { recursive: true });
    fs.writeFileSync(path.join(target, fileName), joined);
  }
  return joined;
}

// --- evidence -----------------------------------------------------------------

export function evidenceDir(): string | null {
  const dir = process.env['AGENTICO_E2E_EVIDENCE_DIR'];
  return dir !== undefined && dir !== '' ? dir : null;
}

function copyTraceToEvidence(tracePath: string, name: string): void {
  const dir = evidenceDir();
  if (dir === null) {
    return;
  }
  const target = path.join(dir, 'behaviors');
  fs.mkdirSync(target, { recursive: true });
  fs.copyFileSync(tracePath, path.join(target, name));
}

/**
 * Named evidence screenshot: always captured (so the flow is exercised in
 * CI too); copied into the evidence screenshots/ directory on evidence runs.
 *
 * `page` targets a window other than the main one (the Settings window), so
 * evidence of a settings surface shows the window that actually renders it.
 */
export async function evidenceShot(handle: AppHandle, name: string, page?: Page): Promise<void> {
  const local = handle.testInfo.outputPath(`${name}.png`);
  // Playwright's screenshot pipeline can stall indefinitely on a packaged
  // Electron window when the suite shares a machine with other heavy work: it
  // reports "fonts loaded" and then never returns, which used to fail a random
  // journey per full run. A stalled capture is not a product failure, so fall
  // back to the main process's own compositor capture — same real window, a
  // different code path — and only fail if that cannot produce bytes either.
  await captureWindow(handle, local, page ?? handle.page);
  const dir = evidenceDir();
  if (dir !== null) {
    const target = path.join(dir, 'screenshots');
    fs.mkdirSync(target, { recursive: true });
    fs.copyFileSync(local, path.join(target, `${name}.png`));
  }
}

/**
 * The pre-fallback budget has to be small enough that a whole journey's worth
 * of stalls still fits the 240s test timeout: the heaviest journey takes ten
 * evidence shots, so anything above ~20s each can blow the test budget even
 * though every capture eventually succeeds. A healthy Playwright capture of
 * this window lands in well under a second, and `capturePage()` is accepted as
 * equivalent evidence — so spend as little as possible waiting on the stall.
 */
const PLAYWRIGHT_SHOT_BUDGET_MS = 5_000;

async function captureWindow(handle: AppHandle, target: string, page: Page): Promise<void> {
  try {
    await page.screenshot({ path: target, timeout: PLAYWRIGHT_SHOT_BUDGET_MS });
    return;
  } catch {
    // Fall through to the main-process capture below.
  }
  // getAllWindows() is not in creation order (macOS returns the frontmost
  // window first), so windows are addressed by the main window's recorded
  // webContents id: that id for the main page, anything else for the other.
  const encoded = await handle.app.evaluate(
    async ({ BrowserWindow }, { mainId, wantMain }) => {
      const window = BrowserWindow.getAllWindows().find(
        (candidate) => (candidate.webContents.id === mainId) === wantMain,
      );
      if (window === undefined) throw new Error('window to capture missing');
      const image = await window.webContents.capturePage();
      return image.toPNG().toString('base64');
    },
    { mainId: handle.mainWebContentsId, wantMain: page === handle.page },
  );
  const bytes = Buffer.from(encoded, 'base64');
  if (bytes.byteLength === 0) throw new Error(`capturePage produced no bytes for ${target}`);
  fs.writeFileSync(target, bytes);
}

/**
 * Final testing-contract evidence is deliberately opt-in: normal packaged
 * journeys do not mutate the phase evidence directory or spend time laying
 * out screenshot-only viewports.
 */
export async function contractEvidenceShot(
  handle: AppHandle,
  name: string,
  width: number,
  height: number,
  theme: 'light' | 'dark',
): Promise<void> {
  const dir = process.env['AGENTICO_EVIDENCE_DIR'];
  if (dir === undefined || dir === '') return;
  await setWindowSize(handle, width, height);
  await setTheme(handle, theme);
  const target = path.join(dir, 'screenshots', `${name}.png`);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  await handle.page.screenshot({ path: target, fullPage: false, scale: 'css' });
}

// --- window/theme helpers -------------------------------------------------------

export async function setWindowSize(
  handle: AppHandle,
  width: number,
  height: number,
): Promise<void> {
  await handle.app.evaluate(
    ({ BrowserWindow }, size) => {
      // By id, not by index: an open Settings window can come first.
      const window = BrowserWindow.getAllWindows().find(
        (candidate) => candidate.webContents.id === size.mainId,
      );
      window?.setContentSize(size.width, size.height);
    },
    { width, height, mainId: handle.mainWebContentsId },
  );
  // Let the renderer re-layout (media queries, narrow-mode attributes).
  await handle.page.waitForTimeout(250);
}

/**
 * Flips the resolved theme via the same IPC channel the real Settings ▸
 * Appearance radiogroup uses, and waits for the CSS to settle.
 *
 * This deliberately does not drive the real switcher: the Appearance
 * radiogroup lives in the separate Settings window, and almost every caller
 * only wants a themed screenshot of the *main* window mid-flow. Making them
 * open (and then close) a second window to get one would add a window to
 * every themed capture and leave the main window's own state — sizing,
 * focus, whatever view is on screen — at the mercy of that detour.
 *
 * Going straight through `setThemePreference` avoids all of that. But
 * `useTheme()` (renderer/src/hooks.ts) only
 * mirrors a change onto `<html data-theme>` when its own `setPreference`
 * runs, or via the `agentico-theme-sync` window CustomEvent it dispatches
 * for sibling instances — calling the IPC method directly never reaches
 * either path, so the mounted hook never re-renders. Dispatching that same
 * sync event ourselves with the IPC response is what the real switcher does
 * internally, just triggered from test code instead of another `useTheme()`
 * instance. The real Settings ▸ Appearance radiogroup is still exercised
 * directly by `window-chrome.spec.ts`, which is the one place that cares
 * about the UI mechanism itself.
 */
export async function setTheme(handle: AppHandle, theme: 'light' | 'dark'): Promise<void> {
  await handle.page.evaluate(async (preference) => {
    const info = await window.agentico.setThemePreference(preference);
    window.dispatchEvent(new CustomEvent('agentico-theme-sync', { detail: info }));
  }, theme);
  await expect(handle.page.locator(`html[data-theme="${theme}"]`)).toBeAttached();
  await handle.page.waitForTimeout(150); // let colors settle before screenshots
}

// --- the Settings window --------------------------------------------------------

/** The pane labels, in source-list order (see features/settingsPanes). */
export type SettingsPaneLabel =
  | 'Workspace roots'
  | 'Servers'
  | 'Providers'
  | 'Appearance'
  | 'Updates'
  | 'Notifications'
  | 'Diagnostics'
  | 'Advanced'
  | 'Workspace defaults';

/**
 * Opens Settings through the native "Settings" menu item (⌘,'s own dispatch
 * path: native menu → route target 'settings' → the Settings window) and
 * returns that window's page.
 *
 * The menu item creates the window on the first call and focuses the existing
 * one afterwards, so this waits for a *new* window only when none is open yet.
 */
export async function openSettings(handle: AppHandle): Promise<Page> {
  const alreadyOpen = settingsPageOrNull(handle);
  const appeared =
    alreadyOpen === null ? handle.app.waitForEvent('window', { timeout: 30_000 }) : null;
  await handle.app.evaluate(({ BrowserWindow, Menu }, mainId) => {
    const item = Menu.getApplicationMenu()?.getMenuItemById('global.settings');
    if (item == null) throw new Error('menu item global.settings missing');
    const main = BrowserWindow.getAllWindows().find(
      (candidate) => candidate.webContents.id === mainId,
    );
    item.click(undefined, main, undefined);
  }, handle.mainWebContentsId);
  const page = appeared === null ? alreadyOpen! : await appeared;
  return waitForSettingsWindow(page);
}

/**
 * The open Settings window's page, or null. The main window is the launch
 * window; the app opens no other kind, so anything else is Settings — and
 * `waitForSettingsWindow` confirms it by what only that window renders (both
 * windows share an origin, so a URL cannot tell them apart).
 */
export function settingsPageOrNull(handle: AppHandle): Page | null {
  return handle.app.windows().find((page) => page !== handle.page) ?? null;
}

/**
 * Waits for a Settings window opened by something other than this helper (the
 * command palette entry, a deep link, an update popover action) and returns
 * its page.
 */
export async function awaitSettingsWindow(handle: AppHandle): Promise<Page> {
  await waitFor(
    () => settingsPageOrNull(handle) !== null,
    'the Settings window to open',
    30_000,
    100,
  );
  const page = settingsPageOrNull(handle);
  if (page === null) throw new Error('the Settings window disappeared while opening');
  return waitForSettingsWindow(page);
}

/** Waits for the Settings window to finish restoring its persisted pane. */
export async function waitForSettingsWindow(page: Page): Promise<Page> {
  await expect(page.locator('.settings-window')).toBeVisible({ timeout: 30_000 });
  // The window paints a "Restoring settings…" status until the stored pane is
  // read back; the source list only exists once a pane is selected.
  await expect(page.getByRole('listbox', { name: 'Settings panes' })).toBeVisible({
    timeout: 30_000,
  });
  return page;
}

/** Selects a pane in the source list and waits for the selection to land. */
export async function selectSettingsPane(page: Page, label: SettingsPaneLabel): Promise<void> {
  const row = page.getByRole('option', { name: label, exact: true });
  await row.click();
  await expect(row).toHaveAttribute('aria-selected', 'true');
  await expect(
    page.locator('.settings-window__pane').getByRole('heading', { name: label }),
  ).toBeVisible();
}

/**
 * Closes Settings the way ⌘W does: File ▸ Close Window is asserted to exist
 * with the platform `close` role and the ⌘W accelerator, and then the window is
 * closed exactly as that role closes it.
 *
 * The item cannot be *clicked* from a test: on macOS the standard roles are
 * NSMenuItem selectors (`close` → `performClose:`) that act on the key window,
 * so `MenuItem.click()` has no JavaScript body to run and returns having done
 * nothing. Measured against the packaged app, with and without focusing the
 * Settings window first: the window count never changed. So the menu wiring
 * (the item exists, carries `role: 'close'`, and is bound to ⌘W) and the close
 * semantics are both asserted here, while the OS accelerator dispatch between
 * them is simply not drivable from the harness. `window.close()` is what the
 * selector performs, so this still runs the app's real close handler —
 * geometry persistence, no quit decision — not a test-only shortcut.
 */
export async function closeSettings(handle: AppHandle): Promise<void> {
  const accelerator = await handle.app.evaluate(({ BrowserWindow, Menu }, mainId) => {
    const target = BrowserWindow.getAllWindows().find((window) => window.webContents.id !== mainId);
    if (target === undefined) throw new Error('no Settings window is open');
    const close = flatten(Menu.getApplicationMenu()).find((item) => item.role === 'close');
    if (close === undefined) throw new Error('File ▸ Close Window is missing from the menu');
    target.close();
    return close.accelerator ?? null;

    function flatten(menu: Electron.Menu | null): Electron.MenuItem[] {
      if (menu === null) return [];
      return menu.items.flatMap((item) => [item, ...flatten(item.submenu ?? null)]);
    }
  }, handle.mainWebContentsId);
  expect(accelerator).toBe('CommandOrControl+W');
  await waitFor(() => settingsPageOrNull(handle) === null, 'the Settings window to close', 15_000);
}

/** Captures the same named moment in both themes (light last, restoring it). */
export async function evidenceShotBothThemes(handle: AppHandle, baseName: string): Promise<void> {
  await setTheme(handle, 'dark');
  await evidenceShot(handle, `${baseName}-dark`);
  await setTheme(handle, 'light');
  await evidenceShot(handle, `${baseName}-light`);
}

/**
 * Replaces the native directory picker inside the RUNNING main process with
 * a deterministic answer. This is Playwright's main-process evaluation — no
 * app code path is modified or added for it; the packaged binary under test
 * is byte-identical to the shipped one.
 */
export async function mockDirectoryPicker(handle: AppHandle, directory: string): Promise<void> {
  await handle.app.evaluate(({ dialog }, dir) => {
    const answer = { canceled: false, filePaths: [dir], bookmarks: [] };
    // Both overloads (with and without a parent window) route here.
    dialog.showOpenDialog = (async () => answer) as typeof dialog.showOpenDialog;
  }, directory);
}

// --- leak detection ---------------------------------------------------------------

/**
 * Asserts nothing is still running against this journey's world (server
 * children, stub servers, stray helpers). Uses the world root path as the
 * process-table needle: every journey process carries it in argv or env.
 */
export function assertNoLeakedProcesses(world: JourneyWorld): void {
  const pids = worldProcessPIDs(world.root);
  expect(pids, `leaked processes still reference ${world.root}`).toEqual([]);
}
