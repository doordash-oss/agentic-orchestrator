/**
 * Launch/teardown plumbing for the packaged app under Playwright. Every
 * launch records a full trace (screenshots + DOM snapshots); teardown stops
 * the trace, closes the app through the normal quit path, and asserts the
 * app process actually exited. Evidence artifacts (named screenshots and
 * trace zips) are additionally copied to AGENTICO_E2E_EVIDENCE_DIR when the
 * run is an evidence run.
 */
import type { ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { _electron as electron, expect } from '@playwright/test';
import type { ElectronApplication, Locator, Page, TestInfo } from '@playwright/test';
import { packagedAppAsar, packagedExecutable, packagedResourcesDir } from './packaged';
import { killProcessTree, worldProcessPIDs } from './processes';
import { minimalEnv, type JourneyWorld } from './world';

export interface AppHandle {
  app: ElectronApplication;
  appProcess: ChildProcess;
  page: Page;
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
    beforeSubmit,
  }: CreateFeatureOptions,
): Promise<Locator> {
  await handle.page.getByRole('button', { name: 'New feature' }).click();
  await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible();
  await handle.page.locator('#feature-name').fill(name);
  if (description !== '') await handle.page.locator('#feature-description').fill(description);
  await handle.page.getByRole('button', { name: 'Next: Where' }).click();
  for (const repoPattern of repoPatterns) {
    await handle.page.getByRole('checkbox', { name: repoPattern }).check();
  }
  await handle.page.getByRole('button', { name: 'Next: Pipeline' }).click();
  await handle.page.getByRole('button', { name: 'Next: Review' }).click();
  await beforeSubmit?.();
  const cockpit = handle.page.getByLabel(`Feature ${name}`);
  // Creation immediately replaces the wizard with a cockpit. Some Electron
  // builds keep Playwright's click action pending after that intentional
  // detach, so bound the dispatch and use the authoritative cockpit as the
  // success condition.
  await handle.page
    .getByRole('button', { name: 'Create feature' })
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
  const app = await electron.launch({
    args,
    env: {
      ...minimalEnv(world),
      AGENTICO_E2E_RESOURCES_PATH: resourcesPath,
      AGENTICO_E2E_INSTALL_EXECUTABLE: installExecutablePath,
      ...(options.env ?? {}),
    },
    timeout: 60_000,
  });
  const logs: string[] = [];
  const appProcess = app.process();
  appProcess.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  appProcess.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  await app.context().tracing.start({ screenshots: true, snapshots: true });
  const page = await app.firstWindow({ timeout: 30_000 });
  await page.bringToFront();
  return {
    app,
    appProcess,
    page,
    logs,
    world,
    traceName: options.traceName ?? path.basename(testInfo.file, '.spec.ts'),
    testInfo,
    closed: false,
  };
}

/**
 * Stops tracing (saving the zip, plus an evidence copy) and quits the app
 * through the regular quit path, asserting the process really exited so a
 * hung shutdown can never pass silently.
 */
export async function closeApp(handle: AppHandle): Promise<void> {
  if (handle.closed) {
    return;
  }
  handle.closed = true;
  const tracePath = handle.testInfo.outputPath(`${handle.traceName}-trace.zip`);
  try {
    await handle.app.context().tracing.stop({ path: tracePath });
    copyTraceToEvidence(tracePath, `${handle.traceName}-trace.zip`);
  } catch {
    // Tracing is evidence, not correctness — never fail teardown on it.
  }
  const appProcess = handle.appProcess;
  await installTeardownQuitDialogAnswer(handle).catch(() => undefined);
  await handle.app.close();
  const deadline = Date.now() + 15_000;
  while (appProcess.exitCode === null && appProcess.signalCode === null) {
    if (Date.now() > deadline) {
      killProcessTree(appProcess.pid);
      throw new Error('the app process did not exit within 15s of close()');
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
 */
export async function evidenceShot(handle: AppHandle, name: string): Promise<void> {
  const local = handle.testInfo.outputPath(`${name}.png`);
  await handle.page.screenshot({ path: local });
  const dir = evidenceDir();
  if (dir !== null) {
    const target = path.join(dir, 'screenshots');
    fs.mkdirSync(target, { recursive: true });
    fs.copyFileSync(local, path.join(target, `${name}.png`));
  }
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
      const window = BrowserWindow.getAllWindows()[0];
      window?.setContentSize(size.width, size.height);
    },
    { width, height },
  );
  // Let the renderer re-layout (media queries, narrow-mode attributes).
  await handle.page.waitForTimeout(250);
}

/** Switches the theme through the app's own radiogroup and waits for CSS. */
export async function setTheme(handle: AppHandle, theme: 'light' | 'dark'): Promise<void> {
  // click (not check): the radio is React-controlled and only flips after
  // the theme IPC round-trip, which check() would misread as a failure.
  const radio = handle.page
    .locator('.theme-switcher')
    .getByRole('radio', { name: theme === 'dark' ? 'Dark' : 'Light' });
  // Evidence can intentionally capture the open inbox, which covers the
  // theme picker. Invoke the real DOM control directly so React receives its
  // change event without altering the captured inbox state.
  await radio.evaluate((input: HTMLInputElement) => input.click());
  await expect(handle.page.locator(`html[data-theme="${theme}"]`)).toBeAttached();
  await handle.page.waitForTimeout(150); // let colors settle before screenshots
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
