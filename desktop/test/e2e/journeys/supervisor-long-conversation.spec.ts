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
 * A long supervisor conversation against the packaged app and the real
 * bundled server: a 600-record transcript seeded in the durable format
 * before launch opens on its newest page; scrolling to the top loads each
 * earlier page behind "Loading earlier messages…" while the previously first
 * visible row holds its place, until seq 1 and no further loading row. A
 * turn cut by SIGKILLing the server shows its committed reply exactly once
 * after the relaunch, and the next turn's committed reply replaces its
 * streamed preview with a single row. The provider CLI is the only stub
 * (`supervisorProvider`).
 */
import fs from 'node:fs';
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { requireDiscovery, waitForNewServer } from '../helpers/runtime';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  seedSupervisorConversation,
  seededSupervisorAnswer,
  seededSupervisorQuestion,
  SUPERVISOR_E2E_MARKERS,
  supervisorStubPartialReply,
  supervisorStubReply,
  supervisorStubResumedReply,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

const RUN_NAME = 'supervisor-long-conversation';
const CHIP_LABEL = 'Claude Haiku · Default';
const PAUSED = 'Paused — interrupted before restart. Send a message to continue.';
const SEEDED_RECORDS = 600;
const SEEDED_TURNS = SEEDED_RECORDS / 2;
/** The server's default transcript page size. */
const PAGE_SIZE = 100;
const LOADING_EARLIER = 'Loading earlier messages…';

/** What one scroll-to-top observed, sampled after every DOM commit in the transcript. */
interface PrependProbe {
  anchorText: string;
  /** The anchor row's offset from the viewport top while the loading row showed. */
  loadingOffset: number | null;
  /** The anchor row's offset once the earlier page was prepended. */
  prependedOffset: number | null;
  sawLoading: boolean;
  firstTextAfter: string | null;
  rowsBefore: number;
  rowsAfter: number;
  timedOut: boolean;
}

test('supervisor long conversation: earlier pages load anchored, and a cut turn reconciles exactly once', async ({}, testInfo) => {
  test.setTimeout(10 * 60_000);
  const transcript = new Transcript(RUN_NAME, 'Supervisor long conversation and exactly-once');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'long-conversation-lab', { commit: true });
  const conversationId = seedSupervisorConversation(world, SEEDED_RECORDS);
  transcript.json('seeded conversation', { conversationId, records: SEEDED_RECORDS });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(supervisorPage(page)).toBeVisible({ timeout: 60_000 });

    transcript.section('The newest page opens; the oldest record is not loaded');
    await expect(
      conversation(page).getByText(seededSupervisorAnswer(SEEDED_TURNS), { exact: true }),
    ).toBeInViewport({
      timeout: 60_000,
    });
    await expect(messageRows(page)).toHaveCount(PAGE_SIZE);
    await expect(
      conversation(page).getByText(seededSupervisorQuestion(1), { exact: true }),
    ).toHaveCount(0);
    expect((await supervisorState(page)).conversationId).toBe(conversationId);
    await evidenceShot(handle, 'supervisor-long-conversation-newest');
    transcript.step(
      `newest ${PAGE_SIZE} rows loaded, "${seededSupervisorAnswer(SEEDED_TURNS)}" in view`,
    );

    transcript.section('Scrolling to the top loads each earlier page with the first row held');
    const probes: PrependProbe[] = [];
    while (probes.length < SEEDED_RECORDS / PAGE_SIZE) {
      const probe = await scrollToTopAndObserve(page, 20_000);
      probes.push(probe);
      expect(probe.timedOut, `prepend ${probes.length} completed`).toBe(false);
      expect(probe.sawLoading, `"${LOADING_EARLIER}" showed for prepend ${probes.length}`).toBe(
        true,
      );
      expect(probe.rowsAfter).toBe(probe.rowsBefore + PAGE_SIZE);
      expect(probe.loadingOffset).not.toBeNull();
      expect(probe.prependedOffset).not.toBeNull();
      expect(
        Math.abs(probe.prependedOffset! - probe.loadingOffset!),
        `"${probe.anchorText}" held its viewport offset`,
      ).toBeLessThanOrEqual(1);
      if (probe.firstTextAfter === seededSupervisorQuestion(1)) break;
    }
    transcript.json('prepends', probes);
    expect(probes).toHaveLength(SEEDED_RECORDS / PAGE_SIZE - 1);
    expect(probes.at(-1)!.firstTextAfter).toBe(seededSupervisorQuestion(1));
    await expect(messageRows(page)).toHaveCount(SEEDED_RECORDS);

    transcript.section('At seq 1 no further page loads');
    const final = await scrollToTopAndObserve(page, 2_000);
    expect(final.timedOut).toBe(true);
    expect(final.sawLoading).toBe(false);
    expect(final.rowsAfter).toBe(SEEDED_RECORDS);
    await expect(
      conversation(page).getByText(seededSupervisorQuestion(1), { exact: true }),
    ).toBeInViewport();
    await expect(conversation(page).getByText(LOADING_EARLIER)).toHaveCount(0);
    await expectNoDuplicateRows(page);
    await evidenceShot(handle, 'supervisor-long-conversation-oldest');
    transcript.step('seq 1 reached; no loading row at the top');

    transcript.section('A turn holds after committing partial text; the server is killed');
    await chooseHaiku(page);
    await sendMessage(page, `Summarise the history ${SUPERVISOR_E2E_MARKERS.partialHold}`);
    await waitForProviderLog(world, 'partial-holding:1');
    const partial = supervisorStubPartialReply(1);
    await expect(conversation(page)).toContainText(partial, { timeout: 30_000 });
    expect(readProviderLog(world)).toContain(`history:${SEEDED_TURNS}`);
    const restart = await killBundledServer(world);
    transcript.step(`server pid ${restart.killed} relaunched as pid ${restart.next}`);

    transcript.section('After the relaunch the committed reply shows exactly once');
    await expect(status(page)).toHaveText(PAUSED, { timeout: 60_000 });
    await expect(
      conversation(page).getByText('Interrupted before restart', { exact: true }),
    ).toBeVisible();
    await expect(messageRows(page).filter({ hasText: partial })).toHaveCount(1);
    await expectNoDuplicateRows(page);
    await evidenceShot(handle, 'supervisor-long-conversation-restarted');

    transcript.section('The next reply replaces its streamed preview with one row');
    await sendMessage(page, 'What did we cover?');
    // The seeded prompts and the cut one preceded the resumed launch.
    const resumedReply = supervisorStubResumedReply(SEEDED_TURNS + 1);
    await expect(conversation(page)).toContainText(resumedReply, { timeout: 60_000 });
    await expectReady(page);
    await expect(messageRows(page).filter({ hasText: resumedReply })).toHaveCount(1);
    await expect(messageRows(page).filter({ hasText: 'Resumed with' })).toHaveCount(1);
    await expectNoDuplicateRows(page);

    await sendMessage(page, 'And one more thing.');
    const plainReply = supervisorStubReply(2);
    await expect(conversation(page)).toContainText(plainReply, { timeout: 60_000 });
    await expectReady(page);
    await expect(messageRows(page).filter({ hasText: plainReply })).toHaveCount(1);
    await expectNoDuplicateRows(page);
    await evidenceShot(handle, 'supervisor-long-conversation-resumed');
    transcript.json('provider log', readProviderLog(world).split('\n'));
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function supervisorPage(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor', exact: true });
}

function conversation(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor conversation' });
}

function messageRows(page: Page): Locator {
  return conversation(page).locator('article.conversation__message');
}

function composer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

function sendButton(page: Page): Locator {
  return supervisorPage(page)
    .locator('.supervisor-page__dock')
    .getByRole('button', { name: 'Send', exact: true });
}

function status(page: Page): Locator {
  return page.getByTestId('supervisor-status');
}

async function chooseHaiku(page: Page): Promise<void> {
  const chip = page.getByTestId('supervisor-model-chip');
  await chip.click();
  const popover = page.getByRole('region', { name: 'Harness and model' });
  await popover.getByRole('group', { name: 'Claude' }).getByText('Haiku', { exact: true }).click();
  await expect(chip).toHaveAccessibleName(CHIP_LABEL);
  await page.keyboard.press('Escape');
  await expect(popover).toHaveCount(0);
}

async function expectReady(page: Page): Promise<void> {
  await expect(status(page)).toHaveAttribute('data-lifecycle', 'idle', { timeout: 30_000 });
  await expect(status(page)).toHaveText('Ready');
}

async function sendMessage(page: Page, text: string): Promise<void> {
  await composer(page).fill(text);
  await expect(sendButton(page)).toBeEnabled({ timeout: 30_000 });
  await sendButton(page).click();
}

function supervisorState(page: Page) {
  return page.evaluate(() => window.agentico.getSupervisorState());
}

/** Every message row's text is unique: the seeded and sent texts all differ. */
async function expectNoDuplicateRows(page: Page): Promise<void> {
  const texts = await messageRows(page).locator('p').allTextContents();
  const seen = new Set<string>();
  const duplicates = texts.filter((text) => (seen.has(text) ? true : (seen.add(text), false)));
  expect(duplicates).toEqual([]);
}

/**
 * Scrolls the transcript to the top and samples, after every DOM commit, the
 * offset of the row that was first visible, until a page is prepended (or
 * `timeoutMs` passes). Sampling runs in a MutationObserver, so the brief
 * loading row is seen however fast the local page arrives.
 */
async function scrollToTopAndObserve(page: Page, timeoutMs: number): Promise<PrependProbe> {
  return page.evaluate(
    ({ timeout, loadingText }) =>
      new Promise<PrependProbe>((resolve) => {
        const container = document.querySelector<HTMLElement>(
          'section[aria-label="Supervisor conversation"]',
        );
        if (container === null) throw new Error('no supervisor transcript');
        const rows = (): HTMLElement[] => [
          ...container.querySelectorAll<HTMLElement>('article.conversation__message'),
        ];
        const anchor = rows()[0];
        if (anchor === undefined) throw new Error('no transcript rows');
        const text = (row: HTMLElement | undefined): string | null =>
          row?.querySelector('p')?.textContent ?? null;
        const offset = (): number =>
          anchor.getBoundingClientRect().top - container.getBoundingClientRect().top;
        const loadingShown = (): boolean =>
          [...container.querySelectorAll('[role="status"]')].some((node) =>
            node.textContent?.includes(loadingText),
          );
        const probe: PrependProbe = {
          anchorText: text(anchor) ?? '',
          loadingOffset: null,
          prependedOffset: null,
          sawLoading: false,
          firstTextAfter: null,
          rowsBefore: rows().length,
          rowsAfter: rows().length,
          timedOut: false,
        };
        const finish = (timedOut: boolean): void => {
          observer.disconnect();
          clearTimeout(timer);
          probe.timedOut = timedOut;
          probe.rowsAfter = rows().length;
          probe.firstTextAfter = text(rows()[0]);
          resolve(probe);
        };
        const observer = new MutationObserver(() => {
          if (loadingShown()) {
            probe.sawLoading = true;
            probe.loadingOffset = offset();
            return;
          }
          if (rows()[0] !== anchor) {
            probe.prependedOffset = offset();
            finish(false);
          }
        });
        observer.observe(container, { childList: true, subtree: true, characterData: true });
        const timer = setTimeout(() => finish(true), timeout);
        container.scrollTop = 0;
      }),
    { timeout: timeoutMs, loadingText: LOADING_EARLIER },
  );
}

/** SIGKILLs the bundled server from its discovery record and waits for the app's relaunch. */
async function killBundledServer(world: JourneyWorld): Promise<{ killed: number; next: number }> {
  const { pid } = requireDiscovery(world);
  process.kill(pid, 'SIGKILL');
  await waitFor(() => !processAlive(pid), `bundled server ${pid} to exit`, 15_000);
  const next = await waitForNewServer(world, pid);
  return { killed: pid, next: next.pid };
}

function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}

async function waitForProviderLog(world: JourneyWorld, needle: string): Promise<void> {
  await waitFor(
    () => readProviderLog(world).includes(needle),
    `provider log to contain ${needle}`,
    30_000,
  );
}
