/**
 * Sandboxed preload. Exposes exactly one narrow, task-specific API
 * (`window.agentico`) via the context bridge. There is no generic invoke,
 * no channel parameter accepted from the renderer, and no token, URL, node,
 * or process material in scope.
 */
import { contextBridge, ipcRenderer } from 'electron';
import {
  ConnectionStateSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  IpcEnvelopeSchema,
  type AgenticoApi,
  type ConnectionState,
  type SettingsPatch,
  type ThemePreference,
} from '../shared/ipc';
import { assertNoPrototypePollution } from '../shared/sanitize';

/** Invokes a fixed channel and unwraps the validated envelope, failing closed. */
async function call<T>(channel: string, ...args: unknown[]): Promise<T> {
  const raw: unknown = await ipcRenderer.invoke(channel, ...args);
  const parsed = IpcEnvelopeSchema.safeParse(raw);
  if (!parsed.success) {
    throw new Error('E_IPC_PROTOCOL: The main process returned an unrecognized response.');
  }
  if (!parsed.data.ok) {
    const { code, message, remediation } = parsed.data.error;
    throw new Error(`${code}: ${message}${remediation ? ` ${remediation}` : ''}`);
  }
  return parsed.data.value as T;
}

const api: AgenticoApi = {
  getConnectionStatus: () => call(IPC_CHANNELS.connectionGetStatus),
  retryConnection: () => call(IPC_CHANNELS.connectionRetry),
  onConnectionChanged: (listener: (state: ConnectionState) => void) => {
    const wrapped = (_event: unknown, payload: unknown): void => {
      try {
        assertNoPrototypePollution(payload);
      } catch {
        return; // drop unsafe events silently — fail closed
      }
      const state = ConnectionStateSchema.safeParse(payload);
      if (state.success) {
        listener(state.data);
      }
    };
    ipcRenderer.on(IPC_EVENTS.connectionChanged, wrapped);
    return () => {
      ipcRenderer.removeListener(IPC_EVENTS.connectionChanged, wrapped);
    };
  },
  getSettings: () => call(IPC_CHANNELS.settingsGet),
  updateSettings: (patch: SettingsPatch) => call(IPC_CHANNELS.settingsUpdate, patch),
  getThemePreference: () => call(IPC_CHANNELS.themeGet),
  setThemePreference: (preference: ThemePreference) => call(IPC_CHANNELS.themeSet, preference),
};

contextBridge.exposeInMainWorld('agentico', api);
