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
 * Supervision of the app-owned bundled server child: exit handling, the
 * bounded crash-restart budget, and silent relaunch of a left-behind child.
 *
 * Supervision is intentionally decoupled from connection state: an
 * unexpected exit only drives ConnectionState (through the host) while the
 * child is the connected app-owned server. A left-behind child keeps its
 * backoff restart budget silently instead of hijacking the connection
 * surface. External servers are never adopted, signalled, or stopped.
 */
import { CanonicalErrorException, redactText, toCanonicalError } from '../../shared/errors';
import type { CanonicalError } from '../../shared/api/parse';
import type { ConnectionState } from '../../shared/ipc';
import type { ResolveResult } from './resources';
import type { ChildExit } from './serverProcess';
import type { SelectedRuntime, ServerChildLike } from './runtimeGateway';

export interface ChildSupervisorTimeouts {
  /** Grace period for stopping the app-owned child before SIGKILL. */
  shutdownGraceMs: number;
  /** Initial delay before automatically relaunching an app-owned crash. */
  crashRestartInitialMs: number;
  /** Rolling crash-budget and healthy-reset window. */
  crashWindowMs: number;
}

/** The surface the supervisor needs from the owning gateway. */
export interface ChildSupervisorHost {
  isShuttingDown(): boolean;
  getState(): ConnectionState;
  setState(next: ConnectionState): void;
  log(line: string): void;
  sleeper(ms: number): Promise<void>;
  clock(): number;
  resolveServerBinary(): ResolveResult;
  spawnServer(binaryPath: string, args: readonly string[]): ServerChildLike;
  /** Clears the bearer/base-URL fields after a connected child dies. */
  clearConnectionCredentials(): void;
  /**
   * Canonical crash error (outcome interpolated into the catalog summary),
   * folding the bounded, redacted owned launch diagnostics.
   */
  ownedCrashError(outcome: string): CanonicalError;
  /** Canonical crash-loop error, folding the owned launch diagnostics. */
  ownedCrashLoopError(): CanonicalError;
  /** Re-enters the connect cycle after a recovery delay. */
  startFromRecovery(): Promise<boolean>;
}

/**
 * The terse, redacted reason a recovery delay failed, for the local
 * diagnostics log: a canonical exception logs its authored summary; a plain
 * error logs its redacted message; anything else never reaches the log.
 */
function recoveryDelayReason(err: unknown): string {
  if (err instanceof CanonicalErrorException) {
    return err.canonical.summary;
  }
  return err instanceof Error && err.message !== ''
    ? redactText(err.message)
    : 'the recovery step failed';
}

export class ChildSupervisor {
  private child: ServerChildLike | null = null;
  private childExitUnsubscribe: (() => void) | null = null;
  private crashAttempts: number[] = [];
  private readySince: number | null = null;
  private recoveryPending = false;
  /**
   * The launch coordinates of the running app-owned child. Supervision is
   * decoupled from connection state: after a switch away, the child keeps
   * running under this identity and its backoff restart budget keeps working
   * silently instead of driving ConnectionState. Cleared whenever the child
   * is deliberately stopped.
   */
  private ownedSelected: SelectedRuntime | null = null;

  constructor(
    private readonly host: ChildSupervisorHost,
    private readonly timeouts: ChildSupervisorTimeouts,
  ) {}

  hasLiveChild(): boolean {
    return this.child !== null && !this.child.exited;
  }

  /** PID of the non-exited child, for launch re-own detection. */
  liveChildPid(): number | null {
    return this.hasLiveChild() ? (this.child?.pid ?? null) : null;
  }

  get isRecoveryPending(): boolean {
    return this.recoveryPending;
  }

  setOwnedSelected(selected: SelectedRuntime): void {
    this.ownedSelected = selected;
  }

  markReady(): void {
    this.readySince = this.host.clock();
  }

  clearReadySince(): void {
    this.readySince = null;
  }

  resetCrashAttempts(): void {
    this.crashAttempts = [];
  }

  adopt(child: ServerChildLike): void {
    this.child = child;
    this.childExitUnsubscribe = child.onExit((info) => this.handleChildExit(info));
  }

  release(): void {
    this.childExitUnsubscribe?.();
    this.childExitUnsubscribe = null;
    this.child = null;
  }

  /** Stops the app-owned child (if any). Never touches external processes. */
  async stop(): Promise<void> {
    const child = this.child;
    if (child === null) {
      return;
    }
    // A deliberate stop ends silent supervision: nothing relaunches a child
    // the gateway itself stopped.
    this.ownedSelected = null;
    if (child.exited) {
      this.release();
      return;
    }
    try {
      await child.stop({ timeoutMs: this.timeouts.shutdownGraceMs });
    } finally {
      if (this.child === child) {
        this.release();
      }
    }
  }

  private handleChildExit(info: ChildExit): void {
    const attached =
      this.host.getState().status === 'ready' && this.host.getState().ownership === 'app-owned';
    const detached: SelectedRuntime | null =
      this.host.getState().ownership === 'app-owned' ? null : this.ownedSelected;
    this.release();
    if (info.expected || this.host.isShuttingDown()) {
      return;
    }
    this.host.log(
      `app-owned runtime exited unexpectedly (code ${String(info.code)}, signal ${String(info.signal)})`,
    );
    if (!attached) {
      if (detached !== null) {
        this.scheduleBackgroundRecovery(detached);
      }
      // Startup-phase exits are reported by the launch loop with more context.
      return;
    }
    this.host.clearConnectionCredentials();
    const now = this.host.clock();
    if (this.readySince !== null && now - this.readySince >= this.timeouts.crashWindowMs) {
      this.crashAttempts = [];
    }
    this.readySince = null;
    this.host.setState({
      status: 'crashed',
      stage: 'connect',
      detail: 'The app-managed runtime exited unexpectedly.',
      ownership: 'none',
      error: this.host.ownedCrashError('exited unexpectedly'),
    });
    this.scheduleAutomaticRecovery();
  }

  /** Relaunches at most three times in the rolling crash window. */
  private scheduleAutomaticRecovery(): void {
    if (this.recoveryPending || this.host.isShuttingDown()) {
      return;
    }
    const now = this.host.clock();
    this.crashAttempts = this.crashAttempts.filter(
      (attemptedAt) => now - attemptedAt < this.timeouts.crashWindowMs,
    );
    if (this.crashAttempts.length >= 3) {
      this.host.setState({
        status: 'crashed',
        stage: 'connect',
        detail: 'The app-managed runtime stopped repeatedly.',
        ownership: 'none',
        error: this.host.ownedCrashLoopError(),
      });
      return;
    }
    const attempt = this.crashAttempts.length;
    this.crashAttempts.push(now);
    this.recoveryPending = true;
    const delay = this.timeouts.crashRestartInitialMs * 2 ** attempt;
    void this.host
      .sleeper(delay)
      .then(async () => {
        if (this.host.isShuttingDown()) {
          this.recoveryPending = false;
          return;
        }
        this.recoveryPending = false;
        const started = await this.host.startFromRecovery();
        if (!started) {
          this.crashAttempts.pop();
          return;
        }
        if (this.host.getState().status !== 'ready' && !this.host.isShuttingDown()) {
          this.host.setState({
            status: 'crashed',
            stage: 'connect',
            detail: 'The app-managed runtime could not be recovered.',
            ownership: 'none',
            error: this.host.ownedCrashError(
              'was restarted automatically but did not reach a healthy state',
            ),
          });
          this.scheduleAutomaticRecovery();
        }
      })
      .catch((err: unknown) => {
        this.recoveryPending = false;
        this.crashAttempts.pop();
        this.host.log(`automatic recovery delay failed: ${recoveryDelayReason(err)}`);
        if (this.host.isShuttingDown()) return;
        this.host.setState({
          status: 'crashed',
          stage: 'connect',
          detail: 'Automatic recovery could not be scheduled.',
          ownership: 'none',
          error: this.host.ownedCrashError('could not be restarted automatically'),
        });
      });
  }

  /**
   * Relaunches a left-behind app-owned child after an unexpected exit, within
   * the same rolling budget as connected supervision, but silently: no
   * ConnectionState change while a different server is active. The relaunched
   * server republishes its discovery/registry record, so switching back
   * re-attaches through the standard scan.
   */
  private scheduleBackgroundRecovery(selected: SelectedRuntime): void {
    if (this.recoveryPending || this.host.isShuttingDown()) {
      return;
    }
    const now = this.host.clock();
    this.crashAttempts = this.crashAttempts.filter(
      (attemptedAt) => now - attemptedAt < this.timeouts.crashWindowMs,
    );
    if (this.crashAttempts.length >= 3) {
      this.host.log('detached app-owned server exhausted its restart budget; leaving it stopped');
      return;
    }
    const attempt = this.crashAttempts.length;
    this.crashAttempts.push(now);
    this.recoveryPending = true;
    const delay = this.timeouts.crashRestartInitialMs * 2 ** attempt;
    void this.host
      .sleeper(delay)
      .then(() => {
        this.recoveryPending = false;
        if (this.host.isShuttingDown() || this.hasLiveChild()) {
          return;
        }
        const resolved = this.host.resolveServerBinary();
        if (!resolved.ok) {
          this.host.log('bundled server binary not found for background relaunch');
          return;
        }
        try {
          const child = this.host.spawnServer(resolved.path, [
            'server',
            '--config',
            selected.configPath,
            '--state-dir',
            selected.stateDir,
          ]);
          this.adopt(child);
          this.host.log('detached app-owned server relaunched silently');
        } catch (err) {
          const safe = toCanonicalError(err, 'E_LAUNCH_FAILED');
          this.host.log(`background server relaunch failed: ${safe.summary}`);
        }
      })
      .catch((err: unknown) => {
        this.recoveryPending = false;
        this.host.log(`background recovery delay failed: ${recoveryDelayReason(err)}`);
      });
  }
}
