import { expect, test, type Page } from '@playwright/test';
import { capture, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * Scrolls the palette's list so a named group starts at the top — the palette
 * holds more commands than 900px shows, and each capture is about a specific
 * group.
 */
async function scrollPaletteGroupToTop(page: Page, group: string): Promise<void> {
  await expect(page.locator(`.command-palette__group[aria-label="${group}"]`)).toBeVisible();
  await page.evaluate((name) => {
    const list = document.querySelector('.command-palette__list');
    const section = document.querySelector(`.command-palette__group[aria-label="${name}"]`);
    if (list instanceof HTMLElement && section instanceof HTMLElement) {
      list.scrollTop = section.offsetTop - list.offsetTop;
    }
  }, group);
}

/**
 * Scrolls a group to the top and points at one of its entries, so the frame
 * shows the palette's selection affordance where a person put it rather than
 * wherever the list happened to open.
 */
async function scrollPaletteGroupAndPointAt(
  page: Page,
  group: string,
  entry: RegExp,
): Promise<void> {
  await scrollPaletteGroupToTop(page, group);
  await page.getByRole('option', { name: entry }).hover();
}

/**
 * A settings scene is already showing exactly one pane — the window's own
 * source-list selection is what puts it there — so all a capture has to
 * confirm is that the expected pane is the selected row and the rendered one.
 */
async function expectSettingsPane(page: Page, label: string, region: string): Promise<void> {
  await expect(page.locator('.settings-window__pane-row[data-selected="true"]')).toHaveText(label);
  await expect(page.getByRole('region', { name: region })).toBeVisible({ timeout: 10_000 });
}

test('capture all visual evidence screenshots', async ({ page }) => {
  skipWithoutEvidenceDir();
  test.setTimeout(180_000);

  await capture(
    page,
    'overview-lanes',
    'light',
    1440,
    900,
    'overview-with-lane-grouped-features-and-refactoring-pass-1440x900-light',
    '.overview-lanes__groups',
  );

  await capture(
    page,
    'overview-lanes',
    'dark',
    1440,
    900,
    'running-lane-row-live-implementing-feature-rendered-by-the-mock-data-screenshot-1440x900',
    '#overview-lane-running',
  );

  await capture(
    page,
    'overview-empty',
    'dark',
    1440,
    900,
    'overview-empty-workspace-mock-harness-1440x900-dark',
    '.overview-surface__cta',
  );

  await capture(
    page,
    'aftercare',
    'dark',
    1440,
    900,
    'published-feature-aftercare-launchpad-with-focused-runway-and-feature-facts-1440x900',
    '.aftercare-workspace',
  );

  await capture(
    page,
    'aftercare',
    'dark',
    1440,
    900,
    'aftercare-change-manifest-with-repository-switchboard-file-index-and-split-diff-1440x900',
    '.aftercare-workspace',
    async (p) => {
      await p.getByRole('button', { name: 'View changes' }).click();
      await expect(p.getByRole('dialog', { name: 'Feature changes' })).toBeVisible();
      await expect(p.getByText('New description with completion workspace support.')).toBeVisible({
        timeout: 15_000,
      });
      await p.waitForTimeout(250);
    },
  );

  await capture(
    page,
    'aftercare',
    'dark',
    1440,
    900,
    'wide-run-record-desk-with-live-activity-artifact-ledger-and-bounded-content-1440x900',
    '.aftercare-workspace',
    async (p) => {
      await p.getByRole('button', { name: 'View run record' }).click();
      await expect(p.getByRole('dialog', { name: 'Run record' })).toBeVisible();
      await expect(p.getByRole('group', { name: 'Preview view' })).toBeVisible({
        timeout: 15_000,
      });
      await p.getByRole('button', { name: 'Files' }).click();
      await p.getByRole('button', { name: 'Open artifact inquire/out.md' }).click();
      await expect(p.getByLabel('Current run artifact content')).toBeVisible({
        timeout: 15_000,
      });
      await p.waitForTimeout(250);
    },
  );

  await capture(
    page,
    'refactor-pass',
    'dark',
    1440,
    900,
    'active-refactor-pass-custody-strip-ready-to-start-1440x900',
    '.refactor-pass',
  );

  await capture(
    page,
    'refactor-pass',
    'light',
    1440,
    900,
    'active-refactor-pass-custody-strip-ready-to-start-1440x900-light',
    '.refactor-pass',
  );

  await capture(
    page,
    'post-cycle-gate',
    'dark',
    1440,
    900,
    'cycle-need-user-input-sheet-free-text-1440x900',
    '.need-input-sheet',
    async (capturePage) => {
      await capturePage.waitForTimeout(250);
    },
  );

  await capture(
    page,
    'aftercare',
    'dark',
    760,
    900,
    'published-feature-aftercare-launchpad-retaining-actions-and-feature-facts-760x900',
    '.aftercare-workspace',
  );

  await capture(
    page,
    'background-command-palette',
    'dark',
    1728,
    1117,
    'current-feature-command-palette-showing-enabled-actions-and-authoritative-disabl-1728x1117',
    '.command-palette',
  );

  // The two command-palette captures: the fifteen-command Feature group over a
  // selected feature, and the same group disabled on Overview beside the global
  // entries. The palette list scrolls at this size, so each capture brings its
  // subject group to the top of the list first — the same scroll a person does
  // with the arrow keys.
  await capture(
    page,
    'command-palette-feature',
    'dark',
    1440,
    900,
    'command-palette-with-a-feature-selected-full-fifteen-command-feature-group-with-1440x900',
    '.command-palette',
    (target) => scrollPaletteGroupAndPointAt(target, 'Feature', /^Restart/),
  );

  await capture(
    page,
    'command-palette-overview',
    'light',
    1440,
    900,
    'command-palette-with-overview-selected-feature-group-disabled-with-the-no-active-1440x900',
    '.command-palette',
    (target) => scrollPaletteGroupAndPointAt(target, 'File', /^New Feature/),
  );

  await capture(
    page,
    'background-close-dialog',
    'light',
    1440,
    900,
    'active-workflow-plus-ama-close-dialog-with-keep-running-stop-work-and-quit-and-c-1440x900',
    '.impact-dialog',
  );

  // The two banner-era update captures are gone with the banner: the transient
  // popover that replaced it is evidenced by attention-update-popover-evidence.
  await capture(
    page,
    'settings-updates-ready',
    'light',
    1440,
    900,
    'settings-updates-panel-with-downloaded-version-signature-state-release-note-link-1440x900',
    '.settings-panel__section--updates',
    async (p) => {
      await expectSettingsPane(p, 'Updates', 'Updates');
      await expect(p.getByRole('button', { name: 'Restart to Update' })).toBeInViewport({
        timeout: 5_000,
      });
    },
  );

  await capture(
    page,
    'settings-updates-deb',
    'dark',
    1440,
    900,
    'settings-updates-panel-for-a-deb-install-with-verified-package-manager-guidance-1440x900',
    '.settings-panel__section--updates',
    async (p) => {
      await expectSettingsPane(p, 'Updates', 'Updates');
      await expect(p.getByText(/package manager/)).toBeVisible({ timeout: 5_000 });
      await expect(
        p.getByRole('button', { name: 'Copy the package-manager command' }),
      ).toBeInViewport({
        timeout: 5_000,
      });
      await expect(p.getByRole('button', { name: 'Restart to Update' })).toHaveCount(0);
      await expect(p.getByRole('button', { name: 'Stop Work and Install Now' })).toHaveCount(0);
    },
  );

  await capture(
    page,
    'settings-install-now-confirm',
    'light',
    1440,
    900,
    'stop-work-and-install-now-impact-confirmation-showing-workflow-and-ama-consequen-1440x900',
    '.settings-panel__section--updates',
    async (p) => {
      await expectSettingsPane(p, 'Updates', 'Updates');
      await p.getByRole('button', { name: 'Stop Work and Install Now' }).click();
      await expect(p.getByRole('dialog', { name: 'Install update confirmation' })).toBeVisible({
        timeout: 5_000,
      });
    },
  );

  await capture(
    page,
    'settings-diagnostics',
    'dark',
    1440,
    900,
    'settings-diagnostics-panel-with-bounded-redacted-entries-retention-summary-revea-1440x900',
    '.settings-panel__section--diagnostics',
    async (p) => {
      await expectSettingsPane(p, 'Diagnostics', 'Diagnostics');
      await expect(p.getByRole('button', { name: 'Reveal Folder' })).toBeInViewport({
        timeout: 5_000,
      });
      await expect(p.getByRole('button', { name: 'Clear Diagnostics' })).toBeInViewport({
        timeout: 5_000,
      });
      await expect(p.locator('.settings-panel__diagnostic').first()).toBeInViewport({
        timeout: 5_000,
      });
    },
  );

  await capture(
    page,
    'archive',
    'light',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-rail-and-histo-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'archive',
    'dark',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-rail-and-histo-1440x900-6658c389',
    '.archive-mode__band',
  );

  await capture(
    page,
    'pinned',
    'light',
    1440,
    900,
    'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'pinned',
    'dark',
    1440,
    900,
    'historical-artifact-log-inspection-with-current-run-change-and-attention-badges-1440x900-a7472731',
    '.archive-mode__band',
  );

  await capture(
    page,
    'rewind-confirm',
    'light',
    1440,
    900,
    'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900',
    '.rewind-journey__backdrop',
    async (p) => {
      await p.locator('.rewind-journey__option input[value="implement"]').check();
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'rewind-confirm',
    'dark',
    1440,
    900,
    'rewind-consequence-confirmation-with-hierarchical-target-advanced-pipeline-upgra-1440x900-371edd9a',
    '.rewind-journey__backdrop',
    async (p) => {
      await p.locator('.rewind-journey__option input[value="implement"]').check();
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'fork',
    'light',
    1440,
    900,
    'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900',
    '.rewind-journey__backdrop',
    async (p) => {
      await p.locator('.rewind-journey__option input[value="implement"]').check();
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
      const input = p.locator('#rewind-confirm-input');
      await input.fill('REWIND');
      await expect(p.locator('.rewind-journey__submit')).toBeEnabled({ timeout: 5_000 });
      await p.locator('.rewind-journey__submit').click();
      await expect(p.locator('.rewind-journey__success')).toBeVisible({ timeout: 15_000 });
    },
  );

  await capture(
    page,
    'fork',
    'dark',
    1440,
    900,
    'new-current-fork-showing-sealed-source-link-carried-from-provenance-badges-and-p-1440x900-bf76e967',
    '.rewind-journey__backdrop',
    async (p) => {
      await p.locator('.rewind-journey__option input[value="implement"]').check();
      await expect(p.locator('.rewind-journey__preview')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.rewind-journey__next')).toBeEnabled({ timeout: 10_000 });
      await p.locator('.rewind-journey__next').click();
      await expect(p.locator('.rewind-journey__type-confirm')).toBeVisible({ timeout: 5_000 });
      const input = p.locator('#rewind-confirm-input');
      await input.fill('REWIND');
      await expect(p.locator('.rewind-journey__submit')).toBeEnabled({ timeout: 5_000 });
      await p.locator('.rewind-journey__submit').click();
      await expect(p.locator('.rewind-journey__success')).toBeVisible({ timeout: 15_000 });
    },
  );

  await capture(
    page,
    'constrained',
    'light',
    760,
    900,
    'archive-selector-and-return-to-current-control-in-constrained-layout-light-theme-760x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'constrained',
    'dark',
    760,
    900,
    'archive-selector-and-return-to-current-control-in-constrained-layout-dark-theme-760x900',
    '.archive-mode__band',
  );

  // Cycles, Bulk Operations, and Recovery

  await capture(
    page,
    'repo-instrument',
    'light',
    1440,
    900,
    'current-run-repository-instrument-with-routine-lifecycle-controls-and-multi-repo-1440x900',
    '.repo-instrument__list',
  );

  await capture(
    page,
    'repo-instrument',
    'dark',
    1440,
    900,
    'current-run-repository-instrument-with-routine-lifecycle-controls-and-multi-repo-1440x900-1e1e02e8',
    '.repo-instrument__list',
  );

  await capture(
    page,
    'refactor-launch',
    'light',
    1440,
    900,
    'refactor-wizard-what-step-with-inherited-where-and-brief-composer-1440x900',
    '.refactor-wizard',
    async (p) => {
      await p.addStyleTag({ content: '* { animation: none !important; }' });
      await expect(p.getByLabel('Child name')).toBeVisible({ timeout: 10_000 });
      await p.getByLabel('Brief').fill('Separate canonical effort values from provider aliases.');
    },
  );

  await capture(
    page,
    'refactor-launch',
    'dark',
    1440,
    900,
    'refactor-wizard-run-contract-with-parent-seeded-models-and-checkpoints-1440x900-dark',
    '.refactor-wizard',
    async (p) => {
      await expect(p.getByLabel('Child name')).toBeVisible({ timeout: 10_000 });
      await p.getByLabel('Brief').fill('Separate canonical effort values from provider aliases.');
      await p.getByRole('button', { name: 'Next: Pipeline' }).click();
      await p.getByRole('button', { name: 'Next: Review' }).click();
      await expect(p.getByRole('heading', { name: 'Review the run contract' })).toBeVisible({
        timeout: 10_000,
      });
    },
  );

  await capture(
    page,
    'bulk-preview',
    'light',
    1440,
    900,
    'bulk-resume-retry-preview-with-eligible-and-excluded-rows-light-theme-1440x900',
    '.bulk-preview__header',
    async (p) => {
      await p.locator('.bulk-preview__refresh').click();
      await expect(p.locator('.bulk-preview__eligible')).toBeVisible({ timeout: 10_000 });
    },
  );

  await capture(
    page,
    'bulk-queue',
    'dark',
    1440,
    900,
    'bulk-queue-after-cancellation-and-partial-failure-with-ordered-outcomes-dark-the-1440x900',
    '.bulk-preview__header',
    async (p) => {
      await p.locator('.bulk-preview__refresh').click();
      await expect(p.locator('.bulk-preview__eligible')).toBeVisible({ timeout: 10_000 });
      await p.locator('.bulk-preview__run').click();
      await expect(p.locator('.bulk-preview__progress')).toBeVisible({ timeout: 10_000 });
      // Wait for Alpha to succeed so the capture shows a partial-success
      // state, then click Cancel while Gamma (the next row) is in-flight.
      // This produces 1 succeeded · 1 failed · 1 not started (Epsilon never
      // dispatches) — the named post-cancellation terminal state.
      await expect(p.locator('.bulk-preview__row[data-outcome="success"]')).toBeVisible({
        timeout: 15_000,
      });
      const cancelButton = p.locator('.bulk-preview__cancel');
      await expect(cancelButton).toBeVisible({ timeout: 5_000 });
      await cancelButton.click();
      await expect(p.locator('.bulk-preview__progress-text')).toContainText(
        /Cancelled after current/,
        { timeout: 30_000 },
      );
      await expect(p.locator('.bulk-preview__row[data-outcome="not-started"]')).toBeVisible({
        timeout: 10_000,
      });
    },
  );

  await capture(
    page,
    'recovery',
    'light',
    1440,
    900,
    'recovery-priority-attention-inbox-and-dedicated-live-process-first-recovery-work-1440x900',
    '.recovery-workspace__header',
    async (p) => {
      await expect(p.locator('.recovery-workspace__queue')).toBeVisible({ timeout: 15_000 });
      await expect(p.locator('.recovery-attention')).toBeVisible({ timeout: 5_000 });
      // Open the attention inbox so the recovery-priority item is visible
      // alongside other attention classes, sorted first.
      await p.locator('.attention-bell').click();
      await expect(p.locator('.attention-popover')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'recovery',
    'dark',
    1440,
    900,
    'recovery-priority-attention-inbox-and-dedicated-live-process-first-recovery-work-1440x900-9a19f85a',
    '.recovery-workspace__header',
    async (p) => {
      await expect(p.locator('.recovery-workspace__queue')).toBeVisible({ timeout: 15_000 });
      await expect(p.locator('.recovery-attention')).toBeVisible({ timeout: 5_000 });
      await p.locator('.attention-bell').click();
      await expect(p.locator('.attention-popover')).toBeVisible({ timeout: 5_000 });
    },
  );

  await capture(
    page,
    'recovery-constrained',
    'light',
    760,
    900,
    'recovery-item-impact-bounded-logs-per-item-actions-and-partial-outcomes-in-const-760x900',
    '.recovery-workspace__header',
    async (p) => {
      await expect(p.locator('.recovery-workspace__queue')).toBeVisible({ timeout: 15_000 });
      // Open the Kill impact dialog on the second item so the per-item
      // action and the confirmation are visible in one viewport.
      const secondItem = p.locator('.recovery-workspace__item').nth(1);
      const killButton = secondItem.locator('.recovery-workspace__action--kill');
      await expect(killButton).toBeVisible({ timeout: 5_000 });
      await killButton.click();
      await expect(p.locator('.impact-dialog__backdrop')).toBeVisible({ timeout: 5_000 });
    },
  );
});

test('phase rail visual evidence screenshots', async ({ page }) => {
  skipWithoutEvidenceDir();
  test.setTimeout(120_000);

  await capture(
    page,
    'run-gauge',
    'dark',
    1440,
    900,
    'live-run-cockpit-full-profile-rail-nine-segments-completed-current-upcoming-stat-1440x900',
    '.phase-rail__trio',
    async (p) => {
      // Full-profile pipeline: Setup + the 8 large-pipeline phases.
      await expect(p.locator('.phase-rail__segment')).toHaveCount(9);
      await expect(p.locator('.phase-rail__segment[data-state="completed"]')).not.toHaveCount(0);
      await expect(p.locator('.phase-rail__segment[data-state="current"]')).toHaveCount(1);
      await expect(p.locator('.phase-rail__segment[data-state="upcoming"]')).not.toHaveCount(0);
      await expect(
        p.locator('.phase-rail__trio').getByText('Elapsed', { exact: true }),
      ).toBeVisible();
      await expect(p.locator('.phase-rail__trio').getByText('Cost', { exact: true })).toBeVisible();
      await expect(
        p.locator('.phase-rail__trio').getByText('Context', { exact: true }),
      ).toBeVisible();
    },
  );

  await capture(
    page,
    'run-gauge',
    'light',
    1440,
    900,
    'live-run-cockpit-same-state-light-theme-1440x900',
    '.phase-rail__trio',
    async (p) => {
      await expect(p.locator('.phase-rail__segment')).toHaveCount(9);
      await expect(
        p.locator('.phase-rail__trio').getByText('Elapsed', { exact: true }),
      ).toBeVisible();
      await expect(p.locator('.phase-rail__trio').getByText('Cost', { exact: true })).toBeVisible();
      await expect(
        p.locator('.phase-rail__trio').getByText('Context', { exact: true }),
      ).toBeVisible();
    },
  );

  await capture(
    page,
    'run-gauge-held-question',
    'dark',
    1440,
    900,
    'held-on-a-question-current-segment-attention-colored-7px-dot-above-it-trio-readi-1440x900',
    '.phase-rail__dot',
    async (p) => {
      // The run is still active (status stays in ACTIVE_STATUSES) while an
      // open question holds the current segment.
      await expect(p.locator('.phase-rail__segment[data-held="true"]')).toHaveCount(1);
      await expect(p.locator('.phase-rail__dot')).toHaveAttribute(
        'title',
        /Held \d+m for your answer/,
      );
      const waitingEntry = p.locator('.phase-rail__trio-entry[data-attention="true"]');
      await expect(waitingEntry.locator('dt')).toHaveText('Waiting');
      await expect(waitingEntry.locator('dd')).toHaveText(/^\d+m$/);
    },
  );

  await capture(
    page,
    'run-gauge-paused',
    'dark',
    1440,
    900,
    'paused-on-a-need_user_input-gate-trio-reading-paused-nm-dark-theme-1440x900',
    '.phase-rail__trio-entry[data-attention="true"]',
    async (p) => {
      const pausedEntry = p.locator('.phase-rail__trio-entry[data-attention="true"]');
      await expect(pausedEntry.locator('dt')).toHaveText('Paused');
      await expect(pausedEntry.locator('dd')).toHaveText(/^\d+m$/);
    },
  );

  await capture(
    page,
    'archive',
    'dark',
    1440,
    900,
    'archive-mode-sealed-run-rail-at-rest-with-elapsed-cost-and-no-context-read-only-1440x900',
    '.archive-mode__band',
    async (p) => {
      // Sealed run: rail at rest, no current/held segment, Elapsed/Cost but
      // no Context in the trio.
      await expect(p.locator('.phase-rail__segment[data-state="current"]')).toHaveCount(0);
      await expect(p.locator('.phase-rail__dot')).toHaveCount(0);
      await expect(
        p.locator('.phase-rail__trio').getByText('Elapsed', { exact: true }),
      ).toBeVisible();
      await expect(p.locator('.phase-rail__trio').getByText('Cost', { exact: true })).toBeVisible();
      await expect(
        p.locator('.phase-rail__trio').getByText('Context', { exact: true }),
      ).toHaveCount(0);
    },
  );

  await capture(
    page,
    'connection-shell',
    'dark',
    1440,
    900,
    'connection-shell-six-stage-segment-track-mid-connect-dark-theme-1440x900',
    '.phase-rail__track',
    async (p) => {
      await expect(p.locator('.phase-rail__segment')).toHaveCount(6);
      await expect(p.locator('.phase-rail__segment[data-state="current"]')).toHaveCount(1);
      await expect(p.getByText('Connect', { exact: true })).toBeVisible();
    },
  );

  await capture(
    page,
    'setup-wizard',
    'dark',
    1440,
    900,
    'setup-wizard-step-indicator-rendered-by-the-shared-segment-track-variable-step-c-1440x900',
    '.phase-rail__track',
    async (p) => {
      const segments = p.locator('.phase-rail__segment');
      await expect(segments).toHaveCount(3);
      await expect(segments.nth(0)).toHaveAttribute('data-state', 'completed');
      await expect(segments.nth(1)).toHaveAttribute('data-state', 'current');
      await expect(segments.nth(2)).toHaveAttribute('data-state', 'upcoming');
      await expect(p.getByText('Models', { exact: true })).toBeVisible();
    },
  );

  await capture(
    page,
    'overview-lanes',
    'dark',
    1440,
    900,
    'sidebar-running-row-sub-line-showing-phase-n-m-iteration-k-beside-the-pip-row-da-1440x900',
    '#sidebar-row-readme-italian-1 .sidebar__row-subline',
    async (p) => {
      const row = p.locator('#sidebar-row-readme-italian-1');
      await expect(row.locator('.sidebar__row-subline')).toHaveText(
        'Implement · phase 3/5 · iteration 2',
      );
      await expect(row.locator('.pip-rail')).toBeVisible();
    },
  );
});

test('completion workspace screenshots', async ({ page }) => {
  skipWithoutEvidenceDir();
  await capture(
    page,
    'completion-inspect',
    'light',
    1440,
    900,
    'guided-completion-with-multi-repository-side-by-side-diff-and-publish-scope-ligh-1440x900',
    '.changes-manifest',
    async (p) => {
      await expect(p.locator('.changes-manifest__repositories')).toBeVisible({ timeout: 15_000 });
      await expect(p.locator('.changes-manifest__files')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.changes-manifest__preview')).toBeVisible({ timeout: 10_000 });
    },
  );

  await capture(
    page,
    'completion-publish',
    'dark',
    1728,
    1117,
    'guided-completion-with-multi-repository-side-by-side-diff-and-partial-publish-re-1728x1117',
    '.completion-workspace__publish',
    async (p) => {
      await expect(p.locator('.completion-workspace__publish')).toBeVisible({ timeout: 15_000 });
      // Verify the partial outcome scene: already-published, failed-with-last_error, and
      // local-only-excluded are all in frame before capturing.
      await expect(p.getByText('Already published')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.completion-workspace__repo-outcome--failure')).toBeVisible({
        timeout: 10_000,
      });
      await expect(p.locator('.completion-workspace__ineligible-repos')).toBeVisible({
        timeout: 10_000,
      });
    },
  );

  await capture(
    page,
    'completion-delete',
    'light',
    1440,
    900,
    'post-done-cleanup-worktrees-impact-dialog-with-removes-and-preserves-consequences-1440x900',
    '.impact-dialog',
    async (p) => {
      // The cleanup dialog surfaces the reversible worktree-cleanup consequence
      // hierarchy: what it removes versus what it preserves, plus the confirm.
      await expect(p.getByRole('heading', { name: 'Clean worktrees?' })).toBeVisible({
        timeout: 10_000,
      });
      await expect(p.getByText('Completed feature worktrees')).toBeVisible();
      await expect(p.getByText('Branches')).toBeVisible();
      await expect(p.getByRole('button', { name: 'Clean worktrees' })).toBeVisible();
    },
  );
});

test('constrained completion workspace screenshot', async ({ page }) => {
  skipWithoutEvidenceDir();
  await capture(
    page,
    'completion-constrained',
    'dark',
    760,
    900,
    'constrained-completion-workspace-with-unified-diff-and-reachable-primary-actions-760x900',
    '.changes-manifest',
    async (p) => {
      await expect(p.locator('.changes-manifest__repositories')).toBeVisible({ timeout: 15_000 });
      await expect(p.locator('.changes-manifest__files')).toBeVisible({ timeout: 10_000 });
      await expect(p.locator('.changes-manifest__preview')).toBeVisible({ timeout: 10_000 });
    },
  );
});
