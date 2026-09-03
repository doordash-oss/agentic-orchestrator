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
 * Bench shell visual-evidence capture: drives the packaged app to produce
 * the contract screenshots for the sidebar/toolbar shell (a populated
 * five-lane sidebar, light theme, the Overview stub, the trailing inspector
 * split-view pane, the collapsed sidebar, and the 400x480 minimum with
 * auto-collapse).
 *
 * Not a behavioral assertion journey — window-chrome.spec.ts,
 * workspace-sidebar.spec.ts, and the unit/component suites already cover
 * the underlying contracts. This spec exists purely to produce evidence
 * artifacts via contractEvidenceShot, which only writes when
 * AGENTICO_EVIDENCE_DIR is set.
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

test('Bench shell evidence: five-lane sidebar, themes, inspector, collapse, minimum size', async ({}, testInfo) => {
  const world = createWorld('shell-evidence', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'evidence-lab', { commit: true });

  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'shell-evidence' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    // Create five features via the real form — every one settles into the
    // "At rest" lane once setup completes.
    const names = [
      'Evidence Selected',
      'Evidence Running',
      'Evidence Published',
      'Evidence Done',
      'Evidence Waiting',
    ];
    for (const name of names) {
      await createFeatureViaForm(handle, {
        name,
        repoPatterns: [/evidence-lab/],
        waitForReady: true,
      });
      await handle.page.getByRole('option', { name: 'Overview' }).click();
    }

    const ids: Record<string, string> = {};
    for (const name of names) {
      ids[name] = await findFeatureId(handle, name);
    }

    // Shut the app down to safely rewrite feature.yaml statuses on disk —
    // same pattern as lifecycle-passes/bulk-resume-retry — then relaunch
    // against the same state dir so the sidebar reclassifies into multiple
    // lanes without re-running any real pipeline work.
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

    // Note: seeding an ACTIVE_STATUSES value (e.g. "Implementing") does not
    // survive a relaunch — the server's own startup reconciliation detects
    // there is no live process actually running that phase and downgrades
    // it to "Interrupted" (still correctly bucketed under "Waiting on
    // you", alongside the explicit NeedUserInput fixture below). That is
    // sufficient to populate four of the five lanes without needing to
    // drive a real, long-running pipeline pass end to end.
    setFeatureStatus(world.stateDir, ids['Evidence Running']!, 'Implementing');
    setFeatureStatus(world.stateDir, ids['Evidence Published']!, 'Published');
    setFeatureStatus(world.stateDir, ids['Evidence Done']!, 'Done');
    setFeatureStatus(world.stateDir, ids['Evidence Waiting']!, 'NeedUserInput');
    // 'Evidence Selected' is left at CodeReady (At rest).

    handle = await launchApp(world, testInfo, { traceName: 'shell-evidence-relaunch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    // Sanity: confirm at least three lanes are now populated before
    // spending time on screenshots. The "Done" lane's <details> disclosure
    // defaults closed (WorkspaceShell's expandedLanes initial state), so
    // expand it explicitly before asserting its contents are visible.
    await expect(handle.page.getByRole('group', { name: 'Waiting on you' })).toBeVisible();
    await expect(handle.page.getByRole('group', { name: 'Published' })).toBeVisible();
    await handle.page.locator('summary.sidebar__lane-summary', { hasText: 'Done' }).click();
    await expect(handle.page.getByRole('group', { name: 'Done' })).toBeVisible();
    await expect(handle.page.getByRole('group', { name: 'At rest' })).toBeVisible();

    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'dark');

    // 1. Feature selected, dark theme, populated five-lane sidebar, toolbar
    //    with bell/overflow/inspector-toggle.
    const selectedRow = handle.page.getByRole('option', { name: /Evidence Selected/ });
    await selectedRow.click();
    await expect(handle.page.getByLabel('Feature Evidence Selected')).toBeVisible({
      timeout: 15_000,
    });
    await expect(handle.page.getByRole('button', { name: 'Toggle inspector' })).toBeVisible();
    await contractEvidenceShot(
      handle,
      'shell-with-a-feature-selected-populated-five-lane-sidebar-toolbar-with-bell-over-1440x900',
      1440,
      900,
      'dark',
    );

    // 2. Same, light theme.
    await expect(handle.page.getByLabel('Feature Evidence Selected')).toBeVisible({
      timeout: 15_000,
    });
    await contractEvidenceShot(
      handle,
      'shell-with-a-feature-selected-light-theme-1440x900',
      1440,
      900,
      'light',
    );
    await setTheme(handle, 'dark');
    await expect(handle.page.getByLabel('Feature Evidence Selected')).toBeVisible({
      timeout: 15_000,
    });

    // 3. Overview selected/active, dark theme, the Bench lane lists.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect(handle.page.locator('.overview-surface')).toBeVisible();
    await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await contractEvidenceShot(
      handle,
      'overview-selected-pinned-row-active-lane-lists-in-the-pane-dark-theme-1440x900',
      1440,
      900,
      'dark',
    );

    // 4. Inspector open as a trailing split-view pane.
    await selectedRow.click();
    await expect(handle.page.getByLabel('Feature Evidence Selected')).toBeVisible({
      timeout: 15_000,
    });
    // Wait past the cockpit's initial data fetch — the aria-label the
    // assertions below key on is present the instant the cockpit mounts,
    // even while it still reads "Loading ... from the runtime.", so a
    // screenshot taken immediately after mount can race the fetch.
    await expect(handle.page.getByText('Ready to start')).toBeVisible({ timeout: 15_000 });
    await handle.page.getByRole('button', { name: 'Toggle inspector' }).click();
    await expect(handle.page.getByLabel('Feature inspector')).toBeVisible();
    await contractEvidenceShot(
      handle,
      'inspector-pane-open-as-a-trailing-split-view-dark-theme-1440x900',
      1440,
      900,
      'dark',
    );
    // Close the inspector before continuing so the next captures start
    // clean. Deselecting and reselecting the feature remounts the cockpit
    // (WorkspaceShell keys FeatureCockpit on featureId), which resets its
    // local inspectorOpen state — a more robust reset than re-clicking the
    // same toggle button, whose element handle can go stale across the
    // theme/window-size churn above.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect(handle.page.locator('.overview-surface')).toBeVisible();
    await selectedRow.click();
    await expect(handle.page.getByLabel('Feature Evidence Selected')).toBeVisible({
      timeout: 15_000,
    });
    await expect(handle.page.getByLabel('Feature inspector')).toBeHidden();

    // 5. Sidebar collapsed via the toolbar toggle, full-width toolbar with
    //    traffic-light clearance.
    // A raw CSS locator, not getByRole: `.sidebar[data-collapsed='true']`
    // sets `display: none` (app.css), which removes the element from the
    // accessibility tree — getByRole would stop resolving it the instant
    // it collapses (see the same fix applied to workspace-sidebar.spec.ts).
    const nav = handle.page.locator('nav.sidebar');
    await handle.page.getByRole('button', { name: 'Hide sidebar' }).click();
    await expect(nav).toHaveAttribute('data-collapsed', 'true');
    await contractEvidenceShot(
      handle,
      'sidebar-collapsed-full-width-toolbar-with-traffic-light-clearance-dark-theme-1440x900',
      1440,
      900,
      'dark',
    );
    await handle.page.getByRole('button', { name: 'Show sidebar' }).click();
    await expect(nav).toHaveAttribute('data-collapsed', 'false');

    // 6. Minimum window size, sidebar auto-collapsed by the ~700px breakpoint.
    await setWindowSize(handle, 400, 480);
    await expect(nav).toHaveAttribute('data-collapsed', 'true');
    await contractEvidenceShot(
      handle,
      'minimum-window-with-auto-collapsed-sidebar-dark-theme-400x480',
      400,
      480,
      'dark',
    );
    await setWindowSize(handle, 1440, 900);

    persistAppLogs(handle, 'shell-evidence');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
