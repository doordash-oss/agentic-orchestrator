import { describe, expect, it, vi } from 'vitest';
import {
  QuitCoordinator,
  activeWorkDialog,
  quitAnywayDialog,
  stopFailureDialog,
  type ActiveWorkCheck,
  type QuitCoordinatorDeps,
  type StopWorkResult,
  type UnresolvedWorkItem,
} from '../quitCoordinator';

function active(
  featureIds: string[] = ['feature-1'],
  chatActive = false,
  detectionFailed = false,
): ActiveWorkCheck {
  return { featureIds, chatActive, detectionFailed };
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

describe('QuitCoordinator', () => {
  it('quits immediately when authoritative activity is idle', async () => {
    const deps = makeDeps({
      detectActiveWork: vi.fn().mockResolvedValue(active([], false, false)),
    });
    const coordinator = new QuitCoordinator(deps);

    await coordinator.requestQuitDecision('window');

    expect(deps.showActiveWorkDialog).not.toHaveBeenCalled();
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: false });
    expect(deps.quitApplication).toHaveBeenCalledOnce();
    expect(coordinator.shouldAllowClose()).toBe(true);
  });

  it('keeps work running by hiding the requested parent window', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('keep-running'),
    });
    const coordinator = new QuitCoordinator(deps);

    await coordinator.requestQuitDecision('window');

    expect(deps.hide).toHaveBeenCalledWith('window');
    expect(deps.shutdown).not.toHaveBeenCalled();
    expect(coordinator.shouldAllowClose()).toBe(false);
  });

  it('cancels active quit without stopping or hiding work', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('cancel'),
    });
    const coordinator = new QuitCoordinator(deps);

    await coordinator.requestQuitDecision('window');

    expect(deps.focusMainWindow).toHaveBeenCalledOnce();
    expect(deps.stopWork).not.toHaveBeenCalled();
    expect(deps.shutdown).not.toHaveBeenCalled();
  });

  it('stops active work before normal shutdown', async () => {
    const deps = makeDeps({
      showActiveWorkDialog: vi.fn().mockResolvedValue('stop-and-quit'),
    });
    const coordinator = new QuitCoordinator(deps);

    await coordinator.requestQuitDecision('window');

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

    await coordinator.requestQuitDecision('window');

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
      runtimeOwnership: vi.fn().mockReturnValue('external'),
    });
    const coordinator = new QuitCoordinator(deps);

    await coordinator.requestQuitDecision('window');

    expect(deps.confirmQuitAnyway).toHaveBeenCalledWith(
      { unresolved: [unresolvedFeature] },
      'external',
      'window',
    );
    expect(deps.shutdown).toHaveBeenCalledWith({ quitAnyway: true });
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
    await coordinator.requestQuitDecision('window');
    releaseStop();
    await first;

    expect(deps.detectActiveWork).toHaveBeenCalledOnce();
    expect(deps.stopWork).toHaveBeenCalledOnce();
    expect(deps.shutdown).toHaveBeenCalledOnce();
  });
});

describe('quit dialog copy', () => {
  it('describes active-work choices and ownership-specific Quit Anyway consequences', () => {
    expect(activeWorkDialog(active(['a', 'b'], true, true)).detail).toContain(
      'AMA session is active',
    );
    expect(stopFailureDialog({ unresolved: [unresolvedFeature] }, 'external').detail).toContain(
      'external runtime and any remaining work will survive',
    );
    expect(quitAnywayDialog({ unresolved: [unresolvedFeature] }, 'app-owned').detail).toContain(
      'forces the app-owned runtime to terminate',
    );
  });
});
