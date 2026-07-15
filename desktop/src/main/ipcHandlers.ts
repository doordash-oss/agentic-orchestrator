/**
 * Registers every IPC handler from the central registry. Each handler:
 *   1. validates the sender (main-window webContents + app origin),
 *   2. size-checks and pollution-scans the payload,
 *   3. validates arguments against the channel's request schema,
 *   4. invokes the service,
 *   5. validates the response against the channel's response schema,
 * and always resolves to a typed { ok } envelope — exceptions never cross
 * the boundary unredacted.
 */
import { toSafeError } from '../shared/errors';
import { validateWithSchema } from '../shared/api/parse';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import {
  IPC_CHANNELS,
  type ConnectionState,
  type IpcChannel,
  type IpcEnvelope,
  type Settings,
  type SettingsPatch,
  type ThemeInfo,
  type ThemePreference,
  ipcContracts,
} from '../shared/ipc';
import { isTrustedSender, type SenderLikeEvent, type TrustedSender } from './security';

export interface IpcServices {
  getConnectionStatus(): ConnectionState;
  retryConnection(): Promise<ConnectionState> | ConnectionState;
  getSettings(): Settings;
  updateSettings(patch: SettingsPatch): Settings;
  getTheme(): ThemeInfo;
  setTheme(preference: ThemePreference): ThemeInfo;
}

export interface IpcMainLike {
  handle(
    channel: string,
    listener: (event: SenderLikeEvent, ...args: unknown[]) => Promise<unknown>,
  ): void;
}

const UNTRUSTED: IpcEnvelope = {
  ok: false,
  error: {
    code: 'E_UNTRUSTED_SENDER',
    message: 'The request did not originate from the application window.',
  },
};

function makeHandler(
  channel: IpcChannel,
  trusted: TrustedSender,
  invoke: (...args: never[]) => unknown,
): (event: SenderLikeEvent, ...args: unknown[]) => Promise<IpcEnvelope> {
  const contract = ipcContracts[channel];
  return async (event, ...args) => {
    if (!isTrustedSender(event, trusted)) {
      return UNTRUSTED;
    }
    try {
      assertWithinByteSize(JSON.stringify(args) ?? '');
      assertNoPrototypePollution(args);
      const parsedArgs = validateWithSchema(args, contract.request);
      const value = await (invoke as (...a: unknown[]) => unknown)(...parsedArgs);
      return { ok: true, value: validateWithSchema(value, contract.response) };
    } catch (err) {
      return { ok: false, error: toSafeError(err, 'E_INTERNAL') };
    }
  };
}

export function registerIpcHandlers(
  ipcMain: IpcMainLike,
  trusted: TrustedSender,
  services: IpcServices,
): void {
  const bindings: Record<IpcChannel, (...args: never[]) => unknown> = {
    [IPC_CHANNELS.connectionGetStatus]: () => services.getConnectionStatus(),
    [IPC_CHANNELS.connectionRetry]: () => services.retryConnection(),
    [IPC_CHANNELS.settingsGet]: () => services.getSettings(),
    [IPC_CHANNELS.settingsUpdate]: (patch: SettingsPatch) => services.updateSettings(patch),
    [IPC_CHANNELS.themeGet]: () => services.getTheme(),
    [IPC_CHANNELS.themeSet]: (preference: ThemePreference) => services.setTheme(preference),
  };
  for (const channel of Object.values(IPC_CHANNELS)) {
    ipcMain.handle(channel, makeHandler(channel, trusted, bindings[channel]));
  }
}
