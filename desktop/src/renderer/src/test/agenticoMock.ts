/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { vi } from 'vitest';
import { CANONICAL_ERROR_MESSAGE_PREFIX } from '../../../shared/errors';
import type { CanonicalError } from '../../../shared/api/parse';
import type {
  AgenticoApi,
  AppEvent,
  AppRouteEvent,
  AttentionItem,
  CloneOperation,
  CreateRepositoryResult,
  InitializeRepositoryResult,
  ConnectionState,
  CreationDefaults,
  DiagnosticsSnapshot,
  FeatureConfig,
  FeatureConfigSnapshot,
  FeatureSnapshot,
  FeatureActionRequest,
  FeatureSummaryView,
  ReadinessSnapshot,
  RepositorySourcesRequest,
  RepositoryOriginStatusRequest,
  RepositoryUpdateSourceRequest,
  RepositorySourceReconcileRequest,
  RepositoryIdentity,
  SessionDetail,
  SessionOutputEvent,
  SessionSummary,
  SessionTranscript,
  ServerListSnapshot,
  Settings,
  SupervisorActionResult,
  SupervisorEvent,
  SupervisorMessageRequest,
  SupervisorMessageResult,
  SupervisorPendingRequest,
  SupervisorRecord,
  SupervisorSettingsRequest,
  SupervisorState,
  SupervisorTranscriptPage,
  ThemeInfo,
  UpdateState,
  ServerUpdateInstallRequest,
  ServerUpdateState,
  WindowPurpose,
} from '../../../shared/ipc';
import { defaultSettings, SupervisorMessageRequestSchema } from '../../../shared/ipc';

/**
 * A rejection shaped the way the preload rethrows envelope errors: the
 * canonical object rides the sentinel-prefixed message (and is attached for
 * same-world recovery). Pass to `mockRejectedValue` on window.agentico calls.
 */
export function ipcError(
  code: string,
  summary: string,
  options: { class?: CanonicalError['class']; title?: string; remediation?: string } = {},
): Error {
  const canonical: CanonicalError = {
    code,
    class: options.class ?? 'blocking',
    title: options.title ?? 'Request failed',
    summary,
    ...(options.remediation === undefined ? {} : { remediation: { hint: options.remediation } }),
  };
  return Object.assign(new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(canonical)), {
    code: canonical.code,
    remediation: canonical.remediation?.hint,
    canonical,
  });
}

/** A snapshot with every mandatory gate unsatisfied (fresh install). */
export function unreadySnapshot(overrides: Partial<ReadinessSnapshot> = {}): ReadinessSnapshot {
  const claudeIssue = {
    code: 'unauthenticated',
    class: 'blocking',
    title: 'Unauthenticated',
    summary: 'A provider CLI is installed but its authentication flow has not been completed.',
    remediation: { hint: 'claude login' },
  } as const;
  const codexIssue = {
    code: 'missing_executable',
    class: 'blocking',
    title: 'Missing executable',
    summary: 'A provider CLI is not installed.',
    remediation: { hint: 'npm install -g @openai/codex' },
  } as const;
  const modelsIssue = {
    code: 'models_unavailable',
    class: 'blocking',
    title: 'Models unavailable',
    summary: 'No usable provider exposes any model.',
  } as const;
  return {
    ready: false,
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue: claudeIssue,
      },
      {
        name: 'codex',
        installed: false,
        ready: false,
        issue: codexIssue,
      },
    ],
    models: {
      available: false,
      issue: modelsIssue,
    },
    configuration: { valid: true },
    workspaceRoots: [],
    repositories: [],
    issues: [claudeIssue, codexIssue, modelsIssue],
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
    workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
    repositories: [
      {
        name: 'repo-a',
        path: '/work/space/repo-a',
        valid: true,
        featureReady: true,
        identity: mockRepoIdentity('/work/space/repo-a'),
      },
    ],
    issues: [],
    ...overrides,
  };
}

/** Deterministic server-resolved identity for mock repositories. */
export function mockRepoIdentity(
  path: string,
  overrides: Partial<RepositoryIdentity> = {},
): RepositoryIdentity {
  return { path, commonDir: `${path}/.git`, device: '16777234', inode: '4242', ...overrides };
}

/** A minimal, valid creation-defaults payload for form tests. */
export function creationDefaults(overrides: Partial<CreationDefaults> = {}): CreationDefaults {
  return {
    workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
    repositories: [
      {
        name: 'repo-a',
        path: '/work/space/repo-a',
        valid: true,
        featureReady: true,
        identity: mockRepoIdentity('/work/space/repo-a'),
      },
      {
        name: 'repo-b',
        path: '/work/space/repo-b',
        valid: false,
        featureReady: false,
        issue: {
          code: 'invalid_repository',
          class: 'blocking',
          title: 'Invalid repository',
          summary: 'A configured repository path is not a git repository.',
        },
      },
    ],
    defaults: {
      pipeline: 'medium',
      inquireness: 'medium',
      models: [{ phase: 'Planning', model: 'model-plan' }],
      effort: [],
      useCurrentBranch: false,
    },
    ...overrides,
  };
}

/** A minimal clone operation snapshot, as the server returns it. */
export function cloneOperation(overrides: Partial<CloneOperation> = {}): CloneOperation {
  return {
    id: 'clone-0123456789abcdef',
    state: 'running',
    stage: 'transferring',
    progress: 'Receiving objects: 50% (1/2)',
    remoteUrl: 'https://example.com/acme/widget.git',
    rootPath: '/work/space',
    destination: 'widget',
    destinationPath: '/work/space/widget',
    idempotencyKey: '0192f0c1-8f2a-7c3e-b9d1-3e4f5a6b7c8d',
    cancelRequested: false,
    createdAt: '2026-09-04T12:00:00Z',
    updatedAt: '2026-09-04T12:00:01Z',
    ...overrides,
  };
}

/** A minimal successful create-repository result, as the server returns it. */
export function createRepositoryResult(
  overrides: Partial<CreateRepositoryResult> = {},
): CreateRepositoryResult {
  const path = '/work/space/created';
  return {
    repoKey: 'created',
    path,
    hasHead: true,
    root: '/work/space',
    identity: mockRepoIdentity(path),
    ...overrides,
  };
}

/** A minimal explicit-initialization result, as the server returns it. */
export function initializeRepositoryResult(
  overrides: Partial<InitializeRepositoryResult> = {},
): InitializeRepositoryResult {
  const path = '/work/space/unborn';
  return {
    result: 'initialized',
    repoKey: 'unborn',
    path,
    hasHead: true,
    root: '/work/space',
    identity: mockRepoIdentity(path),
    ...overrides,
  };
}

/** A parent feature's structured configuration, as the refactor wizard seeds it. */
export function featureConfigSnapshot(
  overrides: {
    featureId?: string;
    current?: Partial<FeatureConfig>;
    defaults?: Partial<FeatureConfig>;
    manualPublishAvailable?: boolean;
  } = {},
): FeatureConfigSnapshot {
  const base: FeatureConfig = {
    models: {},
    effort: {},
    inquireness: 'medium',
    checkpoints: {
      inquiryReview: false,
      researchReview: false,
      designReview: false,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: false,
      draftPublish: false,
    },
    pipeline: 'medium',
    inputNotifications: 'default',
    automaticReviewMode: 'default',
  };
  return {
    featureId: overrides.featureId ?? 'abcd1234ef567890',
    current: { ...base, ...overrides.current },
    defaults: { ...base, ...overrides.defaults },
    manualPublishAvailable: overrides.manualPublishAvailable ?? true,
  };
}

/** A canonical warning-class error as the renderer receives it over IPC. */
export function canonicalWarning(overrides: Partial<CanonicalError> = {}): CanonicalError {
  return {
    code: 'effort_capability_drift',
    class: 'warning',
    title: 'Effort exceeds the selected model',
    summary: 'The Implement effort "high" is beyond what claude-sonnet-4-5 supports.',
    ...overrides,
  };
}

/**
 * A canonical needs_action orphan-session error as the renderer receives it
 * over IPC, mirroring the catalog's live-orphan rendering.
 */
export function orphanSessionError(overrides: Partial<CanonicalError> = {}): CanonicalError {
  return {
    code: 'orphan_session_live',
    class: 'needs_action',
    title: 'Orphaned session running',
    summary: "The Implement phase at iteration 2 is still running outside Agentico's supervision.",
    remediation: {
      hint: 'Resume the session to bring it back under supervision, or kill it.',
      actions: ['resume'],
    },
    context: { phase: { name: 'Implement', iteration: 2 } },
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
    automaticReview: {
      mode: 'default',
      enabled: true,
      source: 'global',
    },
    warnings: [],
    errors: [],
    reviewGate: {
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    },
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
    chooseConnectionServer: ReturnType<typeof vi.fn>;
    switchConnectionServer: ReturnType<typeof vi.fn>;
    startLocalRuntime: ReturnType<typeof vi.fn>;
    listServers: ReturnType<typeof vi.fn>;
    probeServers: ReturnType<typeof vi.fn>;
    addRemoteServer: ReturnType<typeof vi.fn>;
    removeServer: ReturnType<typeof vi.fn>;
    getServerTokenStatus: ReturnType<typeof vi.fn>;
    onRouteRequest: ReturnType<typeof vi.fn>;
    getSettings: ReturnType<typeof vi.fn>;
    updateSettings: ReturnType<typeof vi.fn>;
    openSettingsWindow: ReturnType<typeof vi.fn>;
    setThemePreference: ReturnType<typeof vi.fn>;
    getRuntimeReadiness: ReturnType<typeof vi.fn>;
    refreshRuntimeReadiness: ReturnType<typeof vi.fn>;
    getReadiness: ReturnType<typeof vi.fn>;
    refreshReadiness: ReturnType<typeof vi.fn>;
    pickWorkspaceDirectory: ReturnType<typeof vi.fn>;
    addWorkspaceRoot: ReturnType<typeof vi.fn>;
    removeWorkspaceRoot: ReturnType<typeof vi.fn>;
    reorderWorkspaceRoots: ReturnType<typeof vi.fn>;
    initRepository: ReturnType<typeof vi.fn>;
    startClone: ReturnType<typeof vi.fn>;
    getCloneOperation: ReturnType<typeof vi.fn>;
    listCloneOperations: ReturnType<typeof vi.fn>;
    cancelCloneOperation: ReturnType<typeof vi.fn>;
    retryCloneCleanup: ReturnType<typeof vi.fn>;
    retryCloneOperation: ReturnType<typeof vi.fn>;
    createRepository: ReturnType<typeof vi.fn>;
    initializeRepository: ReturnType<typeof vi.fn>;
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
    inspectRepositorySources: ReturnType<typeof vi.fn>;
    checkRepositoryOriginStatus: ReturnType<typeof vi.fn>;
    updateRepositorySource: ReturnType<typeof vi.fn>;
    reconcileSourceUpdate: ReturnType<typeof vi.fn>;
    pickCreationFiles: ReturnType<typeof vi.fn>;
    uploadCreationFiles: ReturnType<typeof vi.fn>;
    readClipboardImage: ReturnType<typeof vi.fn>;
    importDroppedCreationFiles: ReturnType<typeof vi.fn>;
    searchCreationFiles: ReturnType<typeof vi.fn>;
    cancelCreationFileSearch: ReturnType<typeof vi.fn>;
    getAttention: ReturnType<typeof vi.fn>;
    answerPermission: ReturnType<typeof vi.fn>;
    answerQuestions: ReturnType<typeof vi.fn>;
    sendHelp: ReturnType<typeof vi.fn>;
    saveGateDraft: ReturnType<typeof vi.fn>;
    resolveGate: ReturnType<typeof vi.fn>;
    waiveTestingContract: ReturnType<typeof vi.fn>;
    getTestingContract: ReturnType<typeof vi.fn>;
    getSupervisorState: ReturnType<typeof vi.fn>;
    updateSupervisorSettings: ReturnType<typeof vi.fn>;
    cancelSupervisorPendingChange: ReturnType<typeof vi.fn>;
    getSupervisorTranscript: ReturnType<typeof vi.fn>;
    sendSupervisorMessage: ReturnType<typeof vi.fn>;
    interruptSupervisor: ReturnType<typeof vi.fn>;
    endSupervisor: ReturnType<typeof vi.fn>;
    onSupervisorEvent: ReturnType<typeof vi.fn>;
    getFeatureConfig: ReturnType<typeof vi.fn>;
    updateFeatureConfig: ReturnType<typeof vi.fn>;
    getWorkspaceDefaults: ReturnType<typeof vi.fn>;
    updateWorkspaceDefaults: ReturnType<typeof vi.fn>;
    getModelCatalogue: ReturnType<typeof vi.fn>;
    refreshProviderModels: ReturnType<typeof vi.fn>;
    listRuns: ReturnType<typeof vi.fn>;
    getRun: ReturnType<typeof vi.fn>;
    listRunSessions: ReturnType<typeof vi.fn>;
    getLivePreview: ReturnType<typeof vi.fn>;
    listRunArtifacts: ReturnType<typeof vi.fn>;
    listRunLogs: ReturnType<typeof vi.fn>;
    getRunArtifactContent: ReturnType<typeof vi.fn>;
    getRunLogContent: ReturnType<typeof vi.fn>;
    getRewindPreview: ReturnType<typeof vi.fn>;
    executeRewind: ReturnType<typeof vi.fn>;
    preflightCompletion: ReturnType<typeof vi.fn>;
    publishUiState: ReturnType<typeof vi.fn>;
    getRepositoryDiff: ReturnType<typeof vi.fn>;
    generatePublishDescription: ReturnType<typeof vi.fn>;
    openExternal: ReturnType<typeof vi.fn>;
    revealPath: ReturnType<typeof vi.fn>;
    launchRebaseChild: ReturnType<typeof vi.fn>;
    launchRefactorChild: ReturnType<typeof vi.fn>;
    discardRefactorChild: ReturnType<typeof vi.fn>;
    deleteFeatureCascade: ReturnType<typeof vi.fn>;
    fetchReviewFeedback: ReturnType<typeof vi.fn>;
    launchReviewFeedbackChild: ReturnType<typeof vi.fn>;
    updateReviewFeedbackSelection: ReturnType<typeof vi.fn>;
    scanRecovery: ReturnType<typeof vi.fn>;
    executeRecovery: ReturnType<typeof vi.fn>;
    readRecoveryLog: ReturnType<typeof vi.fn>;
    bulkPreview: ReturnType<typeof vi.fn>;
    getUpdates: ReturnType<typeof vi.fn>;
    checkForUpdates: ReturnType<typeof vi.fn>;
    installUpdateWhenIdle: ReturnType<typeof vi.fn>;
    installUpdateNow: ReturnType<typeof vi.fn>;
    restartToUpdate: ReturnType<typeof vi.fn>;
    getServerUpdate: ReturnType<typeof vi.fn>;
    checkServerUpdate: ReturnType<typeof vi.fn>;
    installServerUpdate: ReturnType<typeof vi.fn>;
    cancelServerUpdate: ReturnType<typeof vi.fn>;
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
  /** Push one supervisor stream event to every `onSupervisorEvent` listener. */
  emitSupervisorEvent(event: SupervisorEvent): void;
  supervisorEventListenerCount(): number;
  /** The mock's current supervisor read model (updated by settings/send/end). */
  supervisorState(): SupervisorState;
  emitServersChanged(snapshot: ServerListSnapshot): void;
  serversChangedListenerCount(): number;
}

/**
 * Deterministic supervisor read model: a never-launched conversation with
 * lifecycle `stopped`, empty settings, no pending requests, and an empty
 * transcript (head seq 0).
 */
export function supervisorState(overrides: Partial<SupervisorState> = {}): SupervisorState {
  return {
    conversationId: 'supervisor-conversation-1',
    generation: 0,
    sessionId: '',
    lifecycle: 'stopped',
    lastTurnOutcome: 'none',
    interruptedBy: 'none',
    settings: { harness: '', model: '', effort: '' },
    effectiveModel: '',
    permissionMode: { requested: 'default', effective: '', restrictedByPolicy: false },
    pendingRequests: [],
    contextUsage: null,
    headSeq: 0,
    streamEpoch: 'supervisor-epoch-1',
    ...overrides,
  };
}

type SupervisorPendingPermission = Extract<SupervisorPendingRequest, { kind: 'permission' }>;
type SupervisorPendingQuestion = Extract<SupervisorPendingRequest, { kind: 'questions' }>;

/**
 * One pending supervisor permission (a root-agent Bash request by default).
 * Pass `origin: 'child'` and a `childSessionId` for a sub-agent's request.
 */
export function supervisorPendingPermission(
  overrides: Partial<SupervisorPendingPermission> = {},
): SupervisorPendingPermission {
  return {
    kind: 'permission',
    id: 'supervisor-permission-1',
    sessionId: '__supervisor__.supervisor-conversation-1.1',
    target: 'supervisor',
    toolName: 'Bash',
    summary: 'make test',
    input: { command: 'make test' },
    waitingSince: '2026-10-06T10:00:00.000Z',
    origin: 'root',
    ...overrides,
  };
}

/**
 * One pending supervisor question (a root-agent single-choice question by
 * default). Pass `origin: 'child'` and a `childSessionId` for a sub-agent's.
 */
export function supervisorPendingQuestion(
  overrides: Partial<SupervisorPendingQuestion> = {},
): SupervisorPendingQuestion {
  return {
    kind: 'questions',
    id: 'supervisor-question-1',
    sessionId: '__supervisor__.supervisor-conversation-1.1',
    target: 'supervisor',
    waitingSince: '2026-10-06T10:00:00.000Z',
    questions: [
      {
        key: 'Which branch should the sub-task use?',
        header: 'Branch',
        multiSelect: false,
        options: [{ label: 'main' }, { label: 'dev' }],
      },
    ],
    origin: 'root',
    ...overrides,
  };
}

/** The canonical launch failure a `failed` supervisor state carries. */
export function supervisorLaunchFailure(
  overrides: Partial<NonNullable<SupervisorState['failure']>> = {},
): NonNullable<SupervisorState['failure']> {
  return {
    code: 'supervisor_launch_failed',
    class: 'blocking',
    title: 'Supervisor failed to start',
    summary: 'The harness exited before it answered the handshake.',
    ...overrides,
  };
}

/** One committed supervisor transcript record (a user message by default). */
export function supervisorRecord(overrides: Partial<SupervisorRecord> = {}): SupervisorRecord {
  const seq = overrides.seq ?? 1;
  return {
    seq,
    id: `supervisor-record-${String(seq)}`,
    conversationId: 'supervisor-conversation-1',
    generation: 1,
    turnId: `supervisor-turn-${String(seq)}`,
    kind: 'user',
    visibility: 'content',
    createdAt: '2026-10-06T10:00:00Z',
    messages: [{ index: seq, role: 'user', type: 'text', text: 'Hello supervisor' }],
    ...overrides,
  };
}

/**
 * One display-only `marker` record (the interrupted notice by default); the
 * turn id names the turn it describes.
 */
export function supervisorMarkerRecord(
  marker: NonNullable<SupervisorRecord['marker']> = {
    marker: 'interrupted',
    text: 'Interrupted before restart',
  },
  overrides: Partial<SupervisorRecord> = {},
): SupervisorRecord {
  return supervisorRecord({
    kind: 'marker',
    visibility: 'display_only',
    messages: [],
    marker,
    ...overrides,
  });
}

export function supervisorCheckpointRecord(
  overrides: Partial<SupervisorRecord> = {},
): SupervisorRecord {
  return supervisorRecord({
    kind: 'checkpoint',
    visibility: 'model_only',
    messages: [],
    checkpoint: {
      coversThroughSeq: 1,
      reason: 'native_auto',
      model: 'claude-opus',
      summary: 'Earlier context',
      truncated: false,
      hasNativeBaseline: true,
    },
    ...overrides,
  });
}

export function supervisorCompactedMarkerRecord(
  summary?: string,
  overrides: Partial<SupervisorRecord> = {},
): SupervisorRecord {
  return supervisorMarkerRecord(
    {
      marker: 'compacted',
      text: 'Conversation compacted',
      ...(summary === undefined ? {} : { summary, truncated: false }),
    },
    overrides,
  );
}

/** One transcript page; empty (all cursors 0, nothing more) by default. */
export function supervisorTranscriptPage(
  overrides: Partial<SupervisorTranscriptPage> = {},
): SupervisorTranscriptPage {
  return {
    conversationId: 'supervisor-conversation-1',
    items: [],
    firstSeq: 0,
    lastSeq: 0,
    hasMoreBefore: false,
    hasMoreAfter: false,
    headSeq: 0,
    ...overrides,
  };
}

export function installAgenticoMock(
  overrides: {
    connection?: ConnectionState;
    settings?: Settings;
    theme?: ThemeInfo;
    readiness?: ReadinessSnapshot;
    features?: FeatureSummaryView[];
    listWarnings?: CanonicalError[];
    feature?: FeatureSnapshot;
    defaults?: CreationDefaults;
    sessions?: SessionSummary[];
    session?: SessionDetail;
    transcript?: SessionTranscript;
    supervisorState?: SupervisorState;
    supervisorTranscript?: SupervisorTranscriptPage;
    attention?: { items: AttentionItem[] };
    cloneOperation?: Partial<CloneOperation>;
    cloneOperations?: CloneOperation[];
    cloneOperationsNextToken?: string;
    createResult?: Partial<CreateRepositoryResult>;
    initializeResult?: Partial<InitializeRepositoryResult>;
    updates?: UpdateState;
    serverUpdate?: ServerUpdateState;
    diagnostics?: DiagnosticsSnapshot;
    platform?: string;
    windowPurpose?: WindowPurpose;
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
  const supervisorEventListeners = new Set<(event: SupervisorEvent) => void>();
  let supervisorCurrent: SupervisorState = overrides.supervisorState ?? supervisorState();
  const supervisorTranscript =
    overrides.supervisorTranscript ??
    supervisorTranscriptPage({ conversationId: supervisorCurrent.conversationId });
  const sessions = overrides.sessions ?? [];
  const updates = overrides.updates ?? defaultUpdateState();
  const serverUpdate = overrides.serverUpdate ?? defaultServerUpdateState();
  const diagnostics = overrides.diagnostics ?? defaultDiagnostics();
  const serversChangedListeners = new Set<(snapshot: ServerListSnapshot) => void>();

  const api = {
    platform: overrides.platform ?? 'darwin',
    windowPurpose: overrides.windowPurpose ?? 'main',
    getConnectionStatus: vi.fn(() => Promise.resolve(connection)),
    retryConnection: vi.fn(() => Promise.resolve(connection)),
    restartConnection: vi.fn(() => Promise.resolve(connection)),
    chooseConnectionServer: vi.fn(() => Promise.resolve(connection)),
    switchConnectionServer: vi.fn(() => Promise.resolve(connection)),
    startLocalRuntime: vi.fn(() => Promise.resolve(connection)),
    listServers: vi.fn(() => Promise.resolve({ rows: [] })),
    probeServers: vi.fn(() => Promise.resolve({ rows: [] })),
    addRemoteServer: vi.fn(() => Promise.reject(new Error('addRemoteServer not mocked'))),
    removeServer: vi.fn(() => Promise.resolve(connection)),
    getServerTokenStatus: vi.fn(() => Promise.resolve({ status: 'local' as const })),
    onServersChanged: vi.fn((listener: (snapshot: ServerListSnapshot) => void) => {
      serversChangedListeners.add(listener);
      return () => serversChangedListeners.delete(listener);
    }),
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
    openSettingsWindow: vi.fn(() => Promise.resolve({ opened: true })),
    getThemePreference: vi.fn(() => Promise.resolve(theme)),
    setThemePreference: vi.fn((preference: ThemeInfo['preference']) => {
      theme = { preference, resolved: preference === 'system' ? theme.resolved : preference };
      return Promise.resolve(theme);
    }),
    getRuntimeReadiness: vi.fn(() => Promise.resolve(readiness)),
    refreshRuntimeReadiness: vi.fn(() => Promise.resolve(readiness)),
    getReadiness: vi.fn(() => Promise.resolve(readiness)),
    refreshReadiness: vi.fn(() => Promise.resolve(readiness)),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.resolve(readiness)),
    removeWorkspaceRoot: vi.fn(() => Promise.resolve(readiness)),
    reorderWorkspaceRoots: vi.fn(() => Promise.resolve(readiness)),
    initRepository: vi.fn(() => Promise.resolve(readiness)),
    startClone: vi.fn(() =>
      Promise.resolve(cloneOperation(overrides.cloneOperation ?? { state: 'accepted' })),
    ),
    getCloneOperation: vi.fn(() =>
      Promise.resolve(cloneOperation(overrides.cloneOperation ?? { state: 'running' })),
    ),
    listCloneOperations: vi.fn(() =>
      Promise.resolve({
        operations: overrides.cloneOperations ?? [cloneOperation()],
        ...(overrides.cloneOperationsNextToken
          ? { nextPageToken: overrides.cloneOperationsNextToken }
          : {}),
      }),
    ),
    cancelCloneOperation: vi.fn(() =>
      Promise.resolve(cloneOperation(overrides.cloneOperation ?? { state: 'cancelling' })),
    ),
    retryCloneCleanup: vi.fn(() =>
      Promise.resolve(cloneOperation(overrides.cloneOperation ?? { state: 'failed' })),
    ),
    retryCloneOperation: vi.fn(() =>
      Promise.resolve(cloneOperation(overrides.cloneOperation ?? { state: 'accepted' })),
    ),
    createRepository: vi.fn(() =>
      Promise.resolve(createRepositoryResult(overrides.createResult ?? {})),
    ),
    initializeRepository: vi.fn(() =>
      Promise.resolve(initializeRepositoryResult(overrides.initializeResult ?? {})),
    ),
    listRepositories: vi.fn(() => Promise.resolve(readiness.repositories)),
    listFeatures: vi.fn(() =>
      Promise.resolve({
        features: overrides.features ?? [],
        warnings: overrides.listWarnings ?? [],
      }),
    ),
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
    waiveTestingContract: vi.fn(() =>
      Promise.resolve({ result: 'waived', contractRevision: 2, waivedItems: [] }),
    ),
    getTestingContract: vi.fn(() => Promise.resolve({ available: false as const })),
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
    getSupervisorState: vi.fn(() => Promise.resolve(supervisorCurrent)),
    updateSupervisorSettings: vi.fn((request: SupervisorSettingsRequest) => {
      supervisorCurrent = {
        ...supervisorCurrent,
        settings: {
          harness: request.harness,
          model: request.model ?? supervisorCurrent.settings.model,
          effort: request.effort ?? '',
        },
      };
      return Promise.resolve(supervisorCurrent);
    }),
    cancelSupervisorPendingChange: vi.fn(() => {
      supervisorCurrent = { ...supervisorCurrent, pendingChange: undefined };
      return Promise.resolve(supervisorCurrent);
    }),
    getSupervisorTranscript: vi.fn(() => Promise.resolve(supervisorTranscript)),
    sendSupervisorMessage: vi.fn(
      (request: SupervisorMessageRequest): Promise<SupervisorMessageResult> => {
        // The IPC contract fails closed on a malformed request (an
        // undisciplined error reference included) before anything is sent.
        if (!SupervisorMessageRequestSchema.safeParse(request).success) {
          return Promise.reject(
            ipcError('E_SCHEMA_MISMATCH', 'The request did not match the expected shape.'),
          );
        }
        const seq = supervisorCurrent.headSeq + 1;
        const launched =
          supervisorCurrent.lifecycle === 'stopped' || supervisorCurrent.lifecycle === 'failed';
        const generation = supervisorCurrent.generation + (launched ? 1 : 0);
        supervisorCurrent = {
          ...supervisorCurrent,
          generation,
          headSeq: seq,
          lifecycle: 'running',
          sessionId: `__supervisor__.${supervisorCurrent.conversationId}.${String(generation)}`,
          // A launch clears the previous launch failure, as the server does.
          failure: undefined,
        };
        return Promise.resolve({
          record: supervisorRecord({
            seq,
            conversationId: supervisorCurrent.conversationId,
            generation,
            clientMessageId: `supervisor-client-message-${String(seq)}`,
            // Only the visible text is committed; the reference rides hidden.
            messages: [{ index: seq, role: 'user', type: 'text', text: request.text }],
          }),
          launched,
          deduplicated: false,
        });
      },
    ),
    interruptSupervisor: vi.fn((): Promise<SupervisorActionResult> =>
      Promise.resolve({ result: 'accepted', state: supervisorCurrent }),
    ),
    endSupervisor: vi.fn((): Promise<SupervisorActionResult> => {
      const result = supervisorCurrent.lifecycle === 'stopped' ? 'not_active' : 'ended';
      supervisorCurrent = { ...supervisorCurrent, lifecycle: 'stopped', sessionId: '' };
      return Promise.resolve({ result, state: supervisorCurrent });
    }),
    onSupervisorEvent: vi.fn((listener: (event: SupervisorEvent) => void) => {
      supervisorEventListeners.add(listener);
      return () => {
        supervisorEventListeners.delete(listener);
      };
    }),
    getCreationDefaults: vi.fn(() => Promise.resolve(defaults)),
    inspectRepositorySources: vi.fn((request: RepositorySourcesRequest) =>
      Promise.resolve({
        repositories: request.repositories.map((repository) => ({
          ...repository,
          mode: request.mode,
          kind: 'branch' as const,
          branch: 'main',
          observedSha: 'a'.repeat(40),
        })),
      }),
    ),
    checkRepositoryOriginStatus: vi.fn((request: RepositoryOriginStatusRequest) =>
      Promise.resolve({
        repositories: request.repositories.map((repository) => ({
          ...repository,
          mode: request.mode,
          kind: 'branch' as const,
          branch: 'main',
          localSha: 'a'.repeat(40),
          status: 'no_origin' as const,
        })),
      }),
    ),
    updateRepositorySource: vi.fn((request: RepositoryUpdateSourceRequest) =>
      Promise.resolve({
        result: 'already_up_to_date' as const,
        repoKey: request.repoKey,
        identity: request.identity,
        mode: request.mode,
        branch: request.branch,
        originBranch: request.originBranch,
        localSha: request.expectedLocalSha,
      }),
    ),
    reconcileSourceUpdate: vi.fn((request: RepositorySourceReconcileRequest) =>
      Promise.resolve({
        outcome: 'original_tip_remains' as const,
        repoKey: request.repoKey,
        identity: request.identity,
        mode: request.mode,
        branch: request.branch,
        originBranch: request.originBranch,
        localSha: request.expectedLocalSha,
      }),
    ),
    pickCreationFiles: vi.fn(() => Promise.resolve({ paths: [] })),
    uploadCreationFiles: vi.fn((kind: string, paths: readonly string[]) =>
      Promise.resolve({
        results: paths.map((filePath) => ({
          ok: true as const,
          name: filePath.split(/[\\/]/).at(-1) ?? 'file',
          upload: {
            reference: `ref-${(filePath.split(/[\\/]/).at(-1) ?? 'file').replace(/[^a-z0-9]/gi, '')}`,
            kind: kind as 'image' | 'attachment',
            name: filePath.split(/[\\/]/).at(-1) ?? 'file',
            size: 10,
            serverKey: 'server-key-1',
          },
        })),
      }),
    ),
    readClipboardImage: vi.fn(() => Promise.resolve({ paths: [] })),
    writeClipboardText: vi.fn(() => Promise.resolve({ ok: true })),
    importDroppedCreationFiles: vi.fn(() => ({ paths: [] })),
    searchCreationFiles: vi.fn((request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [],
        truncated: false,
        cancelled: false,
      }),
    ),
    cancelCreationFileSearch: vi.fn(() => Promise.resolve(false)),
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
    getFeatureConfig: vi.fn(() => Promise.reject(new Error('unused'))),
    updateFeatureConfig: vi.fn(() => Promise.reject(new Error('unused'))),
    getWorkspaceDefaults: vi.fn(() => Promise.reject(new Error('unused'))),
    updateWorkspaceDefaults: vi.fn(() => Promise.reject(new Error('unused'))),
    getModelCatalogue: vi.fn(() =>
      Promise.resolve({
        providerOrder: [],
        providerModels: {},
        phaseDefaults: {},
        phaseProviderModels: {},
      }),
    ),
    refreshProviderModels: vi.fn(() =>
      Promise.resolve({
        readiness,
        catalogue: {
          providerOrder: [],
          providerModels: {},
          phaseDefaults: {},
          phaseProviderModels: {},
        },
      }),
    ),
    listRuns: vi.fn(() =>
      Promise.resolve({ runs: [], page: 1, pageSize: 20, total: 0, totalPages: 0 }),
    ),
    getRun: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunSessions: vi.fn(() => Promise.resolve({ runNumber: 1, sessions })),
    getLivePreview: vi.fn(() =>
      Promise.resolve({
        featureId: feature.id,
        activity: feature.status,
        contextPercentage: -1,
        totalSeconds: 0,
        totalUsd: 0,
        transcript: [],
      }),
    ),
    listRunArtifacts: vi.fn(() => Promise.resolve({ artifacts: [] })),
    listRunLogs: vi.fn(() => Promise.resolve({ logs: [] })),
    getRunArtifactContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunLogContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRewindPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRewind: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightCompletion: vi.fn(() => Promise.reject(new Error('unused'))),
    publishUiState: vi.fn(() => Promise.resolve({ accepted: true })),
    getRepositoryDiff: vi.fn(() => Promise.reject(new Error('unused'))),
    generatePublishDescription: vi.fn(() => Promise.reject(new Error('unused'))),
    openExternal: vi.fn(() => Promise.reject(new Error('unused'))),
    revealPath: vi.fn(() => Promise.reject(new Error('unused'))),
    launchRebaseChild: vi.fn(() => Promise.reject(new Error('unused'))),
    launchRefactorChild: vi.fn(() => Promise.reject(new Error('unused'))),
    discardRefactorChild: vi.fn(() => Promise.reject(new Error('unused'))),
    deleteFeatureCascade: vi.fn(() => Promise.reject(new Error('unused'))),
    fetchReviewFeedback: vi.fn(() => Promise.reject(new Error('unused'))),
    launchReviewFeedbackChild: vi.fn(() => Promise.reject(new Error('unused'))),
    updateReviewFeedbackSelection: vi.fn(() => Promise.reject(new Error('unused'))),
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
    getServerUpdate: vi.fn(() => Promise.resolve(serverUpdate)),
    checkServerUpdate: vi.fn(() => Promise.resolve(serverUpdate)),
    installServerUpdate: vi.fn((request: ServerUpdateInstallRequest) =>
      Promise.resolve({
        ...serverUpdate,
        status: 'scheduled',
        method: request.when,
        targetVersion: serverUpdate.latestVersion,
      }),
    ),
    cancelServerUpdate: vi.fn(() => Promise.resolve({ ...serverUpdate, status: 'available' })),
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
    emitSupervisorEvent: (event) => {
      for (const listener of supervisorEventListeners) listener(event);
    },
    supervisorEventListenerCount: () => supervisorEventListeners.size,
    supervisorState: () => supervisorCurrent,
    emitServersChanged: (snapshot) => {
      for (const listener of serversChangedListeners) listener(snapshot);
    },
    serversChangedListenerCount: () => serversChangedListeners.size,
  };
}

export function defaultServerUpdateState(
  overrides: Partial<ServerUpdateState> = {},
): ServerUpdateState {
  return {
    status: 'up_to_date',
    policy: 'notify',
    currentVersion: '0.1.0',
    latestVersion: '0.1.0',
    installation: 'tarball',
    signature: 'unverified',
    ...overrides,
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
