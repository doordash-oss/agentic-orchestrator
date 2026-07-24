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
  UpdateState,
  DiagnosticsSnapshot,
} from '../../../src/shared/ipc';
import { CHAT_SESSION_ID, defaultSettings } from '../../../src/shared/ipc';

const DEMO_FEATURE_CONFIG = {
  models: { planning: 'demo:planner' },
  inquireness: 'medium' as const,
  checkpoints: {
    inquiryReview: true,
    researchReview: false,
    designReview: false,
    roadmapReview: true,
    phasePlanReview: true,
    manualPublish: true,
    draftPublish: false,
  },
  pipeline: 'large',
  inputNotifications: 'default' as const,
};

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
  reviewGate: {
    reviewingGate: true,
    reviewFixing: false,
    validatingPlan: false,
    validatorStatuses: {
      Craft: 'APPROVED',
      'Functionality/Evidence': 'running',
      Cleanliness: 'CHANGES_REQUESTED',
      Design: 'running',
    },
  },
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

/** Fixtures for the Home flight-board capture: three in-flight runs and a
 * shipped ledger, exercising live, needs-you, and published treatments. */
function flightSnapshot(
  id: string,
  name: string,
  status: string,
  currentPhase: string,
  repos: string[],
  createdAt: string,
  totalSeconds: number,
  pipeline: string = 'medium',
): FeatureSnapshot {
  return {
    id,
    name,
    slug: id,
    status,
    currentPhase,
    pipeline,
    repos,
    createdAt,
    activeRun: 1,
    reviewGate: {
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    },
    actions: [],
    timing: { totalSeconds },
  };
}

const FLIGHT_BOARD_FEATURES: FeatureSnapshot[] = [
  flightSnapshot(
    'updater-auto-1',
    'electron App auto-updater',
    'NeedUserInput',
    'Review',
    ['agentic-orchestrator'],
    '2026-07-23T09:00:00Z',
    2462,
  ),
  flightSnapshot(
    'readme-italian-1',
    'translate README to Italian',
    'Implementing',
    'Implement',
    ['agentic-orchestrator'],
    '2026-07-23T08:30:00Z',
    760,
    'large',
  ),
  flightSnapshot(
    'taulu-ttl-1',
    'Taulu TTL compaction',
    'CodeReady',
    'Publish',
    ['taulu'],
    '2026-07-23T08:00:00Z',
    5940,
  ),
  flightSnapshot(
    'pub-electron-app',
    'electron APP for agentic Orchestrator',
    'Published',
    'Publish',
    ['agentic-orchestrator'],
    '2026-07-22T10:00:00Z',
    5220,
  ),
  flightSnapshot(
    'pub-taulu-mv',
    'Taulu materialized views',
    'Published',
    'Publish',
    ['taulu'],
    '2026-07-21T10:00:00Z',
    3180,
  ),
  flightSnapshot(
    'pub-smart-zone',
    'The Smart Zone',
    'Published',
    'Publish',
    ['agentic-orchestrator'],
    '2026-07-20T10:00:00Z',
    1890,
  ),
  flightSnapshot(
    'pub-prod-fallback',
    'Prod tenant fallback on READ',
    'Published',
    'Publish',
    ['dev-console', 'taulu'],
    '2026-07-19T10:00:00Z',
    4410,
  ),
  flightSnapshot(
    'pub-static-shard',
    'Taulu Static Sharding',
    'Published',
    'Publish',
    ['taulu'],
    '2026-07-18T10:00:00Z',
    2020,
  ),
];

const FLIGHT_BOARD_SUMMARY: FeatureSummaryView[] = FLIGHT_BOARD_FEATURES.map((feature) => ({
  id: feature.id,
  name: feature.name,
  status: feature.status,
  currentPhase: feature.currentPhase,
  repos: feature.repos,
  createdAt: feature.createdAt,
  activeRun: feature.activeRun,
  runCount: 1,
  warnings: [],
}));

const FLIGHT_BOARD_SNAPSHOTS: Record<string, FeatureSnapshot> = Object.fromEntries(
  FLIGHT_BOARD_FEATURES.map((feature) => [feature.id, feature]),
);

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
  reviewGate: {
    reviewingGate: false,
    reviewFixing: false,
    validatingPlan: false,
    validatorStatuses: {},
  },
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

const AFTERCARE_FEATURE_SNAPSHOT: FeatureSnapshot = {
  ...CYCLES_FEATURE_SNAPSHOT,
  name: 'Configure per-phase effort level',
  slug: 'configure-per-phase-effort-level',
  status: 'Published',
  currentPhase: 'Publish',
  pipeline: 'large',
  description: 'Give each orchestration phase an explicit, durable effort level.',
  repos: ['agentic-orchestrator'],
  activeRun: 8,
  setup: {
    status: 'done',
    attempt: 1,
    tasks: [
      {
        key: 'worktree:agentic-orchestrator',
        kind: 'worktree',
        label: 'Create worktree',
        repo: 'agentic-orchestrator',
        branch: 'feature/configure-per-phase-effort-level',
        status: 'done',
        attempt: 1,
      },
    ],
  },
  reviewGate: {
    reviewingGate: false,
    reviewFixing: false,
    validatingPlan: false,
    validatorStatuses: {},
  },
  actions: [
    { id: 'rebase', enabled: true, disabledReasons: [] },
    {
      id: 'review-comments',
      enabled: true,
      disabledReasons: [],
      inputs: [
        { name: 'repo', options: ['agentic-orchestrator'] },
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
    {
      id: 'publish',
      enabled: false,
      disabledReasons: [{ code: 'already_published', message: 'Already published.' }],
    },
    { id: 'cleanup', enabled: true, disabledReasons: [] },
    { id: 'mark-done', enabled: true, disabledReasons: [] },
    { id: 'rewind', enabled: true, disabledReasons: [] },
  ],
  repoStatus: [
    {
      name: 'agentic-orchestrator',
      publishable: true,
      touched: true,
      prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
      freshness: 'in sync',
      cycleType: 'review-comments',
      cycleStatus: 'completed',
    },
  ],
  cycle: {},
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
    allowedActions: ['resume', 'kill'],
    defaultAction: 'resume',
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
    allowedActions: ['resume', 'kill'],
    defaultAction: 'resume',
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

/** Live run transcript with tool activity and bounded file-change diffs. */
const RUN_SESSION_TRANSCRIPT: SessionTranscript['messages'] = [
  {
    index: 0,
    role: 'assistant',
    type: 'text',
    text: 'Wiring the streamed preview into the cockpit so file changes surface as diffs.',
  },
  {
    index: 1,
    role: 'assistant',
    type: 'tool_use',
    tool: 'Edit',
    redacted: true,
    fileChange: {
      path: 'src/renderer/app.tsx',
      operation: 'update',
      detail:
        '- const preview = usePolledPreview(featureId);\n+ const preview = useStreamedPreview(featureId);\n+ const transcript = preview?.transcript ?? [];',
    },
  },
  { index: 2, role: 'system', type: 'tool_progress', tool: 'Write', redacted: true },
  {
    index: 3,
    role: 'assistant',
    type: 'tool_use',
    tool: 'Write',
    redacted: true,
    fileChange: {
      path: 'src/renderer/preview/stream.ts',
      operation: 'write',
      detail:
        '+ export function useStreamedPreview(featureId: string) {\n+   const [preview, setPreview] = useState<LivePreviewView | null>(null);\n+   useEffect(() => subscribePreview(featureId, setPreview), [featureId]);\n+   return preview;\n+ }',
    },
  },
  {
    index: 4,
    role: 'assistant',
    type: 'text',
    text: 'The live preview now streams each file change with a capped diff view.',
  },
];

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

function makeMockApi(
  scene: string,
  listeners: Set<(event: AppEvent) => void>,
  sessionOutputListeners: Set<(event: SessionOutputEvent) => void>,
): AgenticoApi {
  const requestedTheme = requestedCaptureTheme();
  let theme: ThemeInfo = { preference: requestedTheme, resolved: requestedTheme };
  const appEventListeners = listeners;
  const routeListeners = new Set<(event: AppRouteEvent) => void>();
  const connectionListeners = new Set<(state: ConnectionState) => void>();
  let currentSettings: Settings = {
    ...defaultSettings(),
    theme: requestedTheme,
    tabs:
      scene === 'home-flight-board'
        ? { open: [], activeFeatureId: null }
        : {
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
            activeFeatureId:
              scene === 'update-passive-active' || scene === 'update-constrained'
                ? null
                : 'abcd1234ef567890',
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
    listFeatures: () =>
      Promise.resolve(scene === 'home-flight-board' ? FLIGHT_BOARD_SUMMARY : FEATURE_SUMMARY),
    getFeature: (_featureId: string) => {
      if (scene === 'home-flight-board') {
        const snapshot = FLIGHT_BOARD_SNAPSHOTS[_featureId];
        if (snapshot !== undefined) {
          return Promise.resolve(snapshot);
        }
      }
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
      if (scene === 'aftercare') {
        return Promise.resolve(AFTERCARE_FEATURE_SNAPSHOT);
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
              cursor: {
                total: RUN_SESSION_TRANSCRIPT.length,
                start: 0,
                end: RUN_SESSION_TRANSCRIPT.length,
              },
              messages: RUN_SESSION_TRANSCRIPT,
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
          inquireness: 'medium',
          models: [],
          useCurrentBranch: false,
        },
      } as CreationDefaults),
    pickCreationFiles: () => Promise.resolve({ paths: [] }),
    readClipboardImage: () => Promise.resolve({ paths: [] }),
    importDroppedCreationFiles: () => ({ paths: [] }),
    searchCreationFiles: (request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [],
        truncated: false,
        cancelled: false,
      }),
    cancelCreationFileSearch: () => Promise.resolve(false),
    loadLocalReviewDraft: () => Promise.resolve(null),
    saveLocalReviewDraft: () => Promise.reject(new Error('unused')),
    discardLocalReviewDraft: () => Promise.resolve(false),
    readReview: () => Promise.reject(new Error('unused')),
    openReview: () => Promise.reject(new Error('unused')),
    saveReview: () => Promise.reject(new Error('unused')),
    validateReview: () => Promise.reject(new Error('unused')),
    decideReview: () => Promise.reject(new Error('unused')),
    getFeatureConfig: () =>
      Promise.resolve({
        featureId: 'feat-demo',
        current: DEMO_FEATURE_CONFIG,
        defaults: DEMO_FEATURE_CONFIG,
        manualPublishAvailable: true,
      }),
    updateFeatureConfig: () => Promise.reject(new Error('unused')),
    getWorkspaceDefaults: () =>
      Promise.resolve({
        models: { planning: 'demo:planner' },
        inquireness: 'medium' as const,
        checkpoints: DEMO_FEATURE_CONFIG.checkpoints,
        pipeline: 'large',
        muteFeatureInput: false,
      }),
    updateWorkspaceDefaults: () => Promise.reject(new Error('unused')),
    getModelCatalogue: () =>
      Promise.resolve({
        providerOrder: ['demo'],
        providerModels: {
          demo: [
            { id: 'demo:planner', displayName: 'Demo Planner' },
            { id: 'demo:builder', displayName: 'Demo Builder' },
          ],
        },
        phaseDefaults: { planning: 'demo:planner' },
        phaseProviderModels: {},
      }),
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
      if (scene === 'aftercare') {
        return Promise.resolve({
          ...RUN_DETAIL,
          runNumber,
          artifactCount: 5,
          timing: { totalSeconds: 14700, byPhase: RUN_DETAIL.timing?.byPhase ?? {} },
          cost: { totalUsd: 95.18, byPhase: RUN_DETAIL.cost?.byPhase ?? {} },
        } as RunDetailView);
      }
      const summary = SEALED_RUNS.find((r) => r.runNumber === runNumber);
      // The active (non-sealed) run still carries per-phase timing/cost detail.
      if (summary === undefined) {
        return Promise.resolve({ ...RUN_DETAIL, runNumber } as RunDetailView);
      }
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
    getLivePreview: (featureId) =>
      Promise.resolve({
        featureId,
        activity: 'Inspecting the current implementation run',
        contextPercentage: 42,
        totalSeconds: 73,
        totalUsd: 0.12,
        session: {
          id: 'sess-impl-live',
          featureId,
          runNumber: 8,
          phase: 'Implement',
          kind: 'implement',
          status: 'running',
          startedAt: '2026-07-19T14:20:00Z',
          model: 'claude-sonnet-5',
          usage: {},
        },
        transcript: [
          {
            index: 0,
            role: 'assistant',
            type: 'tool_use',
            tool: 'Read',
            text: 'src/renderer/app.tsx',
          },
          {
            index: 1,
            role: 'assistant',
            type: 'tool_use',
            tool: 'Edit',
            redacted: true,
            fileChange: {
              path: 'src/renderer/app.tsx',
              operation: 'update',
              detail:
                '- const preview = usePolledPreview(featureId);\n+ const preview = useStreamedPreview(featureId);\n+ const transcript = preview?.transcript ?? [];',
            },
          },
          {
            index: 2,
            role: 'assistant',
            type: 'tool_use',
            tool: 'Write',
            redacted: true,
            fileChange: {
              path: 'src/renderer/preview/stream.ts',
              operation: 'write',
              detail:
                '+ export function useStreamedPreview(featureId: string) {\n+   const [preview, setPreview] = useState<LivePreviewView | null>(null);\n+   useEffect(() => subscribePreview(featureId, setPreview), [featureId]);\n+   return preview;\n+ }',
            },
          },
          {
            index: 3,
            role: 'assistant',
            type: 'text',
            text: 'I updated the live preview to stream the agent transcript directly into the cockpit.',
          },
        ],
      }),
    listRunArtifacts: ({ runNumber }) =>
      Promise.resolve({
        artifacts: RUN_ARTIFACTS.map((a) => ({ ...a, runNumber })),
      } as RunArtifactsListResult),
    listRunLogs: () =>
      Promise.resolve({
        logs: [
          {
            id: 'log-research',
            path: 'research/output.txt',
            size: ARTIFACT_CONTENT.length,
            modifiedAt: '2026-07-20T12:00:00Z',
          },
        ],
      }),
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
        text: '[implement] iteration 3 started\n[model] requesting review-comments cycle\n[implement] NEED_USER_INPUT: apply suggested fix?\n[cycle] review-comments paused on gate\n[recovery] orphan process detected (pid 412)\n[recovery] log excerpt available — Resume or Kill.',
        truncated: false,
      }),
    bulkPreview: () => Promise.resolve(BULK_PREVIEW_DATA),
    getUpdates: () => Promise.resolve(updateStateForScene(scene)),
    checkForUpdates: () => Promise.resolve(updateStateForScene(scene)),
    installUpdateWhenIdle: () =>
      Promise.resolve({
        ...updateStateForScene(scene),
        status: 'scheduled' as const,
        message: 'Update installation is scheduled for the next idle window.',
      }),
    installUpdateNow: () =>
      Promise.resolve({
        ...updateStateForScene(scene),
        status: 'installing' as const,
        message: 'Restarting to apply the verified update.',
      }),
    restartToUpdate: () =>
      Promise.resolve({
        ...updateStateForScene(scene),
        status: 'installing' as const,
        message: 'Restarting to apply the verified update.',
      }),
    getDiagnostics: () => Promise.resolve(diagnosticsSnapshotForScene(scene)),
    revealDiagnostics: () => Promise.resolve({ ok: true }),
    clearDiagnostics: () =>
      Promise.resolve({
        ...diagnosticsSnapshotForScene(scene),
        entries: [],
        crashes: [],
        retention: {
          ...diagnosticsSnapshotForScene(scene).retention,
          entryCount: 0,
          crashCount: 0,
          currentBytes: 0,
        },
      }),
    preflightCompletion: () => {
      const isPublishScene = scene === 'completion-publish';
      const isDeleteScene = scene === 'completion-delete';
      if (scene === 'aftercare') {
        return Promise.resolve({
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-aftercare-mock',
          canMarkDone: true,
          repos: [
            {
              repo: 'agentic-orchestrator',
              publishable: true,
              touched: true,
              status: 'already_published',
              prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
              baseBranch: 'main',
              branch: 'feature/configure-per-phase-effort-level',
              freshness: 'up_to_date',
            },
          ],
        });
      }
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
            path: 'desktop/src/renderer/src/features/completion/PublishModal.tsx',
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

function updateStateForScene(scene: string): UpdateState {
  if (scene === 'settings-updates-deb') {
    return {
      status: 'available',
      currentVersion: '0.1.0',
      targetVersion: '0.2.0',
      packageFormat: 'deb',
      signatureStatus: 'verified',
      checkedAt: '2026-07-20T10:00:00.000Z',
      nextCheckAt: '2026-07-20T16:00:00.000Z',
      releaseNotesUrl: 'https://github.com/doordash-oss/agentic-orchestrator/releases/tag/v0.2.0',
      message: 'A verified DEB update is available.',
      guidance: [
        'DEB installs are updated by the package manager, not by in-app replacement.',
        'Download the signed DEB and checksum from the GitHub release.',
        'Install with: sudo apt install ./agentico_0.2.0_amd64.deb',
      ],
    };
  }
  if (scene === 'update-passive-active' || scene === 'settings-install-now-confirm') {
    return {
      ...readyUpdateState(),
      activeWorkSummary: '1 workflow and AMA session are active.',
    };
  }
  return readyUpdateState();
}

function readyUpdateState(): UpdateState {
  return {
    status: 'ready',
    currentVersion: '0.1.0',
    targetVersion: '0.2.0',
    packageFormat: 'macos',
    signatureStatus: 'verified',
    checkedAt: '2026-07-20T10:00:00.000Z',
    nextCheckAt: '2026-07-20T16:00:00.000Z',
    releaseNotesUrl: 'https://github.com/doordash-oss/agentic-orchestrator/releases/tag/v0.2.0',
    message: 'A verified update is downloaded and ready to install.',
  };
}

function diagnosticsSnapshotForScene(_scene: string): DiagnosticsSnapshot {
  return {
    retention: {
      maxBytes: 25 * 1024 * 1024,
      maxAgeDays: 7,
      maxCrashRecords: 10,
      currentBytes: 4096,
      entryCount: 4,
      crashCount: 1,
    },
    entries: [
      {
        id: 'evt-update',
        time: '2026-07-20T10:06:00.000Z',
        source: 'update',
        level: 'info',
        message: 'Verified update metadata staged.',
        detail: 'v0.2.0 Agentico-mac-universal.dmg',
      },
      {
        id: 'evt-server',
        time: '2026-07-20T10:04:00.000Z',
        source: 'server',
        level: 'warn',
        message: 'Gateway retry scheduled with token redacted.',
      },
      {
        id: 'evt-electron',
        time: '2026-07-20T10:00:00.000Z',
        source: 'electron',
        level: 'info',
        message: 'Agentico desktop process started.',
      },
    ],
    crashes: [
      {
        id: 'crash-1',
        time: '2026-07-20T09:55:00.000Z',
        version: '0.1.0',
        platform: 'darwin',
        architecture: 'arm64',
        processRole: 'renderer',
        category: 'crashed',
        context: 'exitCode=9',
      },
    ],
  };
}

export function installMockApi(scene: string): {
  emitAppEvent: (event: AppEvent) => void;
  emitSessionOutput: (event: SessionOutputEvent) => void;
  sessionOutputListenerCount: () => number;
  setTheme: (theme: 'light' | 'dark') => void;
} {
  const appEventListeners = new Set<(event: AppEvent) => void>();
  const sessionOutputListeners = new Set<(event: SessionOutputEvent) => void>();
  const api = makeMockApi(scene, appEventListeners, sessionOutputListeners);
  Object.defineProperty(window, 'agentico', {
    value: api,
    writable: true,
    configurable: true,
  });

  return {
    emitAppEvent: (event) => {
      appEventListeners.forEach((l) => l(event));
    },
    emitSessionOutput: (event) => {
      sessionOutputListeners.forEach((listener) => listener(event));
    },
    sessionOutputListenerCount: () => sessionOutputListeners.size,
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
  AFTERCARE_FEATURE_SNAPSHOT,
  REWIND_PREVIEW,
  REWIND_RESULT,
};
