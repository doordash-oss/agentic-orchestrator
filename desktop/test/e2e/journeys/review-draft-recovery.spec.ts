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
import { captureBoth, editMonaco, findFeatureId } from '../helpers/reviewHelpers';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, waitFor } from '../helpers/world';

test('packaged review recovers unsaved drafts and keeps hostile markdown inert', async ({}, testInfo) => {
  const transcript = new Transcript('review-draft-recovery', 'Local review-draft recovery journey');
  const world = createWorld('review-draft-recovery', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'recovery-lab', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'review-draft-recovery-create' });
    await createFeatureViaForm(handle, {
      name: 'Packaged recovery review',
      description: 'A real local-draft recovery fixture.',
      repoPatterns: [/recovery-lab/],
    });
    const featureId = await findFeatureId(handle, 'Packaged recovery review');
    await closeApp(handle);
    handle = null;

    const hostile = `${VALID_PLAN}\n<script>window.__escaped = true</script>\n[unsafe](javascript:alert(1))\n![unsafe](https://example.invalid/image.png)\n`;
    seedPlanReview(world, featureId, hostile);
    handle = await launchApp(world, testInfo, { traceName: 'review-draft-recovery-edit' });
    await openReview(handle, 'Packaged recovery review');
    await handle.page.getByRole('button', { name: 'Preview' }).click();
    const preview = handle.page.getByLabel('Sanitized Markdown preview');
    await expect(preview).toContainText('window.__escaped = true');
    await expect(preview.locator('script')).toHaveCount(0);
    await expect(preview.locator('a[href^="javascript:"]')).toHaveCount(0);
    await expect(preview.locator('img')).toHaveCount(0);
    await handle.page.getByRole('button', { name: 'Edit' }).click();
    await editMonaco(handle, `${hostile}\nRecovered after relaunch.\n`);
    const session = await handle.page.evaluate(
      (id) => window.agentico.readReview({ featureId: id }),
      featureId,
    );
    await waitFor(
      () =>
        handle!.page
          .evaluate(
            async ({ id, review }) =>
              window.agentico.loadLocalReviewDraft({
                // Local review drafts are keyed by the connected server's identity.
                runtimeId:
                  (await window.agentico.getConnectionStatus()).serverKey ?? 'default-runtime',
                featureId: id,
                reviewId: review.reviewId,
              }),
            { id: featureId, review: session },
          )
          .then((draft) => draft?.text.includes('Recovered after relaunch.') === true),
      'owner-local review draft persisted before relaunch',
      10_000,
    );
    await closeApp(handle);
    handle = null;

    handle = await launchApp(world, testInfo, { traceName: 'review-draft-recovery' });
    await openReview(handle, 'Packaged recovery review');
    await expect(handle.page.getByText('Recovered unsaved draft', { exact: true })).toBeVisible();
    await expect(
      handle.page.getByRole('button', { name: 'Discard recovered draft' }),
    ).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Compare to server' })).toBeVisible();
    await setWindowSize(handle, 1440, 900);
    await captureBoth(handle, 'visual_423c997270af', 'visual_0e4c3aa31ec2');
    await setWindowSize(handle, 760, 900);
    await expect(handle.page.getByRole('button', { name: 'Split' })).toHaveCount(0);
    await captureBoth(handle, 'visual_2227f2e34e7a', 'visual_096964bea111');
    await handle.page.getByRole('button', { name: 'Discard recovered draft' }).click();
    await expect(handle.page.getByText('Saved draft', { exact: true })).toBeVisible();
    await expect(handle.page.getByText('Recovered unsaved draft', { exact: true })).toHaveCount(0);
    // After discard the editor buffer must reflect the server draft, not the discarded text.
    const editorText = await handle.page.evaluate(() => {
      const editors = (
        window as unknown as {
          monaco?: { editor: { getEditors: () => { getValue: () => string }[] } };
        }
      ).monaco;
      const ed = editors?.editor.getEditors()[0];
      return ed ? ed.getValue() : '';
    });
    expect(editorText).not.toContain('Recovered after relaunch.');
    transcript.step(
      'unsaved owner-local text survived app shutdown, was explicitly labeled, compared, then discarded',
    );
    persistAppLogs(handle, 'review-draft-recovery-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function openReview(handle: AppHandle, name: string): Promise<void> {
  await expect(handle.page.getByRole('option', { name })).toBeVisible({ timeout: 60_000 });
  await handle.page.getByRole('option', { name }).click();
  await expect(handle.page.getByLabel('Review editor')).toBeVisible({ timeout: 30_000 });
  await expect(handle.page.getByRole('button', { name: 'Preview' })).toBeVisible({
    timeout: 30_000,
  });
}
