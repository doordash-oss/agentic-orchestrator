import { expect, test, type Page } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * The secondary surfaces — command palette, settings providers, completion
 * publish, setup wizard, gate sheet, refactor pass — photographed beside the
 * frameless cockpit and the Overview house glyph. Each shot also
 * asserts the migration held on the surface it photographs — a resolved Bench
 * token, a system face, sentence case where the mono-uppercase voice lived —
 * so a capture cannot look plausible while the styling regressed.
 */
const WIDTH = 1440;
const HEIGHT = 900;

/**
 * Only the three Bench stacks may resolve. Asserting the whole computed stack
 * (rather than the absence of the retired faces by name) is both stronger and
 * keeps the retired family names out of the tree, where the removal greps look.
 */
const BENCH_STACK_HEADS = ['-apple-system', 'ui-serif', 'ui-monospace'];

/**
 * Every shot runs with the darwin platform dataset — the vibrant materials and
 * the inset traffic lights are what the app is judged on — and settles past the
 * sheet and panel entrance transitions before the shutter.
 */
async function open(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  waitFor: string,
): Promise<void> {
  await openScene(page, scene, theme, WIDTH, HEIGHT, waitFor, { platform: 'darwin' });
  await page.waitForTimeout(500);
}

async function expectSystemFaces(page: Page, selector: string): Promise<void> {
  const family = await page
    .locator(selector)
    .first()
    .evaluate((el) => getComputedStyle(el).fontFamily);
  // Chromium rewrites BlinkMacSystemFont in the computed value, so the head of
  // the stack is what identifies which Bench stack a rule asked for.
  expect(BENCH_STACK_HEADS, `${selector} resolves "${family}"`).toContain(family.split(',')[0]);
}

/** Sentence case is now CSS-only: no surface may re-apply the label voice. */
async function expectSentenceCase(page: Page, selector: string): Promise<void> {
  const transform = await page
    .locator(selector)
    .first()
    .evaluate((el) => getComputedStyle(el).textTransform);
  expect(transform, `${selector} still uppercases its label`).toBe('none');
}

/** Every migrated declaration must resolve; an unresolved var computes empty. */
async function expectResolvedInk(page: Page, selector: string): Promise<void> {
  const color = await page
    .locator(selector)
    .first()
    .evaluate((el) => getComputedStyle(el).color);
  expect(color, `${selector} has no resolved color`).toMatch(/^rgb/);
}

test('secondary surfaces visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();
  test.setTimeout(180_000);

  // 1. The command palette over the shell: group headers in the sentence-case
  //    caption voice, on system faces.
  await open(page, 'command-palette-feature', 'dark', '.command-palette');
  await expect(page.locator('.command-palette__group h2').first()).toBeVisible();
  await expectSentenceCase(page, '.command-palette__group h2');
  await expectSystemFaces(page, '.command-palette__group h2');
  await shoot(
    page,
    'command-palette-open-over-the-shell-sentence-case-group-headers-system-faces-dar-1440x900',
  );

  // 2. Settings, Providers pane: provider rows and status pills off the
  //    mono-uppercase voice.
  await open(page, 'settings-providers', 'light', '.settings-window__nav-list');
  await expect(page.locator('.settings-window__pane-row[data-selected="true"]')).toHaveText(
    'Providers',
  );
  await expect(page.locator('.settings-panel__provider').first()).toBeVisible({ timeout: 15_000 });
  await expectSentenceCase(page, '.settings-panel__provider-status');
  await expectSystemFaces(page, '.settings-panel__provider-status');
  await expectResolvedInk(page, '.settings-panel__provider-status');
  await shoot(
    page,
    'settings-window-providers-pane-provider-rows-and-status-pills-restyled-off-the-m-1440x900',
  );

  // 3. Completion workspace, guided publish: headings and merge metadata on
  //    Bench tokens.
  await open(page, 'completion-publish', 'dark', '.completion-workspace__publish');
  await expectSystemFaces(page, '.completion-workspace__publish');
  await expectResolvedInk(page, '.completion-workspace__publish');
  await shoot(
    page,
    'completion-workspace-guided-publish-headings-and-merge-metadata-on-bench-tokens-1440x900',
  );

  // 4. Setup wizard: shell cards and status labels on Bench tokens and system
  //    faces.
  await open(page, 'setup-wizard', 'light', '.shell-card__title');
  await expectSentenceCase(page, '.shell-card__title');
  await expectSystemFaces(page, '.shell-card__title');
  await expectResolvedInk(page, '.shell-card__title');
  await expectSentenceCase(page, '.setup-wizard__help-toggle');
  await expectSystemFaces(page, '.setup-wizard__help-toggle');
  await shoot(
    page,
    'setup-wizard-shell-cards-and-status-labels-on-bench-tokens-and-system-faces-ligh-1440x900',
  );

  // 5. Gate sheet: the eyebrow in sentence case, as the mock renders it. The
  //    Recommended badge is not the sheet's — the sheet only ever asks free
  //    text or a verification decision — so the badge is asserted on the
  //    option branch that owns it, which the inline-question captures
  //    photograph.
  await open(page, 'feature-question-bench', 'dark', '.attention-option__recommended');
  await expectSentenceCase(page, '.attention-option__recommended');
  await expectSystemFaces(page, '.attention-option__recommended');
  await expectResolvedInk(page, '.attention-option__recommended');

  await open(page, 'gate-sheet-plain', 'dark', '.need-input-sheet');
  await expect(page.getByRole('button', { name: 'Resume agent' })).toBeDisabled();
  await expectSentenceCase(page, '.need-input-sheet__eyebrow');
  await expectSystemFaces(page, '.need-input-sheet__eyebrow');
  await shoot(
    page,
    'gate-sheet-sentence-case-eyebrow-and-recommended-badge-matching-the-mock-dark-th-1440x900',
  );

  // 6. Refactor pass workspace: station eyebrows and comment types restyled.
  //    A review-feedback pass is the branch that carries both — its selected
  //    comments sit directly under the custody strip — so the disclosure is
  //    opened before the shutter and one frame shows the eyebrows and the
  //    comment-type chips together.
  await open(page, 'refactor-pass-review', 'light', '.refactor-pass');
  await expectSentenceCase(page, '.refactor-pass__station-eyebrow');
  await expectSystemFaces(page, '.refactor-pass__station-eyebrow');
  await page.locator('.refactor-pass__comments-rollup').click();
  await expect(page.locator('.refactor-pass__comment-type').first()).toBeVisible();
  await expectSentenceCase(page, '.refactor-pass__comment-type');
  await expectSystemFaces(page, '.refactor-pass__comment-type');
  await expectResolvedInk(page, '.refactor-pass__comment-type');
  await shoot(
    page,
    'refactor-pass-workspace-station-eyebrows-and-comment-types-restyled-light-theme-1440x900',
  );

  // 7. The cockpit's Live view reached through the real shell, so the same
  //    frame carries the bare transcript and the Overview row's house glyph.
  await open(page, 'overview-lanes', 'dark', '.sidebar__list');
  const overviewGlyph = page.locator('#sidebar-overview .sidebar__row-glyph--house');
  await expect(overviewGlyph).toBeAttached();
  await expect(overviewGlyph.locator('svg')).toBeAttached();
  await page.getByRole('option', { name: /translate README to Italian/i }).click();
  await expect(page.locator('.current-inspection')).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('.conversation__scroll')).toBeVisible({ timeout: 15_000 });
  // Nothing between the content pane and the reading column draws a panel: the
  // column's parent is the preview container itself, with no edge or fill.
  const framing = await page
    .locator('.live-preview')
    .first()
    .evaluate((el) => {
      const parent = el.parentElement!;
      const style = getComputedStyle(parent);
      return {
        parentClass: parent.className,
        borderWidth: style.borderTopWidth,
        background: style.backgroundColor,
      };
    });
  expect(framing.parentClass).toContain('current-inspection__preview');
  expect(framing.borderWidth).toBe('0px');
  expect(framing.background).toBe('rgba(0, 0, 0, 0)');
  await expectSystemFaces(page, '.conversation__scroll');
  // Park the pointer off every row so no hover reads as a selection.
  await page.mouse.move(WIDTH - 12, HEIGHT - 12);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'cockpit-live-view-transcript-bare-in-the-content-pane-with-no-framed-panel-overv-1440x900',
  );
});
