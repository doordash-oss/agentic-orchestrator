/**
 * Electron main entry point. All privileged state (settings, theme,
 * connection lifecycle, and later the server transport with its bearer
 * token) lives here; the renderer only ever sees the narrow preload API.
 */
import path from 'node:path';
import { BrowserWindow, app, ipcMain, nativeTheme, session } from 'electron';
import { StubConnectionSource } from './connection';
import { registerIpcHandlers, type IpcServices } from './ipcHandlers';
import {
  installSecurityPolicies,
  mainWindowWebPreferences,
  originOf,
  type TrustedSender,
} from './security';
import { SettingsStore } from './settings';
import { ThemeController } from './theme';
import { IPC_EVENTS } from '../shared/ipc';

const devRendererUrl = process.env['ELECTRON_RENDERER_URL'];
const appOrigins = new Set<string>(['file://']);
if (devRendererUrl !== undefined) {
  const devOrigin = originOf(devRendererUrl);
  if (devOrigin !== null) {
    appOrigins.add(devOrigin);
  }
}

/** Updated whenever the main window is (re)created; -1 trusts nothing. */
const trusted: TrustedSender & { webContentsId: number } = {
  webContentsId: -1,
  allowedOrigins: appOrigins,
};

function createMainWindow(settings: SettingsStore, connection: StubConnectionSource): void {
  const bounds = settings.get().window.bounds;
  const window = new BrowserWindow({
    title: 'Agentico',
    width: bounds?.width ?? 1080,
    height: bounds?.height ?? 720,
    ...(bounds !== undefined ? { x: bounds.x, y: bounds.y } : {}),
    minWidth: 400,
    minHeight: 480,
    show: false,
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#16181D' : '#F5F6F7',
    webPreferences: mainWindowWebPreferences(
      path.join(import.meta.dirname, '../preload/index.cjs'),
    ),
  });
  trusted.webContentsId = window.webContents.id;

  window.on('ready-to-show', () => {
    window.show();
    connection.start();
  });

  window.on('close', () => {
    const { x, y, width, height } = window.getBounds();
    try {
      settings.update({ window: { bounds: { x, y, width, height } } });
    } catch {
      // Never block shutdown on settings persistence.
    }
  });

  window.on('closed', () => {
    trusted.webContentsId = -1;
  });

  const unsubscribe = connection.subscribe((state) => {
    if (!window.isDestroyed()) {
      window.webContents.send(IPC_EVENTS.connectionChanged, state);
    }
  });
  window.on('closed', unsubscribe);

  if (devRendererUrl !== undefined) {
    void window.loadURL(devRendererUrl);
  } else {
    void window.loadFile(path.join(import.meta.dirname, '../renderer/index.html'));
  }
}

void app.whenReady().then(() => {
  installSecurityPolicies({ app, session: session.defaultSession, appOrigins });

  const settings = new SettingsStore(app.getPath('userData'));
  const connection = new StubConnectionSource();

  const theme = new ThemeController(
    nativeTheme,
    () => settings.get().theme,
    (preference) => settings.setTheme(preference),
  );
  theme.applyStored();

  const services: IpcServices = {
    getConnectionStatus: () => connection.getState(),
    getSettings: () => settings.get(),
    updateSettings: (patch) => settings.update(patch),
    getTheme: () => theme.getInfo(),
    setTheme: (preference) => theme.setPreference(preference),
  };
  registerIpcHandlers(ipcMain, trusted, services);

  createMainWindow(settings, connection);

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createMainWindow(settings, connection);
    }
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});
