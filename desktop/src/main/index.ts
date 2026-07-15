/**
 * Electron main entry point. All privileged state (settings, theme, the
 * runtime gateway with its bearer token and child-server supervision) lives
 * here; the renderer only ever sees the narrow preload API.
 */
import path from 'node:path';
import { BrowserWindow, app, dialog, ipcMain, nativeTheme, session } from 'electron';
import { createRuntimeGateway } from './gateway/wiring';
import type { RuntimeGateway } from './gateway/runtimeGateway';
import { registerIpcHandlers, type IpcServices } from './ipcHandlers';
import {
  installSecurityPolicies,
  mainWindowWebPreferences,
  originOf,
  type TrustedSender,
} from './security';
import { SettingsStore } from './settings';
import { SetupService } from './setup';
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

function createMainWindow(settings: SettingsStore, gateway: RuntimeGateway): void {
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
    void gateway.start();
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

  const unsubscribe = gateway.subscribe((state) => {
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
  const { gateway } = createRuntimeGateway({
    getRuntimeSelection: () => settings.get().runtime.selection,
    isPackaged: app.isPackaged,
    resourcesPath: process.resourcesPath,
    // out/main → out → desktop → repository root (development layout).
    appRoot: path.resolve(import.meta.dirname, '../../..'),
  });

  const theme = new ThemeController(
    nativeTheme,
    () => settings.get().theme,
    (preference) => settings.setTheme(preference),
  );
  theme.applyStored();

  const setup = new SetupService({
    transport: gateway,
    dialogs: {
      pickDirectory: async () => {
        const focused = BrowserWindow.getFocusedWindow() ?? BrowserWindow.getAllWindows()[0];
        const options = {
          title: 'Choose a folder',
          properties: ['openDirectory', 'createDirectory'] as Array<
            'openDirectory' | 'createDirectory'
          >,
        };
        const result =
          focused !== undefined
            ? await dialog.showOpenDialog(focused, options)
            : await dialog.showOpenDialog(options);
        const picked = result.filePaths[0];
        return result.canceled || picked === undefined ? null : picked;
      },
    },
  });

  const services: IpcServices = {
    getConnectionStatus: () => gateway.getState(),
    retryConnection: () => gateway.retry(),
    getSettings: () => settings.get(),
    updateSettings: (patch) => settings.update(patch),
    getTheme: () => theme.getInfo(),
    setTheme: (preference) => theme.setPreference(preference),
    getReadiness: () => setup.getReadiness(),
    refreshReadiness: () => setup.refreshReadiness(),
    pickWorkspaceDirectory: () => setup.pickWorkspaceDirectory(),
    addWorkspaceRoot: (rootPath) => setup.addWorkspaceRoot(rootPath),
    initRepository: (request) => setup.initRepository(request),
    listRepositories: () => setup.listRepositories(),
  };
  registerIpcHandlers(ipcMain, trusted, services);

  createMainWindow(settings, gateway);

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createMainWindow(settings, gateway);
    }
  });

  // Graceful, bounded shutdown of the app-owned server child on quit.
  // External servers are never signalled and survive app exit.
  let shutdownStarted = false;
  app.on('before-quit', (event) => {
    if (shutdownStarted) {
      return;
    }
    shutdownStarted = true;
    event.preventDefault();
    void gateway.shutdown().finally(() => {
      app.quit();
    });
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});
