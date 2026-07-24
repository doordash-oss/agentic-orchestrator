import { expect, test, type Page } from '@playwright/test';
import path from 'node:path';
import fs from 'node:fs';

const EVIDENCE_DIR = process.env['AGENTICO_EVIDENCE_DIR'] ?? '';

function evidencePath(name: string): string {
  if (EVIDENCE_DIR === '') {
    throw new Error('AGENTICO_EVIDENCE_DIR must be set');
  }
  return path.join(EVIDENCE_DIR, 'screenshots', `${name}.png`);
}

function ensureDir(filePath: string): void {
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
}

async function capture(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  width: number,
  height: number,
  fileName: string,
  waitFor: string,
  preScreenshot?: (page: Page) => Promise<void>,
): Promise<void> {
  await page.goto(`http://localhost:9871/?scene=${scene}&theme=${theme}`);
  await page.evaluate((t) => {
    document.documentElement.dataset['theme'] = t;
  }, theme);
  await page.setViewportSize({ width, height });
  await expect(page.locator(waitFor)).toBeVisible({ timeout: 15_000 });
  if (preScreenshot) {
    await preScreenshot(page);
  }
  const target = evidencePath(fileName);
  ensureDir(target);
  await page.screenshot({ path: target, fullPage: false });
}

async function scrollSettingsSectionIntoCapture(
  page: Page,
  name: 'Updates' | 'Diagnostics',
): Promise<void> {
  await page.addStyleTag({ content: '* { scroll-behavior: auto !important; }' });
  const target = page.getByRole('region', { name });
  await expect(target).toBeVisible({ timeout: 10_000 });
  await target.evaluate((element) => {
    const scroller = element.closest('.tab-panel');
    if (scroller instanceof HTMLElement) {
      const scrollerRect = scroller.getBoundingClientRect();
      const targetRect = element.getBoundingClientRect();
      scroller.scrollTop += targetRect.top - scrollerRect.top - 16;
      return;
    }
    element.scrollIntoView({ block: 'start', inline: 'nearest' });
  });
  await expect(target.getByRole('heading', { name })).toBeInViewport({ timeout: 5_000 });
  await expect(target).toBeInViewport({ timeout: 5_000 });
}

test('capture all visual evidence screenshots', async ({ page }) => {
  test.setTimeout(180_000);

  await capture(
    page,
    'aftercare',
    'dark',
    1440,
    900,
    'published-feature-aftercare-desk-with-maintenance-runway-run-ledger-and-repository-readiness-1440x900',
    '.aftercare',
  );

  await capture(
    page,
    'aftercare',
    'dark',
    760,
    900,
    'published-feature-aftercare-desk-retaining-all-cycle-actions-in-a-narrow-workspace-760x900',
    '.aftercare',
  );

  await capture(
    page,
    'background-ama-expanded',
    'dark',
    1440,
    900,
    'expanded-bottom-docked-ama-streaming-transcript-with-an-inline-question-and-glob-1440x900',
    '.ama-dock[data-mode="expanded"]',
  );

  await capture(
    page,
    'background-ama-compact',
    'light',
    1440,
    900,
    'persistent-compact-ama-composer-with-background-attention-inbox-and-preview-pref-1440x900',
    '.settings-panel__toggle',
    async (p) => {
      const previewToggle = p.locator('.settings-panel__toggle');
      await previewToggle.scrollIntoViewIfNeeded();
      await expect(previewToggle).toBeInViewport({ timeout: 5_000 });
    },
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

  await capture(
    page,
    'background-close-dialog',
    'light',
    1440,
    900,
    'active-workflow-plus-ama-close-dialog-with-keep-running-stop-work-and-quit-and-c-1440x900',
    '.impact-dialog',
  );

  await capture(
    page,
    'background-ama-constrained',
    'dark',
    760,
    900,
    'constrained-workspace-with-compact-ama-question-badge-expanded-exact-question-ta-760x900',
    '.command-palette',
  );

  await capture(
    page,
    'update-passive-active',
    'dark',
    1440,
    900,
    'passive-verified-update-notice-with-active-workflow-and-non-interrupting-install-1440x900',
    '.update-notice',
    async (p) => {
      await p.getByRole('tab', { name: 'History and Rewind' }).click();
      await expect(p.getByRole('group', { name: 'Feature actions' })).toBeVisible({
        timeout: 15_000,
      });
      await expect(p.getByLabel('Current feature status')).toBeVisible({ timeout: 15_000 });
      await expect(p.getByRole('button', { name: 'Install When Idle' })).toBeVisible({
        timeout: 5_000,
      });
    },
  );

  await capture(
    page,
    'settings-updates-ready',
    'light',
    1440,
    900,
    'settings-updates-panel-with-downloaded-version-signature-state-release-note-link-1440x900',
    '.settings-panel__section--updates',
    async (p) => {
      await scrollSettingsSectionIntoCapture(p, 'Updates');
      await p.getByRole('button', { name: 'Restart to Update' }).scrollIntoViewIfNeeded();
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
      await scrollSettingsSectionIntoCapture(p, 'Updates');
      await expect(p.getByText(/package manager/)).toBeVisible({ timeout: 5_000 });
      await p
        .getByRole('button', { name: 'Copy the package-manager command' })
        .scrollIntoViewIfNeeded();
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
      await scrollSettingsSectionIntoCapture(p, 'Updates');
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
      await scrollSettingsSectionIntoCapture(p, 'Diagnostics');
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
    'update-constrained',
    'light',
    760,
    900,
    'constrained-workspace-with-passive-update-notice-and-reachable-updates-status-li-760x900',
    '.update-notice',
    async (p) => {
      await p.getByRole('button', { name: 'Updates' }).click();
      await expect(p.locator('.settings-panel__section--updates')).toBeVisible({
        timeout: 10_000,
      });
      await scrollSettingsSectionIntoCapture(p, 'Updates');
    },
  );

  await capture(
    page,
    'archive',
    'light',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-spine-and-histo-1440x900',
    '.archive-mode__band',
  );

  await capture(
    page,
    'archive',
    'dark',
    1440,
    900,
    'sealed-run-archive-mode-with-selector-read-only-band-muted-phase-spine-and-histo-1440x900-6658c389',
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
    'rebase-preflight',
    'light',
    1440,
    900,
    'guarded-rebase-preflight-with-repository-targets-freshness-blockers-and-impact-c-1440x900',
    '.cycle-journey--rebase',
  );

  await capture(
    page,
    'rebase-preflight',
    'dark',
    1440,
    900,
    'guarded-rebase-preflight-with-repository-targets-freshness-blockers-and-impact-c-1440x900-28539a80',
    '.cycle-journey--rebase',
  );

  await capture(
    page,
    'review-refactor',
    'light',
    1440,
    900,
    'review-comments-preview-beside-explicitly-scoped-refactor-inputs-and-repository-1440x900',
    '.cycle-journey--review-comments',
    async (p) => {
      await p.locator('.cycle-journey--review-comments select').selectOption('signal-lab');
      await p.locator('.cycle-journey__fetch').click();
      await expect(p.locator('.cycle-journey__comments-preview')).toBeVisible({ timeout: 10_000 });
    },
  );

  await capture(
    page,
    'review-refactor',
    'dark',
    1440,
    900,
    'review-comments-preview-beside-explicitly-scoped-refactor-inputs-and-repository-1440x900-dfb57b6e',
    '.cycle-journey--review-comments',
    async (p) => {
      await p.locator('.cycle-journey--review-comments select').selectOption('signal-lab');
      await p.locator('.cycle-journey__fetch').click();
      await expect(p.locator('.cycle-journey__comments-preview')).toBeVisible({ timeout: 10_000 });
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
      await expect(p.locator('.attention-inbox')).toBeVisible({ timeout: 5_000 });
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
      await expect(p.locator('.attention-inbox')).toBeVisible({ timeout: 5_000 });
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

  await capture(
    page,
    'cycle-gate',
    'dark',
    760,
    900,
    'repository-cycle-need_user_input-and-recovery-navigation-in-constrained-layout-d-760x900',
    '.cycle-journey__gate',
  );
});

test('completion workspace screenshots', async ({ page }) => {
  await capture(
    page,
    'completion-inspect',
    'light',
    1440,
    900,
    'guided-completion-with-multi-repository-side-by-side-diff-and-publish-scope-ligh-1440x900',
    '.completion-workspace__inspect',
    async (p) => {
      await expect(p.locator('.completion-workspace__repos')).toBeVisible({ timeout: 15_000 });
      await p.locator('.completion-workspace__repo-select').first().click();
      await expect(p.locator('.completion-workspace__files')).toBeVisible({ timeout: 10_000 });
      await p.locator('.completion-workspace__file').first().click();
      await expect(p.locator('.completion-workspace__file-diff')).toBeVisible({ timeout: 10_000 });
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
      await p.getByRole('button', { name: 'Generate PR narrative' }).click();
      await expect(p.getByPlaceholder('Enter PR title')).not.toHaveValue('');
    },
  );

  await capture(
    page,
    'completion-constrained',
    'dark',
    760,
    900,
    'constrained-completion-workspace-with-unified-diff-and-reachable-primary-actions-760x900',
    '.completion-workspace__inspect',
    async (p) => {
      await expect(p.locator('.completion-workspace__repos')).toBeVisible({ timeout: 15_000 });
      await p.locator('.completion-workspace__repo-select').first().click();
      await expect(p.locator('.completion-workspace__files')).toBeVisible({ timeout: 10_000 });
      await p.locator('.completion-workspace__file').first().click();
      await expect(p.locator('.completion-workspace__file-diff')).toBeVisible({ timeout: 10_000 });
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
