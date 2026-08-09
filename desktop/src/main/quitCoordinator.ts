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
}

export interface QuitCoordinatorOptions {
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

  async requestQuitDecision(parent: TParent | null = null): Promise<void> {
    if (this.forceQuit || this.quitPromptInFlight || this.quitInProgress) {
      return;
    }
    this.quitPromptInFlight = true;
    try {
      if (this.options.testMode === true) {
        await this.shutdown({ quitAnyway: true });
        return;
      }
      if (this.deps.runtimeOwnership() === 'external') {
        await this.shutdown({ quitAnyway: false });
        return;
      }
      const active = await this.deps.detectActiveWork();
      if (!hasActiveWork(active)) {
        await this.shutdown({ quitAnyway: false });
        return;
      }

      const decision = await this.deps.showActiveWorkDialog(active, parent);
      if (decision === 'keep-running') {
        this.deps.hide(parent);
        return;
      }
      if (decision === 'stop-and-quit') {
        await this.stopAndQuit(active, parent);
        return;
      }
      this.deps.focusMainWindow();
    } finally {
      this.quitPromptInFlight = false;
    }
  }

  private async stopAndQuit(initialActive: ActiveWorkCheck, parent: TParent | null): Promise<void> {
    if (this.quitInProgress) {
      return;
    }
    this.quitInProgress = true;
    try {
      let active = initialActive;
      for (;;) {
        const result = await this.deps.stopWork(active);
        if (result.unresolved.length === 0) {
          await this.shutdown({ quitAnyway: false });
          return;
        }

        const ownership = this.deps.runtimeOwnership();
        const decision = await this.deps.showStopFailureDialog(result, ownership, parent);
        if (decision === 'retry') {
          active = await this.deps.detectActiveWork();
          if (!hasActiveWork(active)) {
            await this.shutdown({ quitAnyway: false });
            return;
          }
          continue;
        }
        if (decision === 'quit-anyway') {
          const confirmed = await this.deps.confirmQuitAnyway(result, ownership, parent);
          if (confirmed) {
            await this.shutdown({ quitAnyway: true });
            return;
          }
        }
        this.deps.focusMainWindow();
        return;
      }
    } finally {
      if (!this.forceQuit) {
        this.quitInProgress = false;
      }
    }
  }

  private async shutdown(options: { quitAnyway: boolean }): Promise<void> {
    this.forceQuit = true;
    try {
      await this.deps.shutdown(options);
    } finally {
      this.deps.quitApplication();
    }
  }
}

export function hasActiveWork(active: ActiveWorkCheck): boolean {
  return active.detectionFailed || active.featureIds.length > 0 || active.chatActive;
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
