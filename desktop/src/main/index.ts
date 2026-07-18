/**
 * Electron main entry point. All privileged state (settings, theme, the
 * runtime gateway with its bearer token and child-server supervision) lives
 * here; the renderer only ever sees the narrow preload API.
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { BrowserWindow, app, dialog, ipcMain, nativeTheme, session } from 'electron';
import { createRuntimeGateway } from './gateway/wiring';
import { resolveTestUserDataDir } from './testHooks';
import { EventStreamSupervisor } from './gateway/events';
import type { RuntimeGateway } from './gateway/runtimeGateway';
import { FeatureService } from './features';
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
import { ResourceService } from './resources';
import { RunHistoryService } from './runHistory';
import { ResourceDraftStore } from './resourceDraftStore';
import { SetupService } from './setup';
import { ThemeController } from './theme';
import { IPC_EVENTS } from '../shared/ipc';

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
  const localDrafts = new LocalDraftStore(app.getPath('userData'));
  const resourceDrafts = new ResourceDraftStore(app.getPath('userData'));
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

  const features = new FeatureService({
    transport: gateway,
    readReadiness: () => setup.getReadiness(),
  });
  const sessions = new SessionService(gateway);
  const reviews = new ReviewService(gateway);
  const resourceService = new ResourceService(gateway);
  const attention = new AttentionService(gateway);
  const runHistory = new RunHistoryService(gateway);

  // Main-process SSE consumption: runs only while the gateway is ready and
  // forwards schema-validated invalidation metadata to the app window.
  const eventSupervisor = new EventStreamSupervisor({
    source: gateway,
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    log: (line) => console.warn(`[agentico-events] ${line}`),
    onStale: () => gateway.handleGlobalStreamStale(),
    onPush: (event) => {
      for (const window of BrowserWindow.getAllWindows()) {
        if (!window.isDestroyed()) {
          window.webContents.send(IPC_EVENTS.appEvent, event);
        }
      }
    },
  });
  gateway.subscribe((state) => {
    if (state.status === 'ready') {
      eventSupervisor.start();
    } else {
      eventSupervisor.stop();
      sessions.cancelAll();
    }
  });
  app.on('before-quit', () => {
    eventSupervisor.stop();
    sessions.cancelAll();
  });

  const services: IpcServices = {
    getConnectionStatus: () => gateway.getState(),
    retryConnection: () => gateway.retry(),
    restartConnection: () => gateway.restart(),
    getSettings: () => settings.get(),
    updateSettings: (patch) => settings.update(patch),
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
    dispatchFeatureAction: (request) => features.dispatchAction(request),
    getAttention: () => attention.getSnapshot(),
    answerPermission: (request) => attention.answerPermission(request),
    answerQuestions: (request) => attention.answerQuestions(request),
    sendHelp: (request) => attention.sendHelp(request),
    saveGateDraft: (request) => attention.saveGateDraft(request),
    resolveGate: (request) => attention.resolveGate(request),
    listSessions: () => sessions.list(),
    getSession: (sessionId) => sessions.get(sessionId),
    getSessionTranscript: (request) => sessions.transcript(request),
    openSessionOutput: (request, emit) => sessions.subscribe(request, emit),
    cancelSessionOutput: (subscriptionId) => sessions.cancel(subscriptionId),
    getCreationDefaults: () => features.creationDefaults(),
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
    listResources: (kind) => resourceService.catalogue(kind),
    readResource: (resourceId) => resourceService.read(resourceId),
    validateResource: (request) => resourceService.validate(request),
    writeResource: (request) => resourceService.write(request),
    loadLocalResourceDraft: (request) => resourceDrafts.load(request),
    saveLocalResourceDraft: (request) => resourceDrafts.save(request),
    discardLocalResourceDraft: (request) => resourceDrafts.discard(request),
    listRuns: (request) => runHistory.listRuns(request),
    getRun: (request) => runHistory.getRun(request),
    listRunSessions: (request) => runHistory.listRunSessions(request),
    listRunArtifacts: (request) => runHistory.listRunArtifacts(request),
    getRunArtifactContent: (request) => runHistory.getRunArtifactContent(request),
    getRunLogContent: (request) => runHistory.getRunLogContent(request),
    getRewindPreview: (request) => runHistory.getRewindPreview(request),
    executeRewind: (request) => runHistory.executeRewind(request),
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
