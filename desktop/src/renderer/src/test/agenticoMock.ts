import { vi } from 'vitest';
import type {
  AgenticoApi,
  AppEvent,
  AppRouteEvent,
  AttentionItem,
  ConnectionState,
  CreationDefaults,
  DiagnosticsSnapshot,
  FeatureSnapshot,
  FeatureActionRequest,
  FeatureSummaryView,
  ReadinessSnapshot,
  SessionDetail,
  SessionOutputEvent,
  SessionSummary,
  SessionTranscript,
  Settings,
  ThemeInfo,
  UpdateState,
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

/** A minimal, valid creation-defaults payload for form tests. */
export function creationDefaults(overrides: Partial<CreationDefaults> = {}): CreationDefaults {
  return {
    repositories: [
      { name: 'repo-a', path: '/work/space/repo-a', valid: true },
      {
        name: 'repo-b',
        path: '/work/space/repo-b',
        valid: false,
        issue: { code: 'invalid_repository', message: 'Not a git repository.' },
      },
    ],
    defaults: {
      pipeline: 'medium',
      inquireness: 'balanced',
      models: [{ phase: 'Planning', model: 'model-plan' }],
      useCurrentBranch: false,
    },
    ...overrides,
  };
}

/** A created feature mid-setup, as the cockpit first sees it. */
export function featureSnapshot(overrides: Partial<FeatureSnapshot> = {}): FeatureSnapshot {
  return {
    id: 'abcd1234ef567890',
    name: 'Search revamp',
    slug: 'search-revamp',
    status: 'SettingUpWorktrees',
    currentPhase: 'Plan',
    pipeline: 'medium',
    description: 'Improve search.',
    repos: ['repo-a'],
    createdAt: '2026-07-14T10:00:00Z',
    activeRun: 1,
    setup: {
      status: 'running',
      attempt: 1,
      tasks: [
        {
          key: 'worktree:repo-a',
          kind: 'worktree',
          label: 'Create worktree',
          repo: 'repo-a',
          status: 'done',
          branch: 'feature/search-revamp',
          attempt: 1,
        },
        {
          key: 'kb:repo-a',
          kind: 'kb',
          label: 'Build knowledge base',
          repo: 'repo-a',
          status: 'running',
          attempt: 1,
        },
      ],
    },
    actions: [
      {
        id: 'setup',
        enabled: false,
        disabledReasons: [{ code: 'no_pending_setup', message: 'setup is already running' }],
      },
      {
        id: 'start',
        enabled: false,
        disabledReasons: [{ code: 'setup_pending', message: 'setup has not completed' }],
      },
    ],
    ...overrides,
  };
}

export interface AgenticoMock {
  api: AgenticoApi & {
    getConnectionStatus: ReturnType<typeof vi.fn>;
    retryConnection: ReturnType<typeof vi.fn>;
    restartConnection: ReturnType<typeof vi.fn>;
    onRouteRequest: ReturnType<typeof vi.fn>;
    getSettings: ReturnType<typeof vi.fn>;
    updateSettings: ReturnType<typeof vi.fn>;
    setThemePreference: ReturnType<typeof vi.fn>;
    getReadiness: ReturnType<typeof vi.fn>;
    refreshReadiness: ReturnType<typeof vi.fn>;
    pickWorkspaceDirectory: ReturnType<typeof vi.fn>;
    addWorkspaceRoot: ReturnType<typeof vi.fn>;
    removeWorkspaceRoot: ReturnType<typeof vi.fn>;
    reorderWorkspaceRoots: ReturnType<typeof vi.fn>;
    initRepository: ReturnType<typeof vi.fn>;
    listRepositories: ReturnType<typeof vi.fn>;
    listFeatures: ReturnType<typeof vi.fn>;
    getFeature: ReturnType<typeof vi.fn>;
    createFeature: ReturnType<typeof vi.fn>;
    dispatchFeatureSetup: ReturnType<typeof vi.fn>;
    dispatchFeatureAction: ReturnType<typeof vi.fn>;
    listSessions: ReturnType<typeof vi.fn>;
    getSession: ReturnType<typeof vi.fn>;
    getSessionTranscript: ReturnType<typeof vi.fn>;
    openSessionOutput: ReturnType<typeof vi.fn>;
    cancelSessionOutput: ReturnType<typeof vi.fn>;
    getCreationDefaults: ReturnType<typeof vi.fn>;
    getAttention: ReturnType<typeof vi.fn>;
    answerPermission: ReturnType<typeof vi.fn>;
    answerQuestions: ReturnType<typeof vi.fn>;
    sendHelp: ReturnType<typeof vi.fn>;
    saveGateDraft: ReturnType<typeof vi.fn>;
    resolveGate: ReturnType<typeof vi.fn>;
    startChat: ReturnType<typeof vi.fn>;
    endChat: ReturnType<typeof vi.fn>;
    listResources: ReturnType<typeof vi.fn>;
    readResource: ReturnType<typeof vi.fn>;
    validateResource: ReturnType<typeof vi.fn>;
    writeResource: ReturnType<typeof vi.fn>;
    loadLocalResourceDraft: ReturnType<typeof vi.fn>;
    saveLocalResourceDraft: ReturnType<typeof vi.fn>;
    discardLocalResourceDraft: ReturnType<typeof vi.fn>;
    listRuns: ReturnType<typeof vi.fn>;
    getRun: ReturnType<typeof vi.fn>;
    listRunSessions: ReturnType<typeof vi.fn>;
    listRunArtifacts: ReturnType<typeof vi.fn>;
    getRunArtifactContent: ReturnType<typeof vi.fn>;
    getRunLogContent: ReturnType<typeof vi.fn>;
    getRewindPreview: ReturnType<typeof vi.fn>;
    executeRewind: ReturnType<typeof vi.fn>;
    preflightCompletion: ReturnType<typeof vi.fn>;
    getRepositoryDiff: ReturnType<typeof vi.fn>;
    generatePublishDescription: ReturnType<typeof vi.fn>;
    openExternal: ReturnType<typeof vi.fn>;
    revealPath: ReturnType<typeof vi.fn>;
    startRebase: ReturnType<typeof vi.fn>;
    preflightRebase: ReturnType<typeof vi.fn>;
    fetchReviewComments: ReturnType<typeof vi.fn>;
    startReviewComments: ReturnType<typeof vi.fn>;
    startRefactor: ReturnType<typeof vi.fn>;
    preflightRefactor: ReturnType<typeof vi.fn>;
    scanRecovery: ReturnType<typeof vi.fn>;
    executeRecovery: ReturnType<typeof vi.fn>;
    readRecoveryLog: ReturnType<typeof vi.fn>;
    bulkPreview: ReturnType<typeof vi.fn>;
    getUpdates: ReturnType<typeof vi.fn>;
    checkForUpdates: ReturnType<typeof vi.fn>;
    installUpdateWhenIdle: ReturnType<typeof vi.fn>;
    installUpdateNow: ReturnType<typeof vi.fn>;
    restartToUpdate: ReturnType<typeof vi.fn>;
    getDiagnostics: ReturnType<typeof vi.fn>;
    revealDiagnostics: ReturnType<typeof vi.fn>;
    clearDiagnostics: ReturnType<typeof vi.fn>;
  };
  /** Push a connection change to every subscribed listener. */
  emitConnection(state: ConnectionState): void;
  listenerCount(): number;
  /** Push a validated app event (invalidation/stream status) to listeners. */
  emitAppEvent(event: AppEvent): void;
  appEventListenerCount(): number;
  emitRouteRequest(event: AppRouteEvent): void;
  routeListenerCount(): number;
  emitSessionOutput(event: SessionOutputEvent): void;
  sessionOutputListenerCount(): number;
}

export function installAgenticoMock(
  overrides: {
    connection?: ConnectionState;
    settings?: Settings;
    theme?: ThemeInfo;
    readiness?: ReadinessSnapshot;
    features?: FeatureSummaryView[];
    feature?: FeatureSnapshot;
    defaults?: CreationDefaults;
    sessions?: SessionSummary[];
    session?: SessionDetail;
    transcript?: SessionTranscript;
    attention?: { items: AttentionItem[] };
    updates?: UpdateState;
    diagnostics?: DiagnosticsSnapshot;
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
  const feature = overrides.feature ?? featureSnapshot();
  const defaults = overrides.defaults ?? creationDefaults();

  const listeners = new Set<(state: ConnectionState) => void>();
  const routeListeners = new Set<(event: AppRouteEvent) => void>();
  const appEventListeners = new Set<(event: AppEvent) => void>();
  const sessionOutputListeners = new Set<(event: SessionOutputEvent) => void>();
  const sessions = overrides.sessions ?? [];
  const updates = overrides.updates ?? defaultUpdateState();
  const diagnostics = overrides.diagnostics ?? defaultDiagnostics();

  const api = {
    getConnectionStatus: vi.fn(() => Promise.resolve(connection)),
    retryConnection: vi.fn(() => Promise.resolve(connection)),
    restartConnection: vi.fn(() => Promise.resolve(connection)),
    onConnectionChanged: vi.fn((listener: (state: ConnectionState) => void) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    }),
    onRouteRequest: vi.fn((listener: (event: AppRouteEvent) => void) => {
      routeListeners.add(listener);
      return () => routeListeners.delete(listener);
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
    removeWorkspaceRoot: vi.fn(() => Promise.resolve(readiness)),
    reorderWorkspaceRoots: vi.fn(() => Promise.resolve(readiness)),
    initRepository: vi.fn(() => Promise.resolve(readiness)),
    listRepositories: vi.fn(() => Promise.resolve(readiness.repositories)),
    listFeatures: vi.fn(() => Promise.resolve(overrides.features ?? [])),
    getFeature: vi.fn(() => Promise.resolve(feature)),
    createFeature: vi.fn(() => Promise.resolve({ featureId: feature.id })),
    dispatchFeatureSetup: vi.fn(() => Promise.resolve({ result: 'setup_started' })),
    dispatchFeatureAction: vi.fn(({ featureId, action }: FeatureActionRequest) =>
      Promise.resolve({ featureId, action, result: 'started', sessionIds: [] }),
    ),
    getAttention: vi.fn(() => Promise.resolve(overrides.attention ?? { items: [] })),
    answerPermission: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    answerQuestions: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    sendHelp: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    saveGateDraft: vi.fn(() => Promise.resolve({ result: 'drafted' })),
    resolveGate: vi.fn(() => Promise.resolve({ result: 'resolved' })),
    startChat: vi.fn(() => Promise.resolve({ sessionId: '__chat__', result: 'started' })),
    endChat: vi.fn(() => Promise.resolve({ sessionId: '__chat__', result: 'ended' })),
    listSessions: vi.fn(() => Promise.resolve(sessions)),
    getSession: vi.fn((sessionId: string) => {
      if (overrides.session !== undefined) return Promise.resolve(overrides.session);
      const summary = sessions.find((entry) => entry.id === sessionId);
      if (summary === undefined) return Promise.reject(new Error('not_found: session not found'));
      return Promise.resolve({
        ...summary,
        transcriptCursor: { total: 0, start: 0, end: 0 },
        pendingControlCount: 0,
        canAttach: false,
        logAvailable: false,
      });
    }),
    getSessionTranscript: vi.fn(({ sessionId }: { sessionId: string }) =>
      Promise.resolve(
        overrides.transcript ?? {
          sessionId,
          cursor: { total: 0, start: 0, end: 0 },
          messages: [],
        },
      ),
    ),
    openSessionOutput: vi.fn(() => Promise.resolve({ subscriptionId: 'subscription-1' })),
    cancelSessionOutput: vi.fn(() => Promise.resolve(true)),
    onSessionOutput: vi.fn((listener: (event: SessionOutputEvent) => void) => {
      sessionOutputListeners.add(listener);
      return () => sessionOutputListeners.delete(listener);
    }),
    getCreationDefaults: vi.fn(() => Promise.resolve(defaults)),
    loadLocalReviewDraft: vi.fn(() => Promise.resolve(null)),
    saveLocalReviewDraft: vi.fn(() =>
      Promise.resolve({
        runtimeId: 'runtime-a',
        featureId: feature.id,
        reviewId: 'review-a',
        baseDraftRevision: 'revision-a',
        text: '',
        savedAt: '2026-07-16T00:00:00.000Z',
      }),
    ),
    discardLocalReviewDraft: vi.fn(() => Promise.resolve(false)),
    readReview: vi.fn(() => Promise.reject(new Error('unused'))),
    openReview: vi.fn(() => Promise.reject(new Error('unused'))),
    saveReview: vi.fn(() => Promise.reject(new Error('unused'))),
    validateReview: vi.fn(() => Promise.reject(new Error('unused'))),
    decideReview: vi.fn(() => Promise.reject(new Error('unused'))),
    listResources: vi.fn(() => Promise.resolve({ resources: [] })),
    readResource: vi.fn(() => Promise.reject(new Error('unused'))),
    validateResource: vi.fn(() => Promise.reject(new Error('unused'))),
    writeResource: vi.fn(() => Promise.reject(new Error('unused'))),
    loadLocalResourceDraft: vi.fn(() => Promise.resolve(null)),
    saveLocalResourceDraft: vi.fn(() => Promise.reject(new Error('unused'))),
    discardLocalResourceDraft: vi.fn(() => Promise.resolve(false)),
    listRuns: vi.fn(() =>
      Promise.resolve({ runs: [], page: 1, pageSize: 20, total: 0, totalPages: 0 }),
    ),
    getRun: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunSessions: vi.fn(() => Promise.resolve({ runNumber: 1, sessions: [] })),
    listRunArtifacts: vi.fn(() => Promise.resolve({ artifacts: [] })),
    getRunArtifactContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunLogContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRewindPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRewind: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightCompletion: vi.fn(() => Promise.reject(new Error('unused'))),
    getRepositoryDiff: vi.fn(() => Promise.reject(new Error('unused'))),
    generatePublishDescription: vi.fn(() => Promise.reject(new Error('unused'))),
    openExternal: vi.fn(() => Promise.reject(new Error('unused'))),
    revealPath: vi.fn(() => Promise.reject(new Error('unused'))),
    startRebase: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightRebase: vi.fn(() => Promise.reject(new Error('unused'))),
    fetchReviewComments: vi.fn(() => Promise.reject(new Error('unused'))),
    startReviewComments: vi.fn(() => Promise.reject(new Error('unused'))),
    startRefactor: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightRefactor: vi.fn(() => Promise.reject(new Error('unused'))),
    scanRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    readRecoveryLog: vi.fn(() => Promise.reject(new Error('unused'))),
    bulkPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    getUpdates: vi.fn(() => Promise.resolve(updates)),
    checkForUpdates: vi.fn(() => Promise.resolve(updates)),
    installUpdateWhenIdle: vi.fn(() =>
      Promise.resolve({
        ...updates,
        status: 'scheduled',
        message: 'Update installation is scheduled for the next idle window.',
      }),
    ),
    installUpdateNow: vi.fn(() =>
      Promise.resolve({
        ...updates,
        status: 'installing',
        message: 'Restarting to apply the verified update.',
      }),
    ),
    restartToUpdate: vi.fn(() =>
      Promise.resolve({
        ...updates,
        status: 'installing',
        message: 'Restarting to apply the verified update.',
      }),
    ),
    getDiagnostics: vi.fn(() => Promise.resolve(diagnostics)),
    revealDiagnostics: vi.fn(() => Promise.resolve({ ok: true })),
    clearDiagnostics: vi.fn(() =>
      Promise.resolve({
        ...diagnostics,
        entries: [],
        crashes: [],
        retention: { ...diagnostics.retention, entryCount: 0, crashCount: 0 },
      }),
    ),
    onAppEvent: vi.fn((listener: (event: AppEvent) => void) => {
      appEventListeners.add(listener);
      return () => appEventListeners.delete(listener);
    }),
  };

  Object.defineProperty(window, 'agentico', { value: api, writable: true, configurable: true });

  return {
    api: api as AgenticoMock['api'],
    emitConnection: (state) => {
      for (const listener of listeners) listener(state);
    },
    listenerCount: () => listeners.size,
    emitAppEvent: (event) => {
      for (const listener of appEventListeners) listener(event);
    },
    appEventListenerCount: () => appEventListeners.size,
    emitRouteRequest: (event) => {
      for (const listener of routeListeners) listener(event);
    },
    routeListenerCount: () => routeListeners.size,
    emitSessionOutput: (event) => {
      for (const listener of sessionOutputListeners) listener(event);
    },
    sessionOutputListenerCount: () => sessionOutputListeners.size,
  };
}

export function defaultUpdateState(overrides: Partial<UpdateState> = {}): UpdateState {
  return {
    status: 'current',
    currentVersion: '0.1.0',
    packageFormat: 'macos',
    signatureStatus: 'unknown',
    checkedAt: '2026-07-20T10:00:00.000Z',
    nextCheckAt: '2026-07-20T16:00:00.000Z',
    message: 'Agentico is up to date.',
    ...overrides,
  };
}

export function defaultDiagnostics(
  overrides: Partial<DiagnosticsSnapshot> = {},
): DiagnosticsSnapshot {
  return {
    retention: {
      maxBytes: 25 * 1024 * 1024,
      maxAgeDays: 7,
      maxCrashRecords: 10,
      currentBytes: 2048,
      entryCount: 2,
      crashCount: 0,
    },
    entries: [
      {
        id: 'evt-1',
        time: '2026-07-20T10:00:00.000Z',
        source: 'electron',
        level: 'info',
        message: 'Agentico desktop process started.',
      },
      {
        id: 'evt-2',
        time: '2026-07-20T10:01:00.000Z',
        source: 'server',
        level: 'warn',
        message: 'Gateway retry scheduled with token redacted.',
      },
    ],
    crashes: [],
    ...overrides,
  };
}
