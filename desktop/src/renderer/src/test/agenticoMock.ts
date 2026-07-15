import { vi } from 'vitest';
import type {
  AgenticoApi,
  ConnectionState,
  ReadinessSnapshot,
  Settings,
  ThemeInfo,
} from '../../../shared/ipc';
import { defaultSettings } from '../../../shared/ipc';

/** A snapshot with every mandatory gate unsatisfied (fresh install). */
export function unreadySnapshot(overrides: Partial<ReadinessSnapshot> = {}): ReadinessSnapshot {
  return {
    ready: false,
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue: {
          code: 'unauthenticated',
          message: 'The claude CLI is installed but not authenticated.',
          remedy: 'claude login',
        },
      },
      {
        name: 'codex',
        installed: false,
        ready: false,
        issue: {
          code: 'missing_executable',
          message: 'The codex CLI is not installed.',
          remedy: 'npm install -g @openai/codex',
        },
      },
    ],
    models: {
      available: false,
      issue: { code: 'models_unavailable', message: 'No usable provider exposes any model.' },
    },
    configuration: { valid: true },
    workspaceRoots: [],
    repositories: [],
    issues: [
      {
        code: 'unauthenticated',
        message: 'The claude CLI is installed but not authenticated.',
        remedy: 'claude login',
      },
      {
        code: 'missing_executable',
        message: 'The codex CLI is not installed.',
        remedy: 'npm install -g @openai/codex',
      },
      { code: 'models_unavailable', message: 'No usable provider exposes any model.' },
    ],
    ...overrides,
  };
}

/** A snapshot where every mandatory gate is satisfied. */
export function readySnapshot(overrides: Partial<ReadinessSnapshot> = {}): ReadinessSnapshot {
  return {
    ready: true,
    probedAt: '2026-07-14T10:00:00Z',
    providers: [{ name: 'claude', installed: true, version: '2.1.0', ready: true }],
    models: { available: true, models: ['claude-sonnet-4-5'] },
    configuration: { valid: true },
    workspaceRoots: [{ path: '/work/space', valid: true }],
    repositories: [{ name: 'repo-a', path: '/work/space/repo-a', valid: true }],
    issues: [],
    ...overrides,
  };
}

export interface AgenticoMock {
  api: AgenticoApi & {
    getConnectionStatus: ReturnType<typeof vi.fn>;
    retryConnection: ReturnType<typeof vi.fn>;
    updateSettings: ReturnType<typeof vi.fn>;
    setThemePreference: ReturnType<typeof vi.fn>;
    getReadiness: ReturnType<typeof vi.fn>;
    refreshReadiness: ReturnType<typeof vi.fn>;
    pickWorkspaceDirectory: ReturnType<typeof vi.fn>;
    addWorkspaceRoot: ReturnType<typeof vi.fn>;
    initRepository: ReturnType<typeof vi.fn>;
    listRepositories: ReturnType<typeof vi.fn>;
  };
  /** Push a connection change to every subscribed listener. */
  emitConnection(state: ConnectionState): void;
  listenerCount(): number;
}

export function installAgenticoMock(
  overrides: {
    connection?: ConnectionState;
    settings?: Settings;
    theme?: ThemeInfo;
    readiness?: ReadinessSnapshot;
  } = {},
): AgenticoMock {
  const connection: ConnectionState = overrides.connection ?? {
    status: 'resolving-runtime',
    stage: 'resolve-runtime',
    detail: 'Resolving the selected runtime.',
    ownership: 'none',
  };
  const settings = overrides.settings ?? defaultSettings();
  let theme: ThemeInfo = overrides.theme ?? { preference: 'system', resolved: 'dark' };
  const readiness = overrides.readiness ?? unreadySnapshot();

  const listeners = new Set<(state: ConnectionState) => void>();

  const api = {
    getConnectionStatus: vi.fn(() => Promise.resolve(connection)),
    retryConnection: vi.fn(() => Promise.resolve(connection)),
    onConnectionChanged: vi.fn((listener: (state: ConnectionState) => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }),
    getSettings: vi.fn(() => Promise.resolve(settings)),
    updateSettings: vi.fn(() => Promise.resolve(settings)),
    getThemePreference: vi.fn(() => Promise.resolve(theme)),
    setThemePreference: vi.fn((preference: ThemeInfo['preference']) => {
      theme = { preference, resolved: preference === 'system' ? theme.resolved : preference };
      return Promise.resolve(theme);
    }),
    getReadiness: vi.fn(() => Promise.resolve(readiness)),
    refreshReadiness: vi.fn(() => Promise.resolve(readiness)),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.resolve(readiness)),
    initRepository: vi.fn(() => Promise.resolve(readiness)),
    listRepositories: vi.fn(() => Promise.resolve(readiness.repositories)),
  };

  Object.defineProperty(window, 'agentico', { value: api, writable: true, configurable: true });

  return {
    api: api as AgenticoMock['api'],
    emitConnection: (state) => {
      for (const listener of listeners) listener(state);
    },
    listenerCount: () => listeners.size,
  };
}
