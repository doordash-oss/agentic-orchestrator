import type {
  AgenticoApi,
  AppEvent,
  AppRouteEvent,
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
import { CHAT_SESSION_ID, defaultSettings } from '../../../src/shared/ipc';

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

const CHAT_SESSION: SessionSummary = {
  id: CHAT_SESSION_ID,
  featureId: CHAT_SESSION_ID,
  runNumber: 0,
  phase: 'AMA',
  kind: 'chat',
  label: 'Ask Agentico',
  provider: 'claude',
  status: 'running',
  startedAt: '2026-07-19T14:20:00Z',
  usage: { inputTokens: 3200, outputTokens: 900, costUsd: 0.08 },
};

const FEATURE_SUMMARY: FeatureSummaryView[] = [
  {
    id: 'abcd1234ef567890',
    name: 'History and Rewind',
    status: 'implementing',
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
  status: 'implementing',
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

const CYCLES_FEATURE_SNAPSHOT: FeatureSnapshot = {
  id: 'abcd1234ef567890',
  name: 'Signal Lab telemetry',
  slug: 'signal-lab-telemetry',
  status: 'ready',
  currentPhase: 'Implement',
  pipeline: 'large',
  description: 'Complete desktop operational parity for routine lifecycle actions and cycles.',
  repos: ['signal-lab', 'orchestrator-core'],
  createdAt: '2026-07-14T10:00:00Z',
  activeRun: 8,
  actions: [
    {
      id: 'start',
      enabled: false,
      disabledReasons: [{ code: 'already_running', message: 'Already running.' }],
    },
    { id: 'pause-stop', enabled: true, disabledReasons: [] },
    {
      id: 'resume',
      enabled: false,
      disabledReasons: [{ code: 'not_paused', message: 'Pause this feature first to resume it.' }],
    },
    {
      id: 'retry',
      enabled: false,
      disabledReasons: [
        { code: 'not_failed', message: 'This feature has not failed, so retry is not available.' },
      ],
    },
    { id: 'restart', enabled: true, disabledReasons: [] },
    { id: 'rewind', enabled: true, disabledReasons: [] },
    { id: 'rebase', enabled: true, disabledReasons: [] },
    {
      id: 'review-comments',
      enabled: true,
      disabledReasons: [],
      inputs: [
        { name: 'repo', options: ['signal-lab', 'orchestrator-core'] },
        { name: 'mode', options: ['auto', 'address_all'] },
      ],
    },
    {
      id: 'refactor',
      enabled: true,
      disabledReasons: [],
      inputs: [
        { name: 'repo' },
        { name: 'prompt' },
        { name: 'pipeline', options: ['medium', 'large', 'moonshot'] },
      ],
    },
  ],
  repoStatus: [
    {
      name: 'signal-lab',
      publishable: true,
      touched: true,
      prUrl: 'https://github.com/example/signal-lab/pull/42',
      freshness: 'in sync',
      cycleType: 'review-comments',
      cycleStatus: 'running',
    },
    {
      name: 'orchestrator-core',
      publishable: true,
      touched: false,
      prUrl: 'https://github.com/example/orchestrator-core/pull/18',
      freshness: 'local changes',
    },
  ],
  cycle: { type: 'review-comments', status: 'running', count: 2, iteration: 1 },
};

const REBASE_FEATURE_SNAPSHOT: FeatureSnapshot = {
  ...CYCLES_FEATURE_SNAPSHOT,
  repoStatus: [
    {
      name: 'signal-lab',
      publishable: true,
      freshness: 'local changes',
      rebaseStatus: 'pending',
      rebaseTarget: 'origin/main',
      conflictFiles: ['src/telemetry/signal.go', 'src/telemetry/collector.go'],
    },
    {
      name: 'orchestrator-core',
      publishable: true,
      freshness: 'in sync',
      rebaseStatus: 'completed',
      rebaseTarget: 'origin/main',
    },
  ],
  cycle: { type: 'rebase', status: 'running', count: 1 },
};

const RECOVERY_ITEMS = [
  {
    key: 'feature-alpha:signal-lab',
    featureId: 'alpha1234ef567890',
    featureName: 'Alpha Feature',
    repoName: 'signal-lab',
    phase: 'implement',
    iteration: 3,
    pid: 12345,
    processAlive: true,
    logAvailable: true,
    allowedActions: ['resume', 'kill', 'skip'],
    defaultAction: 'skip',
  },
  {
    key: 'feature-beta:orchestrator-core',
    featureId: 'beta1234ef567890',
    featureName: 'Beta Feature',
    repoName: 'orchestrator-core',
    phase: 'implement',
    iteration: 1,
    pid: 0,
    processAlive: false,
    logAvailable: true,
    allowedActions: ['resume', 'kill', 'skip'],
    defaultAction: 'skip',
  },
];

const BULK_PREVIEW_DATA = {
  previewId: 'bulk-preview-001',
  eligible: [
    {
      featureId: 'alpha1234ef567890',
      featureName: 'Alpha Feature',
      action: 'resume' as const,
      enabled: true,
      repos: ['signal-lab'],
    },
    {
      featureId: 'gamma1234ef567890',
      featureName: 'Gamma Feature',
      action: 'retry' as const,
      enabled: true,
      repos: ['orchestrator-core'],
    },
    {
      featureId: 'epsilon1234ef567890',
      featureName: 'Epsilon Feature',
      action: 'resume' as const,
      enabled: true,
      repos: ['data-platform'],
    },
  ],
  excluded: [
    {
      featureId: 'beta1234ef567890',
      featureName: 'Beta Feature',
      action: 'resume' as const,
      enabled: false,
      disabledReason: 'Pause this feature first to resume it.',
    },
    {
      featureId: 'delta1234ef567890',
      featureName: 'Delta Feature',
      action: 'retry' as const,
      enabled: false,
      disabledReason: 'This feature has not failed, so retry is not available.',
    },
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

const AMA_TRANSCRIPT: SessionTranscript = {
  sessionId: CHAT_SESSION_ID,
  cursor: { total: 4, start: 0, end: 4 },
  messages: [
    {
      index: 0,
      role: 'user',
      type: 'text',
      text: 'How should I finish the background supervision phase?',
    },
    {
      index: 1,
      role: 'assistant',
      type: 'text',
      text: 'Start with the singleton AMA dock, then verify notifications, close policy, and native command parity.',
    },
    {
      index: 2,
      role: 'assistant',
      type: 'text',
      text: 'Streaming update 042: the transcript is bounded, deduplicated by row index, and still readable after the session ends.',
    },
    {
      index: 3,
      role: 'assistant',
      type: 'text',
      text: 'Next question is waiting inline without stealing focus from the command palette or attention inbox.',
    },
  ],
};

function isBackgroundScene(scene: string): boolean {
  return scene.startsWith('background-');
}

function backgroundAttentionItems(scene: string): AttentionSnapshot['items'] {
  if (scene === 'background-ama-compact') {
    return [
      {
        kind: 'permission',
        id: 'perm-background-preview',
        featureId: 'abcd1234ef567890',
        sessionId: 'sess-impl-03',
        phase: 'Implement',
        toolName: 'Bash',
        summary: 'Run the bounded verification command.',
        input: { command: 'npm run check' },
        waitingSince: '2026-07-19T14:18:00Z',
      },
    ];
  }
  return [
    {
      kind: 'questions',
      id: 'ask-ama-exact-target',
      sessionId: CHAT_SESSION_ID,
      waitingSince: '2026-07-19T14:19:00Z',
      questions: [
        {
          key: 'Which background behavior should be verified first?',
          header: 'Verification target',
          multiSelect: false,
          options: [
            {
              label: 'Close coordinator',
              description: 'Exercise Keep Running before quit.',
              confidence: 0.83,
            },
            {
              label: 'Notification preview',
              description: 'Check generic body by default.',
              confidence: 0.64,
            },
            {
              label: 'Native menu',
              description: 'Confirm command routing parity.',
              confidence: 0.58,
            },
          ],
        },
      ],
    },
  ];
}

function makeMockApi(scene: string, listeners: Set<(event: AppEvent) => void>): AgenticoApi {
  const requestedTheme = requestedCaptureTheme();
  let theme: ThemeInfo = { preference: requestedTheme, resolved: requestedTheme };
  const appEventListeners = listeners;
  const routeListeners = new Set<(event: AppRouteEvent) => void>();
  const connectionListeners = new Set<(state: ConnectionState) => void>();
  const sessionOutputListeners = new Set<(event: SessionOutputEvent) => void>();
  let currentSettings: Settings = {
    ...defaultSettings(),
    theme: requestedTheme,
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
    onRouteRequest: (listener) => {
      routeListeners.add(listener);
      return () => routeListeners.delete(listener);
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
          status: 'implementing',
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
      if (scene === 'repo-instrument' || scene === 'review-refactor' || scene === 'cycle-gate') {
        return Promise.resolve(CYCLES_FEATURE_SNAPSHOT);
      }
      if (scene === 'rebase-preflight') {
        return Promise.resolve(REBASE_FEATURE_SNAPSHOT);
      }
      if (scene === 'bulk-preview' || scene === 'bulk-queue') {
        const id = _featureId;
        if (id === 'alpha1234ef567890') {
          return Promise.resolve({
            ...CYCLES_FEATURE_SNAPSHOT,
            id,
            name: 'Alpha Feature',
            status: 'paused',
            actions: [
              { id: 'start', enabled: false, disabledReasons: [] },
              { id: 'pause-stop', enabled: false, disabledReasons: [] },
              { id: 'resume', enabled: true, disabledReasons: [] },
              { id: 'retry', enabled: false, disabledReasons: [] },
              { id: 'restart', enabled: false, disabledReasons: [] },
              { id: 'rewind', enabled: false, disabledReasons: [] },
            ],
          });
        }
        if (id === 'gamma1234ef567890') {
          return Promise.resolve({
            ...CYCLES_FEATURE_SNAPSHOT,
            id,
            name: 'Gamma Feature',
            status: 'failed',
            actions: [
              { id: 'start', enabled: false, disabledReasons: [] },
              { id: 'pause-stop', enabled: false, disabledReasons: [] },
              { id: 'resume', enabled: false, disabledReasons: [] },
              { id: 'retry', enabled: true, disabledReasons: [] },
              { id: 'restart', enabled: false, disabledReasons: [] },
              { id: 'rewind', enabled: false, disabledReasons: [] },
            ],
          });
        }
        if (id === 'epsilon1234ef567890') {
          return Promise.resolve({
            ...CYCLES_FEATURE_SNAPSHOT,
            id,
            name: 'Epsilon Feature',
            status: 'paused',
            actions: [
              { id: 'start', enabled: false, disabledReasons: [] },
              { id: 'pause-stop', enabled: false, disabledReasons: [] },
              { id: 'resume', enabled: true, disabledReasons: [] },
              { id: 'retry', enabled: false, disabledReasons: [] },
              { id: 'restart', enabled: false, disabledReasons: [] },
              { id: 'rewind', enabled: false, disabledReasons: [] },
            ],
          });
        }
        return Promise.resolve(CYCLES_FEATURE_SNAPSHOT);
      }
      return Promise.resolve(FEATURE_SNAPSHOT);
    },
    createFeature: () => Promise.resolve({ featureId: 'abcd1234ef567890' } as CreateFeatureResult),
    dispatchFeatureSetup: () => Promise.resolve({ result: 'setup_started' } as SetupDispatchResult),
    dispatchFeatureAction: ({ action, featureId }) => {
      if (scene === 'bulk-queue' && featureId === 'alpha1234ef567890') {
        return new Promise((resolve) =>
          setTimeout(
            () => resolve({ featureId, action, result: 'started', sessionIds: [] }),
            3_000,
          ),
        );
      }
      if (scene === 'bulk-queue' && featureId === 'gamma1234ef567890') {
        return new Promise((_, reject) =>
          setTimeout(
            () => reject(new Error('Server rejected retry: feature is no longer failed.')),
            8_000,
          ),
        );
      }
      if (scene === 'bulk-queue' && featureId === 'epsilon1234ef567890') {
        return new Promise((resolve) =>
          setTimeout(
            () => resolve({ featureId, action, result: 'started', sessionIds: [] }),
            5_000,
          ),
        );
      }
      return Promise.resolve({ featureId, action, result: 'started', sessionIds: [] });
    },
    getAttention: () =>
      Promise.resolve({
        items: isBackgroundScene(scene)
          ? backgroundAttentionItems(scene)
          : scene === 'recovery' || scene === 'recovery-constrained'
            ? ([
                {
                  kind: 'recovery' as const,
                  id: 'recovery-scan',
                  waitingSince: new Date(Date.now() - 120_000).toISOString(),
                  liveCount: 1,
                  deadCount: 1,
                },
              ] as AttentionSnapshot['items'])
            : ([] as AttentionSnapshot['items']),
      } as AttentionSnapshot),
    answerPermission: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    answerQuestions: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    sendHelp: () => Promise.resolve({ result: 'submitted' } as AttentionActionResult),
    saveGateDraft: () => Promise.resolve({ result: 'drafted' } as AttentionActionResult),
    resolveGate: () => Promise.resolve({ result: 'resolved' } as AttentionActionResult),
    startChat: () => Promise.resolve({ sessionId: '__chat__', result: 'started' }),
    endChat: () => Promise.resolve({ sessionId: '__chat__', result: 'ended' }),
    listSessions: () =>
      Promise.resolve(isBackgroundScene(scene) ? [CHAT_SESSION, ...SESSIONS] : SESSIONS),
    getSession: (sessionId) => {
      const summary = (isBackgroundScene(scene) ? [CHAT_SESSION, ...SESSIONS] : SESSIONS).find(
        (s) => s.id === sessionId,
      );
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
      Promise.resolve(
        sessionId === CHAT_SESSION_ID
          ? AMA_TRANSCRIPT
          : ({
              sessionId,
              cursor: { total: 0, start: 0, end: 0 },
              messages: [],
            } as SessionTranscript),
      ),
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
    startRebase: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        cycleType: 'rebase',
        result: 'started',
      }),
    preflightRebase: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'rebase-rev-001',
        repos: [
          {
            repo: 'signal-lab',
            target: 'main',
            publishable: true,
            freshness: 'behind',
            behind: true,
          },
          {
            repo: 'telemetry-sdk',
            target: 'main',
            publishable: true,
            freshness: 'up_to_date',
            behind: false,
            blocker: '',
          },
        ],
      }),
    fetchReviewComments: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        repo: 'signal-lab',
        comments: [
          {
            id: 1,
            file: 'src/main.ts',
            line: 42,
            body: 'Consider extracting this logic.',
            author: 'reviewer',
          },
          {
            id: 2,
            file: 'src/utils.ts',
            line: 10,
            body: 'Add error handling here.',
            author: 'reviewer',
          },
        ],
        revision: 'a1b2c3d4e5f6',
        modes: ['auto', 'address_all'],
      }),
    startReviewComments: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        cycleType: 'review-comments',
        result: 'started',
      }),
    startRefactor: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        cycleType: 'refactor',
        result: 'started',
      }),
    preflightRefactor: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        sourceRevision: 'refactor-rev-001',
        scope: 'all',
        repos: ['signal-lab', 'telemetry-sdk'],
        prompt: 'Rename foo to bar across the codebase',
      }),
    scanRecovery: () =>
      Promise.resolve({
        snapshotId: 'recovery-snapshot-001',
        items: RECOVERY_ITEMS,
      }),
    executeRecovery: () => Promise.resolve({ result: 'Action processed.' }),
    readRecoveryLog: () =>
      Promise.resolve({
        id: 'abcd1234ef567890:signal-lab',
        offset: 0,
        limit: 65536,
        size: 220,
        text: '[implement] iteration 3 started\n[model] requesting review-comments cycle\n[implement] NEED_USER_INPUT: apply suggested fix?\n[cycle] review-comments paused on gate\n[recovery] orphan process detected (pid 412)\n[recovery] log excerpt available — Resume, Kill, or Skip.',
        truncated: false,
      }),
    bulkPreview: () => Promise.resolve(BULK_PREVIEW_DATA),
    preflightCompletion: () => {
      const isPublishScene = scene === 'completion-publish';
      const isDeleteScene = scene === 'completion-delete';
      if (isPublishScene) {
        return Promise.resolve({
          featureId: 'feat-electron-app',
          sourceRevision: 'rev-completion-mock',
          canMarkDone: true,
          repos: [
            {
              repo: 'publish-api',
              publishable: true,
              touched: true,
              status: 'already_published',
              prUrl: 'https://github.example/agentico/publish-api/pull/42',
              baseBranch: 'main',
              branch: 'feature/electron-app',
              freshness: 'up_to_date',
            },
            {
              repo: 'publish-web',
              publishable: true,
              touched: true,
              status: 'eligible',
              lastError: 'push denied by completion fixture; retry only this repository',
              baseBranch: 'main',
              branch: 'feature/electron-app',
              freshness: 'behind',
            },
            {
              repo: 'local-core',
              publishable: false,
              touched: true,
              status: 'ineligible',
              blocker: 'local-only repository — not configured for publishing',
              baseBranch: 'main',
              branch: 'feature/electron-app',
              freshness: 'up_to_date',
            },
          ],
        });
      }
      if (isDeleteScene) {
        return Promise.resolve({
          featureId: 'feat-electron-app',
          sourceRevision: 'rev-completion-mock',
          canMarkDone: false,
          markDoneBlocker: 'feature is already done',
          repos: [
            {
              repo: 'publish-api',
              publishable: true,
              touched: true,
              status: 'completed',
              prUrl: 'https://github.example/agentico/publish-api/pull/42',
            },
            { repo: 'local-core', publishable: false, touched: true, status: 'completed' },
          ],
        });
      }
      return Promise.resolve({
        featureId: 'feat-electron-app',
        sourceRevision: 'rev-completion-mock',
        canMarkDone: true,
        repos: [
          {
            repo: 'agentic-orchestrator',
            publishable: true,
            touched: true,
            status: 'eligible',
            baseBranch: 'main',
            branch: 'feature/electron-app',
            freshness: 'up_to_date',
          },
          {
            repo: 'publish-web',
            publishable: true,
            touched: true,
            status: 'already_published',
            prUrl: 'https://github.example/agentico/publish-web/pull/17',
            baseBranch: 'main',
            branch: 'feature/electron-app',
            freshness: 'up_to_date',
          },
          {
            repo: 'local-core',
            publishable: false,
            touched: true,
            status: 'eligible',
            baseBranch: 'main',
            branch: 'feature/electron-app',
            freshness: 'behind',
            blocker: 'worktree not available',
          },
        ],
      });
    },
    getRepositoryDiff: (_req: { featureId: string; repo: string; filePath?: string }) => {
      const repo = _req.repo;
      const filePath = _req.filePath;
      const sampleDiff = `diff --git a/README.md b/README.md
index 5c32b6a..8a9b3c1 100644
--- a/README.md
+++ b/README.md
@@ -1,4 +1,5 @@
 # Agentic Orchestrator

-Old description here.
+New description with completion workspace support.
+The diff-to-merge journey is now guided.

 ## Features`;
      if (filePath !== undefined) {
        return Promise.resolve({
          featureId: 'feat-electron-app',
          repo,
          sourceRevision: 'rev-completion-mock',
          files: [],
          fileDiff: sampleDiff,
        });
      }
      return Promise.resolve({
        featureId: 'feat-electron-app',
        repo,
        sourceRevision: 'rev-completion-mock',
        files: [
          {
            path: 'README.md',
            operation: 'modify',
            addedLines: 3,
            removedLines: 1,
          },
          {
            path: 'desktop/src/renderer/src/features/CompletionWorkspace.tsx',
            operation: 'add',
            addedLines: 722,
          },
          {
            path: 'internal/orchestrator/completion.go',
            operation: 'modify',
            addedLines: 280,
            removedLines: 15,
          },
        ],
      });
    },
    generatePublishDescription: () =>
      Promise.resolve({
        featureId: 'feat-electron-app',
        title: 'Complete repository-aware Electron workflow',
        body: 'Adds the completion workspace, bounded diffs, explicit merge and mark-done controls, cleanup, and protected deletion.',
      }),
    openExternal: () => Promise.resolve({ ok: true }),
    revealPath: () => Promise.resolve({ ok: true }),
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

function requestedCaptureTheme(): 'light' | 'dark' {
  const param = new URLSearchParams(window.location.search).get('theme');
  return param === 'light' ? 'light' : 'dark';
}

export {
  SEALED_RUNS,
  RUN_DETAIL,
  RUN_ARTIFACTS,
  SESSIONS,
  FEATURE_SNAPSHOT,
  CYCLES_FEATURE_SNAPSHOT,
  REBASE_FEATURE_SNAPSHOT,
  REWIND_PREVIEW,
  REWIND_RESULT,
};
