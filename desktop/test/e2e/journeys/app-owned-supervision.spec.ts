import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  setTheme,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { requireDiscovery, tailText, waitForNewServer } from '../helpers/runtime';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('app-owned transient recovery, crash-loop exhaustion, and manual retry', async ({}, testInfo) => {
  const transcript = new Transcript(
    'app-owned-supervision',
    'App-owned runtime recovery and bounded crash-loop supervision',
  );
  const world = createWorld('app-owned-supervision', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'recovery-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'app-owned-supervision' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const initial = requireDiscovery(world);
    const initialConnection = await handle.page.evaluate(() =>
      window.agentico.getConnectionStatus(),
    );
    expect(initialConnection.ownership).toBe('app-owned');
    transcript.json('initial app-owned connection', initialConnection);

    transcript.section('A transient app-owned crash relaunches and adopts a new epoch');
    process.kill(initial.pid, 'SIGKILL');
    await waitFor(() => !processAlive(initial.pid), 'first app-owned server to exit', 15_000);
    const recovered = await waitForNewServer(world, initial.pid);
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const recoveredConnection = await handle.page.evaluate(() =>
      window.agentico.getConnectionStatus(),
    );
    expect(recoveredConnection.status).toBe('ready');
    expect(recoveredConnection.ownership).toBe('app-owned');
    expect(recovered.pid).not.toBe(initial.pid);
    transcript.step(`unexpected exit pid ${initial.pid} recovered as pid ${recovered.pid}`);
    transcript.json('revalidated connection after transient recovery', recoveredConnection);
    await evidenceShot(handle, 'supervision-transient-crash-recovered');

    transcript.section('Rapid success/crash cycles consume the rolling restart budget');
    let currentPid = recovered.pid;
    for (let cycle = 2; cycle <= 3; cycle += 1) {
      process.kill(currentPid, 'SIGKILL');
      await waitFor(() => !processAlive(currentPid), `crash cycle ${cycle} server exit`, 15_000);
      const next = await waitForNewServer(world, currentPid);
      transcript.step(`crash cycle ${cycle}: pid ${currentPid} relaunched as ${next.pid}`);
      currentPid = next.pid;
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 60_000,
      });
    }

    // The next rapid exit occurs after three automatic relaunches inside the
    // rolling minute. No fourth automatic child may be adopted.
    process.kill(currentPid, 'SIGKILL');
    await waitFor(() => !processAlive(currentPid), 'budget-exhausting server exit', 15_000);
    const shell = handle.page.getByRole('region', { name: 'Agentico connection' });
    await expect(shell.getByRole('status')).toContainText(/Crashed/i, { timeout: 60_000 });
    await expect(shell).toContainText(/E_SERVER_CRASH|crash/i);
    await expect(shell).not.toContainText(/External runtime/i);
    await expect(shell.getByRole('button', { name: 'Retry' })).toBeVisible();
    await evidenceShot(handle, 'supervision-crash-loop-exhausted-shell');
    transcript.step('rolling restart budget exhausted; automation stopped on the connection shell');

    transcript.section('Manual retry begins a fresh validated supervision cycle');
    await shell.getByRole('button', { name: 'Retry' }).click();
    const manual = await waitForNewServer(world, currentPid);
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const manualConnection = await handle.page.evaluate(() =>
      window.agentico.getConnectionStatus(),
    );
    expect(manualConnection.status).toBe('ready');
    expect(manualConnection.ownership).toBe('app-owned');
    transcript.step(`manual Retry launched and revalidated pid ${manual.pid}`);
    transcript.json('connection after manual retry', manualConnection);
    await setTheme(handle, 'dark');
    await evidenceShot(handle, 'supervision-manual-retry-recovered');
  } finally {
    if (handle !== null) {
      const logs = persistAppLogs(handle, 'app-owned-supervision-app-server');
      const discovery = readDiscovery(world);
      if (discovery?.auth_token) expect(logs).not.toContain(discovery.auth_token);
      transcript.codeBlock('redacted app/server supervision log tail', tailText(logs, 60));
      transcript.write(testInfo);
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
