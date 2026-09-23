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

import type { ServerOwnership } from '../shared/ipc';

export interface ActiveWorkCheck {
  featureIds: string[];
  chatActive: boolean;
  detectionFailed: boolean;
}

export interface UnresolvedWorkItem {
  kind: 'feature' | 'ama' | 'detection';
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
  return active.detectionFailed || active.featureIds.length > 0 || active.chatActive;
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
    active.chatActive ? 'The AMA session is active.' : '',
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
