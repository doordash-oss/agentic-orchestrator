import { vi } from 'vitest';
import type { AgenticoApi, ConnectionState, Settings, ThemeInfo } from '../../../shared/ipc';
import { defaultSettings } from '../../../shared/ipc';

export interface AgenticoMock {
  api: AgenticoApi & {
    getConnectionStatus: ReturnType<typeof vi.fn>;
    retryConnection: ReturnType<typeof vi.fn>;
    updateSettings: ReturnType<typeof vi.fn>;
    setThemePreference: ReturnType<typeof vi.fn>;
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
