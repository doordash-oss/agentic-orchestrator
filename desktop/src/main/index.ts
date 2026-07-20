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
  dialog,
  ipcMain,
  nativeTheme,
  session,
  type Event as ElectronEvent,
  type MessageBoxReturnValue,
} from 'electron';
import { createRuntimeGateway } from './gateway/wiring';
import { resolveTestUserDataDir } from './testHooks';
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
import { ResourceService } from './resources';
import { RunHistoryService } from './runHistory';
import { ResourceDraftStore } from './resourceDraftStore';
import { SetupService } from './setup';
import { ThemeController } from './theme';
import {
  CHAT_SESSION_ID,
  IPC_EVENTS,
  isActiveChatSession,
  type AppRouteEvent,
  type AttentionSnapshot,
  type FeatureSnapshot,
} from '../shared/ipc';
import { toSafeError } from '../shared/errors';
import { AttentionNotificationCoordinator, electronNotificationSink } from './notifications';
import { NativeCommandController, type NativeCommandSnapshot } from './nativeCommands';
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
    void window.loadFile(path.join(import.meta.dirname, '../renderer/index.html'));
  }
  return window;
}

const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
  app.quit();
} else {
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
    const completion = new CompletionService({
      transport: gateway,
    });
    const recovery = new RecoveryService(gateway);
    const bulk = new BulkService(features);
    const sessions = new SessionService(gateway);
    const reviews = new ReviewService(gateway);
    const resourceService = new ResourceService(gateway);
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

    const showMainWindow = (): BrowserWindow => {
      if (mainWindow === null || mainWindow.isDestroyed()) {
        mainWindow = createMainWindow(settings, gateway, handleWindowClose);
        const created = mainWindow;
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
      route: (event) => {
        showMainWindow();
        route(event);
      },
    });

    const quitCoordinator = new QuitCoordinator<BrowserWindow>({
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
    });

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

    app.on('second-instance', () => {
      showMainWindow();
    });

    async function detectActiveWork(): Promise<ActiveWorkCheck> {
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
        for (const window of BrowserWindow.getAllWindows()) {
          if (!window.isDestroyed()) {
            window.webContents.send(IPC_EVENTS.appEvent, event);
          }
        }
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
        void refreshBackgroundState();
      } else {
        eventSupervisor.stop();
        sessions.cancelAll();
        nativeCommands?.update({ attentionCount: 0, amaActive: false });
        publishNativeCommandTestState(nativeCommands);
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
      dispatchFeatureAction: (request) => features.dispatchAction(request),
      getAttention: () => attention.getSnapshot(),
      answerPermission: (request) => attention.answerPermission(request),
      answerQuestions: (request) => attention.answerQuestions(request),
      sendHelp: (request) => attention.sendHelp(request),
      saveGateDraft: (request) => attention.saveGateDraft(request),
      resolveGate: (request) => attention.resolveGate(request),
      startChat: (request) => sessions.startChat(request),
      endChat: () => sessions.endChat(),
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
      startRebase: (request) => features.startRebase(request),
      preflightRebase: (request) => features.preflightRebase(request),
      fetchReviewComments: (request) => features.fetchReviewComments(request),
      startReviewComments: (request) => features.startReviewComments(request),
      startRefactor: (request) => features.startRefactor(request),
      preflightRefactor: (request) => features.preflightRefactor(request),
      scanRecovery: () => recovery.scan(),
      executeRecovery: (request) => recovery.execute(request),
      readRecoveryLog: (request) => recovery.readLog(request),
      bulkPreview: () => bulk.preview(),
      preflightCompletion: (request) => completion.preflightCompletion(request),
      getRepositoryDiff: (request) => completion.getRepositoryDiff(request),
      generatePublishDescription: (request) =>
        features.generatePublishDescription(request.featureId, request.repos ?? []),
      openExternal: (request) => completion.openExternal(request),
      revealPath: (request) => completion.revealPath(request),
    };
    registerIpcHandlers(ipcMain, trusted, services);

    showMainWindow();

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
