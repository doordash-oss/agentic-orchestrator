import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { performance } from 'node:perf_hooks';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { chromium, _electron as electron } from '@playwright/test';
import { installProcessReaper, trackProcess, untrackProcess } from './lib/appTermination.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const repoRoot = dirname(desktopDir);
const distDir = join(desktopDir, 'dist');
const baselinePath = join(desktopDir, 'performance-baselines.json');
const reportPath = join(distDir, 'performance-report.json');
const screenshotHarnessDir = join(desktopDir, 'test', 'e2e', 'screenshot-capture');
const rendererPort = process.env.AGENTICO_PERFORMANCE_PORT ?? '19871';
const rendererOrigin = `http://localhost:${rendererPort}`;
const KiB = 1024;
const MiB = 1024 * KiB;
const MAX_TRANSCRIPT_MESSAGES = 200;

export const WORKLOAD_NAMES = Object.freeze([
  'cold-shell-readiness',
  'authoritative-dashboard-render',
  'maximum-bounded-transcript-append-render',
  'repeated-tab-and-session-changes',
  'reconnect-storms',
  'first-monaco-lazy-load',
  'post-stress-process-memory',
]);

const baselineConfig = JSON.parse(readFileSync(baselinePath, 'utf8'));
const platformKey = `${process.platform}-${process.arch}`;
const budgets = performanceBudgetsForPlatform(baselineConfig, platformKey);

const machine = {
  platform: process.platform,
  arch: process.arch,
  node: process.version,
  packagedLaunchMode: 'packaged-asar-via-local-electron-runtime',
  cpus: os.cpus().map((cpu) => cpu.model)[0] ?? 'unknown',
  cpuCount: os.cpus().length,
  totalMemoryBytes: os.totalmem(),
};

function median(values) {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)] ?? 0;
}

export function performanceBudgetsForPlatform(config, key) {
  if (config.schemaVersion !== 1) {
    throw new Error(`unsupported performance baseline schemaVersion=${config.schemaVersion}`);
  }
  if (config.regressionTolerance !== 1.2) {
    throw new Error('performance baseline regressionTolerance must be 1.2 for the 20% gate');
  }
  const selected = config.platforms?.[key] ?? config.platforms?.default;
  if (selected === undefined) {
    throw new Error(`no performance baselines for ${key} in ${baselinePath}`);
  }
  const missing = WORKLOAD_NAMES.filter((name) => selected[name] === undefined);
  if (missing.length > 0) {
    throw new Error(`performance baselines for ${key} missing ${missing.join(', ')}`);
  }
  return selected;
}

async function sample(name, measure, options = {}) {
  console.log(`performance: measuring ${name}`);
  const policy = baselineConfig.samplePolicy?.[name] ?? {};
  const warmups =
    options.warmups ?? policy.warmups ?? baselineConfig.samplePolicy?.defaultWarmups ?? 1;
  const samples =
    options.samples ?? policy.samples ?? baselineConfig.samplePolicy?.defaultSamples ?? 5;
  for (let i = 0; i < warmups; i += 1) {
    await withTimeout(measure(), `${name} warmup`, 45_000);
  }
  const values = [];
  for (let i = 0; i < samples; i += 1) {
    values.push(await withTimeout(measure(), `${name} sample ${i + 1}`, 45_000));
  }
  const budget = budgets[name];
  if (budget === undefined) {
    throw new Error(`missing performance budget for ${name}`);
  }
  return {
    name,
    unit: 'ms',
    median: median(values),
    samples: values,
    baseline: budget.baselineMs,
    ceiling: budget.ceilingMs,
    regressionLimit: budget.baselineMs * baselineConfig.regressionTolerance,
  };
}

async function memoryScalar(name, measure) {
  console.log(`performance: measuring ${name}`);
  const result = await withTimeout(measure(), `${name} sample`, 60_000);
  const budget = budgets[name];
  if (budget === undefined) {
    throw new Error(`missing performance budget for ${name}`);
  }
  return {
    name,
    unit: 'bytes',
    median: result.after,
    samples: result.samples,
    baseline: budget.baselineBytes,
    ceiling: budget.ceilingBytes,
    regressionLimit: budget.baselineBytes * baselineConfig.regressionTolerance,
    before: result.before,
    settled: result.settled,
  };
}

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

async function withTimeout(promise, label, timeoutMs) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(
          () => reject(new Error(`${label} timed out after ${timeoutMs}ms`)),
          timeoutMs,
        );
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function withRendererHarness(run) {
  const server = await ensureRendererHarness();
  const browser = await chromium.launch();
  try {
    return await run(browser);
  } finally {
    await browser.close();
    await server?.stop();
  }
}

async function ensureRendererHarness() {
  if (await isListening()) {
    return null;
  }
  const viteBin = join(repoRoot, 'node_modules', 'vite', 'bin', 'vite.js');
  const child = spawn(process.execPath, [viteBin, '--port', rendererPort, '--strictPort'], {
    cwd: screenshotHarnessDir,
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, BROWSER: 'none' },
  });
  trackProcess(child);
  const exited = new Promise((resolveStop) => child.once('exit', resolveStop));
  const logs = [];
  child.stdout.on('data', (chunk) => logs.push(chunk.toString()));
  child.stderr.on('data', (chunk) => logs.push(chunk.toString()));
  const deadline = Date.now() + 30_000;
  while (!(await isListening())) {
    if (child.exitCode !== null) {
      throw new Error(`renderer harness exited early:\n${logs.join('')}`);
    }
    if (Date.now() > deadline) {
      child.kill('SIGTERM');
      throw new Error(`timed out waiting for renderer harness:\n${logs.join('')}`);
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  return {
    async stop() {
      child.kill('SIGTERM');
      await Promise.race([exited, delay(5_000).then(() => child.kill('SIGKILL'))]);
      await Promise.race([exited, delay(2_000)]);
      untrackProcess(child);
      child.unref();
      child.stdout.unref();
      child.stderr.unref();
    },
  };
}

function isListening() {
  return new Promise((resolveListening) => {
    const request = http.get(`${rendererOrigin}/`, (response) => {
      response.resume();
      resolveListening(response.statusCode !== undefined && response.statusCode < 500);
    });
    request.on('error', () => resolveListening(false));
    request.setTimeout(500, () => {
      request.destroy();
      resolveListening(false);
    });
  });
}

async function coldShellReadiness() {
  const launch = readPackagedLaunchConfig();
  if (!existsSync(launch.installExecutable)) {
    throw new Error('package verification manifest does not point at a runnable unpacked app');
  }
  if (!existsSync(launch.electronExecutable)) {
    throw new Error(`missing local Electron runtime at ${launch.electronExecutable}`);
  }
  if (!existsSync(launch.appAsar)) {
    throw new Error(`missing packaged app.asar at ${launch.appAsar}`);
  }
  const tempRoot = mkdtempSync(join(os.tmpdir(), 'agentico-perf-shell-'));
  const userData = join(tempRoot, 'user-data');
  const home = join(tempRoot, 'home');
  const readyFile = join(tempRoot, 'ready.json');
  mkdirSync(userData, { recursive: true });
  mkdirSync(home, { recursive: true });
  const started = performance.now();
  const child = spawn(
    launch.electronExecutable,
    [launch.appAsar, ...(process.env.AGENTICO_E2E_NO_SANDBOX === '1' ? ['--no-sandbox'] : [])],
    {
      env: {
        PATH: '/usr/bin:/bin:/usr/sbin:/sbin',
        TMPDIR: process.env.TMPDIR ?? os.tmpdir(),
        LANG: process.env.LANG ?? 'en_US.UTF-8',
        HOME: home,
        AGENTICO_E2E_USER_DATA: userData,
        AGENTICO_E2E_ALLOW_LARGE_WINDOW: '1',
        AGENTICO_E2E_READY_FILE: readyFile,
        AGENTICO_E2E_RESOURCES_PATH: launch.resourcesPath,
        AGENTICO_E2E_INSTALL_EXECUTABLE: launch.installExecutable,
      },
    },
  );
  const logs = [];
  child.stdout?.on('data', (chunk) => logs.push(chunk.toString()));
  child.stderr?.on('data', (chunk) => logs.push(chunk.toString()));
  trackProcess(child);
  const exited = new Promise((resolveExit) => child.once('exit', resolveExit));
  try {
    await waitForReadyFile(readyFile, child, logs, 30_000);
    return performance.now() - started;
  } finally {
    await terminateChild(child, exited);
    untrackProcess(child);
    rmSync(tempRoot, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
  }
}

function readPackagedLaunchConfig() {
  const manifestPath = join(distDir, 'package-verification.json');
  if (!existsSync(manifestPath)) {
    throw new Error('missing dist/package-verification.json; run package verification first');
  }
  return packagedAsarLaunchConfig(JSON.parse(readFileSync(manifestPath, 'utf8')));
}

async function launchPackagedApp(label) {
  const launch = readPackagedLaunchConfig();
  if (!existsSync(launch.installExecutable)) {
    throw new Error('package verification manifest does not point at a runnable unpacked app');
  }
  if (!existsSync(launch.electronExecutable)) {
    throw new Error(`missing local Electron runtime at ${launch.electronExecutable}`);
  }
  if (!existsSync(launch.appAsar)) {
    throw new Error(`missing packaged app.asar at ${launch.appAsar}`);
  }
  const tempRoot = mkdtempSync(join(os.tmpdir(), `agentico-perf-${label}-`));
  const userData = join(tempRoot, 'user-data');
  const home = join(tempRoot, 'home');
  mkdirSync(userData, { recursive: true });
  mkdirSync(home, { recursive: true });
  const args = [launch.appAsar];
  if (process.env.AGENTICO_E2E_NO_SANDBOX === '1') args.push('--no-sandbox');
  const app = await electron.launch({
    executablePath: launch.electronExecutable,
    args,
    env: {
      PATH: '/usr/bin:/bin:/usr/sbin:/sbin',
      TMPDIR: process.env.TMPDIR ?? os.tmpdir(),
      LANG: process.env.LANG ?? 'en_US.UTF-8',
      HOME: home,
      AGENTICO_E2E_USER_DATA: userData,
      AGENTICO_E2E_ALLOW_LARGE_WINDOW: '1',
      AGENTICO_E2E_RESOURCES_PATH: launch.resourcesPath,
      AGENTICO_E2E_INSTALL_EXECUTABLE: launch.installExecutable,
      ...(process.platform === 'linux'
        ? {
            XDG_CONFIG_HOME: join(home, '.config'),
            XDG_CACHE_HOME: join(home, '.cache'),
            XDG_DATA_HOME: join(home, '.local', 'share'),
          }
        : {}),
    },
    timeout: 60_000,
  });
  const appProcess = app.process();
  trackProcess(appProcess);
  const page = await app.firstWindow({ timeout: 30_000 });
  await page.bringToFront();
  await page.waitForFunction(() => 'agentico' in window, undefined, { timeout: 15_000 });
  return {
    app,
    page,
    appProcess,
    pid: appProcess.pid,
    async close() {
      try {
        await app.close();
      } finally {
        untrackProcess(appProcess);
        rmSync(tempRoot, { recursive: true, force: true, maxRetries: 3, retryDelay: 100 });
      }
    },
  };
}

export function packagedAsarLaunchConfig(manifest, platform = process.platform) {
  const installExecutable = manifest.unpacked_app;
  if (typeof installExecutable !== 'string') {
    throw new Error('package verification manifest does not include unpacked_app');
  }
  const resourcesPath = packagedResourcesDirForExecutable(installExecutable, platform);
  return {
    installExecutable,
    resourcesPath,
    appAsar: join(resourcesPath, 'app.asar'),
    electronExecutable: electronExecutableForPlatform(repoRoot, platform),
  };
}

export function electronExecutableForPlatform(root, platform = process.platform) {
  if (platform === 'darwin') {
    return join(
      root,
      'node_modules',
      'electron',
      'dist',
      'Electron.app',
      'Contents',
      'MacOS',
      'Electron',
    );
  }
  if (platform === 'win32') {
    return join(root, 'node_modules', 'electron', 'dist', 'electron.exe');
  }
  return join(root, 'node_modules', 'electron', 'dist', 'electron');
}

export function packagedResourcesDirForExecutable(executable, platform = process.platform) {
  if (platform === 'darwin') {
    return join(dirname(dirname(executable)), 'Resources');
  }
  return join(dirname(executable), 'resources');
}

async function waitForReadyFile(filePath, child, logs, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (!existsSync(filePath)) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(`packaged app exited before readiness:\n${logs.join('')}`);
    }
    if (Date.now() > deadline) {
      throw new Error(
        `packaged app did not report readiness within ${timeoutMs}ms:\n${logs.join('')}`,
      );
    }
    await delay(100);
  }
}

async function terminateChild(child, exited) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  child.kill('SIGTERM');
  if (await Promise.race([exited.then(() => true), delay(5_000).then(() => false)])) {
    return;
  }
  child.kill('SIGKILL');
  await Promise.race([exited, delay(2_000)]);
}

async function renderScene(browser, scene, waitFor, viewport = { width: 1440, height: 900 }) {
  const page = await browser.newPage({ viewport });
  const started = performance.now();
  try {
    await page.goto(`${rendererOrigin}/?scene=${scene}&theme=dark`, {
      waitUntil: 'domcontentloaded',
    });
    await page.locator(waitFor).waitFor({ state: 'visible', timeout: 15_000 });
    return performance.now() - started;
  } finally {
    await page.close();
  }
}

async function boundedTranscriptAppendRender(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  try {
    await page.goto(`${rendererOrigin}/?scene=background-ama-expanded&theme=dark`, {
      waitUntil: 'domcontentloaded',
    });
    await page.locator('.ama-dock[data-mode="expanded"]').waitFor({
      state: 'visible',
      timeout: 15_000,
    });
    await page.waitForFunction(
      () => window.__agenticoMock?.sessionOutputListenerCount() > 0,
      undefined,
      { timeout: 15_000 },
    );
    const started = await page.evaluate(() => performance.now());
    await page.evaluate((maxMessages) => {
      const controls = window.__agenticoMock;
      if (controls === undefined) throw new Error('mock controls are not installed');
      for (let index = 4; index < maxMessages + 80; index += 1) {
        controls.emitSessionOutput({
          subscriptionId: 'subscription-1',
          type: 'record',
          sessionId: '__chat__',
          index,
          message: {
            index,
            role: 'assistant',
            type: 'text',
            text: `Bounded transcript append ${index}`,
          },
        });
      }
    }, MAX_TRANSCRIPT_MESSAGES);
    await page.getByText(`Bounded transcript append ${MAX_TRANSCRIPT_MESSAGES + 79}`).waitFor({
      state: 'visible',
      timeout: 15_000,
    });
    const messageCount = await page.locator('.ama-dock__message').count();
    if (messageCount > MAX_TRANSCRIPT_MESSAGES) {
      throw new Error(
        `bounded transcript render kept ${messageCount} messages; expected <= ${MAX_TRANSCRIPT_MESSAGES}`,
      );
    }
    return (await page.evaluate(() => performance.now())) - started;
  } finally {
    await page.close();
  }
}

async function repeatedTabAndSessionChanges(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  try {
    await page.goto(`${rendererOrigin}/?scene=update-passive-active&theme=light`);
    await page.getByRole('tab', { name: 'Settings' }).waitFor({ state: 'visible' });
    const samples = [];
    for (let i = 0; i < 12; i += 1) {
      const started = performance.now();
      await page.getByRole('tab', { name: i % 2 === 0 ? 'Settings' : 'Home' }).click();
      await page.evaluate(async () => {
        await window.agentico.listSessions();
        await window.agentico.getUpdates();
      });
      samples.push(performance.now() - started);
    }
    return median(samples);
  } finally {
    await page.close();
  }
}

async function reconnectStormPackaged() {
  const launched = await launchPackagedApp('reconnect');
  try {
    const started = await launched.page.evaluate(() => performance.now());
    await launched.page.evaluate(async () => {
      const unsubscribers = Array.from({ length: 60 }, () =>
        window.agentico.onConnectionChanged(() => undefined),
      );
      for (let i = 0; i < 30; i += 1) {
        await window.agentico.getConnectionStatus();
        await window.agentico.getSettings();
        await window.agentico.getUpdates();
      }
      for (const unsubscribe of unsubscribers) {
        unsubscribe();
      }
    });
    return (await launched.page.evaluate(() => performance.now())) - started;
  } finally {
    await launched.close();
  }
}

async function firstMonacoLazyLoad(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  try {
    await page.goto(`${rendererOrigin}/?scene=monaco-lazy-load&theme=dark`);
    await page.getByRole('button', { name: 'Open editor' }).click();
    const started = await page.evaluate(() => performance.now());
    await page.locator('.perf-monaco__editor').waitFor({ state: 'visible', timeout: 15_000 });
    await page.waitForFunction(
      () => {
        const host = document.querySelector('.perf-monaco__editor');
        return Boolean(host && '__monacoEditor' in host);
      },
      undefined,
      { timeout: 15_000 },
    );
    return (await page.evaluate(() => performance.now())) - started;
  } finally {
    await page.close();
  }
}

async function postStressProcessMemory() {
  const launched = await launchPackagedApp('memory');
  try {
    const before = readProcessRssBytes(launched.pid);
    await launched.page.evaluate(async () => {
      for (let cycle = 0; cycle < 20; cycle += 1) {
        await Promise.all([
          window.agentico.getConnectionStatus(),
          window.agentico.getSettings(),
          window.agentico.getUpdates(),
          window.agentico.getThemePreference(),
        ]);
        await window.agentico.setThemePreference(cycle % 2 === 0 ? 'dark' : 'light');
        await window.agentico.updateSettings({
          ama: { drawer: cycle % 2 === 0 ? 'expanded' : 'compact' },
          notifications: { previewEnabled: cycle % 2 === 0 },
        });
        await window.agentico.getThemePreference();
      }
    });
    const samples = await collectRssSamples(launched.pid);
    return {
      before,
      after: samples[samples.length - 1] ?? before,
      samples,
      settled: memorySamplesSettled(samples),
    };
  } finally {
    await launched.close();
  }
}

export async function collectRssSamples(
  pid,
  read = readProcessRssBytes,
  sampleCount = 5,
  intervalMs = 400,
) {
  const samples = [];
  for (let i = 0; i < sampleCount; i += 1) {
    if (i > 0) await delay(intervalMs);
    samples.push(read(pid));
  }
  return samples;
}

export function memorySamplesSettled(samples, toleranceBytes = MiB) {
  if (samples.length < 2) return true;
  for (let i = 1; i < samples.length; i += 1) {
    if (samples[i] <= samples[i - 1] + toleranceBytes) {
      return true;
    }
  }
  return false;
}

function readProcessRssBytes(pid) {
  if (process.platform === 'win32') {
    const result = spawnSync(
      'powershell.exe',
      ['-NoProfile', '-Command', `(Get-Process -Id ${Number(pid)}).WorkingSet64`],
      {
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'pipe'],
      },
    );
    const rssBytes = Number(result.stdout.trim());
    if (!Number.isFinite(rssBytes) || rssBytes <= 0) {
      throw new Error(`could not read RSS for process ${pid}: ${result.stderr.trim()}`);
    }
    return rssBytes;
  }
  const result = spawnSync('ps', ['-o', 'rss=', '-p', String(pid)], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const rssKiB = Number(result.stdout.trim());
  if (!Number.isFinite(rssKiB) || rssKiB <= 0) {
    throw new Error(`could not read RSS for process ${pid}: ${result.stderr.trim()}`);
  }
  return rssKiB * 1024;
}

export function failuresFor(checks) {
  return checks.flatMap((check) => {
    const failures = [];
    if (check.median > check.ceiling) {
      failures.push(
        `${check.name}: ${formatValue(check.median, check.unit)} > ceiling ${formatValue(
          check.ceiling,
          check.unit,
        )}`,
      );
    }
    if (check.median > check.regressionLimit) {
      failures.push(
        `${check.name}: ${formatValue(check.median, check.unit)} > 20% regression budget ${formatValue(
          check.regressionLimit,
          check.unit,
        )}`,
      );
    }
    if (check.settled === false) {
      failures.push(`${check.name}: RSS samples did not settle after stress`);
    }
    return failures;
  });
}

function formatValue(value, unit) {
  if (unit === 'bytes') return `${Math.round(value / 1024 / 1024)} MiB`;
  return `${value.toFixed(1)} ms`;
}

async function main() {
  installProcessReaper();
  const checks = [];
  checks.push(await sample('cold-shell-readiness', coldShellReadiness, { warmups: 0, samples: 2 }));
  await withRendererHarness(async (browser) => {
    checks.push(await sample('first-monaco-lazy-load', () => firstMonacoLazyLoad(browser)));
    checks.push(
      await sample('authoritative-dashboard-render', () =>
        renderScene(browser, 'update-passive-active', '.home-surface__header'),
      ),
    );
    checks.push(
      await sample('maximum-bounded-transcript-append-render', () =>
        boundedTranscriptAppendRender(browser),
      ),
    );
    checks.push(
      await sample('repeated-tab-and-session-changes', () => repeatedTabAndSessionChanges(browser)),
    );
  });
  checks.push(await sample('reconnect-storms', reconnectStormPackaged));
  checks.push(await memoryScalar('post-stress-process-memory', postStressProcessMemory));
  const measured = new Set(checks.map((check) => check.name));
  const missing = WORKLOAD_NAMES.filter((name) => !measured.has(name));
  if (missing.length > 0) {
    throw new Error(`performance harness did not measure ${missing.join(', ')}`);
  }

  const failures = failuresFor(checks);
  mkdirSync(distDir, { recursive: true });
  writeFileSync(
    reportPath,
    `${JSON.stringify(
      {
        checkedAt: new Date().toISOString(),
        harnessVersion: baselineConfig.harnessVersion,
        platformKey,
        machine,
        clock: baselineConfig.clock,
        samplePolicy: baselineConfig.samplePolicy,
        regressionTolerance: baselineConfig.regressionTolerance,
        checks,
        failures,
      },
      null,
      2,
    )}\n`,
  );
  if (failures.length > 0) {
    console.error(`performance check failed:\n- ${failures.join('\n- ')}`);
    console.error(`performance report: ${resolve(reportPath)}`);
    process.exit(1);
  }
  console.log(`performance checks passed: ${checks.map((check) => check.name).join(', ')}`);
  console.log(`performance report: ${resolve(reportPath)}`);
  process.exit(0);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
