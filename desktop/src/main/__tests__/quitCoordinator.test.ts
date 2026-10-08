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

import { describe, expect, it, vi } from 'vitest';
import type { SupervisorLifecycle } from '../../shared/ipc';
import {
  QuitCoordinator,
  SUPERVISOR_BUSY_LIFECYCLES,
  activeWorkDialog,
  detectActiveWork,
  endWaitingSupervisor,
  hasActiveWork,
  isSupervisorBusy,
  quitAnywayDialog,
  shouldRequestQuitOnMainWindowClose,
  stopActiveWork,
  stopFailureDialog,
  supervisorBlocksIdleInstall,
  supervisorGrade,
  type ActiveWorkCheck,
  type ActiveWorkDecision,
  type ActiveWorkSources,
  type EndWaitingSupervisorDeps,
  type QuitCoordinatorDeps,
  type StopActiveWorkDeps,
  type StopWorkResult,
  type UnresolvedWorkItem,
} from '../quitCoordinator';

function active(
  featureIds: string[] = ['feature-1'],
  supervisorActive = false,
  detectionFailed = false,
  supervisorWaiting = false,
): ActiveWorkCheck {
  return { featureIds, supervisorActive, supervisorWaiting, detectionFailed };
}

/** A supervisor parked on a permission or question, alongside the given features. */
function waiting(featureIds: string[] = []): ActiveWorkCheck {
  return active(featureIds, true, false, true);
}

const unresolvedFeature: UnresolvedWorkItem = {
  kind: 'feature',
  id: 'feature-1',
  label: 'Background Command Lifecycle',
  reason: 'The server did not report a terminal state before the timeout.',
};

function makeDeps(
  overrides: Partial<QuitCoordinatorDeps<string>> = {},
): QuitCoordinatorDeps<string> {
  return {
    detectActiveWork: vi.fn().mockResolvedValue(active()),
    stopWork: vi.fn().mockResolvedValue({ unresolved: [] } satisfies StopWorkResult),
    showActiveWorkDialog: vi.fn().mockResolvedValue('cancel'),
    showStopFailureDialog: vi.fn().mockResolvedValue('cancel'),
    confirmQuitAnyway: vi.fn().mockResolvedValue(false),
    hide: vi.fn(),
    focusMainWindow: vi.fn(),
    runtimeOwnership: vi.fn().mockReturnValue('app-owned'),
    shutdown: vi.fn().mockResolvedValue(undefined),
    quitApplication: vi.fn(),
    ...overrides,
  };
}

describe('shouldRequestQuitOnMainWindowClose', () => {
  it.each([
    ['darwin', false],
    ['win32', true],
    ['linux', true],
  ] as const)('returns %s => %s', (platform, expected) => {
    expect(shouldRequestQuitOnMainWindowClose(platform)).toBe(expected);
  });
});

describe('QuitCoordinator', () => {
  it('quits anyway when a shutdown step exceeds its bound, then forces exit', async () => {
    vi.useFakeTimers();
    try {
      const log: string[] = [];
      const deps = makeDeps({
        detectActiveWork: vi.fn().mockResolvedValue(active([])),
        shutdown: vi.fn().mockReturnValue(new Promise<void>(() => {})),
        exitApplication: vi.fn(),
        log: (line) => log.push(line),
      });
      const coordinator = new QuitCoordinator(deps, {
        shutdownTimeoutMs: 1_000,
        exitWatchdogMs: 500,
      });
      const decision = coordinator.requestQuitDecision();
      await vi.advanceTimersByTimeAsync(999);
      expect(deps.quitApplication).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);
      await expect(decision).resolves.toBe(true);
      expect(deps.quitApplication).toHaveBeenCalledTimes(1);
      expect(deps.exitApplication).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(500);
      expect(deps.exitApplication).toHaveBeenCalledTimes(1);
      expect(log).toEqual([
        'shutdown started (quitAnyway=false)',
        'shutdown exceeded 1000ms; quitting anyway',
        'requesting application quit',
        'process still alive 500ms after quit; forcing exit',
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('quits on a failed shutdown step and never forces exit without an exit primitive', async () => {
    vi.useFakeTimers();
    try {
      const log: string[] = [];
      const deps = makeDeps({
        detectActiveWork: vi.fn().mockResolvedValue(active([])),
        shutdown: vi.fn().mockRejectedValue(new Error('runtime stop threw')),
        log: (line) => log.push(line),
      });
      const coordinator = new QuitCoordinator(deps, { exitWatchdogMs: 100 });
      await expect(coordinator.requestQuitDecision()).resolves.toBe(true);
      expect(deps.quitApplication).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(log).toEqual([
        'shutdown started (quitAnyway=false)',
        'shutdown step failed: runtime stop threw',
        'shutdown failed',
        'requesting application quit',
      ]);
    } finally {
      vi.useRealTimers();
    }
  });

  it('arms the off-thread exit guard before the first shutdown step, past both in-thread bounds', async () => {
    const log: string[] = [];
    const order: string[] = [];
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([])),
      shutdown: vi.fn().mockImplementation(async () => {
        order.push('shutdown');
      }),
      armHardExit: vi.fn().mockImplementation(() => {
        order.push('arm');
      }),
      exitApplication: vi.fn(),
      log: (line) => log.push(line),
    });
    const coordinator = new QuitCoordinator(deps, {
      shutdownTimeoutMs: 1_000,
      exitWatchdogMs: 500,
      hardExitMarginMs: 250,
    });
    await expect(coordinator.requestQuitDecision()).resolves.toBe(true);
    expect(order).toEqual(['arm', 'shutdown']);
    expect(deps.armHardExit).toHaveBeenCalledWith(1_750);
    expect(log).toEqual([
      'shutdown started (quitAnyway=false)',
      'hard exit guard armed (1750ms)',
      'shutdown completed',
      'requesting application quit',
    ]);
  });

  it('still quits when the exit guard cannot be armed', async () => {
    const log: string[] = [];
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([])),
      armHardExit: vi.fn().mockImplementation(() => {
        throw new Error('worker threads unavailable');
      }),
      log: (line) => log.push(line),
    });
    const coordinator = new QuitCoordinator(deps);
    await expect(coordinator.requestQuitDecision()).resolves.toBe(true);
    expect(deps.shutdown).toHaveBeenCalledTimes(1);
    expect(deps.quitApplication).toHaveBeenCalledTimes(1);
    expect(log).toContain('hard exit guard failed to arm: worker threads unavailable');
  });

  it('shows the active-work dialog naming the supervisor when only the supervisor is busy', async () => {
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], true, false)),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.showActiveWorkDialog).toHaveBeenCalledWith(active([], true, false), 'window');
    expect(activeWorkDialog(active([], true, false))).toMatchObject({
      title: 'Work is still running',
      detail: expect.stringContaining('The supervisor is working.'),
    });
    expect(deps.shutdown).not.toHaveBeenCalled();
  });

  it('quits immediately when authoritative activity is idle', async () => {
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], false, false)),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.showActiveWorkDialog).not.toHaveBeenCalled();
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
    expect(coordinator.shouldAllowClose()).toBe(true);
  });

  it('detaches from an external runtime without stopping active work', async () => {
    const deps = makeDeps({
      runtimeOwnership: vi.fn().mockReturnValue('external'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.detectActiveWork).not.toHaveBeenCalled();
    expect(deps.showActiveWorkDialog).not.toHaveBeenCalled();
    expect(deps.stopWork).not.toHaveBeenCalled();
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
    expect(coordinator.shouldAllowClose()).toBe(true);
  });

  it('keeps work running by hiding the requested parent window', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('keep-running'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.hide).toHaveBeenCalledWith('window');
    expect(deps.shutdown).not.toHaveBeenCalled();
    expect(coordinator.shouldAllowClose()).toBe(false);
  });

  it('reports false on a reentrant call while a decision is already in flight', async () => {
    let releaseDialog!: () => void;
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn(
        () =>
          new Promise<ActiveWorkDecision>((resolve) => {
            releaseDialog = () => resolve('cancel');
          }),
      ),
    });
    const coordinator = new QuitCoordinator(deps);

    const first = coordinator.requestQuitDecision('window');
    await vi.waitFor(() => expect(deps.showActiveWorkDialog).toHaveBeenCalledOnce());
    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    releaseDialog();
    await expect(first).resolves.toBe(false);
  });

  it('reports true immediately once a quit is already underway', async () => {
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], false, false)),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);
    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.shutdown).toHaveBeenCalledOnce();
  });

  it('cancels active quit without stopping or hiding work', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('cancel'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.focusMainWindow).toHaveBeenCalledOnce();
    expect(deps.stopWork).not.toHaveBeenCalled();
    expect(deps.shutdown).not.toHaveBeenCalled();
  });

  it('stops active work before normal shutdown', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.stopWork).toHaveBeenCalledWith(active());
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
  });

  it('keeps Agentico open on partial stop failure until Retry confirms completion', async () => {
    const deps = makeDeps({
      detectActiveWork: vi
        .fn()
        .mockResolvedValueOnce(active(['feature-1']))
        .mockResolvedValueOnce(active(['feature-1'])),
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
      stopWork: vi
        .fn()
        .mockResolvedValueOnce({ unresolved: [unresolvedFeature] } satisfies StopWorkResult)
        .mockResolvedValueOnce({ unresolved: [] } satisfies StopWorkResult),
      showStopFailureDialog: vi.fn().mockResolvedValue('retry'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.showStopFailureDialog).toHaveBeenCalledOnce();
    expect(deps.stopWork).toHaveBeenCalledTimes(2);
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
  });

  it('requires a second confirmation before Quit Anyway shutdown', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
      stopWork: vi
        .fn()
        .mockResolvedValue({ unresolved: [unresolvedFeature] } satisfies StopWorkResult),
      showStopFailureDialog: vi.fn().mockResolvedValue('quit-anyway'),
      confirmQuitAnyway: vi.fn().mockResolvedValue(true),
      runtimeOwnership: vi.fn().mockReturnValue('app-owned'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.confirmQuitAnyway).toHaveBeenCalledWith(
      { unresolved: [unresolvedFeature] },
      'app-owned',
      'window',
    );
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: true });
  });

  it('stays open when Quit Anyway is declined at the second confirmation', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
      stopWork: vi
        .fn()
        .mockResolvedValue({ unresolved: [unresolvedFeature] } satisfies StopWorkResult),
      showStopFailureDialog: vi.fn().mockResolvedValue('quit-anyway'),
      confirmQuitAnyway: vi.fn().mockResolvedValue(false),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.focusMainWindow).toHaveBeenCalledOnce();
    expect(deps.shutdown).not.toHaveBeenCalled();
  });

  it('stays open when the stop-failure dialog is cancelled', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
      stopWork: vi
        .fn()
        .mockResolvedValue({ unresolved: [unresolvedFeature] } satisfies StopWorkResult),
      showStopFailureDialog: vi.fn().mockResolvedValue('cancel'),
    });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.focusMainWindow).toHaveBeenCalledOnce();
    expect(deps.shutdown).not.toHaveBeenCalled();
  });

  it('in test mode, quits immediately without dialogs even when detection would fail', async () => {
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active(['feature-1'], true, true)),
    });
    const coordinator = new QuitCoordinator(deps, { testMode: true });

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(true);

    expect(deps.detectActiveWork).not.toHaveBeenCalled();
    expect(deps.showActiveWorkDialog).not.toHaveBeenCalled();
    expect(deps.showStopFailureDialog).not.toHaveBeenCalled();
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: true });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
    expect(coordinator.shouldAllowClose()).toBe(true);
  });

  it('ignores duplicate quit requests while a stop is in progress', async () => {
    let releaseStop!: () => void;
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
      stopWork: vi.fn(
        () =>
          new Promise<StopWorkResult>((resolve) => {
            releaseStop = () => resolve({ unresolved: [] });
          }),
      ),
    });
    const coordinator = new QuitCoordinator(deps);

    const first = coordinator.requestQuitDecision('window');
    await vi.waitFor(() => expect(deps.stopWork).toHaveBeenCalledOnce());
    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);
    releaseStop();
    await expect(first).resolves.toBe(true);

    expect(deps.detectActiveWork).toHaveBeenCalledOnce();
    expect(deps.stopWork).toHaveBeenCalledOnce();
    expect(deps.shutdown).toHaveBeenCalledOnce();
  });
});

describe('quit decision for a waiting supervisor', () => {
  it('still shows "Work is still running", naming the wait', async () => {
    const deps = makeDeps({ detectActiveWork: vi.fn().mockResolvedValue(waiting()) });
    const coordinator = new QuitCoordinator(deps);

    await expect(coordinator.requestQuitDecision('window')).resolves.toBe(false);

    expect(deps.showActiveWorkDialog).toHaveBeenCalledWith(waiting(), 'window');
    const dialog = activeWorkDialog(waiting());
    expect(dialog.title).toBe('Work is still running');
    expect(dialog.detail).toContain('The supervisor is waiting for your answer.');
    expect(dialog.detail).not.toContain('The supervisor is working.');
    expect(deps.shutdown).not.toHaveBeenCalled();
  });
});

describe('quit dialog copy', () => {
  it('describes active-work choices and ownership-specific Quit Anyway consequences', () => {
    expect(activeWorkDialog(active(['a', 'b'], true, true)).detail).toContain(
      'The supervisor is working.',
    );
    expect(activeWorkDialog(active(['a'], false, false)).detail).not.toContain('supervisor');
    expect(stopFailureDialog({ unresolved: [unresolvedFeature] }, 'external').detail).toContain(
      'external runtime and any remaining work will survive',
    );
    expect(quitAnywayDialog({ unresolved: [unresolvedFeature] }, 'app-owned').detail).toContain(
      'forces the app-owned runtime to terminate',
    );
  });
});

// --- active-work detection --------------------------------------------------

function sources({
  lifecycle = 'idle',
  features = {},
  overrides = {},
}: {
  lifecycle?: SupervisorLifecycle;
  /** Feature id → whether its pause-stop action is enabled. */
  features?: Record<string, boolean>;
  overrides?: Partial<ActiveWorkSources>;
} = {}): ActiveWorkSources {
  return {
    listFeatures: vi
      .fn()
      .mockResolvedValue({ features: Object.keys(features).map((id) => ({ id })) }),
    getFeature: vi.fn((id: string) =>
      Promise.resolve({
        id,
        actions: [{ id: 'pause-stop', enabled: features[id] ?? false }],
      }),
    ),
    getSupervisorState: vi.fn().mockResolvedValue({ lifecycle }),
    ...overrides,
  };
}

describe('detectActiveWork', () => {
  it('reports active work while the supervisor is running and none while it is idle', async () => {
    const running = await detectActiveWork(sources({ lifecycle: 'running' }));
    expect(running).toEqual({
      featureIds: [],
      supervisorActive: true,
      supervisorWaiting: false,
      detectionFailed: false,
    });
    expect(hasActiveWork(running)).toBe(true);

    const idle = await detectActiveWork(sources({ lifecycle: 'idle' }));
    expect(idle).toEqual({
      featureIds: [],
      supervisorActive: false,
      supervisorWaiting: false,
      detectionFailed: false,
    });
    expect(hasActiveWork(idle)).toBe(false);
  });

  it.each(['starting', 'running', 'waiting_permission', 'waiting_question'] as const)(
    'treats the supervisor as active while %s',
    async (lifecycle) => {
      expect(isSupervisorBusy(lifecycle)).toBe(true);
      await expect(detectActiveWork(sources({ lifecycle }))).resolves.toMatchObject({
        supervisorActive: true,
        detectionFailed: false,
      });
    },
  );

  it.each(['stopped', 'idle', 'failed'] as const)(
    'treats the supervisor as inactive while %s',
    async (lifecycle) => {
      expect(isSupervisorBusy(lifecycle)).toBe(false);
      await expect(detectActiveWork(sources({ lifecycle }))).resolves.toMatchObject({
        supervisorActive: false,
        detectionFailed: false,
      });
    },
  );

  it('uses exactly the four busy lifecycles', () => {
    expect([...SUPERVISOR_BUSY_LIFECYCLES].sort()).toEqual([
      'running',
      'starting',
      'waiting_permission',
      'waiting_question',
    ]);
  });

  it.each([
    ['stopped', false, false, 'idle'],
    ['starting', false, true, 'working'],
    ['idle', false, false, 'idle'],
    ['running', false, true, 'working'],
    ['waiting_permission', true, false, 'waiting'],
    ['waiting_question', true, false, 'waiting'],
    ['failed', false, false, 'idle'],
  ] as const)(
    'reads %s as waiting=%s, blocks-idle-install=%s, grade %s',
    async (lifecycle, waitingOnUser, blocksIdle, grade) => {
      expect(supervisorBlocksIdleInstall(lifecycle)).toBe(blocksIdle);
      expect(supervisorGrade(lifecycle)).toBe(grade);
      await expect(detectActiveWork(sources({ lifecycle }))).resolves.toMatchObject({
        supervisorWaiting: waitingOnUser,
      });
    },
  );

  it('marks detection failed when the supervisor state cannot be fetched', async () => {
    const result = await detectActiveWork(
      sources({
        features: { 'feature-1': true },
        overrides: { getSupervisorState: vi.fn().mockRejectedValue(new Error('offline')) },
      }),
    );
    expect(result).toEqual({
      featureIds: ['feature-1'],
      supervisorActive: false,
      supervisorWaiting: false,
      detectionFailed: true,
    });
    expect(hasActiveWork(result)).toBe(true);
  });

  it('reports only stoppable features alongside the supervisor state', async () => {
    const deps = sources({
      lifecycle: 'waiting_question',
      features: { 'feature-1': true, 'feature-2': false },
    });
    await expect(detectActiveWork(deps)).resolves.toEqual({
      featureIds: ['feature-1'],
      supervisorActive: true,
      supervisorWaiting: true,
      detectionFailed: false,
    });
    expect(deps.getSupervisorState).toHaveBeenCalledOnce();
  });

  it('marks detection failed when the feature list or a snapshot cannot be fetched', async () => {
    await expect(
      detectActiveWork(
        sources({ overrides: { listFeatures: vi.fn().mockRejectedValue(new Error('down')) } }),
      ),
    ).resolves.toEqual({
      featureIds: [],
      supervisorActive: false,
      supervisorWaiting: false,
      detectionFailed: true,
    });

    await expect(
      detectActiveWork(
        sources({
          features: { 'feature-1': true },
          overrides: { getFeature: vi.fn().mockRejectedValue(new Error('gone')) },
        }),
      ),
    ).resolves.toEqual({
      featureIds: [],
      supervisorActive: false,
      supervisorWaiting: false,
      detectionFailed: true,
    });
  });
});

// --- stop path --------------------------------------------------------------

function stopDeps(overrides: Partial<StopActiveWorkDeps> = {}): StopActiveWorkDeps {
  let clock = 0;
  return {
    stopFeature: vi.fn().mockResolvedValue(undefined),
    endSupervisor: vi.fn().mockResolvedValue(undefined),
    detectActiveWork: vi.fn().mockResolvedValue(active([], false, false)),
    featureLabel: (featureId) => `Label ${featureId}`,
    describeStopFailure: (error) => (error instanceof Error ? error.message : String(error)),
    timeoutMs: 1_000,
    pollIntervalMs: 250,
    sleep: vi.fn((ms: number) => {
      clock += ms;
      return Promise.resolve();
    }),
    now: () => clock,
    ...overrides,
  };
}

describe('stopActiveWork', () => {
  it('ends an active supervisor through the end route and re-polls until it is idle', async () => {
    const deps = stopDeps({
      detectActiveWork: vi
        .fn()
        .mockResolvedValueOnce(active([], true, false))
        .mockResolvedValueOnce(active([], false, false)),
    });

    await expect(stopActiveWork(active([], true, false), deps)).resolves.toEqual({
      unresolved: [],
    });

    expect(deps.endSupervisor).toHaveBeenCalledOnce();
    expect(deps.stopFeature).not.toHaveBeenCalled();
    expect(deps.detectActiveWork).toHaveBeenCalledTimes(2);
  });

  it('leaves an idle supervisor alone and stops only the active features', async () => {
    const deps = stopDeps();

    await expect(stopActiveWork(active(['feature-1'], false, false), deps)).resolves.toEqual({
      unresolved: [],
    });

    expect(deps.stopFeature).toHaveBeenCalledWith('feature-1');
    expect(deps.endSupervisor).not.toHaveBeenCalled();
  });

  it('reports an unresolved supervisor item when the supervisor stays active', async () => {
    const deps = stopDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], true, false)),
    });

    const result = await stopActiveWork(active([], true, false), deps);

    expect(result.unresolved).toEqual([
      {
        kind: 'supervisor',
        id: 'supervisor',
        label: 'Supervisor',
        reason: 'The server did not report that the supervisor stopped before the timeout.',
      },
    ]);
    expect(deps.endSupervisor).toHaveBeenCalledOnce();
  });

  it('carries the end-route failure as the unresolved supervisor reason', async () => {
    const deps = stopDeps({
      endSupervisor: vi.fn().mockRejectedValue(new Error('end refused')),
      detectActiveWork: vi.fn().mockResolvedValue(active(['feature-1'], true, false)),
    });

    const result = await stopActiveWork(active(['feature-1'], true, false), deps);

    expect(result.unresolved).toEqual([
      {
        kind: 'feature',
        id: 'feature-1',
        label: 'Label feature-1',
        reason: 'The server did not report a terminal state before the timeout.',
      },
      { kind: 'supervisor', id: 'supervisor', label: 'Supervisor', reason: 'end refused' },
    ]);
  });

  it('reports a detection item when the re-poll cannot verify the outcome', async () => {
    const deps = stopDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], false, true)),
    });

    await expect(stopActiveWork(active([], true, false), deps)).resolves.toEqual({
      unresolved: [
        {
          kind: 'detection',
          id: 'active-work-detection',
          label: 'Active work check',
          reason: 'Agentico could not verify whether all work stopped.',
        },
      ],
    });
    expect(deps.detectActiveWork).toHaveBeenCalledOnce();
  });
});

// --- ending a waiting supervisor for a scheduled install ----------------------

function endDeps(overrides: Partial<EndWaitingSupervisorDeps> = {}): EndWaitingSupervisorDeps {
  let clock = 0;
  return {
    endSupervisor: vi.fn().mockResolvedValue(undefined),
    detectActiveWork: vi.fn().mockResolvedValue(active([], false, false)),
    describeStopFailure: (error) => (error instanceof Error ? error.message : String(error)),
    timeoutMs: 1_000,
    pollIntervalMs: 250,
    sleep: vi.fn((ms: number) => {
      clock += ms;
      return Promise.resolve();
    }),
    now: () => clock,
    ...overrides,
  };
}

describe('endWaitingSupervisor', () => {
  it('ends once and re-polls until the lifecycle leaves the busy set', async () => {
    const deps = endDeps({
      detectActiveWork: vi
        .fn()
        .mockResolvedValueOnce(waiting())
        .mockResolvedValueOnce(active([], true, false))
        .mockResolvedValueOnce(active([], false, false)),
    });

    await expect(endWaitingSupervisor(deps)).resolves.toEqual({ ended: true });

    expect(deps.endSupervisor).toHaveBeenCalledOnce();
    expect(deps.detectActiveWork).toHaveBeenCalledTimes(3);
  });

  it('reports the end-route failure without polling', async () => {
    const deps = endDeps({ endSupervisor: vi.fn().mockRejectedValue(new Error('end refused')) });

    await expect(endWaitingSupervisor(deps)).resolves.toEqual({
      ended: false,
      reason: 'end refused',
    });
    expect(deps.detectActiveWork).not.toHaveBeenCalled();
  });

  it('gives up once the bound passes with the supervisor still busy', async () => {
    const deps = endDeps({ detectActiveWork: vi.fn().mockResolvedValue(waiting()) });

    await expect(endWaitingSupervisor(deps)).resolves.toEqual({
      ended: false,
      reason: 'The server did not report that the supervisor stopped before the timeout.',
    });
    expect(deps.endSupervisor).toHaveBeenCalledOnce();
    expect(deps.detectActiveWork).toHaveBeenCalledTimes(5);
  });

  it('lets the following quit decision restart without the active-work dialog', async () => {
    const detect = vi
      .fn<() => Promise<ActiveWorkCheck>>()
      .mockResolvedValueOnce(waiting())
      .mockResolvedValue(active([], false, false));
    const ended = await endWaitingSupervisor(endDeps({ detectActiveWork: detect }));
    expect(ended).toEqual({ ended: true });

    const deps = makeDeps({ detectActiveWork: detect });
    await expect(new QuitCoordinator(deps).requestQuitDecision()).resolves.toBe(true);

    expect(deps.showActiveWorkDialog).not.toHaveBeenCalled();
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
  });
});
