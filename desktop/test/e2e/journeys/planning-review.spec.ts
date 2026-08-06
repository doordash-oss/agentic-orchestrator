import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { VALID_PLAN, seedPlanReview } from '../helpers/reviewFixture';
import {
  captureAtSize,
  captureBoth,
  editMonaco,
  findFeatureId,
  waitForFeatureStatus,
  waitForFeatureToLeaveStatus,
} from '../helpers/reviewHelpers';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('packaged planning review saves, reconciles, iterates, and approves deliberately', async ({}, testInfo) => {
  const transcript = new Transcript('planning-review', 'Conflict-safe planning review journey');
  const world = createWorld('planning-review', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'review-lab', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'planning-review-create' });
    const feature = await createFeatureViaForm(handle, {
      name: 'Packaged planning review',
      description: 'A real bundled-server review fixture.',
      repoPatterns: [/review-lab/],
    });
    await expect(feature).toBeVisible();
    const featureId = await findFeatureId(handle, 'Packaged planning review');
    await closeApp(handle);
    handle = null;
    seedPlanReview(world, featureId);

    handle = await launchApp(world, testInfo, { traceName: 'planning-review' });
    await waitForReview(handle, 'Packaged planning review');
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await handle.page.getByRole('button', { name: /Attention inbox, 1 pending/ }).click();
    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await inbox.getByRole('button', { name: 'Review' }).click();
    await expect(inbox).toHaveCount(0);
    await expect(handle.page.getByLabel('Review editor')).toBeVisible();
    await setWindowSize(handle, 1440, 900);
    await captureBoth(handle, 'visual_a86879bee8f1', 'visual_64bb02b54688');

    // Validation failure with findings and proceed disabled with reason.
    await editMonaco(handle, '# invalid');
    await expect(handle.page.getByLabel('Validation findings')).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Approve' })).toBeDisabled();
    await setWindowSize(handle, 1440, 900);
    await handle.page.getByLabel('Validation findings').scrollIntoViewIfNeeded();
    await captureBoth(handle, 'visual_f01a4f700061', 'visual_c1bd3b890004');

    // Edit mode with unsaved changes and dirty state.
    await editMonaco(handle, `${VALID_PLAN}\nUnsaved edit before capture.\n`);
    await expect(handle.page.getByRole('button', { name: 'Save draft' })).toBeEnabled();
    await captureAtSize(handle, 1440, 900, 'visual_8bb6c56c9453', 'visual_de8458c48d42');

    // Sanitized Markdown preview of the plan artifact.
    await handle.page.getByRole('button', { name: 'Preview' }).click();
    await expect(handle.page.getByLabel('Sanitized Markdown preview')).toBeVisible();
    await captureAtSize(handle, 1440, 900, 'visual_21029b374925', 'visual_917f1838402a');
    await handle.page.getByRole('button', { name: 'Edit' }).click();

    // Save the draft with the expected revision.
    await handle.page.getByRole('button', { name: 'Save draft' }).click();
    await expect(
      handle.page.getByRole('status').filter({ hasText: 'Saved to the server.' }),
    ).toBeVisible();

    // Side-by-side edit and preview split at wide width.
    await handle.page.getByRole('button', { name: 'Split' }).click();
    await setWindowSize(handle, 1728, 1117);
    await captureBoth(handle, 'visual_59cf5a463325', 'visual_2282eb0674a2');
    await handle.page.getByRole('button', { name: 'Edit' }).click();
    await editMonaco(handle, `${VALID_PLAN}\nLocal revision before conflict.\n`);
    const session = await handle.page.evaluate(
      (id) => window.agentico.readReview({ featureId: id }),
      featureId,
    );
    await handle.page.evaluate(
      ({ id, review }) =>
        window.agentico.saveReview({
          featureId: id,
          reviewId: review.reviewId,
          baseRevision: review.draftRevision,
          text: `${review.text}\nConcurrent server change.\n`,
        }),
      { id: featureId, review: session },
    );
    await handle.page.getByRole('button', { name: 'Save draft' }).click();
    await expect(handle.page.getByLabel('Reconcile stale review draft')).toBeVisible();
    await captureAtSize(handle, 1440, 900, 'visual_d77d4cf593b2', 'visual_0081a79aab16');
    await handle.page.getByRole('button', { name: 'Replace server with mine' }).click();
    await expect(
      handle.page.getByRole('status').filter({ hasText: 'replaced the server draft' }),
    ).toBeVisible();

    await handle.page.getByRole('button', { name: 'Iterate' }).click();
    await waitForFeatureToLeaveStatus(handle.page, featureId, 'PlanNeedsReview');
    transcript.step(
      'saved revision, exposed validation failure, reconciled a concurrent change, then iterated',
    );

    // Create the follow-up feature through the running server, then stop it
    // before writing its deterministic review fixture. The iterated feature
    // intentionally remains under live planning supervision; mutating its
    // run files while that session is active would violate server ownership.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await createFeatureViaForm(handle, {
      name: 'Follow-up planning review',
      description: 'A fresh review gate after an iterate decision.',
      repoPatterns: [/review-lab/],
    });
    const followUpFeatureId = await findFeatureId(handle, 'Follow-up planning review');
    await closeApp(handle);
    handle = null;
    seedPlanReview(world, followUpFeatureId, VALID_PLAN);
    handle = await launchApp(world, testInfo, { traceName: 'planning-review-approve' });
    await waitForReview(handle, 'Follow-up planning review');
    await editMonaco(handle, `${VALID_PLAN}\nApproved follow-up revision.\n`);
    await handle.page.getByRole('button', { name: 'Save draft' }).click();
    await expect(
      handle.page.getByRole('status').filter({ hasText: 'Saved to the server.' }),
    ).toBeVisible();
    // Wait for the debounced validation to complete before checking Approve.
    await handle.page.waitForTimeout(2000);
    await expect(handle.page.getByRole('button', { name: 'Approve' })).toBeEnabled();
    await handle.page.getByRole('button', { name: 'Approve' }).click();
    await waitForFeatureStatus(handle.page, followUpFeatureId, 'Implementing');
    transcript.step('fresh authoritative snapshot cleared the review and resumed implementation');
    const followUpCockpit = handle.page.getByLabel('Feature Follow-up planning review');
    await expect(followUpCockpit.getByRole('button', { name: 'Stop' })).toBeEnabled({
      timeout: 60_000,
    });
    await followUpCockpit.getByRole('button', { name: 'Stop' }).click();
    const stopDialog = handle.page.getByRole('dialog', { name: 'Stop Follow-up planning review?' });
    await expect(stopDialog).toContainText(/currently affects \d+ live sessions?/);
    await stopDialog.getByRole('button', { name: 'Confirm stop' }).click();
    await expect(stopDialog).toHaveCount(0);
    await waitForFeatureToLeaveStatus(handle.page, followUpFeatureId, 'Implementing');
    persistAppLogs(handle, 'planning-review-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'planning-review-app-server');
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function waitForReview(handle: AppHandle, featureName: string): Promise<void> {
  await expect(handle.page.getByRole('option', { name: featureName })).toBeVisible({
    timeout: 60_000,
  });
  await handle.page.getByRole('option', { name: featureName }).click();
  await expect(handle.page.getByLabel('Review editor')).toBeVisible({ timeout: 30_000 });
  await expect(handle.page.getByRole('button', { name: 'Preview' })).toBeVisible({
    timeout: 30_000,
  });
}
