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

import type { ServerOwnership, SupervisorLifecycle } from '../shared/ipc';

export interface ActiveWorkCheck {
  featureIds: string[];
  /** The supervisor lifecycle is one of {@link SUPERVISOR_BUSY_LIFECYCLES}. */
  supervisorActive: boolean;
  /**
   * The supervisor is parked on the user (`waiting_permission` or
   * `waiting_question`); implies supervisorActive. Such a supervisor still
   * counts for quit and install-now, but no longer holds up an idle install.
   */
  supervisorWaiting: boolean;
  detectionFailed: boolean;
}

export interface UnresolvedWorkItem {
  kind: 'feature' | 'supervisor' | 'detection';
  id: string;
  label: string;
  reason: string;
}

export interface StopWorkResult {
  unresolved: UnresolvedWorkItem[];
}

export type ActiveWorkDecision = 'keep-running' | 'stop-and-quit' | 'cancel';
export type StopFailureDecision = 'retry' | 'quit-anyway' | 'cancel';

export interface QuitDialogOptions {
  type: 'warning' | 'error';
  title: string;
  message: string;
  detail: string;
  buttons: string[];
  defaultId: number;
  cancelId: number;
  noLink: true;
}

export interface QuitCoordinatorDeps<TParent> {
  detectActiveWork(): Promise<ActiveWorkCheck>;
  stopWork(active: ActiveWorkCheck): Promise<StopWorkResult>;
  showActiveWorkDialog(
    active: ActiveWorkCheck,
    parent: TParent | null,
  ): Promise<ActiveWorkDecision>;
  showStopFailureDialog(
    result: StopWorkResult,
    ownership: ServerOwnership,
    parent: TParent | null,
  ): Promise<StopFailureDecision>;
  confirmQuitAnyway(
    result: StopWorkResult,
    ownership: ServerOwnership,
    parent: TParent | null,
  ): Promise<boolean>;
  hide(parent: TParent | null): void;
  focusMainWindow(): void;
  runtimeOwnership(): ServerOwnership;
  shutdown(options: { quitAnyway: boolean }): Promise<void>;
  quitApplication(): void;
  /**
   * Last resort after quitApplication() failed to end the process within the
   * watchdog window (e.g. a close handler or native teardown that never
   * completes). Optional so hosts without a hard-exit primitive keep working.
   */
  exitApplication?(): void;
  /**
   * Arms a guard that ends the process at deadlineMs without depending on
   * this thread's event loop. The shutdown bound and exit watchdog are
   * main-thread timers: once the main thread stops running callbacks, they
   * never fire, and neither does exitApplication. Optional so hosts without
   * worker threads keep the in-thread bounds only.
   */
  armHardExit?(deadlineMs: number): void;
  log?(line: string): void;
}

export const DEFAULT_SHUTDOWN_TIMEOUT_MS = 30_000;
export const DEFAULT_EXIT_WATCHDOG_MS = 10_000;
export const DEFAULT_HARD_EXIT_MARGIN_MS = 5_000;

export interface QuitCoordinatorOptions {
  /** Upper bound on deps.shutdown before quitting regardless. */
  shutdownTimeoutMs?: number;
  /** Delay after quitApplication() before exitApplication() is forced. */
  exitWatchdogMs?: number;
  /** Slack past both in-thread bounds before the off-thread guard fires. */
  hardExitMarginMs?: number;
  /**
   * Hermetic test launches (AGENTICO_E2E_USER_DATA) must never block quit on
   * a native dialog: automation cannot answer it, so every launch would leak
   * a live window. In test mode quit skips detection and dialogs entirely.
   */
  testMode?: boolean;
}

export class QuitCoordinator<TParent = unknown> {
  private quitPromptInFlight = false;
  private quitInProgress = false;
  private forceQuit = false;

  constructor(
    private readonly deps: QuitCoordinatorDeps<TParent>,
    private readonly options: QuitCoordinatorOptions = {},
  ) {}

  shouldAllowClose(): boolean {
    return this.forceQuit;
  }

  /**
   * Runs the full quit decision flow and reports whether an actual quit was
   * initiated (deps.shutdown ran and deps.quitApplication was called) versus
   * the app staying open (kept running, cancelled, or a reentrant call while
   * another decision is already in flight). Callers that need to gate an
   * irreversible side effect on the user genuinely agreeing to quit — e.g.
   * applying a staged update — must check this return value rather than
   * assuming the promise resolving means quit happened.
   */
  async requestQuitDecision(parent: TParent | null = null): Promise<boolean> {
    if (this.forceQuit) {
      return true;
    }
    if (this.quitPromptInFlight || this.quitInProgress) {
      return false;
    }
    this.quitPromptInFlight = true;
    try {
      if (this.options.testMode === true) {
        await this.shutdown({ quitAnyway: true });
        return true;
      }
      if (this.deps.runtimeOwnership() === 'external') {
        await this.shutdown({ quitAnyway: false });
        return true;
      }
      const active = await this.deps.detectActiveWork();
      if (!hasActiveWork(active)) {
        await this.shutdown({ quitAnyway: false });
        return true;
      }

      const decision = await this.deps.showActiveWorkDialog(active, parent);
      if (decision === 'keep-running') {
        this.deps.hide(parent);
        return false;
      }
      if (decision === 'stop-and-quit') {
        return await this.stopAndQuit(active, parent);
      }
      this.deps.focusMainWindow();
      return false;
    } finally {
      this.quitPromptInFlight = false;
    }
  }

  private async stopAndQuit(
    initialActive: ActiveWorkCheck,
    parent: TParent | null,
  ): Promise<boolean> {
    if (this.quitInProgress) {
      return false;
    }
    this.quitInProgress = true;
    try {
      let active = initialActive;
      for (;;) {
        const result = await this.deps.stopWork(active);
        if (result.unresolved.length === 0) {
          await this.shutdown({ quitAnyway: false });
          return true;
        }

        const ownership = this.deps.runtimeOwnership();
        const decision = await this.deps.showStopFailureDialog(result, ownership, parent);
        if (decision === 'retry') {
          active = await this.deps.detectActiveWork();
          if (!hasActiveWork(active)) {
            await this.shutdown({ quitAnyway: false });
            return true;
          }
          continue;
        }
        if (decision === 'quit-anyway') {
          const confirmed = await this.deps.confirmQuitAnyway(result, ownership, parent);
          if (confirmed) {
            await this.shutdown({ quitAnyway: true });
            return true;
          }
        }
        this.deps.focusMainWindow();
        return false;
      }
    } finally {
      if (!this.forceQuit) {
        this.quitInProgress = false;
      }
    }
  }

  /**
   * Shutdown is bounded end to end: a wedged shutdown step or a quit that
   * never reaches exit must not leave a process the user cannot close. Each
   * stage logs so a hung quit is diagnosable from the app log alone.
   */
  private async shutdown(options: { quitAnyway: boolean }): Promise<void> {
    this.forceQuit = true;
    const shutdownTimeoutMs = this.options.shutdownTimeoutMs ?? DEFAULT_SHUTDOWN_TIMEOUT_MS;
    this.log(`shutdown started (quitAnyway=${String(options.quitAnyway)})`);
    // Before any step runs, while this thread is provably still responsive.
    this.armHardExit(shutdownTimeoutMs);
    try {
      const outcome = await Promise.race([
        this.deps.shutdown(options).then(
          () => 'completed' as const,
          (error: unknown) => {
            this.log(`shutdown step failed: ${describeError(error)}`);
            return 'failed' as const;
          },
        ),
        new Promise<'timed-out'>((resolve) => {
          setTimeout(() => resolve('timed-out'), shutdownTimeoutMs).unref?.();
        }),
      ]);
      if (outcome === 'timed-out') {
        this.log(`shutdown exceeded ${String(shutdownTimeoutMs)}ms; quitting anyway`);
      } else {
        this.log(`shutdown ${outcome}`);
      }
    } finally {
      this.armExitWatchdog();
      this.log('requesting application quit');
      this.deps.quitApplication();
    }
  }

  private armHardExit(shutdownTimeoutMs: number): void {
    const arm = this.deps.armHardExit;
    if (arm === undefined) {
      return;
    }
    const deadlineMs =
      shutdownTimeoutMs +
      (this.options.exitWatchdogMs ?? DEFAULT_EXIT_WATCHDOG_MS) +
      (this.options.hardExitMarginMs ?? DEFAULT_HARD_EXIT_MARGIN_MS);
    try {
      arm(deadlineMs);
      this.log(`hard exit guard armed (${String(deadlineMs)}ms)`);
    } catch (error) {
      this.log(`hard exit guard failed to arm: ${describeError(error)}`);
    }
  }

  private armExitWatchdog(): void {
    const exit = this.deps.exitApplication;
    if (exit === undefined) {
      return;
    }
    const exitWatchdogMs = this.options.exitWatchdogMs ?? DEFAULT_EXIT_WATCHDOG_MS;
    setTimeout(() => {
      this.log(`process still alive ${String(exitWatchdogMs)}ms after quit; forcing exit`);
      exit();
    }, exitWatchdogMs).unref?.();
  }

  private log(line: string): void {
    this.deps.log?.(line);
  }
}

function describeError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export function hasActiveWork(active: ActiveWorkCheck): boolean {
  return active.detectionFailed || active.featureIds.length > 0 || active.supervisorActive;
}

/**
 * The supervisor lifecycles that count as live work for quit, install now and
 * restart-to-update on demand: a process is launching, a turn is running, or
 * a turn is parked on the user. `stopped`, `idle` and `failed` have nothing to
 * interrupt. Install when idle reads {@link supervisorBlocksIdleInstall}.
 */
export const SUPERVISOR_BUSY_LIFECYCLES: ReadonlySet<SupervisorLifecycle> = new Set([
  'starting',
  'running',
  'waiting_permission',
  'waiting_question',
]);

export function isSupervisorBusy(lifecycle: SupervisorLifecycle): boolean {
  return SUPERVISOR_BUSY_LIFECYCLES.has(lifecycle);
}

/** The busy lifecycles in which the supervisor is waiting on the user's answer. */
export function isSupervisorWaiting(lifecycle: SupervisorLifecycle): boolean {
  return lifecycle === 'waiting_permission' || lifecycle === 'waiting_question';
}

/**
 * The supervisor lifecycles that hold up an unattended (install-when-idle)
 * install: a process is launching or a turn is running. A supervisor waiting
 * on the user does not block it; the scheduled install ends it instead.
 */
export function supervisorBlocksIdleInstall(lifecycle: SupervisorLifecycle): boolean {
  return lifecycle === 'starting' || lifecycle === 'running';
}

/** The three grades tray, quit and install surfaces read the lifecycle in. */
export type SupervisorGrade = 'working' | 'waiting' | 'idle';

export function supervisorGrade(lifecycle: SupervisorLifecycle): SupervisorGrade {
  if (supervisorBlocksIdleInstall(lifecycle)) return 'working';
  if (isSupervisorWaiting(lifecycle)) return 'waiting';
  return 'idle';
}

/** The grade an active-work check implies. */
export function supervisorGradeOf(active: {
  supervisorActive: boolean;
  supervisorWaiting: boolean;
}): SupervisorGrade {
  if (active.supervisorWaiting) return 'waiting';
  return active.supervisorActive ? 'working' : 'idle';
}

/**
 * The one sentence active-work summaries use for the supervisor, or null when
 * it has nothing to interrupt.
 */
export function supervisorActivitySentence(active: {
  supervisorActive: boolean;
  supervisorWaiting: boolean;
}): string | null {
  const grade = supervisorGradeOf(active);
  if (grade === 'working') return 'The supervisor is working.';
  if (grade === 'waiting') return 'The supervisor is waiting for your answer.';
  return null;
}

/** The reads active-work detection needs; structurally satisfied by the main-process services. */
export interface ActiveWorkSources {
  listFeatures(): Promise<{ features: readonly { id: string }[] }>;
  getFeature(
    featureId: string,
  ): Promise<{ id: string; actions: readonly { id: string; enabled: boolean }[] }>;
  getSupervisorState(): Promise<{ lifecycle: SupervisorLifecycle }>;
}

/**
 * Authoritative active-work check: the stoppable features and the supervisor
 * lifecycle, fetched side by side. Any read that fails marks detection failed,
 * which every caller treats as potentially active.
 */
export async function detectActiveWork(sources: ActiveWorkSources): Promise<ActiveWorkCheck> {
  const [featureResult, supervisorResult] = await Promise.all([
    detectStoppableFeatures(sources),
    sources.getSupervisorState().then(
      (state) => ({
        supervisorActive: isSupervisorBusy(state.lifecycle),
        supervisorWaiting: isSupervisorWaiting(state.lifecycle),
        failed: false,
      }),
      () => ({ supervisorActive: false, supervisorWaiting: false, failed: true }),
    ),
  ]);
  return {
    featureIds: featureResult.featureIds,
    supervisorActive: supervisorResult.supervisorActive,
    supervisorWaiting: supervisorResult.supervisorWaiting,
    detectionFailed: featureResult.failed || supervisorResult.failed,
  };
}

async function detectStoppableFeatures(
  sources: ActiveWorkSources,
): Promise<{ featureIds: string[]; failed: boolean }> {
  const featureIds: string[] = [];
  let failed = false;
  try {
    const list = await sources.listFeatures();
    const snapshots = await Promise.allSettled(
      list.features.map((summary) => sources.getFeature(summary.id)),
    );
    for (const result of snapshots) {
      if (result.status === 'rejected') {
        failed = true;
        continue;
      }
      if (result.value.actions.some((action) => action.id === 'pause-stop' && action.enabled)) {
        featureIds.push(result.value.id);
      }
    }
  } catch {
    failed = true;
  }
  return { featureIds: [...new Set(featureIds)], failed };
}

export const SUPERVISOR_UNRESOLVED_ID = 'supervisor';
export const SUPERVISOR_UNRESOLVED_LABEL = 'Supervisor';
export const DEFAULT_STOP_TIMEOUT_MS = 10_000;
export const DEFAULT_STOP_POLL_INTERVAL_MS = 250;

export interface StopActiveWorkDeps {
  /** Sends the feature's pause-stop action. */
  stopFeature(featureId: string): Promise<void>;
  /** Ends the supervisor through its existing end route. */
  endSupervisor(): Promise<void>;
  detectActiveWork(): Promise<ActiveWorkCheck>;
  featureLabel(featureId: string): string;
  /** A user-safe one-line reason for a failed stop request. */
  describeStopFailure(error: unknown): string;
  timeoutMs?: number;
  pollIntervalMs?: number;
  sleep?(ms: number): Promise<void>;
  now?(): number;
}

/**
 * Sends a stop to every active item, then re-polls until the server reports
 * nothing active or the timeout passes. Whatever is still active comes back
 * as unresolved, carrying the stop request's own failure when it had one.
 */
export async function stopActiveWork(
  active: ActiveWorkCheck,
  deps: StopActiveWorkDeps,
): Promise<StopWorkResult> {
  const sleep = deps.sleep ?? ((ms: number) => new Promise((resolve) => setTimeout(resolve, ms)));
  const now = deps.now ?? Date.now;
  const stopFailures = new Map<string, string>();
  const stops = active.featureIds.map(async (featureId) => {
    try {
      await deps.stopFeature(featureId);
    } catch (error) {
      stopFailures.set(`feature:${featureId}`, deps.describeStopFailure(error));
    }
  });
  if (active.supervisorActive) {
    stops.push(
      deps.endSupervisor().catch((error: unknown) => {
        stopFailures.set(SUPERVISOR_UNRESOLVED_ID, deps.describeStopFailure(error));
      }),
    );
  }
  await Promise.all(stops);

  const deadline = now() + (deps.timeoutMs ?? DEFAULT_STOP_TIMEOUT_MS);
  let latest = await deps.detectActiveWork();
  while (!latest.detectionFailed && hasActiveWork(latest) && now() < deadline) {
    await sleep(deps.pollIntervalMs ?? DEFAULT_STOP_POLL_INTERVAL_MS);
    latest = await deps.detectActiveWork();
  }
  if (!hasActiveWork(latest)) {
    return { unresolved: [] };
  }

  const unresolved: UnresolvedWorkItem[] = [];
  if (latest.detectionFailed) {
    unresolved.push({
      kind: 'detection',
      id: 'active-work-detection',
      label: 'Active work check',
      reason: 'Agentico could not verify whether all work stopped.',
    });
  }
  for (const featureId of latest.featureIds) {
    unresolved.push({
      kind: 'feature',
      id: featureId,
      label: deps.featureLabel(featureId),
      reason:
        stopFailures.get(`feature:${featureId}`) ??
        'The server did not report a terminal state before the timeout.',
    });
  }
  if (latest.supervisorActive) {
    unresolved.push({
      kind: 'supervisor',
      id: SUPERVISOR_UNRESOLVED_ID,
      label: SUPERVISOR_UNRESOLVED_LABEL,
      reason:
        stopFailures.get(SUPERVISOR_UNRESOLVED_ID) ??
        'The server did not report that the supervisor stopped before the timeout.',
    });
  }
  return { unresolved };
}

export interface EndWaitingSupervisorDeps {
  /** Ends the supervisor through its existing end route. */
  endSupervisor(): Promise<void>;
  detectActiveWork(): Promise<ActiveWorkCheck>;
  /** A user-safe one-line reason for a failed end request. */
  describeStopFailure(error: unknown): string;
  timeoutMs?: number;
  pollIntervalMs?: number;
  sleep?(ms: number): Promise<void>;
  now?(): number;
}

/**
 * Ends a supervisor that is waiting on the user so a scheduled install can
 * restart without a dialog: one end request, then a re-poll, bounded like
 * {@link stopActiveWork}, until the lifecycle leaves the busy set. Reports
 * why when the end request fails or the supervisor never reports stopped.
 */
export async function endWaitingSupervisor(
  deps: EndWaitingSupervisorDeps,
): Promise<{ ended: true } | { ended: false; reason: string }> {
  const sleep = deps.sleep ?? ((ms: number) => new Promise((resolve) => setTimeout(resolve, ms)));
  const now = deps.now ?? Date.now;
  try {
    await deps.endSupervisor();
  } catch (error) {
    return { ended: false, reason: deps.describeStopFailure(error) };
  }
  const deadline = now() + (deps.timeoutMs ?? DEFAULT_STOP_TIMEOUT_MS);
  let latest = await deps.detectActiveWork();
  while ((latest.detectionFailed || latest.supervisorActive) && now() < deadline) {
    await sleep(deps.pollIntervalMs ?? DEFAULT_STOP_POLL_INTERVAL_MS);
    latest = await deps.detectActiveWork();
  }
  if (latest.detectionFailed || latest.supervisorActive) {
    return {
      ended: false,
      reason: 'The server did not report that the supervisor stopped before the timeout.',
    };
  }
  return { ended: true };
}

export function shouldRequestQuitOnMainWindowClose(platform: NodeJS.Platform): boolean {
  return platform !== 'darwin';
}

export function activeWorkDialog(active: ActiveWorkCheck): QuitDialogOptions {
  const details = [
    active.detectionFailed
      ? 'Agentico could not verify all background work, so quitting is treated as potentially active.'
      : '',
    active.featureIds.length > 0
      ? `${active.featureIds.length} feature ${active.featureIds.length === 1 ? 'run is' : 'runs are'} stoppable.`
      : '',
    supervisorActivitySentence(active) ?? '',
    'Keep Running hides the window and leaves work attached. Stop Work and Quit sends stop requests before shutdown.',
  ].filter((line) => line !== '');

  return {
    type: 'warning',
    title: 'Work is still running',
    message: 'Agentico has background work that may continue without the window.',
    detail: details.join('\n'),
    buttons: ['Keep Running', 'Stop Work and Quit', 'Cancel'],
    defaultId: 0,
    cancelId: 2,
    noLink: true,
  };
}

export function stopFailureDialog(
  result: StopWorkResult,
  ownership: ServerOwnership,
): QuitDialogOptions {
  const unresolved = result.unresolved.slice(0, 8).map((item) => `${item.label}: ${item.reason}`);
  const remaining =
    result.unresolved.length > unresolved.length
      ? [`${result.unresolved.length - unresolved.length} more item(s) unresolved.`]
      : [];
  return {
    type: 'error',
    title: 'Some work is still unresolved',
    message: 'Agentico could not confirm that every background item stopped.',
    detail: [
      ...unresolved,
      ...remaining,
      'Retry sends stop requests again after a fresh activity check. Cancel keeps Agentico open.',
      quitAnywayConsequence(ownership),
    ].join('\n'),
    buttons: ['Retry', 'Quit Anyway', 'Cancel'],
    defaultId: 0,
    cancelId: 2,
    noLink: true,
  };
}

export function quitAnywayDialog(
  result: StopWorkResult,
  ownership: ServerOwnership,
): QuitDialogOptions {
  return {
    type: 'warning',
    title: 'Quit anyway?',
    message: `${result.unresolved.length} background item ${
      result.unresolved.length === 1 ? 'was' : 'were'
    } not confirmed stopped.`,
    detail: quitAnywayConsequence(ownership),
    buttons: ['Quit Anyway', 'Cancel'],
    defaultId: 1,
    cancelId: 1,
    noLink: true,
  };
}

function quitAnywayConsequence(ownership: ServerOwnership): string {
  if (ownership === 'app-owned') {
    return 'Quit Anyway forces the app-owned runtime to terminate; unconfirmed work may be interrupted.';
  }
  if (ownership === 'external') {
    return 'Quit Anyway exits this client only; the external runtime and any remaining work will survive client exit.';
  }
  return 'Quit Anyway exits without confirmed stops; any remaining work state could not be verified.';
}
