import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, seedRunHistory } from '../helpers/world';

test('history: paginated sealed runs, restored archive selection, immutable inspection, pinned-current-update', async ({}, testInfo) => {
  const transcript = new Transcript(
    'history-readonly',
    'Complete history, restored archive selection, immutable inspection, and pinned-current-update journey',
  );
  const world = createWorld('history-readonly', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'signal-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'history-readonly' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('Create one isolated feature');
    await createFeatureViaForm(handle, {
      name: 'History Journey',
      description: 'Exercise sealed-run history and archive mode.',
      repoPatterns: [/signal-lab/],
      waitForReady: true,
    });

    const featureId = await handle.page.evaluate(async () => {
      const features = await window.agentico.listFeatures();
      return features.find((feature) => feature.name === 'History Journey')?.id;
    });
    if (!featureId) throw new Error('created history feature was not listed');
    transcript.section('Seed seven run-authentic runs with the bundled server stopped');
    await closeApp(handle);
    handle = null;
    seedRunHistory(world, featureId);
    handle = await launchApp(world, testInfo, { traceName: 'history-readonly-seeded' });
    await expect(handle.page.getByRole('option', { name: 'History Journey' })).toBeVisible({
      timeout: 60_000,
    });
    await handle.page.getByRole('option', { name: 'History Journey' }).click();
    const cockpit = handle.page.getByLabel('Feature History Journey');

    transcript.section('Open run history via the History button');
    await handle.page.locator('summary[aria-label="More actions"]').click();
    let historyButton = handle.page.getByRole('menuitem', { name: 'View run history' });
    await expect(historyButton).toBeVisible();
    const seededRuns = await handle.page.evaluate(
      (id) => window.agentico.listRuns({ featureId: id, page: 1, pageSize: 20 }),
      featureId,
    );
    expect(seededRuns.total).toBe(7);
    expect(seededRuns.runs.some((run) => run.runNumber === 6 && run.sealedAt !== undefined)).toBe(
      true,
    );
    await historyButton.click();

    transcript.section('Verify archive mode renders the read-only band');
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Sealed run');
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Read only');

    transcript.section('Verify the run selector is present');
    await expect(handle.page.locator('.archive-mode__select')).toBeVisible();

    transcript.section('Page through older sealed history without losing read-only mode');
    await expect(handle.page.getByLabel('Sealed run pages')).toContainText('Page 1 of 2');
    await handle.page.getByRole('button', { name: 'Older' }).click();
    await expect(handle.page.getByLabel('Sealed run pages')).toContainText('Page 2 of 2');
    await handle.page.locator('.archive-mode__select').selectOption('1');
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Run 1');
    await expect(handle.page.getByLabel('Sealed run pages')).toContainText('Page 2 of 2');
    await handle.page.getByRole('button', { name: /Return to current/ }).click();
    await handle.page.locator('summary[aria-label="More actions"]').click();
    historyButton = handle.page.getByRole('menuitem', { name: 'View run history' });
    await historyButton.click();
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Run 6');

    transcript.section('Inspect a run-authentic artifact and bounded historical log');
    await handle.page.getByRole('button', { name: /history-6/ }).click();
    await expect(handle.page.getByLabel('Artifact content')).toContainText('belongs only to Run 6');
    await handle.page.getByRole('button', { name: 'Open log logs/session.log' }).click();
    await expect(handle.page.getByLabel('Historical log content')).toContainText('sealed run 6');

    transcript.section('Keep history pinned while the current run changes');
    await handle.app.evaluate(({ BrowserWindow }, id) => {
      const window = BrowserWindow.getAllWindows()[0];
      window?.webContents.send('agentico:events:app', {
        type: 'invalidated',
        kind: 'lifecycle.updated',
        featureId: id,
      });
    }, featureId);
    await expect(handle.page.getByText('Current run changed')).toBeVisible();
    await expect(handle.page.locator('.archive-mode__band')).toContainText('Sealed run');

    transcript.section('Verify no mutation controls are mounted in archive mode');
    await expect(cockpit.getByRole('button', { name: 'Start', exact: true })).not.toBeVisible();
    await expect(cockpit.getByRole('button', { name: 'Stop' })).not.toBeVisible();
    await expect(cockpit.getByRole('button', { name: 'Rewind' })).not.toBeVisible();

    transcript.section('Verify the return-to-current control is visible');
    await expect(handle.page.getByRole('button', { name: /Return to current/ })).toBeVisible();

    transcript.section('Capture light-theme archive screenshot at 1440x900');
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    await evidenceShot(
      handle,
      'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-rail-and-histo-1440x900',
    );

    transcript.section('Capture dark-theme archive screenshot at 1440x900');
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-rail-and-histo-1440x900-6658c389',
    );

    transcript.section('Capture light-theme pinned-history screenshot at 1440x900');
    await setTheme(handle, 'light');
    await evidenceShot(
      handle,
      'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900',
    );

    transcript.section('Capture dark-theme pinned-history screenshot at 1440x900');
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900-a7472731',
    );

    transcript.section('Return to current run');
    await handle.page.getByRole('button', { name: /Return to current/ }).click();
    await expect(cockpit.getByText('Code ready', { exact: true }).first()).toBeVisible({
      timeout: 10_000,
    });

    transcript.section('Capture constrained-layout archive screenshots at 760x900');
    await handle.page.locator('summary[aria-label="More actions"]').click();
    historyButton = handle.page.getByRole('menuitem', { name: 'View run history' });
    await historyButton.click();
    await setWindowSize(handle, 760, 900);
    await setTheme(handle, 'light');
    await evidenceShot(
      handle,
      'archive-selector-and-return-to-current-control-in-constrained-layout-light-theme-760x900',
    );
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'archive-selector-and-return-to-current-control-in-constrained-layout-dark-theme-760x900',
    );

    // The archive-run selection is session state, not persisted: the plan
    // explicitly drops per-tab archive-run selections along with the tab
    // strip itself ("archive-run choice becomes session state until Phase
    // 5's run popup"). A relaunch must land back on the current run's own
    // surface, not resurrect the archived selection from before restart.
    transcript.section('Verify relaunch resets to the current run, not the archived selection');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, { traceName: 'history-readonly-relaunch' });
    await expect(handle.page.getByRole('option', { name: 'History Journey' })).toBeVisible({
      timeout: 60_000,
    });
    await handle.page.getByRole('option', { name: 'History Journey' }).click();
    const relaunchedCockpit = handle.page.getByLabel('Feature History Journey');
    await expect(relaunchedCockpit.getByText('Code ready', { exact: true }).first()).toBeVisible({
      timeout: 15_000,
    });
    await expect(handle.page.locator('.archive-mode__band')).toHaveCount(0);

    transcript.section('Journey complete');
  } finally {
    if (handle !== null) {
      transcript.codeBlock(
        'redacted app/server log tail',
        await persistAppLogs(handle, 'history-readonly'),
      );
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
