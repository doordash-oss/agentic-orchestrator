import type {
  AgenticoApi,
  AppEvent,
  AttentionActionResult,
  AttentionSnapshot,
  ConnectionState,
  CreateFeatureResult,
  CreationDefaults,
  FeatureActionResult,
  FeatureSnapshot,
  FeatureSummaryView,
  PickedDirectory,
  ReadinessSnapshot,
  RewindPreviewView,
  RunArtifactView,
  RunArtifactsListResult,
  RunDetailView,
  RunListResult,
  RunSessionsListResult,
  RunSummaryView,
  RunTextContent,
  SessionDetail,
  SessionOutputEvent,
  SessionOutputOpenResult,
  SessionSummary,
  SessionTranscript,
  Settings,
  SetupDispatchResult,
  ThemeInfo,
  ResourceCatalogue,
} from '../../../src/shared/ipc';
import { defaultSettings } from '../../../src/shared/ipc';

const SEALED_RUNS: RunSummaryView[] = [
  {
    runNumber: 7,
    sealedAt: '2026-07-17T14:22:00Z',
    sealReason: 'rewind',
    currentPhase: 'Implement',
    iteration: 3,
    roadmapPhase: 2,
    roadmapTotal: 4,
    artifactCount: 12,
    isRewind: true,
  },
  {
    runNumber: 6,
    sealedAt: '2026-07-17T11:05:00Z',
    sealReason: 'rewind',
    currentPhase: 'Plan',
    iteration: 2,
    artifactCount: 8,
    isRewind: true,
  },
  {
    runNumber: 5,
    sealedAt: '2026-07-16T18:40:00Z',
    sealReason: 'rewind',
    currentPhase: 'Research',
    artifactCount: 5,
    isRewind: true,
  },
  {
    runNumber: 4,
    sealedAt: '2026-07-16T09:15:00Z',
    sealReason: 'rewind',
    currentPhase: 'Inquire',
    artifactCount: 3,
    isRewind: true,
  },
  {
    runNumber: 3,
    sealedAt: '2026-07-15T22:30:00Z',
    sealReason: 'rewind',
    currentPhase: 'Implement',
    iteration: 5,
    roadmapPhase: 1,
    roadmapTotal: 4,
    artifactCount: 15,
    isRewind: true,
  },
  {
    runNumber: 2,
    sealedAt: '2026-07-15T14:00:00Z',
    sealReason: 'rewind',
    currentPhase: 'Design',
    artifactCount: 6,
    isRewind: true,
  },
  {
    runNumber: 1,
    sealedAt: '2026-07-14T10:00:00Z',
    sealReason: 'rewind',
    currentPhase: 'Inquire',
    artifactCount: 2,
    isRewind: true,
  },
];

const RUN_DETAIL: RunDetailView = {
  runNumber: 7,
  artifactCount: 12,
  sealedAt: '2026-07-17T14:22:00Z',
  sealReason: 'rewind',
  currentPhase: 'Implement',
  iteration: 3,
  roadmapPhase: 2,
  roadmapTotal: 4,
  isRewind: true,
  rewindTarget: 'Implement',
  rewindRoadmapPhase: 2,
  carriedFromRun: 6,
  carriedPhases: ['inquire', 'research', 'design', 'roadmap', 'plan', 'phase-01/plan'],
  backupBranchRepos: ['signal-lab', 'orchestrator-core'],
  timing: {
    totalSeconds: 14400,
    byPhase: {
      Inquire: 1200,
      Research: 3600,
      Design: 2400,
      Plan: 1800,
      Implement: 5400,
    },
  },
  cost: {
    totalUsd: 4.82,
    byPhase: {
      Inquire: 0.12,
      Research: 0.84,
      Design: 0.45,
      Plan: 0.33,
      Implement: 3.08,
    },
  },
};

const RUN_ARTIFACTS: RunArtifactView[] = [
  {
    id: 'inquire/out.md',
    type: 'text/markdown',
    category: 'phase-output',
    runNumber: 7,
    phase: 'Inquire',
    size: 4823,
    modifiedAt: '2026-07-17T12:00:00Z',
    contentAvailable: true,
  },
  {
    id: 'research/research.md',
    type: 'text/markdown',
    category: 'phase-output',
    runNumber: 7,
    phase: 'Research',
    size: 18234,
    modifiedAt: '2026-07-17T12:30:00Z',
    contentAvailable: true,
  },
  {
    id: 'design/design.md',
    type: 'text/markdown',
    category: 'phase-output',
    runNumber: 7,
    phase: 'Design',
    size: 9421,
    modifiedAt: '2026-07-17T13:00:00Z',
    contentAvailable: true,
  },
  {
    id: 'phase-01/plan/phase-plan.md',
    type: 'text/markdown',
    category: 'phase-plan',
    runNumber: 7,
    phase: 'Plan',
    size: 12056,
    modifiedAt: '2026-07-17T13:15:00Z',
    contentAvailable: true,
  },
  {
    id: 'phase-02/implement/iteration-03/progress.md',
    type: 'text/markdown',
    category: 'iteration-progress',
    runNumber: 7,
    phase: 'Implement',
    size: 3478,
    modifiedAt: '2026-07-17T14:20:00Z',
    contentAvailable: true,
  },
];

const ARTIFACT_CONTENT = `# Phase 2 — Implementation Plan

## Iteration 3

### Completed this iteration
- Screenshot capture infrastructure for visual evidence artifacts
- Mock API layer for standalone component rendering
- Playwright spec for automated screenshot capture

### Remaining from the plan
- Integration with packaged e2e journey specs
- Regression tests for screenshot dimensions

### Where I stopped
All visual artifacts captured. Awaiting verification.
`;

const SESSIONS: SessionSummary[] = [
  {
    id: 'sess-impl-03',
    featureId: 'abcd1234ef567890',
    runNumber: 7,
    phase: 'Implement',
    kind: 'implement',
    provider: 'claude',
    status: 'completed',
    startedAt: '2026-07-17T13:30:00Z',
    usage: { inputTokens: 45000, outputTokens: 12000, costUsd: 1.2 },
  },
  {
    id: 'sess-impl-02',
    featureId: 'abcd1234ef567890',
    runNumber: 7,
    phase: 'Implement',
    kind: 'implement',
    provider: 'claude',
    status: 'completed',
    startedAt: '2026-07-17T13:00:00Z',
    usage: { inputTokens: 38000, outputTokens: 10000, costUsd: 0.95 },
  },
  {
    id: 'sess-plan-01',
    featureId: 'abcd1234ef567890',
    runNumber: 7,
    phase: 'Plan',
    kind: 'plan',
    provider: 'claude',
    status: 'completed',
    startedAt: '2026-07-17T12:45:00Z',
    usage: { inputTokens: 22000, outputTokens: 8000, costUsd: 0.33 },
  },
];

const FEATURE_SUMMARY: FeatureSummaryView[] = [
  {
    id: 'abcd1234ef567890',
    name: 'History and Rewind',
    status: 'StatusImplementing',
    currentPhase: 'Implement',
    repos: ['signal-lab', 'orchestrator-core'],
    createdAt: '2026-07-14T10:00:00Z',
    activeRun: 8,
    runCount: 8,
    phaseStatus: 'implementing',
    warnings: [],
  },
];

const FEATURE_SNAPSHOT: FeatureSnapshot = {
  id: 'abcd1234ef567890',
  name: 'History and Rewind',
  slug: 'history-and-rewind',
  status: 'StatusImplementing',
  currentPhase: 'Implement',
  pipeline: 'large',
  description:
    'Expose sealed-run history and deliver rewind as a server-authored seal-and-fork journey.',
  repos: ['signal-lab', 'orchestrator-core'],
  createdAt: '2026-07-14T10:00:00Z',
  activeRun: 8,
  actions: [
    {
      id: 'start',
      enabled: false,
      disabledReasons: [{ code: 'already_running', message: 'The feature is already running.' }],
    },
    { id: 'pause-stop', enabled: true, disabledReasons: [] },
    { id: 'rewind', enabled: true, disabledReasons: [] },
  ],
};

const REWIND_PREVIEW = {
  eligible: true,
  sourceRunNumber: 8,
  sourceRevision: 'a1b2c3d4e5f6',
  targetPhase: 'implement',
  effectivePhase: 'Implement',
  roadmapPhase: 2,
  validPhases: [
    { phase: 'inquire' },
    { phase: 'research' },
    { phase: 'design' },
    { phase: 'plan' },
    { phase: 'implement', escalatesTo: 'Implement', overridePhase: 'Implement' },
  ],
  validRoadmapPhases: [1, 2, 3, 4],
  upgradePipelineOptions: ['medium', 'large', 'moonshot'],
  carriedPhases: ['inquire', 'research', 'design', 'roadmap', 'plan', 'phase-01/plan'],
  carriedFromRun: 7,
  prConsequences: [
    { repo: 'signal-lab', prUrl: 'https://github.com/example/signal-lab/pull/42' },
    { repo: 'orchestrator-core', prUrl: 'https://github.com/example/orchestrator-core/pull/18' },
  ],
  worktreeConsequences: [
    { repo: 'signal-lab', resetKind: 'anchor' as const },
    { repo: 'orchestrator-core', resetKind: 'anchor' as const },
  ],
  backupBranchRepos: ['signal-lab', 'orchestrator-core'],
  validationFindings: [],
};

const REWIND_RESULT: FeatureActionResult = {
  featureId: 'abcd1234ef567890',
  action: 'rewind',
  result: 'rewound',
  phase: 'Implement',
  sessionIds: [],
  sourceRunNumber: 8,
  newRunNumber: 9,
  warnings: [
    'Branch feature/history-and-rewind on signal-lab was force-reset to anchor abc123; the previous tip is preserved as backup branch feature/history-and-rewind-v8.',
    'PR #42 on signal-lab will be closed automatically by the rewind.',
  ],
};

const READY_SNAPSHOT: ReadinessSnapshot = {
  ready: true,
  probedAt: '2026-07-17T10:00:00Z',
  providers: [{ name: 'claude', installed: true, version: '2.1.0', ready: true }],
  models: { available: true, models: ['claude-sonnet-4-5'] },
  configuration: { valid: true },
  workspaceRoots: [{ path: '/work/space', valid: true }],
  repositories: [
    { name: 'signal-lab', path: '/work/space/signal-lab', valid: true },
    { name: 'orchestrator-core', path: '/work/space/orchestrator-core', valid: true },
  ],
  issues: [],
};

const CONNECTION_STATE: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected to runtime.',
  ownership: 'app-owned',
};

function makeMockApi(scene: string, listeners: Set<(event: AppEvent) => void>): AgenticoApi {
  let theme: ThemeInfo = { preference: 'dark', resolved: 'dark' };
  const appEventListeners = listeners;
  const connectionListeners = new Set<(state: ConnectionState) => void>();
  const sessionOutputListeners = new Set<(event: SessionOutputEvent) => void>();
  let currentSettings: Settings = {
    ...defaultSettings(),
    theme: 'dark',
    tabs: {
      open: [
        {
          featureId: 'abcd1234ef567890',
          titleHint: 'History and Rewind',
          selectedRunNumber:
            scene.startsWith('archive') ||
            scene.startsWith('pinned') ||
            scene.startsWith('constrained')
              ? 7
              : null,
        },
      ],
      activeFeatureId: 'abcd1234ef567890',
    },
  };

  return {
    getConnectionStatus: () => Promise.resolve(CONNECTION_STATE),
    retryConnection: () => Promise.resolve(CONNECTION_STATE),
    restartConnection: () => Promise.resolve(CONNECTION_STATE),
    onConnectionChanged: (listener) => {
      connectionListeners.add(listener);
      return () => connectionListeners.delete(listener);
    },
    getSettings: () => Promise.resolve(currentSettings),
    updateSettings: (patch) => {
      currentSettings = { ...currentSettings, ...patch };
      return Promise.resolve(currentSettings);
    },
    getThemePreference: () => Promise.resolve(theme),
    setThemePreference: (preference) => {
      theme = { preference, resolved: preference === 'system' ? theme.resolved : preference };
      return Promise.resolve(theme);
    },
    getReadiness: () => Promise.resolve(READY_SNAPSHOT),
    refreshReadiness: () => Promise.resolve(READY_SNAPSHOT),
    pickWorkspaceDirectory: () => Promise.resolve({ path: null } as PickedDirectory),
    addWorkspaceRoot: () => Promise.resolve(READY_SNAPSHOT),
    removeWorkspaceRoot: () => Promise.resolve(READY_SNAPSHOT),
    reorderWorkspaceRoots: () => Promise.resolve(READY_SNAPSHOT),
    initRepository: () => Promise.resolve(READY_SNAPSHOT),
    listRepositories: () => Promise.resolve(READY_SNAPSHOT.repositories),
    listFeatures: () => Promise.resolve(FEATURE_SUMMARY),
    getFeature: (_featureId: string) => {
      if (scene === 'fork') {
        return Promise.resolve({
          ...FEATURE_SNAPSHOT,
          activeRun: 9,
          runCount: 9,
          status: 'StatusImplementing',
          actions: [
            {
              id: 'start',
              enabled: false,
              disabledReasons: [
                { code: 'already_running', message: 'The feature is already running.' },
              ],
            },
            { id: 'pause-stop', enabled: true, disabledReasons: [] },
            { id: 'rewind', enabled: true, disabledReasons: [] },
          ],
        });
      }
      return Promise.resolve(FEATURE_SNAPSHOT);
    },
    createFeature: () => Promise.resolve({ featureId: 'abcd1234ef567890' } as CreateFeatureResult),
    dispatchFeatureSetup: () => Promise.resolve({ result: 'setup_started' } as SetupDispatchResult),
    dispatchFeatureAction: ({ action }) =>
      Promise.resolve({ featureId: 'abcd1234ef567890', action, result: 'started', sessionIds: [] }),
    getAttention: () => Promise.resolve({ items: [] } as AttentionSnapshot),
    answerPermission: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    answerQuestions: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    sendHelp: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    saveGateDraft: () => Promise.resolve({ result: 'drafted' } as AttentionActionResult),
    resolveGate: () => Promise.resolve({ result: 'resolved' } as AttentionActionResult),
    listSessions: () => Promise.resolve(SESSIONS),
    getSession: (sessionId) => {
      const summary = SESSIONS.find((s) => s.id === sessionId);
      if (!summary) return Promise.reject(new Error('not_found: session not found'));
      return Promise.resolve({
        ...summary,
        transcriptCursor: { total: 0, start: 0, end: 0 },
        pendingControlCount: 0,
        canAttach: false,
        logAvailable: false,
      } as SessionDetail);
    },
    getSessionTranscript: ({ sessionId }) =>
      Promise.resolve({
        sessionId,
        cursor: { total: 0, start: 0, end: 0 },
        messages: [],
      } as SessionTranscript),
    openSessionOutput: () =>
      Promise.resolve({ subscriptionId: 'subscription-1' } as SessionOutputOpenResult),
    cancelSessionOutput: () => Promise.resolve(true),
    onSessionOutput: (listener) => {
      sessionOutputListeners.add(listener);
      return () => sessionOutputListeners.delete(listener);
    },
    getCreationDefaults: () =>
      Promise.resolve({
        repositories: READY_SNAPSHOT.repositories!.map((r) => ({ ...r, valid: true })),
        defaults: {
          pipeline: 'large',
          inquireness: 'balanced',
          models: [],
          useCurrentBranch: false,
        },
      } as CreationDefaults),
    loadLocalReviewDraft: () => Promise.resolve(null),
    saveLocalReviewDraft: () => Promise.reject(new Error('unused')),
    discardLocalReviewDraft: () => Promise.resolve(false),
    readReview: () => Promise.reject(new Error('unused')),
    openReview: () => Promise.reject(new Error('unused')),
    saveReview: () => Promise.reject(new Error('unused')),
    validateReview: () => Promise.reject(new Error('unused')),
    decideReview: () => Promise.reject(new Error('unused')),
    listResources: () => Promise.resolve({ resources: [] } as ResourceCatalogue),
    readResource: () => Promise.reject(new Error('unused')),
    validateResource: () => Promise.reject(new Error('unused')),
    writeResource: () => Promise.reject(new Error('unused')),
    loadLocalResourceDraft: () => Promise.resolve(null),
    saveLocalResourceDraft: () => Promise.reject(new Error('unused')),
    discardLocalResourceDraft: () => Promise.resolve(false),
    listRuns: ({ page, pageSize }) => {
      const ps = pageSize ?? 20;
      const p = page ?? 1;
      const total = SEALED_RUNS.length;
      const totalPages = Math.ceil(total / ps);
      const start = (p - 1) * ps;
      const runs = SEALED_RUNS.slice(start, start + ps);
      return Promise.resolve({
        runs,
        page: p,
        pageSize: ps,
        total,
        totalPages,
      } as RunListResult);
    },
    getRun: ({ runNumber }) => {
      const summary = SEALED_RUNS.find((r) => r.runNumber === runNumber);
      if (summary === undefined) return Promise.reject(new Error(`Unknown mock run ${runNumber}`));
      return Promise.resolve({
        ...RUN_DETAIL,
        ...summary,
        rewindTarget: summary.currentPhase,
        ...(summary.roadmapPhase !== undefined ? { rewindRoadmapPhase: summary.roadmapPhase } : {}),
      } as RunDetailView);
    },
    listRunSessions: ({ runNumber }) =>
      Promise.resolve({
        runNumber,
        sessions: SESSIONS,
      } as RunSessionsListResult),
    listRunArtifacts: ({ runNumber }) =>
      Promise.resolve({
        artifacts: RUN_ARTIFACTS.map((a) => ({ ...a, runNumber })),
      } as RunArtifactsListResult),
    getRunArtifactContent: ({ artifactId }) =>
      Promise.resolve({
        id: artifactId,
        offset: 0,
        limit: 65536,
        size: ARTIFACT_CONTENT.length,
        text: ARTIFACT_CONTENT,
        truncated: false,
      } as RunTextContent),
    getRunLogContent: ({ logId }) =>
      Promise.resolve({
        id: logId,
        offset: 0,
        limit: 65536,
        size: ARTIFACT_CONTENT.length,
        text: ARTIFACT_CONTENT,
        truncated: false,
      } as RunTextContent),
    getRewindPreview: (request) =>
      Promise.resolve({
        ...REWIND_PREVIEW,
        targetPhase: request.targetPhase,
        roadmapPhase: request.roadmapPhase,
      } as RewindPreviewView),
    executeRewind: () =>
      new Promise((resolve) => {
        setTimeout(() => resolve(REWIND_RESULT), 500);
      }),
    onAppEvent: (listener) => {
      appEventListeners.add(listener);
      return () => appEventListeners.delete(listener);
    },
  };
}

export function installMockApi(scene: string): {
  emitAppEvent: (event: AppEvent) => void;
  setTheme: (theme: 'light' | 'dark') => void;
} {
  const appEventListeners = new Set<(event: AppEvent) => void>();
  const api = makeMockApi(scene, appEventListeners);
  Object.defineProperty(window, 'agentico', {
    value: api,
    writable: true,
    configurable: true,
  });

  return {
    emitAppEvent: (event) => {
      appEventListeners.forEach((l) => l(event));
    },
    setTheme: (themeMode) => {
      document.documentElement.dataset['theme'] = themeMode;
    },
  };
}

export {
  SEALED_RUNS,
  RUN_DETAIL,
  RUN_ARTIFACTS,
  SESSIONS,
  FEATURE_SNAPSHOT,
  REWIND_PREVIEW,
  REWIND_RESULT,
};
