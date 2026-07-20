/**
 * Bounded termination for Playwright-launched Electron apps plus a
 * last-resort reaper, so automation can never leak live app windows: a
 * graceful close is raced against a timeout, then escalated SIGTERM →
 * SIGKILL, and anything still tracked when the harness itself dies is
 * SIGKILLed from the process exit path.
 */

const trackedProcesses = new Set();
let reaperInstalled = false;

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

function hasExited(handle) {
  return handle.exitCode !== null || handle.signalCode !== null;
}

function exited(handle) {
  if (hasExited(handle)) {
    return Promise.resolve();
  }
  return new Promise((resolveExit) => handle.once('exit', resolveExit));
}

/**
 * Closes a Playwright ElectronApplication with bounded escalation. Returns
 * `{ method: 'graceful' | 'sigterm' | 'sigkill' }` describing what it took;
 * the process is guaranteed dead (or SIGKILLed) when this resolves.
 */
export async function terminateElectronApp(app, handle, options = {}) {
  const { closeTimeoutMs = 5_000, sigtermTimeoutMs = 5_000, sigkillTimeoutMs = 2_000 } = options;
  const exitPromise = exited(handle);
  await Promise.race([app.close().catch(() => undefined), delay(closeTimeoutMs)]);
  if (hasExited(handle)) {
    return { method: 'graceful' };
  }
  handle.kill('SIGTERM');
  if (
    await Promise.race([exitPromise.then(() => true), delay(sigtermTimeoutMs).then(() => false)])
  ) {
    return { method: 'sigterm' };
  }
  handle.kill('SIGKILL');
  await Promise.race([exitPromise, delay(sigkillTimeoutMs)]);
  return { method: 'sigkill' };
}

/** Registers a child process to be SIGKILLed if the harness exits first. */
export function trackProcess(handle) {
  trackedProcesses.add(handle);
}

/** Removes a process from the reaper registry after confirmed shutdown. */
export function untrackProcess(handle) {
  trackedProcesses.delete(handle);
}

/** SIGKILLs every tracked process that is still alive, then clears the set. */
export function reapTrackedProcesses() {
  for (const handle of trackedProcesses) {
    if (!hasExited(handle)) {
      try {
        handle.kill('SIGKILL');
      } catch {
        // already gone
      }
    }
  }
  trackedProcesses.clear();
}

/**
 * Hooks the reaper into harness shutdown: normal exit, Ctrl-C, and the
 * SIGTERM a CI timeout sends. Without this, killing the harness mid-run
 * orphans every launched app.
 */
export function installProcessReaper(proc = process) {
  if (reaperInstalled) {
    return;
  }
  reaperInstalled = true;
  proc.once('exit', reapTrackedProcesses);
  for (const signal of ['SIGINT', 'SIGTERM']) {
    proc.once(signal, () => {
      reapTrackedProcesses();
      proc.exit(1);
    });
  }
}

/**
 * Returns an error message when a launch needed forced termination (the
 * quit-guard-leak condition), or null for a clean quit. Callers should
 * fail fast on a non-null result instead of leaking a window per sample.
 */
export function cleanShutdownFailure(method) {
  if (method === 'graceful') {
    return null;
  }
  return (
    `packaged app did not quit cleanly (required ${method}); ` +
    'the quit guard is blocking automated shutdown, so aborting instead of leaking app instances'
  );
}
