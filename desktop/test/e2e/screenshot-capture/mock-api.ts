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
import {
  applyServersPatch,
  applyShellPatch,
  CHAT_SESSION_ID,
  defaultAmaPrefs,
  defaultSettings,
  defaultSettingsWindowPrefs,
  type SettingsPaneId,
} from '../../../src/shared/ipc';

const DEMO_FEATURE_CONFIG = {
  models: { planning: 'demo:planner' },
  effort: { planning: 'high' as const },
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
  automaticReviewMode: 'default' as const,
};

const AUTOMATIC_REVIEW = {
  mode: 'default',
  enabled: true,
  source: 'global',
} as const;

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
    taskActivities: [],
    runningTaskCount: 0,
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
    taskActivities: [],
    runningTaskCount: 0,
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
    taskActivities: [],
    runningTaskCount: 0,
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
  taskActivities: [],
  runningTaskCount: 0,
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
  automaticReview: AUTOMATIC_REVIEW,
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

/** Fixtures for the Overview lanes capture: three in-flight runs and a
 * shipped ledger, exercising live, needs-you, and published treatments. */
function overviewLaneSnapshot(
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
    automaticReview: AUTOMATIC_REVIEW,
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

const OVERVIEW_LANE_FEATURES: FeatureSnapshot[] = [
  overviewLaneSnapshot(
    'updater-auto-1',
    'electron App auto-updater',
    'NeedUserInput',
    'Review',
    ['agentic-orchestrator'],
    '2026-07-23T09:00:00Z',
    2462,
  ),
  {
    ...overviewLaneSnapshot(
      'refactoring-parent-1',
      'Configure per-phase effort level',
      'Published',
      'Publish',
      ['agentic-orchestrator'],
      '2026-07-23T07:45:00Z',
      420_120,
      'large',
    ),
    activeChild: {
      id: 'f73d148b32f070a2',
      name: 'Slop removal pass',
      kind: 'refactor',
      displayToken: 'refactor:f73d148b32f070a2',
      displayState: 'Active — Implementing',
      pipeline: 'large',
      status: 'Implementing',
      relationshipState: 'active',
      startedAt: '2026-07-23T08:10:00Z',
      cost: { totalUsd: 4.2, byPhase: {} },
      integrationState: 'pending',
      attention: [],
      cleanupWarnings: [],
    },
  },
  {
    // Carries both a roadmap phase-of-total and an iteration number so the
    // sidebar's running-row sub-line reads "Implement · phase N/M ·
    // iteration K" beside the pip row (see the sidebar screenshot capture).
    ...overviewLaneSnapshot(
      'readme-italian-1',
      'translate README to Italian',
      'Implementing',
      'Implement',
      ['agentic-orchestrator'],
      '2026-07-23T08:30:00Z',
      760,
      'large',
    ),
    currentRoadmapPhase: 3,
    totalRoadmapPhases: 5,
    currentIteration: 2,
  },
  overviewLaneSnapshot(
    'taulu-ttl-1',
    'Taulu TTL compaction',
    'CodeReady',
    'Publish',
    ['taulu'],
    '2026-07-23T08:00:00Z',
    5940,
  ),
  overviewLaneSnapshot(
    'pub-electron-app',
    'electron APP for agentic Orchestrator',
    'Published',
    'Publish',
    ['agentic-orchestrator'],
    '2026-07-22T10:00:00Z',
    5220,
  ),
  overviewLaneSnapshot(
    'pub-taulu-mv',
    'Taulu materialized views',
    'Published',
    'Publish',
    ['taulu'],
    '2026-07-21T10:00:00Z',
    3180,
  ),
  overviewLaneSnapshot(
    'pub-smart-zone',
    'The Smart Zone',
    'Published',
    'Publish',
    ['agentic-orchestrator'],
    '2026-07-20T10:00:00Z',
    1890,
  ),
  overviewLaneSnapshot(
    'pub-prod-fallback',
    'Prod tenant fallback on READ',
    'Published',
    'Publish',
    ['dev-console', 'taulu'],
    '2026-07-19T10:00:00Z',
    4410,
  ),
  overviewLaneSnapshot(
    'pub-static-shard',
    'Taulu Static Sharding',
    'Published',
    'Publish',
    ['taulu'],
    '2026-07-18T10:00:00Z',
    2020,
  ),
  overviewLaneSnapshot(
    'done-signal-lab-onboarding',
    'Signal Lab onboarding checklist',
    'Done',
    'Publish',
    ['signal-lab'],
    '2026-07-15T10:00:00Z',
    9600,
  ),
];

/**
 * The palette-evidence catalogue: a mid-run feature whose authoritative action
 * catalogue enables some verbs, disables others with real reasons, and never
 * offers a few at all — so the Feature group photographs as the mixed state a
 * person actually sees, not a uniformly enabled list.
 */
const COMMAND_PALETTE_FEATURE_SNAPSHOT: FeatureSnapshot = {
  ...FEATURE_SNAPSHOT,
  actions: [
    {
      id: 'start',
      enabled: false,
      disabledReasons: [{ code: 'already_running', message: 'The feature is already running.' }],
    },
    { id: 'pause-stop', enabled: true, disabledReasons: [] },
    { id: 'rewind', enabled: true, disabledReasons: [] },
    { id: 'restart', enabled: true, disabledReasons: [] },
    {
      id: 'resume',
      enabled: false,
      disabledReasons: [{ code: 'not_paused', message: 'The run is not paused at a gate.' }],
    },
    {
      id: 'retry',
      enabled: false,
      disabledReasons: [{ code: 'no_failure', message: 'Nothing has failed to retry.' }],
    },
    {
      id: 'publish',
      enabled: false,
      disabledReasons: [{ code: 'run_active', message: 'Review has not finished yet.' }],
    },
    {
      id: 'merge',
      enabled: false,
      disabledReasons: [{ code: 'not_published', message: 'Publish the feature first.' }],
    },
    {
      id: 'mark-done',
      enabled: false,
      disabledReasons: [{ code: 'run_active', message: 'The run is still in flight.' }],
    },
    {
      id: 'cleanup',
      enabled: false,
      disabledReasons: [{ code: 'run_active', message: 'The run is still in flight.' }],
    },
    {
      id: 'refactor',
      enabled: false,
      disabledReasons: [{ code: 'run_active', message: 'Wait for the run to come to rest.' }],
    },
    {
      id: 'review-feedback',
      enabled: false,
      disabledReasons: [{ code: 'no_pull_request', message: 'No pull request is open yet.' }],
    },
    { id: 'delete', enabled: true, disabledReasons: [] },
  ],
};

const OVERVIEW_LANE_SUMMARY: FeatureSummaryView[] = OVERVIEW_LANE_FEATURES.map((feature) => ({
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

const OVERVIEW_LANE_SNAPSHOTS: Record<string, FeatureSnapshot> = Object.fromEntries(
  OVERVIEW_LANE_FEATURES.map((feature) => [feature.id, feature]),
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
  automaticReview: AUTOMATIC_REVIEW,
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
    },
    {
      name: 'orchestrator-core',
      publishable: true,
      touched: false,
      prUrl: 'https://github.com/example/orchestrator-core/pull/18',
      freshness: 'local changes',
    },
  ],
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
      id: 'refactor',
      enabled: true,
      disabledReasons: [],
      inputs: [
        { name: 'repo' },
        { name: 'prompt' },
        { name: 'pipeline', options: ['medium', 'large', 'moonshot'] },
      ],
    },
    { id: 'review-feedback', enabled: true, disabledReasons: [] },
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
    },
  ],
};

/**
 * Published aftercare with the verification items still carried. The server
 * clears them the moment harness verification finishes, so this fixture
 * exercises the carried-items branch of the receipt's Verification row; the
 * `aftercare-bare` fixture below exercises the (commoner) cleared case.
 */
const AFTERCARE_VERIFIED_SNAPSHOT: FeatureSnapshot = {
  ...AFTERCARE_FEATURE_SNAPSHOT,
  verificationItems: [
    { name: 'lint', state: 'passed' },
    { name: 'typecheck', state: 'passed' },
    { name: 'unit', state: 'passed' },
    { name: 'e2e', state: 'passed' },
  ],
};

/** Published with nothing to show: no pull request, no reachable diff. */
const AFTERCARE_BARE_SNAPSHOT: FeatureSnapshot = {
  ...AFTERCARE_FEATURE_SNAPSHOT,
  name: 'Taulu TTL compaction',
  slug: 'taulu-ttl-compaction',
  repos: ['taulu'],
  repoStatus: [{ name: 'taulu', publishable: false, touched: true, freshness: 'local only' }],
};

/** Code-ready with undelivered commits and Mark done blocked by the preflight. */
const AFTERCARE_CODEREADY_SNAPSHOT: FeatureSnapshot = {
  ...AFTERCARE_FEATURE_SNAPSHOT,
  status: 'CodeReady',
  currentPhase: 'Review',
  actions: [
    { id: 'rebase', enabled: true, disabledReasons: [] },
    { id: 'refactor', enabled: true, disabledReasons: [] },
    { id: 'review-feedback', enabled: true, disabledReasons: [] },
    // The pull request already exists, so publishing again is the delivery of
    // the new commits — the runway's `Publish new commits` row.
    {
      id: 'publish',
      enabled: false,
      disabledReasons: [{ code: 'already_published', message: 'Already published.' }],
    },
    { id: 'cleanup', enabled: true, disabledReasons: [] },
    { id: 'mark-done', enabled: true, disabledReasons: [] },
  ],
};

const REFACTOR_PASS_CHILD_ID = 'f73d148b32f070a2';

/** Ready-to-start refactor child for the refactor-pass workspace scene. */
const REFACTOR_PASS_CHILD_SNAPSHOT: FeatureSnapshot = {
  ...AFTERCARE_FEATURE_SNAPSHOT,
  id: REFACTOR_PASS_CHILD_ID,
  name: 'Slop removal pass',
  slug: 'slop-removal-pass',
  status: 'Created',
  currentPhase: 'Knowledge Base',
  pipeline: 'large',
  description: 'Remove the redundancy the implementation run left behind.',
  activeRun: 1,
  parentId: AFTERCARE_FEATURE_SNAPSHOT.id,
  parentKind: 'refactor',
  active: true,
  setupComplete: true,
  setup: {
    status: 'done',
    attempt: 1,
    tasks: [
      {
        key: 'worktree:agentic-orchestrator',
        kind: 'worktree',
        label: 'Create worktree',
        repo: 'agentic-orchestrator',
        branch: 'feature/slop-removal-pass',
        status: 'done',
        attempt: 1,
      },
    ],
  },
  timing: { totalSeconds: 0 },
  repoStatus: [{ name: 'agentic-orchestrator', publishable: true, freshness: 'in sync' }],
  actions: [
    { id: 'start', enabled: true, disabledReasons: [] },
    {
      id: 'pause-stop',
      enabled: false,
      disabledReasons: [{ code: 'not_running', message: 'The pass has not started yet.' }],
    },
    {
      id: 'discard',
      enabled: true,
      disabledReasons: [],
      impactPreview: {
        kind: 'child_discard',
        subject: { id: REFACTOR_PASS_CHILD_ID, name: 'Slop removal pass' },
        categories: [
          { key: 'sessions', label: 'Sessions stopped', items: [] },
          {
            key: 'worktrees',
            label: 'Disposable worktrees removed',
            items: ['agentic-orchestrator … slop-removal-pass'],
          },
          {
            key: 'branches',
            label: 'Ephemeral branches removed',
            items: ['feature/slop-removal-pass (repo agentic-orchestrator)'],
          },
        ],
        retained: ['Review configuration retained', 'Pass becomes immutable Discarded history'],
      },
    },
  ],
};

const REFACTOR_PASS_PARENT_SNAPSHOT: FeatureSnapshot = {
  ...AFTERCARE_FEATURE_SNAPSHOT,
  actions: AFTERCARE_FEATURE_SNAPSHOT.actions.map((action) => ({
    ...action,
    enabled: false,
    disabledReasons: [
      {
        code: 'parent_locked',
        message: 'parent mutations are locked while a child is active',
      },
    ],
  })),
  activeChild: {
    id: REFACTOR_PASS_CHILD_ID,
    name: 'Slop removal pass',
    kind: 'refactor',
    displayToken: `refactor:${REFACTOR_PASS_CHILD_ID}`,
    displayState: 'Active — Created',
    pipeline: 'large',
    status: 'Created',
    relationshipState: 'active',
    startedAt: '2026-07-31T22:41:00Z',
    cost: { totalUsd: 0, byPhase: {} },
    integrationState: 'pending',
    attention: [],
    cleanupWarnings: [],
  },
  childHistory: [
    {
      id: 'a1b2c3d4e5f60718',
      name: 'Tighten IPC schemas',
      kind: 'refactor',
      displayToken: 'refactor:a1b2c3d4e5f60718',
      displayState: 'Closed — Completed',
      pipeline: 'medium',
      status: 'Done',
      relationshipState: 'completed',
      outcome: 'completed',
      startedAt: '2026-07-27T09:12:00Z',
      closedAt: '2026-07-27T14:03:00Z',
      cost: { totalUsd: 12.4, byPhase: {} },
      integrationState: 'merged',
      attention: [],
      cleanupWarnings: [],
      diffSummary:
        'Repository: agentic-orchestrator\n 14 files changed, 220 insertions(+), 412 deletions(-)',
    },
  ],
};

const REVIEW_FEEDBACK_PASS_CHILD_ID = 'c41e77b90ad35216';

/**
 * A review-feedback pass instead of a refactor pass: same custody strip, but the
 * selected reviewer comments sit right under it, so one frame shows both the
 * station eyebrows and the comment-type chips.
 */
const REVIEW_FEEDBACK_PASS_CHILD_SNAPSHOT: FeatureSnapshot = {
  ...REFACTOR_PASS_CHILD_SNAPSHOT,
  id: REVIEW_FEEDBACK_PASS_CHILD_ID,
  name: 'Address review feedback',
  slug: 'address-review-feedback',
  description: 'Answer the reviewer comments left on pull request 107.',
  parentKind: 'review-feedback',
  setup: {
    ...REFACTOR_PASS_CHILD_SNAPSHOT.setup!,
    tasks: REFACTOR_PASS_CHILD_SNAPSHOT.setup!.tasks.map((task) => ({
      ...task,
      branch: 'feature/address-review-feedback',
    })),
  },
  reviewFeedback: [
    {
      repo: 'agentic-orchestrator',
      id: 4181,
      type: 'review' as const,
      path: 'internal/orchestrator/phase.go',
      line: 214,
      author: 'dana-reviewer',
      body: 'The retry budget is read twice here — hoist it above the loop so both branches agree.',
    },
    {
      repo: 'agentic-orchestrator',
      id: 4182,
      type: 'issue' as const,
      author: 'dana-reviewer',
      body: 'Packaged runs still write the evidence bundle under the old path on Linux.',
    },
    {
      repo: 'agentic-orchestrator',
      id: 4183,
      type: 'review_body' as const,
      author: 'sam-maintainer',
      body: 'Solid pass overall. Two things to settle before merge, both noted inline.',
    },
  ],
};

const REVIEW_FEEDBACK_PASS_PARENT_SNAPSHOT: FeatureSnapshot = {
  ...REFACTOR_PASS_PARENT_SNAPSHOT,
  activeChild: {
    ...REFACTOR_PASS_PARENT_SNAPSHOT.activeChild!,
    id: REVIEW_FEEDBACK_PASS_CHILD_ID,
    name: 'Address review feedback',
    kind: 'review-feedback',
    displayToken: `review-feedback:${REVIEW_FEEDBACK_PASS_CHILD_ID}`,
  },
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

/** The mock server's identity key; per-server shell prefs are keyed on it. */
const SERVER_KEY = 'a'.repeat(64);

const CONNECTION_STATE: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected to runtime.',
  ownership: 'app-owned',
  kind: 'local',
  serverKey: SERVER_KEY,
};

/** Mid-connect state for the connection-shell capture: two of the six
 * lifecycle stages (Resolve, Discover) are behind it, `connect` is current. */
const CONNECTION_STATE_MID_CONNECT: ConnectionState = {
  status: 'attaching',
  stage: 'connect',
  detail: 'Attaching to the resolved runtime…',
  ownership: 'none',
};

/** The image the AMA panel scene attaches, matching the mock's chip. */
const AMA_ATTACHMENT_PATH = '/Users/you/Desktop/cockpit-poll.png';

const AMA_TRANSCRIPT: SessionTranscript = {
  sessionId: CHAT_SESSION_ID,
  cursor: { total: 4, start: 0, end: 4 },
  messages: [
    {
      index: 0,
      role: 'user',
      type: 'text',
      text: 'Which features still touch the old polled preview?',
    },
    {
      index: 1,
      role: 'assistant',
      type: 'text',
      text: 'Two, both in agentic-orchestrator: ArchiveMode.tsx reads usePolledPreview for sealed runs, and RefactorPassWorkspace.tsx uses the same hook for live passes. The run you have open is replacing the shared hook, so both will need the new subscription. Neither is in its plan.',
    },
    {
      index: 2,
      role: 'user',
      type: 'text',
      text: "Add that to the current run's plan?",
    },
    {
      index: 3,
      role: 'assistant',
      type: 'text',
      text: "I can't change a plan mid-phase. Two routes: answer the next phase-plan checkpoint with these two files, or start a refactor pass after publish.",
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

/** Scenes that need the singleton AMA chat session to exist. */
function hasChatSession(scene: string): boolean {
  return isBackgroundScene(scene) || scene === 'ama-panel';
}

export const FEATURE_QUESTION_ITEM = {
  kind: 'questions',
  id: 'ask-feature-direction',
  featureId: 'abcd1234ef567890',
  sessionId: 'sess-impl-03',
  phase: 'Design',
  waitingSince: '2026-07-29T16:00:00Z',
  questions: [
    {
      key: 'For the agentic-orchestrator project, which overall direction should guide the next major investment while preserving the reliability of the existing orchestration engine, keeping the desktop experience understandable during long-running agent work, and giving maintainers enough evidence to distinguish a genuinely useful workflow improvement from a visually attractive change that adds operational complexity without improving completion quality? The answer should also account for teams adopting the tool gradually, repositories with different verification costs, and operators who need to recover interrupted work without reconstructing hidden runtime context.',
      header: 'Project direction',
      multiSelect: false,
      options: [
        {
          label: 'Harden the review pipeline (Recommended)',
          description:
            'Double down on the recently added multi-axis review workflow by making evidence collection more consistent, tightening the handoff between implementation and review, and ensuring that a partially successful verification run leaves enough structured context for the next agent to resume without repeating expensive work. This direction would also clarify which findings block progression, which can be acknowledged with evidence, and how a resumed reviewer distinguishes newly introduced regressions from findings that were already evaluated in an earlier iteration.',
          confidence: 0.86,
        },
        {
          label: 'Build user-facing features',
          description:
            'Shift focus toward capabilities that operators can see and control directly in the desktop application, including clearer intervention points, stronger progress communication, and focused workflows that turn complex runtime state into decisions a person can make quickly without learning the internal protocol.',
          confidence: 0.68,
        },
        {
          label: 'Invest in runtime resilience',
          description:
            'Prioritize process supervision, recovery, and deterministic state convergence so interrupted work can be diagnosed and resumed safely across app restarts, provider failures, and repository-level conflicts while preserving the exact evidence needed for a trustworthy audit trail.',
          confidence: 0.73,
        },
        {
          label: 'Simplify the platform surface',
          description:
            'Consolidate overlapping controls and retire low-value presentation layers so the product exposes fewer concepts, keeps the strongest workflows prominent, and reduces the amount of renderer and orchestration code maintainers must reason about during future changes.',
          confidence: 0.59,
        },
      ],
    },
  ],
} satisfies AttentionSnapshot['items'][number];
const FEATURE_QUESTION = FEATURE_QUESTION_ITEM.questions[0]!;

/**
 * The compact ask the Bench mock shows: three short options with real
 * descriptions. `FEATURE_QUESTION_ITEM` above is deliberately extreme (a
 * paragraph-length prompt and four paragraph-length options) and exists to
 * prove the layout survives overflow; this one is what the surface looks like
 * in ordinary use, so design evidence is judged against it.
 */
export const FEATURE_QUESTION_BENCH_ITEM = {
  kind: 'questions',
  id: 'ask-existing-translation',
  featureId: 'abcd1234ef567890',
  sessionId: 'sess-impl-03',
  phase: 'Implement',
  waitingSince: new Date(Date.now() - 6 * 60_000).toISOString(),
  questions: [
    {
      key: 'There is already an Italian README at docs/it/README.md. What should happen to it?',
      header: 'Existing translation',
      multiSelect: false,
      options: [
        {
          label: 'Replace it with the new translation',
          description: 'Overwrites the file and keeps its path, so existing links stay valid.',
          confidence: 0.88,
        },
        {
          label: 'Keep both and cross-link them',
          description:
            'Leaves the old file in place and adds a banner at the top of each pointing to the other.',
          confidence: 0.54,
        },
        {
          label: 'Archive it as README.legacy.md',
          description:
            'Preserves the previous translation for reference without shadowing the new one.',
          confidence: 0.41,
        },
      ],
    },
  ],
} satisfies AttentionSnapshot['items'][number];
const FEATURE_QUESTION_BENCH = FEATURE_QUESTION_BENCH_ITEM.questions[0]!;

/**
 * The raw question text the transcript carries, so the turn's suppression
 * matches whichever question the scene actually has pending.
 */
function sceneQuestion(scene: string): typeof FEATURE_QUESTION | typeof FEATURE_QUESTION_BENCH {
  return scene === 'feature-question-bench' ? FEATURE_QUESTION_BENCH : FEATURE_QUESTION;
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

/**
 * The mixed inbox the `attention-popover` scene shows, so the bell carries a
 * real count and the popover list holds several distinct kinds at once:
 *
 * - an ownerless verification gate, which is the only kind that expands inline
 *   inside the popover (owned items jump to their feature instead). It carries
 *   the structured verification branch — blockers plus exactly the WAIVE /
 *   RETRY_AFTER_AUTH pair against a single question — so the expanded row shows
 *   the decision radios rather than a free-text answer box.
 * - a feature-owned permission and a feature-owned review, so the list is
 *   genuinely mixed rather than three copies of one row.
 *
 * The gate deliberately has no `featureId`: that is what "ownerless" means to
 * `attentionOwnerFeatureId`, and the schema's required `featureId` is why the
 * literal is cast rather than annotated.
 */
const ATTENTION_POPOVER_ITEMS: AttentionSnapshot['items'] = [
  {
    kind: 'gate',
    id: 'verification-gate-signal-lab',
    waitingSince: new Date(Date.now() - 9 * 60_000).toISOString(),
    scope: 'repo',
    repoName: 'signal-lab',
    iteration: 3,
    // One blocker, tersely worded: the decision radios are the point of this
    // evidence, and the popover is only 34rem tall before it scrolls.
    summary: 'Verification stopped: a check needs credentials this run does not hold.',
    questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
    verification: {
      blockers: [
        {
          itemId: 'deploy-smoke',
          name: 'Deployment smoke test',
          repoName: 'signal-lab',
          command: 'npm run smoke -- --stage=canary',
          reason: 'The canary deploy token expired.',
          capabilities: ['network', 'deploy-token'],
          remediation: 'Refresh the token, then retry.',
        },
      ],
      allowedActions: ['RETRY_AFTER_AUTH', 'WAIVE'],
    },
  } as AttentionSnapshot['items'][number],
  {
    kind: 'permission',
    id: 'perm-rewrite-preview',
    featureId: 'abcd1234ef567890',
    sessionId: 'sess-impl-03',
    phase: 'Implement',
    toolName: 'Bash',
    summary: 'Run the bounded verification command for the rewind preview.',
    input: { command: 'npm run check -- --scope=rewind' },
    waitingSince: new Date(Date.now() - 4 * 60_000).toISOString(),
  },
  {
    kind: 'review',
    id: 'review-craft-run-8',
    featureId: 'abcd1234ef567890',
    waitingSince: new Date(Date.now() - 2 * 60_000).toISOString(),
    reviewKind: 'Craft',
    phase: 'Review',
  },
];

/**
 * The `update-popover` scene's inbox. That capture is about the update popover
 * and the footer dot, but it also shows the bell beside the Overview "Waiting on
 * you" lane, and those two read from different sources — the lane from the
 * feature summaries, the badge from this snapshot. One gate owned by the lane's
 * single waiting feature (`updater-auto-1`, whose display state is
 * NeedUserInput) keeps the two agreeing at 1 rather than photographing a zero
 * badge beside a populated lane.
 */
const UPDATE_POPOVER_ITEMS: AttentionSnapshot['items'] = [
  {
    kind: 'gate',
    id: 'gate-updater-auto-1',
    featureId: 'updater-auto-1',
    waitingSince: new Date(Date.now() - 6 * 60_000).toISOString(),
    scope: 'repo',
    repoName: 'agentic-orchestrator',
    iteration: 1,
    summary: 'The updater needs a signing identity before it can publish.',
    questions: [{ index: 1, prompt: 'Which signing identity should it use?', answer: '' }],
  },
];

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
    // The AMA scenes open the panel from the persisted preference, exactly as
    // the app does, rather than routing it open from the scene.
    ama: { ...defaultAmaPrefs(), drawer: scene === 'ama-panel' ? 'expanded' : 'compact' },
    // The settings scenes open on their pane through the same persisted
    // preference the app restores on open, rather than being routed there.
    settingsWindow: { ...defaultSettingsWindowPrefs(), pane: settingsScenePane(scene) },
    shell: {
      featureByServer:
        scene === 'overview-lanes' ||
        scene === 'overview-empty' ||
        scene === 'command-palette-overview' ||
        // The update popover is evidenced over Overview; the attention popover
        // is evidenced over a real feature cockpit, so it keeps the default.
        scene === 'update-popover' ||
        scene.startsWith('creation-sheet')
          ? {}
          : { [SERVER_KEY]: 'abcd1234ef567890' },
      sidebarCollapsed: false,
    },
  };

  return {
    platform: 'darwin',
    // Every settings scene mounts the Settings window root; the other scenes
    // mount the main app root, exactly as the real entry point chooses.
    windowPurpose: scene.startsWith('settings-') ? 'settings' : 'main',
    getConnectionStatus: () =>
      Promise.resolve(
        scene === 'connection-shell' ? CONNECTION_STATE_MID_CONNECT : CONNECTION_STATE,
      ),
    retryConnection: () => Promise.resolve(CONNECTION_STATE),
    restartConnection: () => Promise.resolve(CONNECTION_STATE),
    chooseConnectionServer: () => Promise.resolve(CONNECTION_STATE),
    switchConnectionServer: () => Promise.resolve(CONNECTION_STATE),
    listServers: () => Promise.resolve({ rows: [] }),
    probeServers: () => Promise.resolve({ rows: [] }),
    addRemoteServer: () => Promise.reject(new Error('addRemoteServer not available in capture')),
    removeServer: () => Promise.reject(new Error('removeServer not available in capture')),
    getServerTokenStatus: () =>
      Promise.reject(new Error('getServerTokenStatus not available in capture')),
    onServersChanged: () => () => {},
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
      const { servers, shell, ...rest } = patch;
      currentSettings = { ...currentSettings, ...rest };
      if (servers !== undefined) {
        currentSettings = {
          ...currentSettings,
          servers: applyServersPatch(currentSettings.servers, servers),
        };
      }
      if (shell !== undefined) {
        currentSettings = {
          ...currentSettings,
          shell: applyShellPatch(currentSettings.shell, shell),
        };
      }
      return Promise.resolve(currentSettings);
    },
    openSettingsWindow: () => Promise.resolve({ opened: true }),
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
      Promise.resolve(
        scene === 'overview-lanes' ||
          scene === 'update-popover' ||
          scene === 'command-palette-overview' ||
          scene.startsWith('creation-sheet')
          ? OVERVIEW_LANE_SUMMARY
          : scene === 'overview-empty'
            ? []
            : FEATURE_SUMMARY,
      ),
    getFeature: (_featureId: string) => {
      if (scene === 'command-palette-feature') {
        return Promise.resolve(COMMAND_PALETTE_FEATURE_SNAPSHOT);
      }
      if (
        scene === 'overview-lanes' ||
        scene === 'update-popover' ||
        scene === 'command-palette-overview' ||
        scene.startsWith('creation-sheet')
      ) {
        const snapshot = OVERVIEW_LANE_SNAPSHOTS[_featureId];
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
      if (scene === 'repo-instrument' || scene === 'refactor-launch') {
        return Promise.resolve(CYCLES_FEATURE_SNAPSHOT);
      }
      // The two hold surfaces get run statuses that make the rail agree with
      // what the surface says: a question pending on a still-running phase
      // reads `Waiting Nm`, a gate reads `Paused Nm`.
      if (scene === 'feature-question-bench') {
        return Promise.resolve({ ...FEATURE_SNAPSHOT, status: 'Implementing' });
      }
      if (scene === 'gate-sheet-plain' || scene === 'gate-sheet-verification') {
        return Promise.resolve({ ...FEATURE_SNAPSHOT, status: 'NeedUserInput' });
      }
      if (scene === 'aftercare-verified' || scene === 'aftercare-inspector') {
        return Promise.resolve(AFTERCARE_VERIFIED_SNAPSHOT);
      }
      if (scene === 'aftercare-bare') {
        return Promise.resolve(AFTERCARE_BARE_SNAPSHOT);
      }
      if (scene === 'aftercare-codeready') {
        return Promise.resolve(AFTERCARE_CODEREADY_SNAPSHOT);
      }
      if (scene.startsWith('aftercare')) {
        return Promise.resolve(AFTERCARE_FEATURE_SNAPSHOT);
      }
      if (scene === 'refactor-pass') {
        return Promise.resolve(
          _featureId === REFACTOR_PASS_CHILD_ID
            ? REFACTOR_PASS_CHILD_SNAPSHOT
            : REFACTOR_PASS_PARENT_SNAPSHOT,
        );
      }
      if (scene === 'refactor-pass-review') {
        return Promise.resolve(
          _featureId === REVIEW_FEEDBACK_PASS_CHILD_ID
            ? REVIEW_FEEDBACK_PASS_CHILD_SNAPSHOT
            : REVIEW_FEEDBACK_PASS_PARENT_SNAPSHOT,
        );
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
          : scene === 'attention-popover'
            ? ATTENTION_POPOVER_ITEMS
            : scene === 'update-popover'
              ? UPDATE_POPOVER_ITEMS
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
      Promise.resolve(hasChatSession(scene) ? [CHAT_SESSION, ...SESSIONS] : SESSIONS),
    getSession: (sessionId) => {
      const summary = (hasChatSession(scene) ? [CHAT_SESSION, ...SESSIONS] : SESSIONS).find(
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
          : scene.startsWith('feature-question')
            ? ({
                sessionId,
                cursor: {
                  total: RUN_SESSION_TRANSCRIPT.length + 1,
                  start: 0,
                  end: RUN_SESSION_TRANSCRIPT.length + 1,
                },
                messages: [
                  ...RUN_SESSION_TRANSCRIPT,
                  {
                    index: RUN_SESSION_TRANSCRIPT.length,
                    role: 'assistant',
                    type: 'text',
                    text: [
                      sceneQuestion(scene).key,
                      '',
                      ...sceneQuestion(scene).options.map(
                        (option, index) =>
                          `${index + 1}. ${option.label}${index === 0 && !/\(Recommended\)$/i.test(option.label) ? ' (Recommended)' : ''} [confidence: ${option.confidence?.toFixed(2)}]`,
                      ),
                    ].join('\n'),
                  },
                ],
              } as SessionTranscript)
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
          effort: [],
          useCurrentBranch: false,
        },
      } as CreationDefaults),
    // The creation-sheet scenes need real-looking attachment chips; every
    // other scene keeps the picker inert.
    pickCreationFiles: (kind) =>
      Promise.resolve({
        paths: scene.startsWith('creation-sheet')
          ? kind === 'image'
            ? ['/work/space/brief/reference-layout.png']
            : ['/work/space/brief/acceptance-notes.md']
          : [],
      }),
    readClipboardImage: () =>
      Promise.resolve({ paths: scene === 'ama-panel' ? [AMA_ATTACHMENT_PATH] : [] }),
    importDroppedCreationFiles: () => ({
      paths: scene === 'ama-panel' ? [AMA_ATTACHMENT_PATH] : [],
    }),
    searchCreationFiles: (request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: scene.startsWith('creation-sheet')
          ? [{ repoKey: 'signal-lab', path: 'docs/style-guide.md' }]
          : [],
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
        effort: { planning: 'high' },
        inquireness: 'medium' as const,
        checkpoints: DEMO_FEATURE_CONFIG.checkpoints,
        pipeline: 'large',
        muteFeatureInput: false,
        automaticReviewEnabled: true,
      }),
    updateWorkspaceDefaults: () => Promise.reject(new Error('unused')),
    getModelCatalogue: () =>
      Promise.resolve({
        providerOrder: ['demo'],
        providerModels: {
          demo: [
            {
              id: 'demo:planner',
              displayName: 'Demo Planner',
              effortCapabilities: ['low', 'medium', 'high'],
            },
            {
              id: 'demo:builder',
              displayName: 'Demo Builder',
              effortCapabilities: ['low', 'medium', 'high', 'max'],
            },
          ],
        },
        phaseDefaults: { planning: 'demo:planner' },
        phaseProviderModels: {},
      }),
    refreshProviderModels: () =>
      Promise.resolve({
        readiness: READY_SNAPSHOT,
        catalogue: {
          providerOrder: ['demo'],
          providerModels: {
            demo: [{ id: 'demo:planner', displayName: 'Demo Planner' }],
          },
          phaseDefaults: { planning: 'demo:planner' },
          phaseProviderModels: {},
        },
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
      if (scene.startsWith('aftercare')) {
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
          taskActivities: [
            {
              taskId: 'task-live-review',
              description: 'Inspect renderer state',
              state: 'running',
              lastToolName: 'Read',
              lastPath: 'src/renderer/app.tsx',
              startedAt: '2026-07-19T14:20:05Z',
              updatedAt: '2026-07-19T14:20:30Z',
            },
          ],
          runningTaskCount: 1,
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
    launchRebaseChild: () => {
      if (scene === 'aftercare-rebase-up-to-date') {
        return Promise.reject(
          new Error(
            'rebase_already_up_to_date: Every repository is already up to date with its target branch. Nothing to merge.',
          ),
        );
      }
      return Promise.resolve({
        childId: 'child1234ef567890',
        parentId: 'abcd1234ef567890',
        result: 'created',
      });
    },
    launchRefactorChild: () =>
      Promise.resolve({
        childId: 'child1234ef567890',
        parentId: 'abcd1234ef567890',
        result: 'created',
      }),
    discardRefactorChild: () =>
      Promise.resolve({
        childId: 'child1234ef567890',
        result: 'discarded',
        status: 'completed' as const,
      }),
    deleteFeatureCascade: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        operationId: 'delete-1',
        status: 'completed' as const,
        diagnostics: [],
      }),
    fetchReviewFeedback: () =>
      Promise.resolve({
        featureId: 'abcd1234ef567890',
        repos: scene.startsWith('aftercare')
          ? [
              {
                repo: 'agentic-orchestrator',
                prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
                comments: [
                  { repo: 'agentic-orchestrator', id: 1, type: 'review' as const },
                  { repo: 'agentic-orchestrator', id: 2, type: 'issue' as const },
                ],
              },
            ]
          : [],
      }),
    launchReviewFeedbackChild: () =>
      Promise.resolve({
        childId: 'child1234ef567890',
        parentId: 'abcd1234ef567890',
        result: 'created',
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
        text: '[implement] iteration 3 started\n[model] requesting rebase cycle\n[verification] capability gate requires a decision\n[cycle] rebase paused on gate\n[recovery] orphan process detected (pid 412)\n[recovery] log excerpt available — Resume or Kill.',
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
    publishUiState: () => Promise.resolve({ accepted: true }),
    preflightCompletion: () => {
      const isPublishScene = scene === 'completion-publish';
      const isDeleteScene = scene === 'completion-delete';
      if (scene === 'aftercare-unpublished') {
        return Promise.resolve({
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-aftercare-mock',
          canMarkDone: true,
          repos: [
            {
              repo: 'agentic-orchestrator',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
              baseBranch: 'main',
              branch: 'feature/configure-per-phase-effort-level',
              freshness: 'up_to_date',
              pendingCommits: 3,
              pendingDirty: false,
              pushMode: 'fast_forward',
            },
          ],
        });
      }
      if (scene === 'aftercare-codeready') {
        return Promise.resolve({
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-aftercare-mock',
          canMarkDone: false,
          markDoneBlocker: 'agentic-orchestrator has 3 commits that are not published yet',
          repos: [
            {
              repo: 'agentic-orchestrator',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
              baseBranch: 'main',
              branch: 'feature/configure-per-phase-effort-level',
              freshness: 'up_to_date',
              pendingCommits: 3,
              pendingDirty: false,
              pushMode: 'fast_forward',
            },
          ],
        });
      }
      if (scene === 'aftercare-bare') {
        return Promise.resolve({
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-aftercare-mock',
          canMarkDone: true,
          repos: [{ repo: 'taulu', publishable: false, touched: true, status: 'completed' }],
        });
      }
      if (
        scene === 'aftercare' ||
        scene === 'aftercare-verified' ||
        scene === 'aftercare-inspector' ||
        scene === 'aftercare-rebase-up-to-date'
      ) {
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
      // Worktrees reclaimed: the aftercare receipt must omit its Changes row.
      if (scene === 'aftercare-bare') {
        return Promise.reject(new Error('no_worktree: the feature worktrees have been cleaned'));
      }
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
    writeClipboardText: () => Promise.resolve({ ok: true }),
    onAppEvent: (listener) => {
      appEventListeners.add(listener);
      return () => appEventListeners.delete(listener);
    },
  };
}

/**
 * The pane each settings scene opens on. Every scene whose id names a pane
 * uses it directly; the update-flavoured scenes (`settings-updates-ready`,
 * `settings-updates-deb`, `settings-install-now-confirm`) all photograph the
 * Updates pane with different update states.
 */
function settingsScenePane(scene: string): SettingsPaneId {
  if (scene === 'settings-diagnostics') return 'diagnostics';
  if (scene === 'settings-appearance') return 'appearance';
  if (scene === 'settings-workspace-roots') return 'workspace-roots';
  if (scene === 'settings-providers') return 'providers';
  return 'updates';
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
  // `update-popover` needs the active-work summary: it is what makes the popover
  // offer Install When Idle, so all three actions are visible at once.
  if (scene === 'update-popover' || scene === 'settings-install-now-confirm') {
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
  AFTERCARE_FEATURE_SNAPSHOT,
  REWIND_PREVIEW,
  REWIND_RESULT,
};
