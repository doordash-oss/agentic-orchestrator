/**
 * Overview visual-evidence capture: drives the packaged app to produce the
 * contract screenshots for the Overview surface — the populated
 * four-seedable-lane masthead+lanes (dark, with the waiting row's attention
 * wash and `Answer`, and light), the empty workspace's preserved empty state
 * with the toolbar's `New feature`, and the restyled recovery/bulk-preview
 * sections scrolled into view below the lanes.
 *
 * Not a behavioral assertion journey — overview.spec.ts and the unit/
 * component suites already cover the underlying contracts. This spec exists
 * purely to produce evidence artifacts via contractEvidenceShot, which only
 * writes when AGENTICO_EVIDENCE_DIR is set.
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  contractEvidenceShot,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { findFeatureId } from '../helpers/reviewHelpers';
import { setFeatureStatus } from '../helpers/seed';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test.skip(process.platform !== 'darwin', 'macOS-only chrome evidence');
test.skip(
  process.env['AGENTICO_EVIDENCE_DIR'] === undefined || process.env['AGENTICO_EVIDENCE_DIR'] === '',
  'evidence-only journey: contractEvidenceShot writes nothing without AGENTICO_EVIDENCE_DIR',
);

test('Overview evidence: populated four seedable lanes, recovery/bulk below the lanes', async ({}, testInfo) => {
  const world = createWorld('overview-evidence', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'overview-evidence-lab', { commit: true });

  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'overview-evidence' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const names = [
      'Evidence Waiting Row',
      'Evidence Running Row',
      'Evidence Rest Row',
      'Evidence Published Row',
      'Evidence Done Row',
    ];
    for (const name of names) {
      await createFeatureViaForm(handle, {
        name,
        repoPatterns: [/overview-evidence-lab/],
        waitForReady: true,
      });
      await handle.page.getByRole('option', { name: 'Overview' }).click();
    }

    const ids: Record<string, string> = {};
    for (const name of names) {
      ids[name] = await findFeatureId(handle, name);
    }

    const discovery = readDiscovery(world);
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `app-owned server ${discovery.pid} to be reaped`,
        15_000,
      );
    }

    setFeatureStatus(world.stateDir, ids['Evidence Running Row']!, 'Implementing');
    setFeatureStatus(world.stateDir, ids['Evidence Published Row']!, 'Published');
    setFeatureStatus(world.stateDir, ids['Evidence Done Row']!, 'Done');
    setFeatureStatus(world.stateDir, ids['Evidence Waiting Row']!, 'NeedUserInput');
    // 'Evidence Rest Row' is left at CodeReady (At rest).

    handle = await launchApp(world, testInfo, { traceName: 'overview-evidence-relaunch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await handle.page.getByRole('option', { name: 'Overview' }).click();

    const waitingRow = handle.page.getByRole('listitem').filter({
      has: handle.page.locator('.overview-row__name', { hasText: 'Evidence Waiting Row' }),
    });
    await expect(waitingRow.getByRole('button', { name: 'Answer' })).toBeVisible({
      timeout: 15_000,
    });

    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'dark');
    await expect(waitingRow.getByRole('button', { name: 'Answer' })).toBeVisible();
    await contractEvidenceShot(
      handle,
      'overview-populated-with-the-four-seedable-lanes-waiting-on-you-at-rest-published-1440x900',
      1440,
      900,
      'dark',
    );

    await setTheme(handle, 'light');
    await expect(waitingRow.getByRole('button', { name: 'Answer' })).toBeVisible();
    await contractEvidenceShot(
      handle,
      'overview-populated-with-the-four-seedable-lanes-light-theme-1440x900',
      1440,
      900,
      'light',
    );
    await setTheme(handle, 'dark');

    // Populate the bulk preview panel with a fresh preview so the scrolled
    // capture shows real eligible/excluded rows, not just its bare header.
    await handle.page.getByRole('button', { name: 'Fresh preview' }).click();
    await expect(handle.page.locator('.bulk-preview__body')).toBeVisible({ timeout: 15_000 });

    await handle.page.locator('.bulk-preview__body').scrollIntoViewIfNeeded();
    await expect(handle.page.locator('.recovery-workspace')).toBeInViewport({ timeout: 5_000 });
    await expect(handle.page.locator('.bulk-preview__body')).toBeInViewport({ timeout: 5_000 });
    await contractEvidenceShot(
      handle,
      'overview-scrolled-to-the-restyled-recovery-workspace-and-bulk-preview-below-the-1440x900',
      1440,
      900,
      'dark',
    );

    persistAppLogs(handle, 'overview-evidence');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('Overview evidence: empty workspace with the preserved empty state and toolbar New feature', async ({}, testInfo) => {
  const world = createWorld('overview-evidence-empty', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });

  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'overview-evidence-empty' });
    const newFeatureButton = handle.page.getByRole('button', { name: 'New feature' });
    await expect(newFeatureButton).toBeVisible({ timeout: 60_000 });
    await expect(handle.page.getByText('Turn a goal into a supervised run.')).toBeVisible({
      timeout: 15_000,
    });

    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'dark');
    await expect(newFeatureButton).toBeVisible();
    await contractEvidenceShot(
      handle,
      'overview-empty-workspace-with-the-preserved-empty-state-and-toolbar-new-feature-1440x900',
      1440,
      900,
      'dark',
    );

    persistAppLogs(handle, 'overview-evidence-empty');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
