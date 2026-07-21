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
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';

type AttentionItems = Awaited<ReturnType<Window['agentico']['getAttention']>>['items'];
type AttentionItem = AttentionItems[number];

interface CycleGateFixture {
  repoName: string;
  cycleType: 'review-comments' | 'refactor';
  summary: string;
  question: string;
  answer: string;
  iteration: number;
}

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
    await handle.page.locator('#feature-name').fill('Spatial Shell Attention Fixture');
    await handle.page
      .locator('#feature-description')
      .fill('A real packaged feature used to exercise the spatial shell journey.');
    await handle.page.getByRole('button', { name: 'Next: Where' }).click();
    await handle.page.getByRole('checkbox', { name: /spatial-shell-lab/ }).check();
    await handle.page.getByRole('button', { name: 'Next: Pipeline' }).click();
    await handle.page.getByRole('button', { name: 'Next: Review' }).click();
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
    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();
    await waitForAttentionItem(handle.page, 'perm-allow-once');
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_19d981d14d86', 'visual_5fb6b6a2bc0b'],
      [1728, 1117, 'visual_e695ba621cd2', 'visual_3cf7d9d2e197'],
      [760, 900, 'visual_7c45b5b3218b', 'visual_990b09a8e40c'],
    ]);

    transcript.section('Overflowing feature tabs remain directly navigable');
    const overflowNames = Array.from({ length: 12 }, (_, index) => `Spatial overflow ${index + 1}`);
    await handle.page.evaluate(async (names) => {
      for (const name of names) {
        await window.agentico.createFeature({
          name,
          description: 'Created through the packaged IPC contract for tab overflow coverage.',
          repoKeys: ['spatial-shell-lab'],
          useCurrentBranch: false,
        });
      }
    }, overflowNames);
    await handle.page.getByRole('tab', { name: 'Home' }).click();
    for (const name of overflowNames) {
      const row = handle.page.getByRole('listitem').filter({ hasText: new RegExp(`^${name}\\b`) });
      await expect(row).toBeVisible({ timeout: 30_000 });
      await row.getByRole('button', { name: 'Open' }).click();
      await handle.page.getByRole('tab', { name: 'Home' }).click();
    }
    await setWindowSize(handle, 760, 900);
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');
    let tabTarget = await featureTabNavigationTarget(handle);
    await evidenceShot(handle, 'visual_43fa4c4627eb');
    await setTheme(handle, 'dark');
    tabTarget = await featureTabNavigationTarget(handle);
    await evidenceShot(handle, 'visual_262f46c3198f');
    await setTheme(handle, 'light');
    tabTarget = await featureTabNavigationTarget(handle);
    await tabTarget.click();
    await expect(handle.page.getByRole('tab', { name: 'Spatial overflow 12' })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    transcript.section('Dirty focused creation requires deliberate cancellation');
    await handle.page.getByRole('tab', { name: 'Home' }).click();
    await captureVisualMatrix(handle, [
      [1440, 900, 'visual_5b3f80a793ab', 'visual_51f1a4efb671'],
      [1728, 1117, 'visual_6f63b933d14e', 'visual_790ad17a43d8'],
      [760, 900, 'visual_2e1f4056d015', 'visual_2923a1678a0a'],
    ]);
    await setWindowSize(handle, 760, 900);
    await setTheme(handle, 'light');
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    await handle.page.locator('#feature-name').fill('Discarded spatial shell draft');
    await handle.page.getByRole('button', { name: 'Back to Home' }).click();
    const discard = handle.page.getByRole('dialog', { name: 'Discard feature draft' });
    await expect(discard).toBeVisible();
    await discard.getByRole('button', { name: 'Discard draft' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeFocused();

    transcript.section('Narrow cockpit resolves blocking attention with inspector access');
    await handle.page.getByRole('tab', { name: 'Spatial Shell Attention Fixture' }).click();
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
    const inlineAttention = cockpit.getByRole('region', { name: 'Feature attention' });
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

async function featureTabNavigationTarget(handle: AppHandle): Promise<Locator> {
  const visibleTab = handle.page.getByRole('tab', { name: 'Spatial overflow 12' });
  if (await visibleTab.isVisible()) return visibleTab;
  await handle.page.getByRole('button', { name: /Tabs/ }).click();
  const menu = handle.page.getByRole('menu', { name: 'Open features' });
  const menuItem = menu.getByRole('menuitem', { name: 'Spatial overflow 12' });
  await expect(menuItem).toBeVisible();
  return menuItem;
}

test('packaged inbox and cockpit resolve real attention classes from the bundled server', async ({
  page: _page,
}, testInfo) => {
  const transcript = new Transcript(
    'attention-resolution',
    'Phase 3 — packaged prompt-class attention resolution',
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
    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();

    transcript.section('Global inbox badge and allow-once resolution');
    await waitForAttentionItem(handle.page, 'perm-allow-once');
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);
    await handle.page.getByRole('tab', { name: 'Home' }).click();
    await expect(
      handle.page.getByRole('status', {
        name: 'Blocking input for Packaged Attention Resolution: 1 pending',
      }),
    ).toHaveCount(2);
    await evidenceShot(handle, 'attention-badges-dashboard-tab-light-wide');
    await handle.page.getByRole('tab', { name: /Packaged Attention Resolution/ }).click();
    let inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Permission/);
    await expect(inbox.getByText(/Bash .*printf allow-once/).first()).toBeVisible();
    await expect(inbox.getByText(/waiting/).first()).toBeVisible();
    await evidenceShot(handle, 'attention-permission-allow-once');
    await evidenceShot(handle, 'attention-permission-allow-once-light');
    await closeInbox(handle.page);
    await setTheme(handle, 'dark');
    inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Permission/);
    await expect(inbox.getByText(/Bash .*printf allow-once/).first()).toBeVisible();
    await evidenceShot(handle, 'attention-permission-allow-once-dark');
    await closeInbox(handle.page);
    await setTheme(handle, 'light');
    inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Permission/);
    await inbox.getByRole('button', { name: 'Allow once' }).click();
    await waitForProviderLog(world, 'response:perm-allow-once:');
    await waitForAttentionMissing(handle.page, 'perm-allow-once');
    transcript.step('allow-once permission was exposed in the inbox and answered through IPC');

    transcript.section('Already-resolved stale path stays calm');
    const staleItem = await waitForAttentionItem(handle.page, 'perm-stale');
    if (staleItem.kind !== 'permission') {
      throw new Error(`perm-stale resolved to unexpected attention kind: ${staleItem.kind}`);
    }
    inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Permission/);
    await expect(inbox.getByText(/stale-resolution/).first()).toBeVisible();
    await serverPost(world, '/api/v1/permissions/answer', {
      request_id: 'perm-stale',
      ...(staleItem.sessionId === undefined ? {} : { session_id: staleItem.sessionId }),
      decision: 'allow_once',
    });
    await expect(inbox.getByText(/already resolved/i)).toBeVisible({ timeout: 30_000 });
    await evidenceShot(handle, 'attention-already-resolved-stale-notice');
    await waitForAttentionMissing(handle.page, 'perm-stale');
    await closeInbox(handle.page);
    transcript.step('a stale permission answer produced the already-resolved notice and refreshed');

    transcript.section('Inline cockpit resolution for feature-scoped attention');
    await waitForAttentionItem(handle.page, 'perm-deny');
    await closeInbox(handle.page);
    await handle.page.getByRole('tab', { name: 'Home' }).click();
    await handle.page.getByRole('tab', { name: /Packaged Attention Resolution/ }).click();
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const inlineAttention = cockpit.getByRole('region', { name: 'Feature attention' });
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
    await expandInboxItem(inbox, /Permission/);
    await expect(inbox.getByText(/npm test/).first()).toBeVisible();
    const rememberButton = inbox.getByRole('button', {
      name: /Allow and remember Bash\(npm test \*\)/,
    });
    await expect(rememberButton).toBeVisible();
    await evidenceShot(handle, 'attention-permission-remember-preview');
    await rememberButton.click();
    await waitForProviderLog(world, 'response:perm-remember:');
    await waitForProviderLog(world, 'response:perm-remember-followup:');
    await waitForAttentionMissing(handle.page, 'perm-remember-followup');
    const auditPath = path.join(world.runtimeDir, 'permissions', 'remember-audit.jsonl');
    await waitFor(() => fs.existsSync(auditPath), 'permission remember audit log', 15_000);
    const audit = fs.readFileSync(auditPath, 'utf8');
    expect(audit).toContain('"pattern":"Bash(npm test *)"');
    expect(audit).toContain('[redacted]');
    expect(audit).not.toContain('private-token');
    transcript.codeBlock('permission remember audit log', audit);
    transcript.step('second matching Bash request was auto-approved and did not become inbox work');

    transcript.section('AskUser bundle drafted in the inbox and resolved from the cockpit');
    await waitForAttentionItem(handle.page, 'ask-bundle');
    inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Questions/);
    await expect(inbox.getByText('Which verification tracks should be included?')).toBeVisible();
    await expect(inbox.getByText('50%').first()).toBeVisible();
    await inbox.getByRole('checkbox', { name: /Unit tests/ }).check();
    await inbox.getByRole('checkbox', { name: /Packaged smoke/ }).check();
    await inbox
      .getByLabel(/Evidence note free text/)
      .fill('Attach the redacted packaged trace bundle.');
    await evidenceShot(handle, 'attention-askuser-bundle');
    await closeInbox(handle.page);
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const inlineQuestions = cockpit.getByRole('region', { name: 'Feature attention' });
    await expect(
      inlineQuestions.getByText('Which verification tracks should be included?'),
    ).toBeVisible();
    await expect(inlineQuestions.getByRole('checkbox', { name: /Unit tests/ })).toBeChecked();
    await expect(inlineQuestions.getByRole('checkbox', { name: /Packaged smoke/ })).toBeChecked();
    await expect(inlineQuestions.getByLabel(/Evidence note free text/)).toHaveValue(
      'Attach the redacted packaged trace bundle.',
    );
    await inlineQuestions.getByRole('button', { name: 'Submit answers' }).click();
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
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await waitForServerPromptText(world, 'Which cockpit help path should continue?');
    const featureHelpItem = await waitForAttentionKind(handle.page, 'help');
    await handle.page.getByRole('tab', { name: /Packaged Attention Resolution/ }).click();
    cockpit = handle.page.getByLabel('Feature Packaged Attention Resolution');
    const inlineHelp = cockpit.getByRole('region', { name: 'Feature attention' });
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
    'Phase 3 — packaged interactive help attention',
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
      message: 'Phase 3 attention chat help',
    });
    transcript.json('chat start response', chatStart);
    await waitForProviderLog(world, 'chat-waiting');
    await waitForAttentionKind(handle.page, 'help');
    await handle.page.reload();
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);

    const inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Help request/);
    await inbox.getByLabel('Help reply').fill('Continue with the compact packaged evidence path.');
    await evidenceShot(handle, 'attention-help-reply');
    await inbox.getByRole('button', { name: 'Send reply' }).click();
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
    'Phase 3 — packaged NEED_USER_INPUT gate attention',
  );
  const world = createWorld('attention-gate', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'gate-lab', { commit: true });
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
    const gatePath = seedNeedUserInputGate(world, feature.id);
    transcript.json('seeded need-user-input gate', { featureId: feature.id, gatePath });

    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-gate-seeded' });
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, feature.id);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);

    const inbox = await openInbox(handle);
    await expandInboxItem(inbox, /Input gate/);
    await expect(
      inbox.getByText(/Choose the deployment window before implementation continues/),
    ).toBeVisible();
    const resume = inbox.getByRole('button', { name: 'Resume' });
    await expect(resume).toBeDisabled();
    await expect(inbox.getByText('Answer every question before resuming.')).toBeVisible();

    await inbox
      .getByLabel(/Which deployment window should implementation use/)
      .fill('After packaged attention evidence passes.');
    await resume.focus();
    await waitFor(
      () => fs.readFileSync(gatePath, 'utf8').includes('After packaged attention evidence passes.'),
      'need-user-input draft persisted to gate file',
      15_000,
    );
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-gate-relaunch' });
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, feature.id);
    const relaunchedInbox = await openInbox(handle);
    await expandInboxItem(relaunchedInbox, /Input gate/);
    await expect(
      relaunchedInbox.getByLabel(/Which deployment window should implementation use/),
    ).toHaveValue('After packaged attention evidence passes.');
    await expect(relaunchedInbox.getByRole('button', { name: 'Resume' })).toBeEnabled();
    await evidenceShot(handle, 'attention-need-user-input-gate');
    await relaunchedInbox.getByRole('button', { name: 'Abort gate', exact: true }).click();
    const cancelDialog = handle.page.getByRole('dialog', { name: 'Confirm abort' });
    await expect(cancelDialog).toContainText('Abort will terminally fail this feature.');
    await evidenceShot(handle, 'attention-abort-confirmation-dialog');
    await handle.page.keyboard.press('Escape');
    await expect(cancelDialog).toHaveCount(0);
    await expect(relaunchedInbox.getByRole('button', { name: 'Resume' })).toBeEnabled();
    await closeInbox(handle.page);
    await handle.page.getByRole('tab', { name: /Packaged Gate Fixture/ }).click();
    const gateCockpit = handle.page.getByLabel('Feature Packaged Gate Fixture');
    const inlineGate = gateCockpit.getByRole('region', { name: 'Feature attention' });
    await expect(
      inlineGate.getByLabel(/Which deployment window should implementation use/),
    ).toHaveValue('After packaged attention evidence passes.');
    await expect(inlineGate.getByRole('button', { name: 'Resume' })).toBeEnabled();
    await inlineGate.getByRole('button', { name: 'Resume' }).click();
    await waitForAttentionMissing(handle.page, feature.id, 'gate');
    await waitForProviderLog(world, 'session');
    transcript.codeBlock('drafted need-user-input gate', fs.readFileSync(gatePath, 'utf8'));
    transcript.step(
      'feature-scoped gate draft survived relaunch and dispatched Resume from the cockpit',
    );

    await handle.page.getByRole('tab', { name: 'Home' }).click();
    await createFeatureViaForm(handle, {
      name: 'Packaged Abort Gate Fixture',
      description: 'Seeded run-level NEED_USER_INPUT gate for confirmed abort evidence.',
      repoPatterns: [/gate-lab/],
    });
    const abortFeature = await waitForFeatureNamed(handle.page, 'Packaged Abort Gate Fixture');
    seedNeedUserInputGate(world, abortFeature.id);
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-gate-abort-seeded' });
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGate(handle.page, abortFeature.id);
    const abortInbox = await openInbox(handle);
    await expandInboxItem(abortInbox, /Input gate/);
    await abortInbox
      .getByLabel(/Which deployment window should implementation use/)
      .fill('Abort this seeded gate for scope verification.');
    await abortInbox.getByRole('button', { name: 'Abort gate', exact: true }).focus();
    await expect(abortInbox.getByText('Draft saved.')).toBeVisible();
    await abortInbox.getByRole('button', { name: 'Abort gate', exact: true }).click();
    const confirmDialog = handle.page.getByRole('dialog', { name: 'Confirm abort' });
    await expect(confirmDialog).toContainText('Abort will terminally fail this feature.');
    await confirmDialog.getByRole('button', { name: 'Confirm abort' }).click();
    await waitForAttentionMissing(handle.page, abortFeature.id, 'gate');
    await waitForFeatureStatus(handle.page, abortFeature.id, 'Failed');
    transcript.step('feature-scoped gate confirmed abort and reached Failed');

    transcript.section('Cycle-scoped gates on one feature resolve independently');
    await closeInbox(handle.page);
    await handle.page.getByRole('tab', { name: 'Home' }).click();
    await createFeatureViaForm(handle, {
      name: 'Packaged Multi-Cycle Gate Fixture',
      description: 'Seeded cycle-scoped NEED_USER_INPUT gates on sibling repositories.',
      repoPatterns: [/gate-api-lab/, /gate-web-lab/],
    });
    const cycleFeature = await waitForFeatureNamed(
      handle.page,
      'Packaged Multi-Cycle Gate Fixture',
    );
    const apiCycleGate = seedCycleNeedUserInputGate(world, cycleFeature.id, {
      repoName: 'gate-api-lab',
      cycleType: 'review-comments',
      summary: 'Choose the API review-comments recovery path.',
      question: 'Which API rollout window should review comments use?',
      answer: 'After the API fixture records cycle-scope evidence.',
      iteration: 4,
    });
    const webCycleGate = seedCycleNeedUserInputGate(world, cycleFeature.id, {
      repoName: 'gate-web-lab',
      cycleType: 'refactor',
      summary: 'Choose the web refactor recovery path.',
      question: 'Which web evidence path should refactor use?',
      answer: 'Use the independent web-cycle abort evidence.',
      iteration: 5,
    });
    transcript.json('seeded cycle need-user-input gates', {
      featureId: cycleFeature.id,
      apiCycleGate,
      webCycleGate,
    });
    await closeApp(handle);
    handle = null;
    await assertNoLeakedProcessesEventually(world);

    handle = await launchApp(world, testInfo, { traceName: 'attention-cycle-gates-seeded' });
    await expect(
      handle.page.getByRole('banner').getByRole('heading', { name: 'Agentico' }),
    ).toBeVisible({
      timeout: 60_000,
    });
    await waitForAttentionGateScope(handle.page, apiCycleGate);
    await waitForAttentionGateScope(handle.page, webCycleGate);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 2 pending/);
    const cycleInbox = await openInbox(handle);
    await expandGateByScope(cycleInbox, apiCycleGate);
    await expect(cycleInbox.getByLabel('Attention context')).toContainText('gate-api-lab');
    await expect(cycleInbox.getByLabel('Attention context')).toContainText('review-comments');
    await expect(cycleInbox.getByLabel(apiCycleGate.question)).toHaveValue(apiCycleGate.answer);
    await evidenceShot(handle, 'attention-cycle-gate-review-comments');
    await expandGateByScope(cycleInbox, webCycleGate);
    await expect(cycleInbox.getByLabel('Attention context')).toContainText('gate-web-lab');
    await expect(cycleInbox.getByLabel('Attention context')).toContainText('refactor');
    await expect(cycleInbox.getByLabel(webCycleGate.question)).toHaveValue(webCycleGate.answer);
    await cycleInbox.getByRole('button', { name: 'Abort gate', exact: true }).click();
    const cycleConfirmDialog = handle.page.getByRole('dialog', { name: 'Confirm abort' });
    await expect(cycleConfirmDialog).toContainText(
      'Abort will fail only this repository cycle; sibling cycles continue.',
    );
    await evidenceShot(handle, 'attention-cycle-abort-confirmation-dialog');
    await cycleConfirmDialog.getByRole('button', { name: 'Confirm abort' }).click();
    await waitForAttentionGateScopeMissing(handle.page, webCycleGate);
    await waitForAttentionGateScope(handle.page, apiCycleGate);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 1 pending/);
    const remainingCycleInbox = await openInbox(handle);
    await expandGateByScope(remainingCycleInbox, apiCycleGate);
    await expect(remainingCycleInbox.getByLabel('Attention context')).toContainText('gate-api-lab');
    await remainingCycleInbox.getByRole('button', { name: 'Abort gate', exact: true }).click();
    await handle.page
      .getByRole('dialog', { name: 'Confirm abort' })
      .getByRole('button', { name: 'Confirm abort' })
      .click();
    await waitForAttentionGateScopeMissing(handle.page, apiCycleGate);
    await expect(attentionBell(handle.page)).toHaveAccessibleName(/Attention inbox, 0 pending/);

    persistAppLogs(handle, 'attention-gate-app-server');
    transcript.step(
      'two paused repo cycles on one feature exposed repo/cycle labels and aborted independently',
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
  if ((await inbox.count()) === 0) return;
  await inbox.getByRole('button', { name: 'Close inbox' }).click();
  await expect(inbox).toHaveCount(0);
}

async function expandInboxItem(inbox: Locator, name: RegExp): Promise<void> {
  const button = inbox.getByRole('button', { name }).first();
  await expect(button).toBeVisible();
  if ((await button.getAttribute('aria-expanded')) !== 'true') {
    await button.click();
  }
  await expect(button).toHaveAttribute('aria-expanded', 'true');
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

function seedNeedUserInputGate(world: JourneyWorld, featureId: string): string {
  const runDir = path.join(world.stateDir, featureId, 'runs', 'run-001');
  const planPath = path.join(runDir, 'phase-03', 'plan', 'phase-plan.md');
  const gatePath = path.join(
    runDir,
    'phase-03',
    'implement',
    'iteration-03',
    'need-user-input.yaml',
  );
  fs.mkdirSync(path.dirname(planPath), { recursive: true });
  fs.writeFileSync(
    planPath,
    [
      '# Seeded Gate Resume Plan',
      '',
      '## Overview',
      'Resume the packaged gate fixture into a real implementation session.',
      '',
      '## Tasks',
      '### Task 1: Resume seeded implementation',
      '',
      '**Repo:** gate-lab',
      '',
      '#### What to build',
      'Record that the packaged gate resume path can relaunch implementation.',
      '',
      '#### Acceptance criteria',
      '- [ ] The workflow provider session starts after Resume.',
      '',
    ].join('\n'),
  );
  fs.mkdirSync(path.dirname(gatePath), { recursive: true });
  fs.writeFileSync(
    gatePath,
    [
      'summary: Choose the deployment window before implementation continues.',
      'iteration: 3',
      'questions:',
      '  - index: 1',
      '    prompt: Which deployment window should implementation use?',
      '    answer: ""',
      '',
    ].join('\n'),
  );

  const runPath = path.join(runDir, 'run.yaml');
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = upsertYamlScalar(runYaml, 'current_iteration', '3');
  runYaml = upsertYamlScalar(runYaml, 'pending_need_user_input_path', gatePath);
  runYaml = upsertYamlMapScalar(runYaml, 'artifacts', 'plan', planPath);
  fs.writeFileSync(runPath, runYaml);

  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'NeedUserInput');
  // Phase does not implement string YAML marshaling; PhaseImplement persists as 2.
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
  fs.writeFileSync(featurePath, featureYaml);
  return gatePath;
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

function seedCycleNeedUserInputGate(
  world: JourneyWorld,
  featureId: string,
  fixture: CycleGateFixture,
): CycleGateFixture & { featureId: string; gatePath: string } {
  const runDir = path.join(world.stateDir, featureId, 'runs', 'run-001');
  const gatePath = path.join(
    runDir,
    'cycles',
    fixture.repoName,
    `${fixture.cycleType}-${fixture.iteration}`,
    `iteration-${String(fixture.iteration).padStart(2, '0')}`,
    'need-user-input.yaml',
  );
  fs.mkdirSync(path.dirname(gatePath), { recursive: true });
  fs.writeFileSync(
    gatePath,
    [
      `summary: ${fixture.summary}`,
      `iteration: ${fixture.iteration}`,
      'questions:',
      '  - index: 1',
      `    prompt: ${fixture.question}`,
      `    answer: ${JSON.stringify(fixture.answer)}`,
      '',
    ].join('\n'),
  );

  const runPath = path.join(runDir, 'run.yaml');
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = upsertYamlNestedBlock(runYaml, 'repo_cycles', fixture.repoName, [
    `type: ${fixture.cycleType}`,
    'status: need_user_input',
    `count: ${fixture.iteration}`,
    `iteration: ${fixture.iteration}`,
    `pending_need_user_input_path: ${gatePath}`,
  ]);
  fs.writeFileSync(runPath, runYaml);

  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'Published');
  fs.writeFileSync(featurePath, featureYaml);
  return { ...fixture, featureId, gatePath };
}

function upsertYamlMapScalar(yaml: string, mapKey: string, key: string, value: string): string {
  const lines = yaml.split('\n');
  const parentIndex = lines.findIndex((line) => line === `${mapKey}:`);
  if (parentIndex === -1) {
    const suffix = yaml.endsWith('\n') ? '' : '\n';
    return `${yaml}${suffix}${mapKey}:\n  ${key}: ${value}\n`;
  }
  let insertIndex = parentIndex + 1;
  while (insertIndex < lines.length) {
    const line = lines[insertIndex] ?? '';
    if (line !== '' && !line.startsWith(' ')) break;
    if (line.startsWith(`  ${key}:`)) {
      lines[insertIndex] = `  ${key}: ${value}`;
      return lines.join('\n');
    }
    insertIndex += 1;
  }
  lines.splice(insertIndex, 0, `  ${key}: ${value}`);
  return lines.join('\n');
}

function upsertYamlNestedBlock(
  yaml: string,
  mapKey: string,
  childKey: string,
  childLines: string[],
): string {
  const lines = yaml.split('\n');
  const childBlock = [`  ${childKey}:`, ...childLines.map((line) => `    ${line}`)];
  const parentIndex = lines.findIndex((line) => line === `${mapKey}:`);
  if (parentIndex === -1) {
    const suffix = yaml.endsWith('\n') ? '' : '\n';
    return `${yaml}${suffix}${mapKey}:\n${childBlock.join('\n')}\n`;
  }

  let parentEnd = parentIndex + 1;
  while (parentEnd < lines.length) {
    const line = lines[parentEnd] ?? '';
    if (line !== '' && !line.startsWith(' ')) break;
    parentEnd += 1;
  }

  let childIndex = -1;
  let childEnd = parentEnd;
  for (let i = parentIndex + 1; i < parentEnd; i += 1) {
    if (lines[i] === `  ${childKey}:`) {
      childIndex = i;
      childEnd = i + 1;
      while (childEnd < parentEnd) {
        const line = lines[childEnd] ?? '';
        if (line !== '' && !line.startsWith('    ')) break;
        childEnd += 1;
      }
      break;
    }
  }

  if (childIndex === -1) {
    lines.splice(parentEnd, 0, ...childBlock);
  } else {
    lines.splice(childIndex, childEnd - childIndex, ...childBlock);
  }
  return lines.join('\n');
}

async function waitForAttentionItem(page: Page, id: string): Promise<AttentionItem> {
  return waitForAttentionMatching(page, (item) => item.id === id);
}

async function waitForAttentionGateScope(
  page: Page,
  scope: { featureId: string; repoName: string; cycleType: string },
): Promise<AttentionItem> {
  return waitForAttentionMatching(
    page,
    (item) =>
      item.kind === 'gate' &&
      item.featureId === scope.featureId &&
      item.repoName === scope.repoName &&
      item.cycleType === scope.cycleType,
  );
}

async function waitForAttentionKind(
  page: Page,
  kind: AttentionItem['kind'],
): Promise<AttentionItem> {
  return waitForAttentionMatching(page, (item) => item.kind === kind);
}

async function waitForAttentionGateScopeMissing(
  page: Page,
  scope: { featureId: string; repoName: string; cycleType: string },
): Promise<void> {
  await waitForAttention(
    page,
    (items) =>
      !items.some(
        (item) =>
          item.kind === 'gate' &&
          item.featureId === scope.featureId &&
          item.repoName === scope.repoName &&
          item.cycleType === scope.cycleType,
      ),
    30_000,
  );
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

async function expandGateByScope(
  inbox: Locator,
  scope: { repoName: string; cycleType: string },
): Promise<void> {
  const buttons = inbox.getByRole('button', { name: /Input gate/ });
  const count = await buttons.count();
  for (let index = 0; index < count; index += 1) {
    const button = buttons.nth(index);
    if ((await button.getAttribute('aria-expanded')) !== 'true') {
      await button.click();
    }
    await expect(button).toHaveAttribute('aria-expanded', 'true');
    const context = inbox.getByLabel('Attention context');
    const text = (await context.textContent()) ?? '';
    if (text.includes(scope.repoName) && text.includes(scope.cycleType)) {
      return;
    }
  }
  throw new Error(`gate ${scope.repoName}/${scope.cycleType} was not found in the inbox`);
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

async function waitForFeatureStatus(page: Page, featureId: string, status: string): Promise<void> {
  await waitFor(
    async () => {
      try {
        const feature = await page.evaluate((id) => window.agentico.getFeature(id), featureId);
        return feature.status === status;
      } catch {
        return false;
      }
    },
    `feature ${featureId} status ${status}`,
    30_000,
  );
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
