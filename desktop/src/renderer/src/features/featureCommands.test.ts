import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  registerFeatureCommandExecutor,
  runFeatureCommand,
  toggleActiveInspector,
} from './featureCommands';
import type { FeatureActionLike } from '../../../shared/commands';

const FEATURE_ID = 'abcd1234ef567890';

let unregister: (() => void) | null = null;

afterEach(() => {
  unregister?.();
  unregister = null;
});

function register(
  featureId: string,
  actions: readonly FeatureActionLike[] | null,
  run = vi.fn(),
  toggleInspector = vi.fn(),
) {
  unregister = registerFeatureCommandExecutor({
    featureId,
    actions: () => actions,
    run,
    toggleInspector,
  });
  return { run, toggleInspector };
}

const startEnabled: FeatureActionLike[] = [
  { id: 'start', enabled: true, disabledReasons: [] },
  {
    id: 'pause-stop',
    enabled: false,
    disabledReasons: [{ code: 'not_running', message: 'nothing is running' }],
  },
];

describe('the feature command funnel', () => {
  it('runs an enabled command against the registered cockpit', () => {
    const { run } = register(FEATURE_ID, startEnabled);
    expect(runFeatureCommand('feature.start', { featureId: FEATURE_ID })).toBe('executed');
    expect(run).toHaveBeenCalledWith('feature.start');
  });

  it('no-ops a command the live catalogue no longer enables', () => {
    const { run } = register(FEATURE_ID, startEnabled);
    expect(runFeatureCommand('feature.pause-stop', { featureId: FEATURE_ID })).toBe('not-enabled');
    expect(run).not.toHaveBeenCalled();
  });

  it('re-reads the catalogue at invocation, so an enablement change lands', () => {
    let actions: FeatureActionLike[] = [{ id: 'start', enabled: false, disabledReasons: [] }];
    const run = vi.fn();
    unregister = registerFeatureCommandExecutor({
      featureId: FEATURE_ID,
      actions: () => actions,
      run,
      toggleInspector: vi.fn(),
    });
    expect(runFeatureCommand('feature.start')).toBe('not-enabled');
    actions = [{ id: 'start', enabled: true, disabledReasons: [] }];
    expect(runFeatureCommand('feature.start')).toBe('executed');
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('no-ops when the invocation targets a feature the mounted cockpit is not showing', () => {
    const { run } = register(FEATURE_ID, startEnabled);
    expect(runFeatureCommand('feature.start', { featureId: '1234abcd5678ef90' })).toBe('no-target');
    expect(run).not.toHaveBeenCalled();
  });

  it('no-ops with no cockpit mounted at all', () => {
    expect(runFeatureCommand('feature.start', { featureId: FEATURE_ID })).toBe('no-target');
    expect(toggleActiveInspector()).toBe(false);
  });

  it('always allows Configuration, which has no server action', () => {
    const { run } = register(FEATURE_ID, []);
    expect(runFeatureCommand('feature.configuration')).toBe('executed');
    expect(run).toHaveBeenCalledWith('feature.configuration');
  });

  it('flips the mounted cockpit inspector', () => {
    const { toggleInspector } = register(FEATURE_ID, startEnabled);
    expect(toggleActiveInspector()).toBe(true);
    expect(toggleInspector).toHaveBeenCalledTimes(1);
  });

  it('drops its registration when the cockpit unmounts', () => {
    const { run } = register(FEATURE_ID, startEnabled);
    unregister?.();
    unregister = null;
    expect(runFeatureCommand('feature.start')).toBe('no-target');
    expect(run).not.toHaveBeenCalled();
  });
});
