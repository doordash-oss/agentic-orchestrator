/**
 * Electron main entry point. All privileged state (settings, theme, the
 * runtime gateway with its bearer token and child-server supervision) lives
 * here; the renderer only ever sees the narrow preload API.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  BrowserWindow,
  app,
  clipboard,
  dialog,
  ipcMain,
  nativeTheme,
  net,
  protocol,
  session,
  shell,
  type Event as ElectronEvent,
  type MessageBoxReturnValue,
} from 'electron';
import { createRuntimeGateway } from './gateway/wiring';
import {
  resolveTestOutputFile,
  resolveTestPackagedResourcesDir,
  resolveTestUserDataDir,
} from './testHooks';
import { EventStreamSupervisor } from './gateway/events';
import type { RuntimeGateway } from './gateway/runtimeGateway';
import { FeatureService } from './features';
import { CompletionService } from './completion';
import { RecoveryService } from './recovery';
import { BulkService } from './bulk';
import { AttentionService } from './attention';
import { SessionService } from './serverClient';
import { registerIpcHandlers, type IpcServices } from './ipcHandlers';
import {
  installSecurityPolicies,
  mainWindowWebPreferences,
  originOf,
  type TrustedSender,
} from './security';
import { SettingsStore } from './settings';
import { LocalDraftStore } from './localDraftStore';
import { ReviewService } from './reviews';
import { ConfigService } from './configService';
import { RunHistoryService } from './runHistory';
import { SetupService } from './setup';
import { CreationFilesService } from './creationFiles';
import { ThemeController } from './theme';
import {
  CHAT_SESSION_ID,
  CREATION_IMAGE_FORMATS,
  IPC_EVENTS,
  isActiveChatSession,
  type AppEvent,
  type AppRouteEvent,
  type AttentionSnapshot,
  type FeatureSnapshot,
} from '../shared/ipc';
import { toSafeError } from '../shared/errors';
import {
  RENDERER_ENTRY_URL,
  RENDERER_ORIGIN,
  RENDERER_SCHEME,
  installRendererProtocol,
} from './rendererProtocol';
import { AttentionNotificationCoordinator, electronNotificationSink } from './notifications';
import { NativeCommandController, type NativeCommandSnapshot } from './nativeCommands';
import { DiagnosticsService } from './diagnostics';
import {
  UpdateCoordinator,
  createUpdateFixtureFetch,
  detectCanInstallInApp,
  detectPackageFormat,
} from './updates';
import {
  QuitCoordinator,
  activeWorkDialog,
  hasActiveWork,
  quitAnywayDialog,
  stopFailureDialog,
  type ActiveWorkCheck,
  type ActiveWorkDecision,
  type QuitDialogOptions,
  type StopFailureDecision,
  type StopWorkResult,
  type UnresolvedWorkItem,
} from './quitCoordinator';

// Packaged-E2E isolation hook: relocate the app-local data directory
// (settings.json) only when the target provably lives inside the OS temp
// directory — see testHooks.ts for the guard rationale. Inert otherwise.
const testUserData = resolveTestUserDataDir(process.env['AGENTICO_E2E_USER_DATA'], {
  realpath: (candidate) => fs.realpathSync(candidate),
  tmpdir: () => os.tmpdir(),
  isAbsolute: (candidate) => path.isAbsolute(candidate),
  sep: path.sep,
});
if (testUserData !== null) {
  app.setPath('userData', testUserData);
}
const forceQuitDialogsInE2E =
  testUserData !== null && process.env['AGENTICO_E2E_FORCE_QUIT_DIALOGS'] === '1';

const testPackagedResources = resolveTestPackagedResourcesDir(
  process.env['AGENTICO_E2E_RESOURCES_PATH'],
  testUserData,
  {
    realpath: (candidate) => fs.realpathSync(candidate),
    isAbsolute: (candidate) => path.isAbsolute(candidate),
    exists: (candidate) => fs.existsSync(candidate),
    join: (...parts) => path.join(...parts),
  },
);
const testReadyFile = resolveTestOutputFile(process.env['AGENTICO_E2E_READY_FILE'], testUserData, {
  realpath: (candidate) => fs.realpathSync(candidate),
  dirname: (candidate) => path.dirname(candidate),
  tmpdir: () => os.tmpdir(),
  isAbsolute: (candidate) => path.isAbsolute(candidate),
  sep: path.sep,
});
const isPackagedRuntime = app.isPackaged || testPackagedResources !== null;
const runtimeResourcesPath = testPackagedResources ?? process.resourcesPath;
const runtimeExecPath =
  testPackagedResources !== null && process.env['AGENTICO_E2E_INSTALL_EXECUTABLE'] !== undefined
    ? process.env['AGENTICO_E2E_INSTALL_EXECUTABLE']
    : process.execPath;

const devRendererUrl = process.env['ELECTRON_RENDERER_URL'];
const appOrigins = new Set<string>([RENDERER_ORIGIN]);
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

app.setAsDefaultProtocolClient('agentico');

// Must be registered before app readiness. The private standard/secure origin
// lets Chromium resolve relative renderer assets without file:// privileges.
protocol.registerSchemesAsPrivileged([
  {
    scheme: RENDERER_SCHEME,
    privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: false },
  },
]);

function createMainWindow(
  settings: SettingsStore,
  gateway: RuntimeGateway,
  onClose: (event: ElectronEvent, window: BrowserWindow) => void,
): BrowserWindow {
  const bounds = settings.get().window.bounds;
  const window = new BrowserWindow({
    title: 'Agentico',
    width: bounds?.width ?? 1080,
    height: bounds?.height ?? 720,
    ...(bounds !== undefined ? { x: bounds.x, y: bounds.y } : {}),
    minWidth: 400,
    minHeight: 480,
    ...(process.env['AGENTICO_E2E_ALLOW_LARGE_WINDOW'] === '1'
      ? { enableLargerThanScreen: true }
      : {}),
    show: false,
    backgroundColor: nativeTheme.shouldUseDarkColors ? '#16181D' : '#F5F6F7',
    webPreferences: mainWindowWebPreferences(
      path.join(import.meta.dirname, '../preload/index.cjs'),
    ),
  });
  trusted.webContentsId = window.webContents.id;

  window.on('ready-to-show', () => {
    window.show();
    if (testReadyFile !== null) {
      fs.writeFileSync(
        testReadyFile,
        `${JSON.stringify({ readyAt: new Date().toISOString(), title: 'Agentico' })}\n`,
        { mode: 0o600 },
      );
    }
    void gateway.start();
  });

  window.on('close', (event) => {
    const { x, y, width, height } = window.getBounds();
    try {
      settings.update({ window: { bounds: { x, y, width, height } } });
    } catch {
      // Never block shutdown on settings persistence.
    }
    onClose(event, window);
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
    void window.loadURL(RENDERER_ENTRY_URL);
  }
  return window;
}

const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
  app.quit();
} else {
  void app.whenReady().then(() => {
    installRendererProtocol(
      session.defaultSession.protocol,
      path.join(import.meta.dirname, '../renderer'),
      (fileUrl) => net.fetch(fileUrl),
    );
    installSecurityPolicies({ app, session: session.defaultSession, appOrigins });

    const settings = new SettingsStore(app.getPath('userData'));
    const localDrafts = new LocalDraftStore(app.getPath('userData'));
    const { gateway, logBuffer } = createRuntimeGateway({
      getRuntimeSelection: () => settings.get().runtime.selection,
      isPackaged: isPackagedRuntime,
      resourcesPath: runtimeResourcesPath,
      // out/main → out → desktop → repository root (development layout).
      appRoot: path.resolve(import.meta.dirname, '../../..'),
    });
    const diagnostics = new DiagnosticsService({
      userDataDir: app.getPath('userData'),
      version: app.getVersion(),
      revision: process.env['AGENTICO_REVISION'],
      readServerLines: () => logBuffer.snapshot(),
    });
    diagnostics.record('electron', 'info', 'Agentico desktop process started.');

    const theme = new ThemeController(
      nativeTheme,
      () => settings.get().theme,
      (preference) => settings.setTheme(preference),
    );
    theme.applyStored();

    // Dock icon follows the effective theme: nativeTheme resolves the user
    // preference (via themeSource) or the system appearance. Packaged builds
    // read the variants from extraResources; dev runs from desktop/build.
    if (process.platform === 'darwin') {
      const iconDir = app.isPackaged
        ? path.join(process.resourcesPath, 'icons')
        : path.join(import.meta.dirname, '../../build');
      const updateDockIcon = () => {
        const icon = path.join(
          iconDir,
          nativeTheme.shouldUseDarkColors ? 'icon-dark.png' : 'icon-light.png',
        );
        if (fs.existsSync(icon)) {
          app.dock?.setIcon(icon);
        }
      };
      updateDockIcon();
      nativeTheme.on('updated', updateDockIcon);
    }

    const pickCreationFiles = async (kind: 'image' | 'attachment'): Promise<string[]> => {
      const focused = BrowserWindow.getFocusedWindow() ?? BrowserWindow.getAllWindows()[0];
      const options = {
        title: kind === 'image' ? 'Choose images' : 'Choose attachments',
        properties: ['openFile', 'multiSelections'] as Array<'openFile' | 'multiSelections'>,
        ...(kind === 'image'
          ? {
              filters: [
                {
                  name: 'Images',
                  extensions: CREATION_IMAGE_FORMATS.map((format) => format.extension),
                },
              ],
            }
          : {}),
      };
      const result = focused
        ? await dialog.showOpenDialog(focused, options)
        : await dialog.showOpenDialog(options);
      return result.canceled ? [] : result.filePaths;
    };
    const readClipboardImage = async (): Promise<{ paths: string[] }> => {
      const image = clipboard.readImage();
      if (image.isEmpty()) return { paths: [] };
      const directory = path.join(app.getPath('temp'), 'agentico-clipboard');
      await fs.promises.mkdir(directory, { recursive: true });
      const imagePath = path.join(directory, `clipboard-${crypto.randomUUID()}.png`);
      await fs.promises.writeFile(imagePath, image.toPNG());
      return { paths: [imagePath] };
    };
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
    const creationFiles = new CreationFilesService({
      pickFiles: pickCreationFiles,
      readReadiness: () => setup.getReadiness(),
    });

    const features = new FeatureService({
      transport: gateway,
      readReadiness: () => setup.getReadiness(),
      resolveRepositoryFiles: (refs) => creationFiles.resolve(refs),
    });
    const completion = new CompletionService({
      transport: gateway,
    });
    const recovery = new RecoveryService(gateway);
    const bulk = new BulkService(features);
    const sessions = new SessionService(gateway);
    const reviews = new ReviewService(gateway);
    const configService = new ConfigService(gateway);
    const attention = new AttentionService(gateway);
    const runHistory = new RunHistoryService(gateway);
    let mainWindow: BrowserWindow | null = null;
    let nativeCommands: NativeCommandController | null = null;
    let featureLabels = new Map<string, string>();
    let mainWindowAttentionFocused = false;
    let stopStreams = (): void => {};

    const route = (event: AppRouteEvent): void => {
      const window = mainWindow;
      if (window !== null && !window.isDestroyed()) {
        window.webContents.send(IPC_EVENTS.routeRequested, event);
      }
    };

    const broadcastAppEvent = (event: AppEvent): void => {
      for (const window of BrowserWindow.getAllWindows()) {
        if (!window.isDestroyed()) {
          window.webContents.send(IPC_EVENTS.appEvent, event);
        }
      }
    };

    const showMainWindow = (): BrowserWindow => {
      if (mainWindow === null || mainWindow.isDestroyed()) {
        mainWindow = createMainWindow(settings, gateway, handleWindowClose);
        const created = mainWindow;
        created.webContents.on('render-process-gone', (_event, details) => {
          diagnostics.recordCrash({
            processRole: 'renderer',
            category: details.reason,
            context: `exitCode=${details.exitCode}`,
          });
        });
        created.on('closed', () => {
          if (mainWindow === created) {
            mainWindow = null;
            mainWindowAttentionFocused = false;
            publishMainWindowFocusTestState(mainWindowAttentionFocused);
          }
        });
        created.on('focus', () => {
          mainWindowAttentionFocused = true;
          publishMainWindowFocusTestState(mainWindowAttentionFocused);
        });
        created.on('blur', () => {
          mainWindowAttentionFocused = false;
          publishMainWindowFocusTestState(mainWindowAttentionFocused);
        });
        created.on('hide', () => {
          mainWindowAttentionFocused = false;
          publishMainWindowFocusTestState(mainWindowAttentionFocused);
        });
      }
      if (mainWindow.isMinimized()) {
        mainWindow.restore();
      }
      mainWindow.show();
      app.focus({ steal: true });
      mainWindow.focus();
      mainWindowAttentionFocused = true;
      publishMainWindowFocusTestState(mainWindowAttentionFocused);
      return mainWindow;
    };

    const notifications = new AttentionNotificationCoordinator({
      sink: electronNotificationSink,
      shouldNotify: () => {
        const window = mainWindow;
        return (
          window === null ||
          window.isDestroyed() ||
          !window.isVisible() ||
          !mainWindowAttentionFocused
        );
      },
      show: () => {
        showMainWindow();
      },
    });

    const quitCoordinator = new QuitCoordinator<BrowserWindow>(
      {
        detectActiveWork,
        stopWork: stopActiveWork,
        showActiveWorkDialog: async (active, parent) => {
          const result = await showMessageBox(parent, activeWorkDialog(active));
          return activeWorkDecision(result.response);
        },
        showStopFailureDialog: async (result, ownership, parent) => {
          const response = await showMessageBox(parent, stopFailureDialog(result, ownership));
          return stopFailureDecision(response.response);
        },
        confirmQuitAnyway: async (result, ownership, parent) => {
          const response = await showMessageBox(parent, quitAnywayDialog(result, ownership));
          return response.response === 0;
        },
        hide: (parent) => {
          if (parent !== null && !parent.isDestroyed()) {
            parent.hide();
          } else {
            mainWindow?.hide();
          }
        },
        focusMainWindow: () => {
          showMainWindow();
        },
        runtimeOwnership: () => gateway.getState().ownership,
        shutdown: async () => {
          stopStreams();
          await gateway.shutdown();
        },
        quitApplication: () => {
          nativeCommands?.destroy();
          publishNativeCommandTestState(nativeCommands);
          app.quit();
        },
      },
      { testMode: testUserData !== null && !forceQuitDialogsInE2E },
    );

    nativeCommands = new NativeCommandController({
      app,
      showWindow: () => {
        showMainWindow();
      },
      route,
      quit: () => {
        void quitCoordinator.requestQuitDecision();
      },
    });
    nativeCommands.install();
    publishNativeCommandTestState(nativeCommands);

    app.on('second-instance', (_event, argv) => {
      showMainWindow();
      const requestedRoute = routeFromArgv(argv);
      if (requestedRoute !== null) {
        route(requestedRoute);
      }
    });

    app.on('open-url', (event, url) => {
      event.preventDefault();
      showMainWindow();
      const requestedRoute = routeFromUrl(url);
      if (requestedRoute !== null) {
        route(requestedRoute);
      }
    });

    async function detectActiveWork(): Promise<ActiveWorkCheck> {
      const forced = forcedActiveWorkForE2E(featureLabels);
      if (forced !== null) {
        return forced;
      }
      const featureIds: string[] = [];
      let detectionFailed = false;
      try {
        const summaries = await features.listFeatures();
        featureLabels = new Map(summaries.map((feature) => [feature.id, feature.name]));
        const snapshots = await Promise.allSettled(
          summaries.map((summary) => features.getFeature(summary.id)),
        );
        for (const result of snapshots) {
          if (result.status === 'rejected') {
            detectionFailed = true;
            continue;
          }
          if (stoppableFeature(result.value)) {
            featureIds.push(result.value.id);
          }
        }
      } catch {
        detectionFailed = true;
      }

      let chatActive = false;
      try {
        chatActive = (await sessions.list()).some(isActiveChatSession);
      } catch {
        detectionFailed = true;
      }

      return { featureIds: [...new Set(featureIds)], chatActive, detectionFailed };
    }

    function handleWindowClose(event: ElectronEvent, window: BrowserWindow): void {
      if (quitCoordinator.shouldAllowClose()) {
        return;
      }
      event.preventDefault();
      void quitCoordinator.requestQuitDecision(window);
    }

    async function stopActiveWork(active: ActiveWorkCheck): Promise<StopWorkResult> {
      const forcedFailure = consumeForcedStopFailure(active, featureLabels);
      if (forcedFailure !== null) {
        return forcedFailure;
      }

      const stopFailures = new Map<string, string>();
      const stops = active.featureIds.map(async (featureId) => {
        try {
          await features.dispatchAction({ featureId, action: 'pause-stop' });
        } catch (error) {
          stopFailures.set(`feature:${featureId}`, safeStopReason(error));
        }
      });
      if (active.chatActive) {
        stops.push(
          sessions
            .endChat()
            .then(() => undefined)
            .catch((error: unknown) => {
              stopFailures.set('ama', safeStopReason(error));
            }),
        );
      }
      await Promise.all(stops);

      const deadline = Date.now() + 10_000;
      let latest = await detectActiveWork();
      while (!latest.detectionFailed && hasActiveWork(latest) && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 250));
        latest = await detectActiveWork();
      }
      if (!hasActiveWork(latest)) {
        return { unresolved: [] };
      }

      const unresolved: UnresolvedWorkItem[] = [];
      if (latest.detectionFailed) {
        unresolved.push({
          kind: 'detection',
          id: 'active-work-detection',
          label: 'Active work check',
          reason: 'Agentico could not verify whether all work stopped.',
        });
      }
      for (const featureId of latest.featureIds) {
        unresolved.push({
          kind: 'feature',
          id: featureId,
          label: featureLabels.get(featureId) ?? `Feature ${featureId}`,
          reason:
            stopFailures.get(`feature:${featureId}`) ??
            'The server did not report a terminal state before the timeout.',
        });
      }
      if (latest.chatActive) {
        unresolved.push({
          kind: 'ama',
          id: CHAT_SESSION_ID,
          label: 'AMA session',
          reason:
            stopFailures.get('ama') ??
            'The server did not report that AMA ended before the timeout.',
        });
      }
      return { unresolved };
    }

    const updatePackageFormat = detectPackageFormat(process.platform, process.env, runtimeExecPath);
    const updates = new UpdateCoordinator({
      currentVersion: app.getVersion(),
      isPackaged: isPackagedRuntime,
      packageFormat: updatePackageFormat,
      canInstallInApp: detectCanInstallInApp(updatePackageFormat, process.env, runtimeExecPath),
      userDataDir: app.getPath('userData'),
      diagnostics,
      ...(process.env.AGENTICO_UPDATE_FIXTURE === undefined
        ? {}
        : { fetch: createUpdateFixtureFetch(process.env.AGENTICO_UPDATE_FIXTURE) }),
      onStateChanged: () => broadcastAppEvent({ type: 'invalidated', kind: 'updates.changed' }),
      detectActiveWork: async () => {
        const active = await detectActiveWork();
        return {
          featureCount: active.featureIds.length,
          amaActive: active.chatActive,
          detectionFailed: active.detectionFailed,
        };
      },
      stopActiveWork: async () => {
        const result = await stopActiveWork(await detectActiveWork());
        return {
          stopped: result.unresolved.length === 0,
          ...(result.unresolved.length === 0
            ? {}
            : {
                message: result.unresolved
                  .slice(0, 3)
                  .map((item) => `${item.label}: ${item.reason}`)
                  .join('\n'),
              }),
        };
      },
      restart: () => {
        app.relaunch();
        void quitCoordinator.requestQuitDecision();
      },
    });

    async function refreshBackgroundState(): Promise<void> {
      if (gateway.getState().status !== 'ready') {
        nativeCommands?.update({ attentionCount: 0, amaActive: false });
        publishNativeCommandTestState(nativeCommands);
        return;
      }
      try {
        const [snapshot, summaries, sessionList] = await Promise.all([
          attention.getSnapshot(),
          features.listFeatures().catch(() => []),
          sessions.list().catch(() => []),
        ]);
        featureLabels = new Map(summaries.map((feature) => [feature.id, feature.name]));
        notifications.update(snapshot, {
          previewEnabled: settings.get().notifications.previewEnabled,
          featureLabel: (featureId) => featureLabels.get(featureId) ?? 'Untitled feature',
        });
        nativeCommands?.update({
          attentionCount: actionableAttentionCount(snapshot),
          amaActive: sessionList.some(isActiveChatSession),
        });
        publishNativeCommandTestState(nativeCommands);
        await updates.reconcileScheduledInstall();
      } catch {
        nativeCommands?.update({ attentionCount: 0, amaActive: false });
        publishNativeCommandTestState(nativeCommands);
      }
    }
    if (testUserData !== null) {
      const global = globalThis as typeof globalThis & {
        __agenticoRefreshBackgroundState?: () => void;
      };
      global.__agenticoRefreshBackgroundState = () => {
        mainWindowAttentionFocused = false;
        void refreshBackgroundState();
      };
    }

    // Main-process SSE consumption: runs only while the gateway is ready and
    // forwards schema-validated invalidation metadata to the app window.
    const eventSupervisor = new EventStreamSupervisor({
      source: gateway,
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      log: (line) => console.warn(`[agentico-events] ${line}`),
      onStale: () => gateway.handleGlobalStreamStale(),
      onPush: (event) => {
        broadcastAppEvent(event);
        void refreshBackgroundState();
      },
    });
    stopStreams = () => {
      eventSupervisor.stop();
      sessions.cancelAll();
    };
    gateway.subscribe((state) => {
      if (state.status === 'ready') {
        eventSupervisor.start();
        updates.startAutomaticChecks();
        void refreshBackgroundState();
      } else {
        eventSupervisor.stop();
        sessions.cancelAll();
        nativeCommands?.update({ attentionCount: 0, amaActive: false });
        publishNativeCommandTestState(nativeCommands);
      }
      if (state.status === 'launch-failed' || state.status === 'crashed') {
        diagnostics.record('server', 'error', state.detail, state.diagnostics?.logTail?.join('\n'));
        if (state.status === 'crashed') {
          diagnostics.recordCrash({
            processRole: 'server',
            category: state.detail,
            context: state.diagnostics?.commandContext,
          });
        }
      }
    });
    app.on('before-quit', (event) => {
      if (quitCoordinator.shouldAllowClose()) {
        return;
      }
      event.preventDefault();
      void quitCoordinator.requestQuitDecision();
    });

    const services: IpcServices = {
      getConnectionStatus: () => gateway.getState(),
      retryConnection: () => gateway.retry(),
      restartConnection: () => gateway.restart(),
      getSettings: () => settings.get(),
      updateSettings: (patch) => {
        const next = settings.update(patch);
        void refreshBackgroundState();
        return next;
      },
      getTheme: () => theme.getInfo(),
      setTheme: (preference) => theme.setPreference(preference),
      getReadiness: () => setup.getReadiness(),
      refreshReadiness: () => setup.refreshReadiness(),
      pickWorkspaceDirectory: () => setup.pickWorkspaceDirectory(),
      addWorkspaceRoot: (rootPath) => setup.addWorkspaceRoot(rootPath),
      removeWorkspaceRoot: (rootPath) => setup.removeWorkspaceRoot(rootPath),
      reorderWorkspaceRoots: (paths) => setup.reorderWorkspaceRoots(paths),
      initRepository: (request) => setup.initRepository(request),
      listRepositories: () => setup.listRepositories(),
      listFeatures: () => features.listFeatures(),
      getFeature: (featureId) => features.getFeature(featureId),
      createFeature: (input) => features.createFeature(input),
      dispatchFeatureSetup: (featureId) => features.dispatchSetup(featureId),
      dispatchFeatureAction: async (request) => {
        const result = await features.dispatchAction(request);
        void updates.reconcileScheduledInstall();
        return result;
      },
      getAttention: () => attention.getSnapshot(),
      answerPermission: (request) => attention.answerPermission(request),
      answerQuestions: (request) => attention.answerQuestions(request),
      sendHelp: (request) => attention.sendHelp(request),
      saveGateDraft: (request) => attention.saveGateDraft(request),
      resolveGate: (request) => attention.resolveGate(request),
      startChat: async (request) => {
        const result = await sessions.startChat(request);
        void updates.refreshActiveWorkSummary();
        return result;
      },
      endChat: async () => {
        const result = await sessions.endChat();
        void updates.reconcileScheduledInstall();
        return result;
      },
      listSessions: () => sessions.list(),
      getSession: (sessionId) => sessions.get(sessionId),
      getSessionTranscript: (request) => sessions.transcript(request),
      openSessionOutput: (request, emit) => sessions.subscribe(request, emit),
      cancelSessionOutput: (subscriptionId) => sessions.cancel(subscriptionId),
      getCreationDefaults: () => features.creationDefaults(),
      pickCreationFiles: (kind) => creationFiles.pickFiles(kind),
      readClipboardImage,
      searchCreationFiles: (request) => creationFiles.search(request),
      cancelCreationFileSearch: (requestId) => creationFiles.cancelSearch(requestId),
      loadLocalReviewDraft: (request) => localDrafts.load(request),
      saveLocalReviewDraft: (request) => localDrafts.save(request),
      discardLocalReviewDraft: (request) => localDrafts.discard(request),
      readReview: async (request) => toReviewSession(await reviews.read(request.featureId)),
      openReview: async (request) => toReviewSession(await reviews.open(request.featureId)),
      saveReview: async (request) => {
        const result = await reviews.save(request);
        return result.type === 'conflict'
          ? result
          : { type: 'saved' as const, session: toReviewSession(result.session) };
      },
      validateReview: async (request) => {
        const result = await reviews.validate(request);
        return {
          applicable: result.applicable,
          valid: result.valid,
          revision: result.revision,
          findings: result.findings.map((finding) => ({
            code: finding.code,
            message: finding.message,
          })),
        };
      },
      decideReview: async (request) => {
        const result = await reviews.decide(request);
        return result.type === 'conflict'
          ? result
          : { type: 'saved' as const, result: result.session.result };
      },
      getFeatureConfig: (featureId) => configService.getFeatureConfig(featureId),
      updateFeatureConfig: (request) => configService.updateFeatureConfig(request),
      getWorkspaceDefaults: () => configService.getWorkspaceDefaults(),
      updateWorkspaceDefaults: (defaults) => configService.updateWorkspaceDefaults(defaults),
      getModelCatalogue: () => configService.getModelCatalogue(),
      refreshProviderModels: (provider) => configService.refreshProviderModels(provider),
      listRuns: (request) => runHistory.listRuns(request),
      getRun: (request) => runHistory.getRun(request),
      listRunSessions: (request) => runHistory.listRunSessions(request),
      getLivePreview: (featureId) => runHistory.getLivePreview(featureId),
      listRunArtifacts: (request) => runHistory.listRunArtifacts(request),
      listRunLogs: (request) => runHistory.listRunLogs(request),
      getRunArtifactContent: (request) => runHistory.getRunArtifactContent(request),
      getRunLogContent: (request) => runHistory.getRunLogContent(request),
      getRewindPreview: (request) => runHistory.getRewindPreview(request),
      executeRewind: (request) => runHistory.executeRewind(request),
      launchRebaseChild: (request) => features.launchRebaseChild(request),
      launchRefactorChild: (request) => features.launchRefactorChild(request),
      discardRefactorChild: (request) => features.discardRefactorChild(request),
      fetchReviewFeedback: (request) => features.fetchReviewFeedback(request),
      launchReviewFeedbackChild: (request) => features.launchReviewFeedbackChild(request),
      deleteFeatureCascade: (request) => features.deleteFeatureCascade(request),
      scanRecovery: () => recovery.scan(),
      executeRecovery: (request) => recovery.execute(request),
      readRecoveryLog: (request) => recovery.readLog(request),
      bulkPreview: () => bulk.preview(),
      getUpdates: () => updates.getState(),
      checkForUpdates: () => updates.checkNow(),
      installUpdateWhenIdle: () => updates.installWhenIdle(),
      installUpdateNow: (request) => updates.installNow(request),
      restartToUpdate: () => updates.restartToUpdate(),
      getDiagnostics: () => diagnostics.snapshot(),
      revealDiagnostics: async () => {
        // Snapshot first so reveal also enforces diagnostics root creation and pruning.
        diagnostics.snapshot();
        const result = await shell.openPath(diagnostics.rootPath());
        return { ok: result === '' };
      },
      clearDiagnostics: () => diagnostics.clear(),
      preflightCompletion: (request) => completion.preflightCompletion(request),
      getRepositoryDiff: (request) => completion.getRepositoryDiff(request),
      generatePublishDescription: (request) =>
        features.generatePublishDescription(request.featureId, request.repos ?? []),
      openExternal: (request) => completion.openExternal(request),
      revealPath: (request) => completion.revealPath(request),
    };
    registerIpcHandlers(ipcMain, trusted, services);

    showMainWindow();
    const initialRoute = routeFromArgv(process.argv);
    if (initialRoute !== null) {
      route(initialRoute);
    }

    app.on('activate', () => {
      showMainWindow();
    });
  });
}

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});

function stoppableFeature(snapshot: FeatureSnapshot): boolean {
  return snapshot.actions.some((action) => action.id === 'pause-stop' && action.enabled);
}

function actionableAttentionCount(snapshot: AttentionSnapshot): number {
  return snapshot.items.filter((item) => item.kind !== 'recovery').length;
}

function publishNativeCommandTestState(nativeCommands: NativeCommandController | null): void {
  if (testUserData === null) {
    return;
  }
  const global = globalThis as typeof globalThis & {
    __agenticoNativeCommandState?: NativeCommandSnapshot;
  };
  global.__agenticoNativeCommandState = nativeCommands?.snapshot() ?? {
    attentionCount: 0,
    amaActive: false,
    trayInstalled: false,
    trayFallbackActive: true,
    platform: process.platform,
  };
}

function publishMainWindowFocusTestState(focused: boolean): void {
  if (testUserData === null) {
    return;
  }
  const global = globalThis as typeof globalThis & {
    __agenticoMainWindowFocusState?: { focused: boolean };
  };
  global.__agenticoMainWindowFocusState = { focused };
}

function routeFromArgv(argv: readonly string[]): AppRouteEvent | null {
  if (argv.some((arg) => arg === '--agentico-route=updates')) {
    return { target: 'settings', settingsSection: 'updates' };
  }
  const url = argv.find((arg) => arg.startsWith('agentico://'));
  return url === undefined ? null : routeFromUrl(url);
}

function routeFromUrl(raw: string): AppRouteEvent | null {
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return null;
  }
  if (parsed.protocol !== 'agentico:') {
    return null;
  }
  if (parsed.hostname === 'updates' || parsed.pathname === '/updates') {
    return { target: 'settings', settingsSection: 'updates' };
  }
  if (parsed.hostname === 'diagnostics' || parsed.pathname === '/diagnostics') {
    return { target: 'settings', settingsSection: 'diagnostics' };
  }
  return { target: 'settings' };
}

function consumeForcedStopFailure(
  active: ActiveWorkCheck,
  featureLabels: ReadonlyMap<string, string>,
): StopWorkResult | null {
  if (testUserData === null) {
    return null;
  }
  const global = globalThis as typeof globalThis & {
    __agenticoForceStopFailureCount?: number;
  };
  const remaining = global.__agenticoForceStopFailureCount ?? 0;
  if (remaining <= 0) {
    return null;
  }
  global.__agenticoForceStopFailureCount = remaining - 1;
  const unresolved: UnresolvedWorkItem[] = active.featureIds.map((featureId) => ({
    kind: 'feature',
    id: featureId,
    label: featureLabels.get(featureId) ?? `Feature ${featureId}`,
    reason: 'Packaged E2E forced one unresolved stop outcome.',
  }));
  if (active.chatActive) {
    unresolved.push({
      kind: 'ama',
      id: CHAT_SESSION_ID,
      label: 'AMA session',
      reason: 'Packaged E2E forced one unresolved stop outcome.',
    });
  }
  if (active.detectionFailed || unresolved.length === 0) {
    unresolved.push({
      kind: 'detection',
      id: 'active-work-detection',
      label: 'Active work check',
      reason: 'Packaged E2E forced one unresolved stop outcome.',
    });
  }
  return { unresolved };
}

function forcedActiveWorkForE2E(featureLabels: Map<string, string>): ActiveWorkCheck | null {
  if (testUserData === null) {
    return null;
  }
  const global = globalThis as typeof globalThis & {
    __agenticoForcedActiveWork?: {
      featureIds?: string[];
      featureLabels?: Record<string, string>;
      chatActive?: boolean;
      detectionFailed?: boolean;
    };
  };
  const forced = global.__agenticoForcedActiveWork;
  if (forced === undefined) {
    return null;
  }
  for (const [id, label] of Object.entries(forced.featureLabels ?? {})) {
    featureLabels.set(id, label);
  }
  return {
    featureIds: forced.featureIds ?? [],
    chatActive: forced.chatActive ?? false,
    detectionFailed: forced.detectionFailed ?? false,
  };
}

function safeStopReason(error: unknown): string {
  const safe = toSafeError(error, 'E_STOP_FAILED');
  return safe.remediation === undefined ? safe.message : `${safe.message} ${safe.remediation}`;
}

function activeWorkDecision(response: number): ActiveWorkDecision {
  if (response === 0) return 'keep-running';
  if (response === 1) return 'stop-and-quit';
  return 'cancel';
}

function stopFailureDecision(response: number): StopFailureDecision {
  if (response === 0) return 'retry';
  if (response === 1) return 'quit-anyway';
  return 'cancel';
}

async function showMessageBox(
  parent: BrowserWindow | null,
  options: QuitDialogOptions,
): Promise<MessageBoxReturnValue> {
  if (parent !== null && !parent.isDestroyed()) {
    return dialog.showMessageBox(parent, options);
  }
  return dialog.showMessageBox(options);
}

function toReviewSession(session: {
  feature_id: string;
  review_id: string;
  review_mode: string;
  target_phase: string;
  run_number: number;
  artifact_id: string;
  text: string;
  draft_revision: string;
  source_revision: string;
  can_iterate: boolean;
}) {
  return {
    featureId: session.feature_id,
    reviewId: session.review_id,
    reviewMode: session.review_mode,
    targetPhase: session.target_phase,
    runNumber: session.run_number,
    artifactId: session.artifact_id,
    text: session.text,
    draftRevision: session.draft_revision,
    sourceRevision: session.source_revision,
    canIterate: session.can_iterate,
  };
}
