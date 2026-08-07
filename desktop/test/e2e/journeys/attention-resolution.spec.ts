import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Locator, type Page } from '@playwright/test';
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
import {
  createRepo,
  createWorld,
  destroyWorld,
  readDiscovery,
  type JourneyWorld,
  waitFor,
} from '../helpers/world';
import { seedVerificationNeedUserInputGate } from '../helpers/verificationGateFixture';
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';

type AttentionItems = Awaited<ReturnType<Window['agentico']['getAttention']>>['items'];
type AttentionItem = AttentionItems[number];

test('packaged spatial shell keeps tab navigation, draft cancellation, and narrow attention resolution reachable', async ({}, testInfo) => {
  const transcript = new Transcript(
    'spatial-shell-primary-journey',
    'Responsive shell navigation and attention reachability journey',
  );
  const world = createWorld('spatial-shell-primary-journey', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    attentionProvider: true,
  });
  createRepo(world, 'spatial-shell-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'spatial-shell-primary-journey' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_42f748a9d2e3', 'visual_e1f2617ccb55'],
      [1728, 1117, 'visual_2d89461823d2', 'visual_d4263bd0e9a3'],
      [760, 900, 'visual_019103655a5d', 'visual_9cf7517d52eb'],
    ]);

    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_69fc8a51c9a9', 'visual_2aa2e6a46add'],
      [1728, 1117, 'visual_a9c08ce40246', 'visual_29023830502f'],
      [760, 900, 'visual_3e791f2255b7', 'visual_3592d20cde63'],
    ]);
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    await handle.page.getByRole('checkbox', { name: /spatial-shell-lab/ }).check();
    await handle.page.getByRole('button', { name: 'Next: What' }).click();
    await handle.page.locator('#feature-name').fill('Spatial Shell Attention Fixture');
    await handle.page
      .locator('#feature-description')
      .fill('A real packaged feature used to exercise the spatial shell journey.');
    await handle.page.getByRole('button', { name: 'Next: Pipeline' }).click();
    await handle.page.getByRole('button', { name: 'Next: Review' }).click();
    await handle.page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await handle.page.getByRole('button', { name: 'Create feature' }).click();

    const cockpit = handle.page.getByLabel('Feature Spatial Shell Attention Fixture');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_1feaacf0f5f4', 'visual_18913f9d2201'],
      [1728, 1117, 'visual_1fd88d89dc78', 'visual_0aca0bf75870'],
      [760, 900, 'visual_d748f2a5751e', 'visual_ba417de60579'],
    ]);
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();
    await waitForAttentionItem(handle.page, 'perm-allow-once');
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_19d981d14d86', 'visual_5fb6b6a2bc0b'],
      [1728, 1117, 'visual_e695ba621cd2', 'visual_3cf7d9d2e197'],
      [760, 900, 'visual_7c45b5b3218b', 'visual_990b09a8e40c'],
    ]);

    transcript.section('Every sidebar feature remains directly navigable with no overflow menu');
    const overflowNames = Array.from({ length: 12 }, (_, index) => `Spatial overflow ${index + 1}`);
    await handle.page.evaluate(async (names) => {
      for (const name of names) {
        await window.agentico.createFeature({
          name,
          description: 'Created through the packaged IPC contract for sidebar-list coverage.',
          repoKeys: ['spatial-shell-lab'],
          useCurrentBranch: false,
        });
      }
    }, overflowNames);
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    for (const name of overflowNames) {
      const row = handle.page.getByRole('listitem').filter({
        has: handle.page.locator('.overview-row__name', { hasText: sidebarRowNamePattern(name) }),
      });
      await expect(row).toBeVisible({ timeout: 30_000 });
      // This section's actual claim is reachability — every bulk-created
      // feature stays one click away with no overflow affordance — not a
      // specific lane. A just-created feature can occasionally pick up a
      // pending attention item of its own (e.g. a review checkpoint) and
      // land in Waiting rather than At rest; either labeled action reaches
      // the feature (Answer opens the cockpit plainly when there is no
      // concrete pending item to jump to), so asserting a specific label
      // here would test lane classification rather than this section's
      // actual claim.
      const actionButton = row.locator('.overview-row__action');
      await expect(actionButton).toBeVisible({ timeout: 30_000 });
      await actionButton.click();
      await handle.page.getByRole('option', { name: 'Overview' }).click();
    }
    await setWindowSize(handle, 760, 900);
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    // The sidebar is one scrolling list with no overflow affordance: every
    // feature, including the one furthest down the list, stays reachable by
    // its own option role with nothing beyond Playwright's own auto-scroll.
    for (const name of overflowNames) {
      await expect(
        handle.page.getByRole('option', { name: sidebarRowNamePattern(name) }),
      ).toBeVisible();
    }
    await evidenceShot(handle, 'visual_43fa4c4627eb');
    await setTheme(handle, 'dark');
    await evidenceShot(handle, 'visual_262f46c3198f');
    await setTheme(handle, 'light');
    const lastOverflowOption = handle.page.getByRole('option', {
      name: sidebarRowNamePattern('Spatial overflow 12'),
    });
    await lastOverflowOption.click();
    await expect(lastOverflowOption).toHaveAttribute('aria-selected', 'true');

    transcript.section('Dirty focused creation requires deliberate cancellation');
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_5b3f80a793ab', 'visual_51f1a4efb671'],
      [1728, 1117, 'visual_6f63b933d14e', 'visual_790ad17a43d8'],
      [760, 900, 'visual_2e1f4056d015', 'visual_2923a1678a0a'],
    ]);
    await setWindowSize(handle, 760, 900);
    await setTheme(handle, 'light');
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await handle.page.getByRole('checkbox', { name: /spatial-shell-lab/ }).check();
    await handle.page.getByRole('button', { name: 'Next: What' }).click();
    await handle.page.locator('#feature-name').fill('Discarded spatial shell draft');
    await handle.page.getByRole('button', { name: 'Back to Overview' }).click();
    const discard = handle.page.getByRole('dialog', { name: 'Discard feature draft' });
    await expect(discard).toBeVisible();
    await discard.getByRole('button', { name: 'Discard draft' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeFocused();

    transcript.section('Narrow cockpit resolves blocking attention with inspector access');
    await handle.page.getByRole('option', { name: 'Spatial Shell Attention Fixture' }).click();
    await expect(cockpit.getByRole('button', { name: 'Inspector' })).toBeVisible();
    await cockpit.getByRole('button', { name: 'Inspector' }).click();
    const inspector = handle.page.getByRole('dialog', { name: 'Feature inspector' });
    await expect(inspector).toContainText('Spatial Shell Attention Fixture');
    await evidenceShot(handle, 'visual_0124bffc2051');
    await inspector.getByRole('button', { name: 'Close inspector' }).click();
    await setTheme(handle, 'dark');
    await cockpit.getByRole('button', { name: 'Inspector' }).click();
    await expect(inspector).toContainText('Spatial Shell Attention Fixture');
    await evidenceShot(handle, 'visual_e6c912334c69');
    await inspector.getByRole('button', { name: 'Close inspector' }).click();
    await setTheme(handle, 'light');
    const inlineAttention = cockpit.getByRole('region', { name: 'Agent request' });
    await expect(inlineAttention).toBeVisible({ timeout: 30_000 });
    await inlineAttention.getByRole('button', { name: 'Allow once' }).click();
    await waitForAttentionMissing(handle.page, 'perm-allow-once');
    await setTheme(handle, 'dark');
    await evidenceShot(handle, 'narrow-attention-resolution-dark');
    transcript.step(
      'overflow menu, discard confirmation, narrow inspector, and inline attention all completed through the packaged app',
    );
    persistAppLogs(handle, 'spatial-shell-primary-journey-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
});

type VisualCell = readonly [width: number, height: number, light: string, dark: string];

async function captureVisualMatrix(handle: AppHandle, cells: readonly VisualCell[]): Promise<void> {
  for (const [width, height, light, dark] of cells) {
    await setWindowSize(handle, width, height);
    await setTheme(handle, 'light');
    await evidenceShot(handle, light);
    await setTheme(handle, 'dark');
    await evidenceShot(handle, dark);
  }
}

/**
 * Anchors a sidebar row's expected accessible name at the start and requires
 * either the end of the name or whitespace right after it, so a numbered
 * feature name (e.g. "Spatial overflow 1") can't accidentally match a row
 * whose name merely starts with it (e.g. "Spatial overflow 12") once a lane
 * sub-line is appended to the row's accessible name.
 */
function sidebarRowNamePattern(name: string): RegExp {
  return new RegExp(`^${escapeRegExp(name)}(?:$|\\s)`);
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

test('packaged inbox and cockpit resolve real attention classes from the bundled server', async ({}, testInfo) => {
  const transcript = new Transcript(
    'attention-resolution',
    'Prompt-class attention resolution via packaged inbox and cockpit',
  );
  const world = createWorld('attention-resolution', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    attentionProvider: true,
  });
  createRepo(world, 'attention-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'attention-resolution' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.section('Create one isolated feature for blocking-control prompts');
    let cockpit = await createFeatureViaForm(handle, {
      name: 'Packaged Attention Resolution',
      description: 'Fixture-backed blocking prompt classes through the real bundled server.',
      repoPatterns: [/attention-lab/],
      waitForReady: true,
    });
    const attentionFeature = await waitForFeatureNamed(
      handle.page,
      'Packaged Attention Resolution',
    );
    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();

    transcript.section('Global inbox badge and allow-once resolution');
    await waitForAttentionItem(handle.page, 'perm-allow-once');
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    // The old tab strip rendered every open tab's own badge alongside the
    // Home dashboard's, so the same status text appeared twice at once. The
    // sidebar has no such second, always-rendered surface — the equivalent
    // signal is the Overview waiting-lane row's own state-cell text, which
    // now carries the pending-approval summary instead of a separate badge.
    await expect(
      handle.page.locator('.overview-row__state', { hasText: 'Approve 1 request' }),
    ).toHaveCount(1);
    await evidenceShot(handle, 'attention-badges-dashboard-tab-light-wide');
    await handle.page.getByRole('option', { name: /Packaged Attention Resolution/ }).click();
    let inbox = await openInbox(handle);
    let attentionDetail = await expandInboxItem(handle, inbox, /Permission/);
    await expect(attentionDetail.getByText(/Bash .*printf allow-once/).first()).toBeVisible();
    await evidenceShot(handle, 'attention-permission-allow-once');
    await evidenceShot(handle, 'attention-permission-allow-once-light');
    await closeInbox(handle.page);
    await setTheme(handle, 'dark');
    inbox = await openInbox(handle);
    attentionDetail = await expandInboxItem(handle, inbox, /Permission/);
    await expect(attentionDetail.getByText(/Bash .*printf allow-once/).first()).toBeVisible();
    await evidenceShot(handle, 'attention-permission-allow-once-dark');
    await closeInbox(handle.page);
    await setTheme(handle, 'light');
    inbox = await openInbox(handle);
    attentionDetail = await expandInboxItem(handle, inbox, /Permission/);
    await attentionDetail.getByRole('button', { name: 'Allow once' }).click();
    await waitForProviderLog(world, 'response:perm-allow-once:');
    await waitForAttentionMissing(handle.page, 'perm-allow-once');
    await closeInbox(handle.page);
    transcript.step('allow-once permission was exposed in the inbox and answered through IPC');

    transcript.section('Already-resolved stale path stays calm');
    const staleItem = await waitForAttentionItem(handle.page, 'perm-stale');
    if (staleItem.kind !== 'permission') {
      throw new Error(`perm-stale resolved to unexpected attention kind: ${staleItem.kind}`);
    }
    inbox = await openInbox(handle);
    attentionDetail = await expandInboxItem(handle, inbox, /Permission/);
    await expect(attentionDetail.getByText(/stale-resolution/).first()).toBeVisible();
    await serverPost(world, '/api/v1/permissions/answer', {
      request_id: 'perm-stale',
      ...(staleItem.sessionId === undefined ? {} : { session_id: staleItem.sessionId }),
      decision: 'allow_once',
    });
    const staleResult = await handle.page.evaluate(
      ({ sessionId }) =>
        window.agentico.answerPermission({
          requestId: 'perm-stale',
          ...(sessionId === undefined ? {} : { sessionId }),
          decision: 'allow_once',
        }),
      { sessionId: staleItem.sessionId },
    );
    expect(staleResult.alreadyResolved).toBe(true);
    expect(staleResult.notice).toMatch(/already resolved/i);
    await waitForAttentionMissing(handle.page, 'perm-stale');
    await evidenceShot(handle, 'attention-already-resolved-stale-refresh');
    await closeInbox(handle.page);
    transcript.step('a stale permission answer converged to the refreshed server snapshot');

    transcript.section('Inline cockpit resolution for feature-scoped attention');
    await waitForAttentionItem(handle.page, 'perm-deny');
    await closeInbox(handle.page);
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await handle.page.getByRole('option', { name: /Packaged Attention Resolution/ }).click();
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const inlineAttention = cockpit.getByRole('region', { name: 'Agent request' });
    await expect(inlineAttention).toBeVisible({ timeout: 30_000 });
    await expect(inlineAttention.getByText(/printf deny-me/).first()).toBeVisible();
    await evidenceShot(handle, 'attention-inline-permission-deny');
    await inlineAttention.getByRole('button', { name: 'Deny', exact: true }).click();
    await waitForProviderLog(world, 'response:perm-deny:');
    await waitForAttentionMissing(handle.page, 'perm-deny');
    transcript.step('feature cockpit inline attention rendered the same server item and denied it');

    transcript.section('Remembered permission preview, redacted audit, and auto-approval');
    await waitForAttentionItem(handle.page, 'perm-remember');
    inbox = await openInbox(handle);
    attentionDetail = await expandInboxItem(handle, inbox, /Permission/);
    await expect(attentionDetail.getByText(/npm test/).first()).toBeVisible();
    const rememberButton = attentionDetail.getByRole('button', {
      name: /Allow and remember Bash\(npm test \*\)/,
    });
    await expect(rememberButton).toBeVisible();
    await evidenceShot(handle, 'attention-permission-remember-preview');
    await rememberButton.click();
    await waitForProviderLog(world, 'response:perm-remember:');
    await waitForProviderLog(world, 'response:perm-remember-followup:');
    await waitForAttentionMissing(handle.page, 'perm-remember-followup');
    await closeInbox(handle.page);
    const auditPath = path.join(world.runtimeDir, 'permissions', 'remember-audit.jsonl');
    await waitFor(() => fs.existsSync(auditPath), 'permission remember audit log', 15_000);
    const audit = fs.readFileSync(auditPath, 'utf8');
    expect(audit).toContain('"pattern":"Bash(npm test *)"');
    expect(audit).toContain('[redacted]');
    expect(audit).not.toContain('private-token');
    transcript.codeBlock('permission remember audit log', audit);
    transcript.step('second matching Bash request was auto-approved and did not become inbox work');

    transcript.section('AskUser bundle routed from the inbox and resolved from the cockpit');
    await waitForAttentionItem(handle.page, 'ask-bundle');

    // While the question is pending on a still-executing run, the rail
    // substitutes the attention-colored Waiting readout for the plain trio —
    // the same hold the cockpit's own surfaces key on.
    await handle.page.getByRole('option', { name: /Packaged Attention Resolution/ }).click();
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const questionRail = cockpit.locator('.phase-rail');
    await expect(questionRail).toBeVisible({ timeout: 30_000 });
    const waitingEntry = questionRail.locator('.phase-rail__trio-entry[data-attention="true"]');
    await expect(waitingEntry).toHaveCount(1, { timeout: 30_000 });
    await expect(waitingEntry.locator('dt')).toHaveText('Waiting');
    await expect(waitingEntry.locator('dd')).toHaveText(/^(<1m|\d+[mhd])$/);
    await evidenceShot(handle, 'attention-question-rail-waiting');
    transcript.step('the rail read the attention-colored Waiting hold while the question waited');

    inbox = await openInbox(handle);
    // Feature-scoped questions route to the cockpit, where the prompt and
    // options render as the agent's conversation turn and the answer is sent
    // from the composer strip.
    await expandInboxItem(handle, inbox, /Questions/);
    const questionPreview = handle.page.getByRole('dialog', { name: 'Live agent preview' });
    const questionTurn = questionPreview.getByRole('group', { name: 'Agent question' });
    await expect(
      questionTurn.getByText('Which verification tracks should be included?'),
    ).toBeVisible();
    await expect(
      questionTurn.getByText('Exercise renderer and server contracts.').first(),
    ).toBeVisible();
    await questionTurn.getByText('Unit tests', { exact: true }).click();
    await questionTurn.getByText('Packaged smoke', { exact: true }).click();
    await questionTurn
      .getByLabel(/Evidence note free text/)
      .fill('Attach the redacted packaged trace bundle.');
    await evidenceShot(handle, 'attention-askuser-bundle');
    await expect(questionTurn.getByRole('checkbox', { name: /Unit tests/ })).toBeChecked();
    await expect(questionTurn.getByRole('checkbox', { name: /Packaged smoke/ })).toBeChecked();
    await expect(questionTurn.getByLabel(/Evidence note free text/)).toHaveValue(
      'Attach the redacted packaged trace bundle.',
    );
    const questionComposer = questionPreview.getByRole('region', { name: 'Agent request' });
    await questionComposer.getByRole('button', { name: /^Send/ }).click();
    await waitForProviderLog(world, 'response:ask-bundle:');
    const providerLogAfterAsk = readProviderLog(world);
    expect(providerLogAfterAsk).toContain('Unit tests, Packaged smoke');
    expect(providerLogAfterAsk).toContain('Attach the redacted packaged trace bundle.');
    await waitForAttentionMissing(handle.page, 'ask-bundle');
    transcript.step('AskUser multi-select and free-text drafts survived cross-surface submission');

    transcript.section('Feature-scoped help request resolved from the cockpit');
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);
    const helpQueuePath = seedFeatureHelpQueue(world, attentionFeature.id);
    const seededFeatureYaml = fs.readFileSync(helpQueuePath, 'utf8');
    expect(seededFeatureYaml.match(/^help_queue:/gm) ?? []).toHaveLength(1);
    expect(seededFeatureYaml).toContain('Which cockpit help path should continue?');
    transcript.json('seeded feature help queue', {
      featureId: attentionFeature.id,
      featurePath: helpQueuePath,
    });

    handle = await launchApp(world, testInfo, { traceName: 'attention-feature-help-seeded' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await waitForServerPromptText(world, 'Which cockpit help path should continue?');
    const featureHelpItem = await waitForAttentionKind(handle.page, 'help');
    await handle.page.getByRole('option', { name: /Packaged Attention Resolution/ }).click();
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const inlineHelp = cockpit.getByRole('region', { name: 'Agent request' });
    await expect(inlineHelp.getByText('Which cockpit help path should continue?')).toBeVisible();
    await expect(inlineHelp.getByLabel('Help reply')).toBeVisible({ timeout: 30_000 });
    await inlineHelp
      .getByLabel('Help reply')
      .fill('Continue with the cockpit-sourced packaged evidence path.');
    await evidenceShot(handle, 'attention-inline-help-reply');
    await inlineHelp.getByRole('button', { name: 'Send reply' }).click();
    await waitForAttentionMissing(handle.page, featureHelpItem.id);
    await waitFor(
      () =>
        fs
          .readFileSync(helpQueuePath, 'utf8')
          .includes('Continue with the cockpit-sourced packaged evidence path.'),
      'feature help queue answer persisted',
      15_000,
    );
    transcript.step('feature-scoped queued help resolved from the cockpit inline region');

    persistAppLogs(handle, 'attention-resolution-app-server');
    transcript.codeBlock('provider control-response log', readProviderLog(world), 120);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
});

test('packaged inbox resolves an interactive help request from chat', async ({}, testInfo) => {
  const transcript = new Transcript(
    'attention-help',
    'Interactive help attention resolution via packaged inbox',
  );
  const world = createWorld('attention-help', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    attentionProvider: true,
  });
  createRepo(world, 'help-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'attention-help' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const chatStart = await serverPost(world, '/api/v1/prompts/chat/start', {
      message: 'attention chat help',
    });
    transcript.json('chat start response', chatStart);
    await waitForProviderLog(world, 'chat-waiting');
    try {
      await waitForAttentionKind(handle.page, 'help');
    } catch (error) {
      const sessions = await handle.page.evaluate(() => window.agentico.listSessions());
      const prompts = await serverGet(world, '/api/v1/prompts');
      throw new Error(
        `chat did not become actionable: ${error instanceof Error ? error.message : String(error)}; sessions=${JSON.stringify(sessions)}; prompts=${JSON.stringify(prompts)}`,
      );
    }
    await handle.page.reload();
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);

    const inbox = await openInbox(handle);
    const helpDetail = await expandInboxItem(handle, inbox, /Agent waiting/);
    await helpDetail
      .getByLabel('Message to the agent')
      .fill('Continue with the compact packaged evidence path.');
    await evidenceShot(handle, 'attention-help-reply');
    await helpDetail.getByRole('button', { name: 'Send message' }).click();
    await waitForProviderLog(world, 'help-response:');
    await closeInbox(handle.page);

    persistAppLogs(handle, 'attention-help-app-server');
    transcript.step('chat help item accepted a reply and delivered it to the interactive session');
    transcript.codeBlock('provider help-response log', readProviderLog(world), 120);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
});

test('packaged inbox renders and drafts a real NEED_USER_INPUT gate', async ({}, testInfo) => {
  const transcript = new Transcript(
    'attention-gate',
    'Needs-user-input gate attention via packaged inbox',
  );
  const world = createWorld('attention-gate', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'gate-lab', { commit: true });
  createRepo(world, 'gate-stop-lab', { commit: true });
  createRepo(world, 'gate-api-lab', { commit: true });
  createRepo(world, 'gate-web-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'attention-gate' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await createFeatureViaForm(handle, {
      name: 'Packaged Gate Fixture',
      description: 'Seeded run-level NEED_USER_INPUT gate through the real bundled server.',
      repoPatterns: [/gate-lab/],
    });
    const feature = await waitForFeatureNamed(handle.page, 'Packaged Gate Fixture');
    const gatePath = seedVerificationNeedUserInputGate(world, feature.id);
    transcript.json('seeded need-user-input gate', { featureId: feature.id, gatePath });

    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-gate-seeded' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, feature.id);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);

    const initialGateDialog = handle.page.getByRole('dialog', {
      name: 'Verification needs your input',
    });
    await expect(initialGateDialog.getByText('Deployment smoke test')).toBeVisible();
    await expect(initialGateDialog.getByText('make deploy-smoke')).toBeVisible();
    await expect(
      initialGateDialog.getByText('missing declared capability "deployment credentials"'),
    ).toBeVisible();
    await expect(
      initialGateDialog.getByText(
        'Make deployment credentials available, then retry verification.',
      ),
    ).toBeVisible();
    await expect(
      initialGateDialog.getByRole('button', { name: 'Retry verification' }),
    ).toBeDisabled();
    await initialGateDialog.getByRole('button', { name: 'Answer later' }).click();
    await expect(initialGateDialog).toHaveCount(0);
    await handle.page.getByRole('option', { name: 'Overview' }).click();

    const inbox = await openInbox(handle);
    await inbox.getByRole('button', { name: /Input gate/ }).click();
    const routedGateDialog = handle.page.getByRole('dialog', {
      name: 'Verification needs your input',
    });
    await expect(routedGateDialog.getByText('Deployment smoke test')).toBeVisible();
    await expect(routedGateDialog.getByText('make deploy-smoke')).toBeVisible();
    await expect(
      routedGateDialog.getByText('missing declared capability "deployment credentials"'),
    ).toBeVisible();
    await expect(
      routedGateDialog.getByText('Make deployment credentials available, then retry verification.'),
    ).toBeVisible();
    const retryVerification = routedGateDialog.getByRole('button', {
      name: 'Retry verification',
    });
    await expect(retryVerification).toBeDisabled();

    await routedGateDialog
      .getByRole('radio', { name: /I've granted access — retry verification/ })
      .click();
    await waitFor(
      () => fs.readFileSync(gatePath, 'utf8').includes('answer: RETRY_AFTER_AUTH'),
      'need-user-input draft persisted to gate file',
      15_000,
    );
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-gate-relaunch' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, feature.id);
    const relaunchedGateDialog = handle.page.getByRole('dialog', {
      name: 'Verification needs your input',
    });
    await expect(relaunchedGateDialog).toBeVisible();
    await expect(
      relaunchedGateDialog.getByRole('radio', {
        name: /I've granted access — retry verification/,
      }),
    ).toBeChecked();
    await evidenceShot(handle, 'attention-need-user-input-gate');
    await expect(
      relaunchedGateDialog.getByRole('button', { name: 'Retry verification' }),
    ).toBeEnabled();
    await relaunchedGateDialog.getByRole('button', { name: 'Retry verification' }).click();
    await waitForAttentionMissing(handle.page, feature.id, 'gate');
    await waitForProviderLog(world, 'session');
    transcript.codeBlock('drafted need-user-input gate', fs.readFileSync(gatePath, 'utf8'));
    transcript.step(
      'feature-scoped retry choice survived relaunch and dispatched verification from the cockpit',
    );

    transcript.section('Feature-scoped Stop clears the paused gate');
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await createFeatureViaForm(handle, {
      name: 'Packaged Feature Stop Gate Fixture',
      description: 'Seeded feature-scoped NEED_USER_INPUT gate stopped through the cockpit.',
      repoPatterns: [/gate-stop-lab/],
    });
    const stopFeature = await waitForFeatureNamed(
      handle.page,
      'Packaged Feature Stop Gate Fixture',
    );
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    const stopGatePath = seedVerificationNeedUserInputGate(world, stopFeature.id, 'gate-stop-lab');
    const stopRunPath = path.join(world.stateDir, stopFeature.id, 'runs', 'run-001', 'run.yaml');
    transcript.json('seeded persisted feature gate', {
      featureId: stopFeature.id,
      stopGatePath,
    });

    handle = await launchApp(world, testInfo, { traceName: 'attention-feature-stop-gate' });
    await expect(handle.page.getByRole('navigation', { name: 'Feature sidebar' })).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, stopFeature.id);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);
    const featureStopGateDialog = handle.page.getByRole('dialog', {
      name: 'Verification needs your input',
    });
    await expect(featureStopGateDialog).toBeVisible();
    await featureStopGateDialog.getByRole('button', { name: 'Answer later' }).click();
    await expect(featureStopGateDialog).toHaveCount(0);

    const stopFeatureOption = handle.page.getByRole('option', {
      name: 'Packaged Feature Stop Gate Fixture',
    });
    await stopFeatureOption.click();
    await expect(stopFeatureOption).toHaveAttribute('aria-selected', 'true');
    await expect(
      handle.page.getByLabel('Feature Packaged Feature Stop Gate Fixture'),
    ).toBeVisible();
    const featureStopButton = handle.page.getByRole('button', {
      name: 'Stop',
      exact: true,
    });
    await expect(featureStopButton).toBeEnabled();
    await featureStopButton.click();
    const featureStopDialog = handle.page.getByRole('dialog', {
      name: 'Stop Packaged Feature Stop Gate Fixture?',
    });
    await expect(featureStopDialog).toBeVisible();
    await featureStopDialog.getByRole('button', { name: 'Confirm stop' }).click();
    await expect(featureStopDialog).toHaveCount(0);

    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(
          (featureId) => window.agentico.getFeature(featureId),
          stopFeature.id,
        );
        return snapshot.status.toLowerCase() === 'interrupted';
      },
      'feature-scoped gate fixture to reach Interrupted after Stop',
      60_000,
    );
    await waitForAttentionMissing(handle.page, stopFeature.id, 'gate');
    await waitFor(
      () => !fs.readFileSync(stopRunPath, 'utf8').includes('pending_need_user_input_path:'),
      'feature-scoped gate pointer to clear from persisted run state',
      30_000,
    );
    await handle.page.reload();
    await waitForAttentionMissing(handle.page, stopFeature.id, 'gate');
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 0 pending/);
    const reloadedStoppedFeature = await handle.page.evaluate(
      (featureId) => window.agentico.getFeature(featureId),
      stopFeature.id,
    );
    expect(reloadedStoppedFeature.status.toLowerCase()).toBe('interrupted');
    expect(fs.readFileSync(stopRunPath, 'utf8')).not.toContain('pending_need_user_input_path:');
    transcript.step(
      'feature Stop persisted Interrupted, cleared its gate pointer, and kept attention empty after reload',
    );

    persistAppLogs(handle, 'attention-gate-app-server');
    transcript.step(
      'the persisted verification gate retained its typed decision context and cleared durably after retry',
    );
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
});

async function openInbox(handle: AppHandle): Promise<Locator> {
  const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
  if ((await inbox.count()) === 0) {
    await attentionBell(handle.page).click();
  }
  await expect(inbox).toBeVisible();
  return inbox;
}

async function closeInbox(page: Page): Promise<void> {
  const inbox = page.getByRole('complementary', { name: 'Attention inbox' });
  if ((await inbox.count()) === 0) {
    const preview = page.getByRole('dialog', { name: 'Live agent preview' });
    if (await preview.isVisible()) {
      await preview.getByRole('button', { name: 'Close live preview' }).click();
      await expect(preview).toHaveCount(0);
    }
    return;
  }
  await inbox.getByRole('button', { name: 'Close inbox' }).click();
  await expect(inbox).toHaveCount(0);
}

async function expandInboxItem(handle: AppHandle, inbox: Locator, name: RegExp): Promise<Locator> {
  const button = inbox.getByRole('button', { name }).first();
  await expect(button).toBeVisible();
  const expandable = (await button.getAttribute('aria-expanded')) !== null;
  await button.click();
  if (expandable) {
    await expect(button).toHaveAttribute('aria-expanded', 'true');
    return inbox;
  }
  const request = handle.page.getByRole('region', { name: 'Agent request' }).last();
  await expect(request).toBeVisible();
  return request;
}

function attentionBell(page: Page): Locator {
  return page.getByRole('button', { name: /Attention inbox, \d+ pending/ });
}

async function waitForFeatureNamed(
  page: Page,
  name: string,
): Promise<{ id: string; name: string; status: string }> {
  let found: { id: string; name: string; status: string } | undefined;
  await waitFor(
    async () => {
      const features = await page.evaluate(() => window.agentico.listFeatures());
      found = features.find((feature) => feature.name === name);
      return found !== undefined;
    },
    `feature named ${name}`,
    30_000,
  );
  return found!;
}

function seedFeatureHelpQueue(world: JourneyWorld, featureId: string): string {
  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'Published');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '3');
  featureYaml = replaceTopLevelBlock(featureYaml, 'help_queue', [
    'help_queue:',
    '  - question: Which cockpit help path should continue?',
    '    time: 2026-07-15T10:00:00Z',
    '    pending: true',
  ]);
  fs.writeFileSync(featurePath, featureYaml);
  return featurePath;
}

async function waitForAttentionItem(page: Page, id: string): Promise<AttentionItem> {
  return waitForAttentionMatching(page, (item) => item.id === id);
}

async function waitForAttentionKind(
  page: Page,
  kind: AttentionItem['kind'],
): Promise<AttentionItem> {
  return waitForAttentionMatching(page, (item) => item.kind === kind);
}

async function waitForAttentionGate(page: Page, featureId: string): Promise<AttentionItem> {
  return waitForAttentionMatching(
    page,
    (item) => item.kind === 'gate' && item.featureId === featureId,
  );
}

async function waitForAttentionMatching(
  page: Page,
  predicate: (item: AttentionItem) => boolean,
): Promise<AttentionItem> {
  let found: AttentionItem | undefined;
  await waitForAttention(
    page,
    (items) => {
      found = items.find(predicate);
      return found !== undefined;
    },
    60_000,
  );
  if (found === undefined) throw new Error('matching attention item was not returned');
  return found;
}

async function waitForAttentionMissing(
  page: Page,
  id: string,
  kind?: AttentionItem['kind'],
): Promise<void> {
  await waitForAttention(
    page,
    (items) =>
      !items.some((item) =>
        kind === undefined
          ? item.id === id
          : item.kind === kind && item.kind !== 'recovery' && item.featureId === id,
      ),
    30_000,
  );
}

async function waitForAttention(
  page: Page,
  predicate: (items: AttentionItems) => boolean,
  timeoutMs = 30_000,
): Promise<void> {
  await waitFor(
    async () => {
      try {
        const snapshot = await page.evaluate(() => window.agentico.getAttention());
        return predicate(snapshot.items);
      } catch (error) {
        if (error instanceof Error && error.message.includes('E_NOT_CONNECTED')) return false;
        throw error;
      }
    },
    'matching attention snapshot',
    timeoutMs,
  );
}

async function waitForProviderLog(world: JourneyWorld, needle: string): Promise<void> {
  await waitFor(
    () => fs.existsSync(world.providerInvocationLog) && readProviderLog(world).includes(needle),
    `provider log entry ${needle}`,
    30_000,
  );
}

async function assertNoLeakedProcessesEventually(world: JourneyWorld): Promise<void> {
  await waitFor(
    () => {
      try {
        assertNoLeakedProcesses(world);
        return true;
      } catch {
        return false;
      }
    },
    `no leaked processes for ${world.root}`,
    15_000,
  );
  assertNoLeakedProcesses(world);
}

function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}

async function serverPost(
  world: JourneyWorld,
  apiPath: string,
  body: Record<string, unknown>,
): Promise<unknown> {
  const discovery = readDiscovery(world);
  expect(discovery, 'server discovery record should exist').not.toBeNull();
  const response = await fetch(`${discovery!.base_url}${apiPath}`, {
    method: 'POST',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/json',
      'X-Agentico-Client': 'local',
      ...(discovery!.auth_token === undefined
        ? {}
        : { Authorization: `Bearer ${discovery!.auth_token}` }),
    },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  expect(response.ok, text).toBe(true);
  return text === '' ? null : JSON.parse(text);
}

async function serverGet(world: JourneyWorld, apiPath: string): Promise<unknown> {
  const discovery = readDiscovery(world);
  expect(discovery, 'server discovery record should exist').not.toBeNull();
  const response = await fetch(`${discovery!.base_url}${apiPath}`, {
    headers: {
      Accept: 'application/json',
      'X-Agentico-Client': 'local',
      ...(discovery!.auth_token === undefined
        ? {}
        : { Authorization: `Bearer ${discovery!.auth_token}` }),
    },
  });
  const text = await response.text();
  expect(response.ok, text).toBe(true);
  return text === '' ? null : JSON.parse(text);
}

async function waitForServerPromptText(world: JourneyWorld, text: string): Promise<void> {
  await waitFor(
    async () => {
      try {
        const prompts = await serverGet(world, '/api/v1/prompts');
        return JSON.stringify(prompts).includes(text);
      } catch {
        return false;
      }
    },
    `server prompt snapshot containing ${text}`,
    30_000,
  );
}
