/**
 * The complete IPC surface between renderer and main. Channel names are
 * centralized here with a zod request/response contract per channel; there is
 * deliberately no generic invoke passthrough anywhere in the app.
 */
import { z } from 'zod';

export const ATTENTION_ALREADY_RESOLVED_NOTICE =
  'This item was already resolved. The inbox has been refreshed.';
export const ATTENTION_SUBMITTED_NOTICE = 'Submitted. Waiting for the server snapshot...';

export const IPC_CHANNELS = {
  connectionGetStatus: 'agentico:connection:get-status',
  connectionRetry: 'agentico:connection:retry',
  connectionRestart: 'agentico:connection:restart',
  settingsGet: 'agentico:settings:get',
  settingsUpdate: 'agentico:settings:update',
  themeGet: 'agentico:theme:get',
  themeSet: 'agentico:theme:set',
  readinessGet: 'agentico:readiness:get',
  readinessRefresh: 'agentico:readiness:refresh',
  workspacePickDirectory: 'agentico:workspace:pick-directory',
  workspaceAddRoot: 'agentico:workspace:add-root',
  workspaceRemoveRoot: 'agentico:workspace:remove-root',
  workspaceReorderRoots: 'agentico:workspace:reorder-roots',
  workspaceInitRepository: 'agentico:workspace:init-repository',
  repositoriesList: 'agentico:repositories:list',
  featuresList: 'agentico:features:list',
  featuresGet: 'agentico:features:get',
  featuresCreate: 'agentico:features:create',
  featuresSetup: 'agentico:features:setup',
  featuresDispatchAction: 'agentico:features:dispatch-action',
  featuresRebase: 'agentico:features:rebase',
  featuresRebasePreflight: 'agentico:features:rebase:preflight',
  featuresReviewCommentsFetch: 'agentico:features:review-comments:fetch',
  featuresReviewCommentsStart: 'agentico:features:review-comments:start',
  featuresRefactor: 'agentico:features:refactor',
  featuresRefactorPreflight: 'agentico:features:refactor:preflight',
  recoveryScan: 'agentico:recovery:scan',
  recoveryExecute: 'agentico:recovery:execute',
  recoveryLogRead: 'agentico:recovery:log-read',
  bulkPreview: 'agentico:bulk:preview',
  updatesGet: 'agentico:updates:get',
  updatesCheck: 'agentico:updates:check',
  updatesInstallWhenIdle: 'agentico:updates:install-when-idle',
  updatesInstallNow: 'agentico:updates:install-now',
  updatesRestart: 'agentico:updates:restart',
  diagnosticsGet: 'agentico:diagnostics:get',
  diagnosticsReveal: 'agentico:diagnostics:reveal',
  diagnosticsClear: 'agentico:diagnostics:clear',
  attentionGet: 'agentico:attention:get',
  attentionAnswerPermission: 'agentico:attention:answer-permission',
  attentionAnswerQuestions: 'agentico:attention:answer-questions',
  attentionSendHelp: 'agentico:attention:send-help',
  attentionSaveGateDraft: 'agentico:attention:save-gate-draft',
  attentionResolveGate: 'agentico:attention:resolve-gate',
  chatStart: 'agentico:chat:start',
  chatEnd: 'agentico:chat:end',
  sessionsList: 'agentico:sessions:list',
  sessionsGet: 'agentico:sessions:get',
  sessionsTranscript: 'agentico:sessions:transcript',
  sessionsOutputOpen: 'agentico:sessions:output-open',
  sessionsOutputCancel: 'agentico:sessions:output-cancel',
  creationDefaults: 'agentico:creation:defaults',
  reviewDraftsLoad: 'agentico:review-drafts:load',
  reviewDraftsSave: 'agentico:review-drafts:save',
  reviewDraftsDiscard: 'agentico:review-drafts:discard',
  reviewsRead: 'agentico:reviews:read',
  reviewsOpen: 'agentico:reviews:open',
  reviewsSave: 'agentico:reviews:save',
  reviewsValidate: 'agentico:reviews:validate',
  reviewsDecide: 'agentico:reviews:decide',
  resourcesCatalogue: 'agentico:resources:catalogue',
  resourcesRead: 'agentico:resources:read',
  resourcesValidate: 'agentico:resources:validate',
  resourcesWrite: 'agentico:resources:write',
  resourceDraftsLoad: 'agentico:resource-drafts:load',
  resourceDraftsSave: 'agentico:resource-drafts:save',
  resourceDraftsDiscard: 'agentico:resource-drafts:discard',
  runsList: 'agentico:runs:list',
  runsGet: 'agentico:runs:get',
  runSessionsList: 'agentico:runs:sessions-list',
  runArtifactsList: 'agentico:runs:artifacts-list',
  runArtifactContent: 'agentico:runs:artifact-content',
  runLogContent: 'agentico:runs:log-content',
  rewindPreview: 'agentico:rewind:preview',
  rewindExecute: 'agentico:rewind:execute',
  completionPreflight: 'agentico:completion:preflight',
  repositoryDiff: 'agentico:completion:repository-diff',
  publishDescription: 'agentico:completion:publish-description',
  openExternal: 'agentico:open:external',
  revealPath: 'agentico:open:reveal',
} as const;

export type IpcChannel = (typeof IPC_CHANNELS)[keyof typeof IPC_CHANNELS];

/** Main-to-renderer push events (webContents.send). */
export const IPC_EVENTS = {
  connectionChanged: 'agentico:connection:changed',
  appEvent: 'agentico:events:app',
  sessionOutput: 'agentico:sessions:output',
  routeRequested: 'agentico:route:requested',
} as const;

// --- Safe error shape crossing the boundary --------------------------------

export const SafeErrorSchema = z.strictObject({
  code: z.string(),
  message: z.string(),
  remediation: z.string().optional(),
});

/** Every invoke resolves to this envelope so failures stay typed. */
export const IpcEnvelopeSchema = z.discriminatedUnion('ok', [
  z.strictObject({ ok: z.literal(true), value: z.unknown() }),
  z.strictObject({ ok: z.literal(false), error: SafeErrorSchema }),
]);

export type IpcEnvelope = z.output<typeof IpcEnvelopeSchema>;

// --- Connection state (drives the connection shell) ------------------------

export const CONNECTION_STAGES = [
  'resolve-runtime',
  'discover',
  'connect',
  'wait-health',
  'authenticate',
  'ready',
] as const;

export const ConnectionStageSchema = z.enum(CONNECTION_STAGES);
export type ConnectionStage = z.output<typeof ConnectionStageSchema>;

/** In-flight startup statuses before any server is attached or owned. */
export const CONNECTION_PENDING_STATUSES = [
  'idle',
  'resolving-runtime',
  'discovering',
  'attaching',
  'launching',
] as const;

/** Terminal states that surface the error panel and a manual retry path. */
export const CONNECTION_ERROR_STATUSES = [
  'incompatible',
  'resources-missing',
  'launch-failed',
  'crashed',
  'error',
] as const;

export const CONNECTION_STATUSES = [
  ...CONNECTION_PENDING_STATUSES,
  'waiting-health',
  'connecting',
  'ready',
  ...CONNECTION_ERROR_STATUSES,
] as const;

export const ConnectionStatusSchema = z.enum(CONNECTION_STATUSES);
export type ConnectionStatus = z.output<typeof ConnectionStatusSchema>;

export type ConnectionErrorStatus = (typeof CONNECTION_ERROR_STATUSES)[number];

export function isConnectionErrorStatus(status: ConnectionStatus): status is ConnectionErrorStatus {
  return (CONNECTION_ERROR_STATUSES as readonly ConnectionStatus[]).includes(status);
}

/**
 * Who owns the server process: `app-owned` children may be stopped by the
 * app on quit; `external` servers are never signalled or terminated.
 */
export const ServerOwnershipSchema = z.enum(['none', 'external', 'app-owned']);
export type ServerOwnership = z.output<typeof ServerOwnershipSchema>;

/** Server build identity (informational; never gates compatibility). */
export const ServerBuildInfoSchema = z.strictObject({
  version: z.string(),
  revision: z.string().optional(),
});

export type ServerBuildInfo = z.output<typeof ServerBuildInfoSchema>;

/** Fields every connection-state variant shares apart from its lifecycle stage. */
const connectionStateBase = {
  detail: z.string(),
  serverBuild: ServerBuildInfoSchema.optional(),
  /**
   * The runtime directory the gateway actually resolved and connected with.
   * Absent until the connect cycle resolves the selected runtime; null when
   * no runtime has been resolved yet. The renderer compares this against
   * `settings.runtime.selection` to derive restart-pending state
   * authoritatively, without transient component state.
   */
  connectedRuntimeDir: z.string().max(4096).nullable().optional(),
} as const;

const connectionStage = <T extends ConnectionStage>(stage: T) => z.literal(stage);

/** Renderer-safe, bounded diagnostics for failures of the app-owned child only. */
export const ConnectionDiagnosticsSchema = z.strictObject({
  commandContext: z.string().max(256),
  logTail: z.array(z.string().max(512)).max(20),
});
export type ConnectionDiagnostics = z.output<typeof ConnectionDiagnosticsSchema>;

const connectionFailureContext = {
  ...connectionStateBase,
  diagnostics: ConnectionDiagnosticsSchema.optional(),
} as const;

/** Startup progress before any server exists to own or attach to. */
const ConnectionIdleStateSchema = z.strictObject({
  status: z.literal('idle'),
  stage: connectionStage('resolve-runtime'),
  ...connectionStateBase,
  ownership: z.literal('none'),
});
const ConnectionResolvingRuntimeStateSchema = z.strictObject({
  status: z.literal('resolving-runtime'),
  stage: connectionStage('resolve-runtime'),
  ...connectionStateBase,
  ownership: z.literal('none'),
});
const ConnectionDiscoveringStateSchema = z.strictObject({
  status: z.literal('discovering'),
  stage: connectionStage('discover'),
  ...connectionStateBase,
  ownership: z.literal('none'),
});
const ConnectionAttachingStateSchema = z.strictObject({
  status: z.literal('attaching'),
  stage: connectionStage('connect'),
  ...connectionStateBase,
  ownership: z.literal('none'),
});
const ConnectionLaunchingStateSchema = z.strictObject({
  status: z.literal('launching'),
  stage: connectionStage('connect'),
  ...connectionStateBase,
  ownership: z.literal('none'),
});

/** Supervising the freshly spawned child, which is always app-owned. */
export const ConnectionSupervisingStateSchema = z.strictObject({
  status: z.literal('waiting-health'),
  stage: connectionStage('wait-health'),
  ...connectionStateBase,
  ownership: z.literal('app-owned'),
});

/** Authenticating against a concrete server, so ownership is decided. */
export const ConnectionAuthenticatingStateSchema = z.strictObject({
  status: z.literal('connecting'),
  stage: connectionStage('authenticate'),
  ...connectionStateBase,
  ownership: z.enum(['external', 'app-owned']),
});

/** Connected: always names who owns the server and never carries an error. */
export const ConnectionReadyStateSchema = z.strictObject({
  status: z.literal('ready'),
  stage: connectionStage('ready'),
  ...connectionStateBase,
  ownership: z.enum(['external', 'app-owned']),
});

/** Terminal failures always carry redacted diagnostics for the shell. */
const ConnectionIncompatibleStateSchema = z.strictObject({
  status: z.literal('incompatible'),
  stage: connectionStage('connect'),
  ...connectionStateBase,
  ownership: ServerOwnershipSchema,
  error: SafeErrorSchema,
});
const ConnectionResourcesMissingStateSchema = z.strictObject({
  status: z.literal('resources-missing'),
  stage: connectionStage('connect'),
  ...connectionStateBase,
  ownership: ServerOwnershipSchema,
  error: SafeErrorSchema,
});
const ConnectionLaunchFailedStateSchema = z.strictObject({
  status: z.literal('launch-failed'),
  stage: z.enum(['connect', 'wait-health', 'authenticate']),
  ...connectionFailureContext,
  ownership: ServerOwnershipSchema,
  error: SafeErrorSchema,
});
const ConnectionCrashedStateSchema = z.strictObject({
  status: z.literal('crashed'),
  stage: connectionStage('connect'),
  ...connectionFailureContext,
  ownership: ServerOwnershipSchema,
  error: SafeErrorSchema,
});
const ConnectionUnexpectedErrorStateSchema = z.strictObject({
  status: z.literal('error'),
  stage: z.enum(['resolve-runtime', 'discover', 'connect', 'wait-health', 'authenticate']),
  ...connectionStateBase,
  ownership: ServerOwnershipSchema,
  error: SafeErrorSchema,
});

export const ConnectionErrorStateSchema = z.union([
  ConnectionIncompatibleStateSchema,
  ConnectionResourcesMissingStateSchema,
  ConnectionLaunchFailedStateSchema,
  ConnectionCrashedStateSchema,
  ConnectionUnexpectedErrorStateSchema,
]);

/**
 * The renderer-visible connection model, discriminated on `status` so
 * impossible combinations — a ready state with no ownership, a terminal
 * failure without error detail, an in-flight state carrying an error —
 * are unrepresentable. Strict by design: any foreign field — in particular
 * anything token-shaped — fails validation at the preload and IPC
 * boundaries. Credentials never appear here.
 */
export const ConnectionStateSchema = z.discriminatedUnion('status', [
  ConnectionIdleStateSchema,
  ConnectionResolvingRuntimeStateSchema,
  ConnectionDiscoveringStateSchema,
  ConnectionAttachingStateSchema,
  ConnectionLaunchingStateSchema,
  ConnectionSupervisingStateSchema,
  ConnectionAuthenticatingStateSchema,
  ConnectionReadyStateSchema,
  ConnectionIncompatibleStateSchema,
  ConnectionResourcesMissingStateSchema,
  ConnectionLaunchFailedStateSchema,
  ConnectionCrashedStateSchema,
  ConnectionUnexpectedErrorStateSchema,
]);

export type ConnectionState = z.output<typeof ConnectionStateSchema>;
export type ConnectionReadyState = z.output<typeof ConnectionReadyStateSchema>;
export type ConnectionErrorState = z.output<typeof ConnectionErrorStateSchema>;

/** Narrows to the failure variant, which always carries error detail. */
export function isConnectionErrorState(state: ConnectionState): state is ConnectionErrorState {
  return isConnectionErrorStatus(state.status);
}

// --- Readiness (renderer-facing view of the authoritative server snapshot) ---
// Strict by design: any foreign field — in particular anything token-shaped —
// fails validation at the IPC boundary. The renderer never receives raw
// server payloads; the main process maps them into this shape.

export const READINESS_ISSUE_CODES = [
  'missing_executable',
  'unsupported_version',
  'unauthenticated',
  'models_unavailable',
  'invalid_configuration',
  'invalid_workspace_root',
  'invalid_repository',
] as const;

export const ReadinessIssueCodeSchema = z.enum(READINESS_ISSUE_CODES);
export type ReadinessIssueCode = z.output<typeof ReadinessIssueCodeSchema>;

export const ReadinessIssueSchema = z.strictObject({
  code: ReadinessIssueCodeSchema,
  /** Server-provided safe summary; never carries credentials. */
  message: z.string(),
  /** Safe remediation metadata, e.g. the provider CLI auth command. */
  remedy: z.string().optional(),
});

export type ReadinessIssue = z.output<typeof ReadinessIssueSchema>;

export const ProviderReadinessSchema = z.strictObject({
  name: z.string(),
  installed: z.boolean(),
  version: z.string().optional(),
  ready: z.boolean(),
  issue: ReadinessIssueSchema.optional(),
});

export type ProviderReadiness = z.output<typeof ProviderReadinessSchema>;

export const ModelsReadinessSchema = z.strictObject({
  available: z.boolean(),
  models: z.array(z.string()).optional(),
  issue: ReadinessIssueSchema.optional(),
});

export type ModelsReadiness = z.output<typeof ModelsReadinessSchema>;

export const ConfigurationReadinessSchema = z.strictObject({
  valid: z.boolean(),
  issue: ReadinessIssueSchema.optional(),
});

export type ConfigurationReadiness = z.output<typeof ConfigurationReadinessSchema>;

export const WorkspaceRootStateSchema = z.strictObject({
  path: z.string(),
  valid: z.boolean(),
  issue: ReadinessIssueSchema.optional(),
});

export type WorkspaceRootState = z.output<typeof WorkspaceRootStateSchema>;

export const RepositoryStateSchema = z.strictObject({
  name: z.string(),
  path: z.string(),
  valid: z.boolean(),
  issue: ReadinessIssueSchema.optional(),
});

export type RepositoryState = z.output<typeof RepositoryStateSchema>;

export const ReadinessSnapshotSchema = z.strictObject({
  /** Server-declared mandatory readiness (providers + models + configuration). */
  ready: z.boolean(),
  /** When provider probes last ran (RFC 3339), if known. */
  probedAt: z.string().optional(),
  providers: z.array(ProviderReadinessSchema),
  models: ModelsReadinessSchema,
  configuration: ConfigurationReadinessSchema,
  workspaceRoots: z.array(WorkspaceRootStateSchema),
  repositories: z.array(RepositoryStateSchema),
  /** Flattened outstanding issues across all sections. */
  issues: z.array(ReadinessIssueSchema),
});

export type ReadinessSnapshot = z.output<typeof ReadinessSnapshotSchema>;

// --- Server event invalidations (pushed main → renderer) --------------------
// Carries ONLY invalidation metadata and stream health — never domain
// payloads, summaries, or credentials. Strict schemas fail closed on any
// foreign field at the preload boundary.

/** Event kinds are dotted lowercase identifiers (e.g. `feature.updated`). */
export const InvalidationKindSchema = z
  .string()
  .min(1)
  .max(64)
  .regex(/^[a-z0-9._-]+$/i);

export const AppEventSchema = z.discriminatedUnion('type', [
  z.strictObject({
    type: z.literal('invalidated'),
    /** Server event kind, or the synthetic `resync` full-refresh signal. */
    kind: InvalidationKindSchema,
    resourceType: z.string().max(64).optional(),
    resourceId: z.string().max(200).optional(),
    featureId: z.string().max(200).optional(),
  }),
  z.strictObject({
    type: z.literal('status'),
    stream: z.enum(['connecting', 'live', 'stale']),
  }),
]);

export type AppEvent = z.output<typeof AppEventSchema>;

export const AppRouteEventSchema = z.strictObject({
  target: z.enum(['palette', 'home', 'settings', 'attention', 'ama', 'bulk']),
  attentionId: z.string().min(1).max(500).optional(),
  featureId: z.string().min(1).max(200).optional(),
  settingsSection: z.enum(['updates', 'diagnostics']).optional(),
});

export type AppRouteEvent = z.output<typeof AppRouteEventSchema>;

export interface RoutedRequest {
  id: number;
  event: AppRouteEvent;
}

// --- Desktop updates --------------------------------------------------------
// Renderer-visible update state is a redacted state machine. Feed access,
// staged filesystem paths, signatures, native install, and restart control stay
// in the main process.

export const UpdatePackageFormatSchema = z.enum(['macos', 'appimage', 'deb', 'unknown']);
export type UpdatePackageFormat = z.output<typeof UpdatePackageFormatSchema>;

export const UpdateStatusSchema = z.enum([
  'idle',
  'checking',
  'current',
  'available',
  'downloading',
  'ready',
  'scheduled',
  'installing',
  'failed',
]);
export type UpdateStatus = z.output<typeof UpdateStatusSchema>;

export const UpdateSignatureStatusSchema = z.enum(['unknown', 'verified', 'failed']);
export type UpdateSignatureStatus = z.output<typeof UpdateSignatureStatusSchema>;

export const UpdateProgressSchema = z.strictObject({
  downloadedBytes: z.number().int().nonnegative(),
  totalBytes: z.number().int().positive().optional(),
});
export type UpdateProgress = z.output<typeof UpdateProgressSchema>;

export const UpdateStateSchema = z.strictObject({
  status: UpdateStatusSchema,
  currentVersion: z.string().min(1).max(80),
  targetVersion: z.string().min(1).max(80).optional(),
  packageFormat: UpdatePackageFormatSchema,
  signatureStatus: UpdateSignatureStatusSchema,
  checkedAt: z.string().datetime().optional(),
  nextCheckAt: z.string().datetime().optional(),
  releaseNotesUrl: z.string().url().optional(),
  message: z.string().max(500),
  guidance: z.array(z.string().max(240)).max(6).optional(),
  progress: UpdateProgressSchema.optional(),
  activeWorkSummary: z.string().max(240).optional(),
});
export type UpdateState = z.output<typeof UpdateStateSchema>;

export const UpdateInstallNowRequestSchema = z.strictObject({
  consent: z.literal(true),
  stopActiveWork: z.boolean(),
});
export type UpdateInstallNowRequest = z.output<typeof UpdateInstallNowRequestSchema>;

// --- Local diagnostics ------------------------------------------------------
// Diagnostics payloads contain already-redacted bounded records only. They do
// not expose the app-owned diagnostics root path, file names, transcripts,
// prompts, arguments, environment values, or arbitrary read/delete targets.

export const DiagnosticLevelSchema = z.enum(['info', 'warn', 'error']);
export type DiagnosticLevel = z.output<typeof DiagnosticLevelSchema>;

export const DiagnosticSourceSchema = z.enum(['electron', 'server', 'update', 'crash']);
export type DiagnosticSource = z.output<typeof DiagnosticSourceSchema>;

export const DiagnosticEntrySchema = z.strictObject({
  id: z.string().min(1).max(80),
  time: z.string().datetime(),
  source: DiagnosticSourceSchema,
  level: DiagnosticLevelSchema,
  message: z.string().min(1).max(700),
  detail: z.string().max(1200).optional(),
});
export type DiagnosticEntry = z.output<typeof DiagnosticEntrySchema>;

export const CrashMetadataSchema = z.strictObject({
  id: z.string().min(1).max(80),
  time: z.string().datetime(),
  version: z.string().max(80),
  revision: z.string().max(80).optional(),
  platform: z.string().max(40),
  architecture: z.string().max(40),
  processRole: z.enum(['main', 'renderer', 'server', 'utility']),
  category: z.string().max(80),
  context: z.string().max(700).optional(),
});
export type CrashMetadata = z.output<typeof CrashMetadataSchema>;

export const DiagnosticsRetentionSchema = z.strictObject({
  maxBytes: z.number().int().positive(),
  maxAgeDays: z.number().int().positive(),
  maxCrashRecords: z.number().int().positive(),
  currentBytes: z.number().int().nonnegative(),
  entryCount: z.number().int().nonnegative(),
  crashCount: z.number().int().nonnegative(),
});
export type DiagnosticsRetention = z.output<typeof DiagnosticsRetentionSchema>;

export const DiagnosticsSnapshotSchema = z.strictObject({
  retention: DiagnosticsRetentionSchema,
  entries: z.array(DiagnosticEntrySchema).max(200),
  crashes: z.array(CrashMetadataSchema).max(10),
});
export type DiagnosticsSnapshot = z.output<typeof DiagnosticsSnapshotSchema>;

// --- Workspace/setup operations ----------------------------------------------

/**
 * The only path shape allowed across the IPC boundary: an absolute POSIX
 * path with no NUL/control separators. Relative paths and anything else
 * fail closed at the schema layer.
 */
export const AbsolutePathSchema = z
  .string()
  .min(1)
  .max(4096)
  .refine((value) => value.startsWith('/') && !value.includes('\0') && !value.includes('\n'), {
    message: 'Expected an absolute path.',
  });

/** Native directory-picker result; `path` is null when the user cancelled. */
export const PickedDirectorySchema = z.strictObject({
  path: AbsolutePathSchema.nullable(),
});

export type PickedDirectory = z.output<typeof PickedDirectorySchema>;

/**
 * Repository initialization requires explicit consent at the schema layer:
 * a request without `consent: true` never reaches the service.
 */
export const InitRepositoryRequestSchema = z.strictObject({
  path: AbsolutePathSchema,
  consent: z.literal(true),
});

export type InitRepositoryRequest = z.output<typeof InitRepositoryRequestSchema>;

// --- Features (renderer-facing views of authoritative server snapshots) -----
// The renderer never receives raw server payloads; the main process maps
// them into these strict shapes. Anything foreign — in particular anything
// token- or path-shaped beyond the declared fields — fails validation.

/**
 * Server feature identifiers are short lowercase hex strings; the schema
 * additionally confines them to the character class the gateway's API-path
 * allowlist accepts, so a malicious id can never smuggle path segments.
 */
export const FeatureIdSchema = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-z0-9_-]+$/i);

export type FeatureId = z.output<typeof FeatureIdSchema>;

/** Durable-setup lifecycle states (server SetupStatus). */
export const FEATURE_SETUP_STATUSES = ['queued', 'running', 'done', 'failed'] as const;
export const FeatureSetupStatusSchema = z.enum(FEATURE_SETUP_STATUSES);
export type FeatureSetupStatus = z.output<typeof FeatureSetupStatusSchema>;

export const SetupTaskViewSchema = z.strictObject({
  key: z.string().min(1),
  kind: z.string(),
  /** Display label; the main process falls back to the key. */
  label: z.string().min(1),
  repo: z.string().optional(),
  status: FeatureSetupStatusSchema,
  branch: z.string().optional(),
  attempt: z.number().int().nonnegative(),
  /** Server-redacted safe failure summary. */
  error: z.string().optional(),
});

export type SetupTaskView = z.output<typeof SetupTaskViewSchema>;

export const FeatureSetupViewSchema = z.strictObject({
  status: FeatureSetupStatusSchema,
  attempt: z.number().int().nonnegative(),
  /** Tasks in the server-owned execution order. */
  tasks: z.array(SetupTaskViewSchema),
  lastError: z.string().optional(),
});

export type FeatureSetupView = z.output<typeof FeatureSetupViewSchema>;

export const FeatureActionViewSchema = z.strictObject({
  id: z.string().min(1),
  enabled: z.boolean(),
  disabledReasons: z.array(z.strictObject({ code: z.string(), message: z.string() })),
  inputs: z
    .array(
      z.strictObject({
        name: z.string().min(1),
        options: z.array(z.string()).max(50).optional(),
      }),
    )
    .max(20)
    .optional(),
});

export type FeatureActionView = z.output<typeof FeatureActionViewSchema>;

export const FeatureSummaryViewSchema = z.strictObject({
  id: FeatureIdSchema,
  name: z.string(),
  status: z.string(),
  currentPhase: z.string(),
  repos: z.array(z.string()),
  createdAt: z.string(),
  activeRun: z.number().int().nonnegative(),
  runCount: z.number().int().nonnegative(),
  phaseStatus: z.string().optional(),
  warnings: z.array(z.strictObject({ code: z.string(), message: z.string() })).max(100),
});

export type FeatureSummaryView = z.output<typeof FeatureSummaryViewSchema>;

/** Per-repository operational status from the server feature detail. */
export const RepoStatusViewSchema = z.strictObject({
  name: z.string(),
  publishable: z.boolean(),
  touched: z.boolean().optional(),
  prUrl: z.string().optional(),
  freshness: z.string().optional(),
  lastError: z.string().optional(),
  cycleType: z.string().optional(),
  cycleStatus: z.string().optional(),
  rebaseStatus: z.string().optional(),
  rebaseTarget: z.string().optional(),
  conflictFiles: z.array(z.string()).max(200).optional(),
});
export type RepoStatusView = z.output<typeof RepoStatusViewSchema>;

/** Active cycle summary from the server feature detail. */
export const CycleViewSchema = z.strictObject({
  type: z.string().optional(),
  status: z.string().optional(),
  count: z.number().int().nonnegative().optional(),
  iteration: z.number().int().nonnegative().optional(),
});
export type CycleView = z.output<typeof CycleViewSchema>;

export const FeatureSnapshotSchema = z.strictObject({
  id: FeatureIdSchema,
  name: z.string(),
  slug: z.string(),
  status: z.string(),
  currentPhase: z.string(),
  pipeline: z.string().optional(),
  description: z.string().optional(),
  repos: z.array(z.string()),
  createdAt: z.string(),
  activeRun: z.number().int().nonnegative(),
  currentRoadmapPhase: z.number().int().nonnegative().optional(),
  totalRoadmapPhases: z.number().int().nonnegative().optional(),
  setup: FeatureSetupViewSchema.optional(),
  /** The authoritative server action catalogue (setup/start/…). */
  actions: z.array(FeatureActionViewSchema),
  /** Per-repository operational status from the server. */
  repoStatus: z.array(RepoStatusViewSchema).optional(),
  /** Active cycle summary from the server. */
  cycle: CycleViewSchema.optional(),
  failure: z
    .strictObject({ type: z.string().optional(), message: z.string().optional() })
    .optional(),
});

export type FeatureSnapshot = z.output<typeof FeatureSnapshotSchema>;

/** Renderer-visible feature actions limited to this audited server catalogue subset. */
export const FeatureOperationalActionSchema = z.enum([
  'start',
  'pause-stop',
  'rewind',
  'resume',
  'retry',
  'restart',
  'publish',
  'merge',
  'mark-done',
  'cleanup',
  'delete',
]);
export type FeatureOperationalAction = z.output<typeof FeatureOperationalActionSchema>;

const CompletionSourceRevisionSchema = z.string().min(1).max(512);
const CompletionRepoNameSchema = z.string().min(1).max(128);

export const FeatureActionRequestSchema = z.discriminatedUnion('action', [
  z.strictObject({
    featureId: FeatureIdSchema,
    action: z.enum(['start', 'pause-stop', 'rewind', 'resume', 'retry', 'restart']),
  }),
  z.strictObject({
    featureId: FeatureIdSchema,
    action: z.literal('publish'),
    body: z.strictObject({
      source_revision: CompletionSourceRevisionSchema,
      repos: z.array(CompletionRepoNameSchema).min(1).max(200),
      title: z.string().trim().min(1).max(200),
      body: z.string().max(4000).optional(),
    }),
  }),
  z.strictObject({
    featureId: FeatureIdSchema,
    action: z.enum(['merge', 'mark-done', 'delete']),
    body: z.strictObject({
      source_revision: CompletionSourceRevisionSchema,
    }),
  }),
  z.strictObject({
    featureId: FeatureIdSchema,
    action: z.literal('cleanup'),
    body: z.strictObject({
      source_revision: CompletionSourceRevisionSchema,
      target: z.literal('worktrees').optional(),
    }),
  }),
]);
export type FeatureActionRequest = z.output<typeof FeatureActionRequestSchema>;

// Compile-time drift guard: every FeatureOperationalActionSchema member must
// appear as a FeatureActionRequestSchema branch's action literal. If an action
// is added to the enum without a matching request branch, this expression
// fails to typecheck.
const _featureActionCatalogueSubset: {
  [K in FeatureOperationalAction]: z.ZodTypeAny;
} = {
  start: z.never(),
  'pause-stop': z.never(),
  rewind: z.never(),
  resume: z.never(),
  retry: z.never(),
  restart: z.never(),
  publish: z.never(),
  merge: z.never(),
  'mark-done': z.never(),
  cleanup: z.never(),
  delete: z.never(),
};
void _featureActionCatalogueSubset;

export const FeatureActionResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  action: FeatureOperationalActionSchema,
  result: z.string().max(500),
  phase: z.string().max(200).optional(),
  sessionIds: z.array(z.string().min(1).max(200)).max(100),
  sourceRunNumber: z.number().int().nonnegative().optional(),
  newRunNumber: z.number().int().nonnegative().optional(),
  warnings: z.array(z.string().max(500)).max(100).optional(),
});
export type FeatureActionResult = z.output<typeof FeatureActionResultSchema>;

// --- Rebase, review-comments, refactor cycle actions ---------------------

export const RebaseRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  sourceRevision: z.string().max(512).optional(),
});
export type RebaseRequest = z.output<typeof RebaseRequestSchema>;

export const RebasePreflightRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
});
export type RebasePreflightRequest = z.output<typeof RebasePreflightRequestSchema>;

export const RebasePreflightRepoSchema = z.strictObject({
  repo: z.string(),
  target: z.string(),
  publishable: z.boolean(),
  freshness: z.string(),
  behind: z.boolean(),
  blocker: z.string().optional(),
  conflictFiles: z.array(z.string()).optional(),
});
export type RebasePreflightRepo = z.output<typeof RebasePreflightRepoSchema>;

export const RebasePreflightResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  sourceRevision: z.string(),
  repos: z.array(RebasePreflightRepoSchema).max(200),
});
export type RebasePreflightResult = z.output<typeof RebasePreflightResultSchema>;

// --- Completion preflight + repository diff ------------------------------

export const CompletionPreflightRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
});
export type CompletionPreflightRequest = z.output<typeof CompletionPreflightRequestSchema>;

export const CompletionPreflightRepoSchema = z.strictObject({
  repo: z.string().max(128),
  publishable: z.boolean(),
  touched: z.boolean(),
  status: z.string().max(50),
  prUrl: z.string().max(2000).optional(),
  blocker: z.string().max(500).optional(),
  freshness: z.string().max(50).optional(),
  lastError: z.string().max(500).optional(),
  baseBranch: z.string().max(128).optional(),
  branch: z.string().max(128).optional(),
});
export type CompletionPreflightRepo = z.output<typeof CompletionPreflightRepoSchema>;

export const CompletionPreflightResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  sourceRevision: z.string().max(512),
  canMarkDone: z.boolean(),
  markDoneBlocker: z.string().max(500).optional(),
  repos: z.array(CompletionPreflightRepoSchema).max(200),
});
export type CompletionPreflightResult = z.output<typeof CompletionPreflightResultSchema>;

export const RepositoryDiffRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().max(128),
  filePath: z.string().max(512).optional(),
});
export type RepositoryDiffRequest = z.output<typeof RepositoryDiffRequestSchema>;

export const RepositoryDiffFileSchema = z.strictObject({
  path: z.string().max(512),
  oldPath: z.string().max(512).optional(),
  operation: z.string().max(20),
  addedLines: z.number().int().nonnegative().optional(),
  removedLines: z.number().int().nonnegative().optional(),
  binary: z.boolean().optional(),
  fingerprint: z.string().max(128).optional(),
});
export type RepositoryDiffFile = z.output<typeof RepositoryDiffFileSchema>;

export const RepositoryDiffResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().max(128),
  sourceRevision: z.string().max(512).optional(),
  truncated: z.boolean().optional(),
  files: z.array(RepositoryDiffFileSchema).max(500),
  fileDiff: z.string().max(70000).optional(),
  fileTruncated: z.boolean().optional(),
  fileBinary: z.boolean().optional(),
  fileUnavailable: z.boolean().optional(),
  partialFailure: z.string().max(500).optional(),
});
export type RepositoryDiffResult = z.output<typeof RepositoryDiffResultSchema>;

export const PublishDescriptionRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repos: z.array(CompletionRepoNameSchema).max(200).optional(),
});
export type PublishDescriptionRequest = z.output<typeof PublishDescriptionRequestSchema>;

export const PublishDescriptionResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  title: z.string().max(200),
  body: z.string().max(4000),
});
export type PublishDescriptionResult = z.output<typeof PublishDescriptionResultSchema>;

export const OpenExternalRequestSchema = z.strictObject({
  url: z.string().max(2000),
});
export type OpenExternalRequest = z.output<typeof OpenExternalRequestSchema>;

export const RevealPathRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().max(128),
});
export type RevealPathRequest = z.output<typeof RevealPathRequestSchema>;

export const RebaseResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  cycleType: z.string(),
  result: z.string().max(500),
  sessionId: z.string().min(1).max(200).optional(),
});
export type RebaseResult = z.output<typeof RebaseResultSchema>;

export const ReviewCommentsFetchRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().min(1).max(200),
});
export type ReviewCommentsFetchRequest = z.output<typeof ReviewCommentsFetchRequestSchema>;

export const ReviewCommentViewSchema = z.strictObject({
  id: z.number().int(),
  file: z.string().max(500).optional(),
  line: z.number().int().optional(),
  body: z
    .string()
    .max(64 * 1024)
    .optional(),
  author: z.string().max(200).optional(),
  threadId: z.string().max(200).optional(),
});
export type ReviewCommentView = z.output<typeof ReviewCommentViewSchema>;

export const ReviewCommentsFetchResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string(),
  comments: z.array(ReviewCommentViewSchema).max(500),
  revision: z.string().max(512).optional(),
  modes: z.array(z.string().max(200)).max(20).optional(),
});
export type ReviewCommentsFetchResult = z.output<typeof ReviewCommentsFetchResultSchema>;

export const ReviewCommentsStartRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().min(1).max(200),
  mode: z.string().min(1).max(200),
});
export type ReviewCommentsStartRequest = z.output<typeof ReviewCommentsStartRequestSchema>;

export const ReviewCommentsStartResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  cycleType: z.string(),
  result: z.string().max(500),
  sessionId: z.string().min(1).max(200).optional(),
});
export type ReviewCommentsStartResult = z.output<typeof ReviewCommentsStartResultSchema>;

export const RefactorRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().max(200).optional(),
  prompt: z.string().min(1).max(4000),
  pipeline: z.string().max(200).optional(),
  sourceRevision: z.string().max(512).optional(),
});
export type RefactorRequest = z.output<typeof RefactorRequestSchema>;

export const RefactorPreflightRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repo: z.string().max(200).optional(),
  prompt: z.string().min(1).max(4000),
  pipeline: z.string().max(200).optional(),
});
export type RefactorPreflightRequest = z.output<typeof RefactorPreflightRequestSchema>;

export const RefactorPreflightResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  sourceRevision: z.string(),
  scope: z.string(),
  repos: z.array(z.string()).max(200),
  prompt: z.string(),
  pipeline: z.string().optional(),
  blockers: z.array(z.string()).optional(),
});
export type RefactorPreflightResult = z.output<typeof RefactorPreflightResultSchema>;

export const RefactorResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  cycleType: z.string(),
  result: z.string().max(500),
  repo: z.string().max(200).optional(),
  pipeline: z.string().max(200).optional(),
  sessionId: z.string().min(1).max(200).optional(),
});
export type RefactorResult = z.output<typeof RefactorResultSchema>;

// --- Recovery ---------------------------------------------------------------

export const RecoveryItemViewSchema = z.strictObject({
  key: z.string().min(1).max(200),
  featureId: FeatureIdSchema,
  featureName: z.string().optional(),
  repoName: z.string().max(500).optional(),
  phase: z.string().max(200).optional(),
  iteration: z.number().int().nonnegative().optional(),
  pid: z.number().int().optional(),
  processAlive: z.boolean(),
  logAvailable: z.boolean().optional(),
  allowedActions: z.array(z.string().max(50)).max(20),
  defaultAction: z.string().max(50),
});
export type RecoveryItemView = z.output<typeof RecoveryItemViewSchema>;

export const RecoverySnapshotSchema = z.strictObject({
  snapshotId: z.string().min(1).max(200),
  items: z.array(RecoveryItemViewSchema).max(1000),
});
export type RecoverySnapshot = z.output<typeof RecoverySnapshotSchema>;

export const RecoveryExecuteRequestSchema = z.strictObject({
  snapshotId: z.string().min(1).max(200),
  actions: z.record(z.string().min(1).max(200), z.string().min(1).max(50)),
});
export type RecoveryExecuteRequest = z.output<typeof RecoveryExecuteRequestSchema>;

export const RecoveryExecuteResultSchema = z.strictObject({
  result: z.string().max(500),
});
export type RecoveryExecuteResult = z.output<typeof RecoveryExecuteResultSchema>;

export const RecoveryLogReadRequestSchema = z.strictObject({
  snapshotId: z.string().min(1).max(200),
  key: z.string().min(1).max(512),
  offset: z.number().int().nonnegative().optional(),
  limit: z
    .number()
    .int()
    .positive()
    .max(256 * 1024)
    .optional(),
});
export type RecoveryLogReadRequest = z.output<typeof RecoveryLogReadRequestSchema>;

export const RecoveryLogReadResultSchema = z.strictObject({
  id: z.string(),
  offset: z.number().int().nonnegative(),
  limit: z.number().int().positive(),
  size: z.number().int().nonnegative(),
  text: z.string(),
  truncated: z.boolean(),
});
export type RecoveryLogReadResult = z.output<typeof RecoveryLogReadResultSchema>;

// --- Bulk resume/retry preview ----------------------------------------------

export const BulkPreviewRowSchema = z.strictObject({
  featureId: FeatureIdSchema,
  featureName: z.string().optional(),
  action: z.enum(['resume', 'retry']),
  enabled: z.boolean(),
  disabledReason: z.string().optional(),
  repos: z.array(z.string()).optional(),
});
export type BulkPreviewRow = z.output<typeof BulkPreviewRowSchema>;

export const BulkPreviewSchema = z.strictObject({
  previewId: z.string().min(1).max(200),
  eligible: z.array(BulkPreviewRowSchema).max(500),
  excluded: z.array(BulkPreviewRowSchema).max(500),
});
export type BulkPreview = z.output<typeof BulkPreviewSchema>;

// --- Run history (GET /runs, GET /runs/{n}, GET /runs/{n}/sessions) ---------

export const RunSummaryViewSchema = z.strictObject({
  runNumber: z.number().int().nonnegative(),
  startedAt: z.string().optional(),
  sealedAt: z.string().optional(),
  sealReason: z.string().optional(),
  currentPhase: z.string().optional(),
  phaseStatus: z.string().optional(),
  iteration: z.number().int().nonnegative().optional(),
  roadmapPhase: z.number().int().nonnegative().optional(),
  roadmapTotal: z.number().int().nonnegative().optional(),
  pendingReviewPhase: z.string().optional(),
  isRewind: z.boolean().optional(),
  artifactCount: z.number().int().nonnegative(),
  hasNeedUserGate: z.boolean().optional(),
});
export type RunSummaryView = z.output<typeof RunSummaryViewSchema>;

export const RunDetailViewSchema = RunSummaryViewSchema.extend({
  rewindTarget: z.string().optional(),
  rewindRoadmapPhase: z.number().int().nonnegative().optional(),
  carriedFromRun: z.number().int().nonnegative().optional(),
  carriedPhases: z.array(z.string()).max(200).optional(),
  backupBranchRepos: z.array(z.string()).max(200).optional(),
  committing: z.boolean().optional(),
  timing: z
    .strictObject({ totalSeconds: z.number().int(), byPhase: z.record(z.string(), z.number()) })
    .optional(),
  cost: z
    .strictObject({ totalUsd: z.number(), byPhase: z.record(z.string(), z.number()) })
    .optional(),
});
export type RunDetailView = z.output<typeof RunDetailViewSchema>;

export const RunListResultSchema = z.strictObject({
  runs: z.array(RunSummaryViewSchema).max(10000),
  page: z.number().int().positive(),
  pageSize: z.number().int().positive(),
  total: z.number().int().nonnegative(),
  totalPages: z.number().int().nonnegative(),
});
export type RunListResult = z.output<typeof RunListResultSchema>;

export const RunListRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  page: z.number().int().positive().optional(),
  pageSize: z.number().int().positive().max(100).optional(),
});
export type RunListRequest = z.output<typeof RunListRequestSchema>;

export const RunGetRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  runNumber: z.number().int().positive(),
});
export type RunGetRequest = z.output<typeof RunGetRequestSchema>;

// RunSessionsListResultSchema is declared after SessionSummarySchema below.

export const RunArtifactsListRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  runNumber: z.number().int().positive(),
});
export type RunArtifactsListRequest = z.output<typeof RunArtifactsListRequestSchema>;

export const RunArtifactViewSchema = z.strictObject({
  id: z.string().min(1).max(500),
  type: z.string().max(100).optional(),
  category: z.string().max(100).optional(),
  runNumber: z.number().int().nonnegative(),
  phase: z.string().max(200).optional(),
  size: z.number().int().nonnegative().optional(),
  modifiedAt: z.string().optional(),
  contentAvailable: z.boolean().optional(),
});
export type RunArtifactView = z.output<typeof RunArtifactViewSchema>;

export const RunArtifactsListResultSchema = z.strictObject({
  artifacts: z.array(RunArtifactViewSchema).max(10000),
});
export type RunArtifactsListResult = z.output<typeof RunArtifactsListResultSchema>;

/** Maximum bounded history text response accepted anywhere in the desktop. */
export const MAX_RUN_CONTENT_BYTES = 256 * 1024;

export const RunArtifactContentRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  runNumber: z.number().int().positive(),
  artifactId: z.string().min(1).max(500),
  offset: z.number().int().nonnegative().optional(),
  limit: z.number().int().positive().max(MAX_RUN_CONTENT_BYTES).optional(),
});
export type RunArtifactContentRequest = z.output<typeof RunArtifactContentRequestSchema>;

export const RunTextContentSchema = z.strictObject({
  id: z.string().min(1).max(500),
  offset: z.number().int().nonnegative(),
  limit: z.number().int().positive(),
  size: z.number().int().nonnegative(),
  text: z.string(),
  truncated: z.boolean(),
});
export type RunTextContent = z.output<typeof RunTextContentSchema>;

export const RunLogContentRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  runNumber: z.number().int().positive(),
  logId: z.string().min(1).max(200),
  offset: z.number().int().nonnegative().optional(),
  limit: z.number().int().positive().max(MAX_RUN_CONTENT_BYTES).optional(),
});
export type RunLogContentRequest = z.output<typeof RunLogContentRequestSchema>;

// --- Rewind preview + execution --------------------------------------------

export const RewindChoiceViewSchema = z.strictObject({
  phase: z.string().min(1),
  escalatesTo: z.string().optional(),
  overridePhase: z.string().optional(),
});
export type RewindChoiceView = z.output<typeof RewindChoiceViewSchema>;

export const RewindPRConsequenceViewSchema = z.strictObject({
  repo: z.string(),
  prUrl: z.string(),
});
export type RewindPRConsequenceView = z.output<typeof RewindPRConsequenceViewSchema>;

export const RewindWorktreeConsequenceViewSchema = z.strictObject({
  repo: z.string(),
  resetKind: z.enum(['anchor', 'base', 'base-local', 'none']),
});
export type RewindWorktreeConsequenceView = z.output<typeof RewindWorktreeConsequenceViewSchema>;

export const RewindPreviewViewSchema = z.strictObject({
  eligible: z.boolean(),
  sourceRunNumber: z.number().int().nonnegative(),
  sourceRevision: z.string(),
  targetPhase: z.string(),
  effectivePhase: z.string(),
  roadmapPhase: z.number().int().nonnegative().optional(),
  upgradePipeline: z.string().optional(),
  validPhases: z.array(RewindChoiceViewSchema).max(50).optional(),
  validRoadmapPhases: z.array(z.number().int().positive()).max(50).optional(),
  upgradePipelineOptions: z.array(z.string()).max(20).optional(),
  carriedPhases: z.array(z.string()).max(200).optional(),
  carriedFromRun: z.number().int().nonnegative().optional(),
  prConsequences: z.array(RewindPRConsequenceViewSchema).max(200).optional(),
  worktreeConsequences: z.array(RewindWorktreeConsequenceViewSchema).max(200).optional(),
  backupBranchRepos: z.array(z.string()).max(200).optional(),
  validationFindings: z.array(z.string()).max(50).optional(),
});
export type RewindPreviewView = z.output<typeof RewindPreviewViewSchema>;

export const RewindPreviewRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  targetPhase: z.string().min(1).max(200),
  roadmapPhase: z.number().int().positive().optional(),
  upgradePipeline: z.string().max(200).optional(),
});
export type RewindPreviewRequest = z.output<typeof RewindPreviewRequestSchema>;

export const RewindExecuteRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  targetPhase: z.string().min(1).max(200),
  roadmapPhase: z.number().int().positive().optional(),
  upgradePipeline: z.string().max(200).optional(),
  sourceRunNumber: z.number().int().nonnegative().optional(),
  sourceRevision: z.string().max(512).optional(),
});
export type RewindExecuteRequest = z.output<typeof RewindExecuteRequestSchema>;

// --- Blocking attention ----------------------------------------------------

const AttentionIDSchema = z.string().min(1).max(200);
const AttentionTextSchema = z.string().max(64 * 1024);
export const AttentionOptionSchema = z.strictObject({
  label: z.string().max(200),
  description: AttentionTextSchema.optional(),
  confidence: z.number().min(0).max(1).optional(),
});
export const AttentionQuestionSchema = z.strictObject({
  key: AttentionTextSchema,
  header: z.string().max(500),
  multiSelect: z.boolean(),
  options: z.array(AttentionOptionSchema).max(100),
});
export const AttentionPermissionSchema = z.strictObject({
  kind: z.literal('permission'),
  id: AttentionIDSchema,
  featureId: FeatureIdSchema.optional(),
  sessionId: AttentionIDSchema.optional(),
  phase: z.string().max(200).optional(),
  toolName: z.string().max(500),
  summary: AttentionTextSchema.optional(),
  input: z.record(z.string().max(200), z.unknown()).optional(),
  waitingSince: z.string().max(100),
  remember: z
    .strictObject({
      pattern: z.string().max(4096),
      scope: z.string().max(4096),
      scopeDisplay: z.string().max(4096),
    })
    .optional(),
});
export const AttentionQuestionBundleSchema = z.strictObject({
  kind: z.literal('questions'),
  id: AttentionIDSchema,
  featureId: FeatureIdSchema.optional(),
  sessionId: AttentionIDSchema.optional(),
  phase: z.string().max(200).optional(),
  waitingSince: z.string().max(100),
  questions: z.array(AttentionQuestionSchema).min(1).max(100),
});
export const AttentionHelpSchema = z.strictObject({
  kind: z.literal('help'),
  id: AttentionIDSchema,
  featureId: FeatureIdSchema,
  sessionId: AttentionIDSchema.optional(),
  waitingSince: z.string().max(100),
  prompt: AttentionTextSchema,
});
export const AttentionGateSchema = z.strictObject({
  kind: z.literal('gate'),
  id: z.string().min(1).max(1000),
  featureId: FeatureIdSchema,
  waitingSince: z.string().max(100),
  scope: z.string().max(100).optional(),
  repoName: z.string().max(500).optional(),
  cycleType: z.string().max(200).optional(),
  iteration: z.number().int().nonnegative().optional(),
  summary: AttentionTextSchema.optional(),
  questions: z
    .array(
      z.strictObject({
        index: z.number().int().nonnegative(),
        prompt: AttentionTextSchema,
        answer: AttentionTextSchema,
      }),
    )
    .max(100),
});
/** A review is actionable only in the cockpit; the inbox is a deliberate jump. */
export const AttentionReviewSchema = z.strictObject({
  kind: z.literal('review'),
  id: AttentionIDSchema,
  featureId: FeatureIdSchema,
  waitingSince: z.string().max(100),
  reviewKind: z.string().max(200),
  phase: z.string().max(200),
});
/** Recovery items are renderer-synthesized from the recovery scan and sorted
 * ahead of all other attention so recovery receives contextual priority. */
export const AttentionRecoverySchema = z.strictObject({
  kind: z.literal('recovery'),
  id: z.string().min(1).max(1000),
  waitingSince: z.string().max(100),
  liveCount: z.number().int().nonnegative(),
  deadCount: z.number().int().nonnegative(),
});
export const AttentionItemSchema = z.discriminatedUnion('kind', [
  AttentionPermissionSchema,
  AttentionQuestionBundleSchema,
  AttentionHelpSchema,
  AttentionGateSchema,
  AttentionReviewSchema,
  AttentionRecoverySchema,
]);
export type AttentionItem = z.output<typeof AttentionItemSchema>;
export const AttentionSnapshotSchema = z.strictObject({
  items: z.array(AttentionItemSchema).max(4000),
});
export type AttentionSnapshot = z.output<typeof AttentionSnapshotSchema>;
export const PermissionDecisionRequestSchema = z.strictObject({
  requestId: AttentionIDSchema,
  sessionId: AttentionIDSchema.optional(),
  decision: z.enum(['allow_once', 'allow_remember', 'deny']),
  rememberPattern: z.string().max(4096).optional(),
  rememberScope: z.string().max(4096).optional(),
});
export type PermissionDecisionRequest = z.output<typeof PermissionDecisionRequestSchema>;
export const AskUserAnswerRequestSchema = z.strictObject({
  requestId: AttentionIDSchema,
  sessionId: AttentionIDSchema.optional(),
  answers: z
    .record(AttentionTextSchema, AttentionTextSchema)
    .refine((answers) => Object.keys(answers).length > 0),
});
export type AskUserAnswerRequest = z.output<typeof AskUserAnswerRequestSchema>;
export const HelpAnswerRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  sessionId: AttentionIDSchema.optional(),
  message: AttentionTextSchema.refine((value) => value.trim() !== ''),
});
export type HelpAnswerRequest = z.output<typeof HelpAnswerRequestSchema>;
export const GateTargetSchema = z.strictObject({
  featureId: FeatureIdSchema,
  repoName: z.string().max(500).optional(),
  cycleType: z.string().max(200).optional(),
});
export const GateDraftRequestSchema = GateTargetSchema.extend({
  answers: z
    .record(AttentionTextSchema, AttentionTextSchema)
    .refine((answers) => Object.keys(answers).length > 0),
});
export type GateDraftRequest = z.output<typeof GateDraftRequestSchema>;
export const GateResolutionRequestSchema = GateTargetSchema.extend({
  decision: z.enum(['resume', 'abort']),
});
export type GateResolutionRequest = z.output<typeof GateResolutionRequestSchema>;
export const AttentionActionResultSchema = z.strictObject({
  result: z.string().max(500),
  alreadyResolved: z.boolean().optional(),
  notice: z.string().max(500).optional(),
});
export type AttentionActionResult = z.output<typeof AttentionActionResultSchema>;

// --- Singleton AMA chat -----------------------------------------------------

export const CHAT_SESSION_ID = '__chat__';

export const ChatStartRequestSchema = z.strictObject({
  message: AttentionTextSchema.refine((value) => value.trim() !== ''),
});
export type ChatStartRequest = z.output<typeof ChatStartRequestSchema>;

// --- Sessions and bounded transcript/output operations ---------------------

/** Canonical safe URL-segment syntax for server-owned session identifiers. */
export const SESSION_ID_SEGMENT_PATTERN = '[a-z0-9._-]{1,200}';

export const SessionIdSchema = z.string().regex(new RegExp(`^${SESSION_ID_SEGMENT_PATTERN}$`, 'i'));
export type SessionId = z.output<typeof SessionIdSchema>;

export const ChatActionResultSchema = z.strictObject({
  sessionId: SessionIdSchema,
  result: z.string().max(500),
});
export type ChatActionResult = z.output<typeof ChatActionResultSchema>;

const BoundedTextSchema = z.string().max(1024 * 1024);
const OptionalBoundedTextSchema = BoundedTextSchema.optional();

export const SessionUsageSchema = z.strictObject({
  inputTokens: z.number().int().nonnegative().optional(),
  outputTokens: z.number().int().nonnegative().optional(),
  costUsd: z.number().nonnegative().optional(),
});

export const SessionSummarySchema = z.strictObject({
  id: SessionIdSchema,
  featureId: FeatureIdSchema,
  runNumber: z.number().int().nonnegative(),
  phase: z.string().max(200),
  repo: z.string().max(500).optional(),
  kind: z.string().max(200),
  label: z.string().max(500).optional(),
  provider: z.string().max(200).optional(),
  model: z.string().max(500).optional(),
  status: z.string().max(200),
  turnState: z.string().max(200).optional(),
  startedAt: z.string().max(100),
  iteration: z.number().int().nonnegative().optional(),
  contextPercentage: z.number().int().min(0).max(100).optional(),
  usage: SessionUsageSchema,
});
export type SessionSummary = z.output<typeof SessionSummarySchema>;

export const TERMINAL_CHAT_STATUSES = [
  'complete',
  'completed',
  'done',
  'ended',
  'failed',
  'cancelled',
  'canceled',
  'stopped',
  'not_active',
] as const;

const TERMINAL_CHAT_STATUS_SET = new Set<string>(TERMINAL_CHAT_STATUSES);

export function isTerminalChatStatus(status: string): boolean {
  return TERMINAL_CHAT_STATUS_SET.has(status.toLocaleLowerCase());
}

export function isActiveChatSession(
  session: Pick<SessionSummary, 'id' | 'featureId' | 'kind' | 'status'>,
): boolean {
  const isChat =
    session.id === CHAT_SESSION_ID ||
    session.featureId === CHAT_SESSION_ID ||
    session.kind.toLocaleLowerCase() === 'chat';
  return isChat && !isTerminalChatStatus(session.status);
}

export const RunSessionsListResultSchema = z.strictObject({
  runNumber: z.number().int().positive(),
  sessions: z.array(SessionSummarySchema).max(1000),
});
export type RunSessionsListResult = z.output<typeof RunSessionsListResultSchema>;

export const TranscriptCursorSchema = z.strictObject({
  total: z.number().int().nonnegative(),
  start: z.number().int().nonnegative(),
  end: z.number().int().nonnegative(),
});
export type TranscriptCursor = z.output<typeof TranscriptCursorSchema>;

export const FileChangeSchema = z.strictObject({
  path: OptionalBoundedTextSchema,
  oldPath: OptionalBoundedTextSchema,
  operation: z.string().max(200).optional(),
  detail: OptionalBoundedTextSchema,
  addedLines: z.number().int().nonnegative().optional(),
  removedLines: z.number().int().nonnegative().optional(),
  hasDiffPatch: z.boolean().optional(),
});

export const ToolCallSchema = z.strictObject({
  summary: OptionalBoundedTextSchema,
  prompt: OptionalBoundedTextSchema,
});

export const TranscriptTaskSchema = z.strictObject({
  id: z.string().max(500).optional(),
  toolUseId: z.string().max(500).optional(),
  description: OptionalBoundedTextSchema,
  taskType: z.string().max(200).optional(),
  prompt: OptionalBoundedTextSchema,
  lastToolName: z.string().max(500).optional(),
  status: z.string().max(200).optional(),
  summary: OptionalBoundedTextSchema,
  outputFile: OptionalBoundedTextSchema,
});

/** Original validated source record retained for semantic timeline/raw inspection. */
export const TranscriptMessageSchema = z.strictObject({
  index: z.number().int().nonnegative(),
  blockIndex: z.number().int().nonnegative().optional(),
  role: z.string().max(200),
  type: z.string().max(200),
  text: OptionalBoundedTextSchema,
  tool: z.string().max(500).optional(),
  status: z.string().max(200).optional(),
  redacted: z.boolean().optional(),
  locallyAppended: z.boolean().optional(),
  autoPicked: z.boolean().optional(),
  autoPickQuestion: OptionalBoundedTextSchema,
  autoPickConfidence: z.number().min(0).max(1).optional(),
  fileChange: FileChangeSchema.optional(),
  toolCall: ToolCallSchema.optional(),
  task: TranscriptTaskSchema.optional(),
});
export type TranscriptMessage = z.output<typeof TranscriptMessageSchema>;

export const SessionDetailSchema = SessionSummarySchema.extend({
  transcriptCursor: TranscriptCursorSchema,
  pendingControlCount: z.number().int().nonnegative(),
  initialPrompt: OptionalBoundedTextSchema,
  canAttach: z.boolean(),
  logAvailable: z.boolean(),
  safeError: OptionalBoundedTextSchema,
});
export type SessionDetail = z.output<typeof SessionDetailSchema>;

export const SessionTranscriptRequestSchema = z.strictObject({
  sessionId: SessionIdSchema,
  offset: z.number().int().nonnegative().optional(),
  limit: z.number().int().min(1).max(500).optional(),
});
export type SessionTranscriptRequest = z.output<typeof SessionTranscriptRequestSchema>;

export const SessionTranscriptSchema = z.strictObject({
  sessionId: SessionIdSchema,
  cursor: TranscriptCursorSchema,
  messages: z.array(TranscriptMessageSchema).max(500),
});
export type SessionTranscript = z.output<typeof SessionTranscriptSchema>;

export const SessionOutputOpenRequestSchema = z.strictObject({
  sessionId: SessionIdSchema,
  /** Transcript row index, deliberately not global epoch/sequence. */
  from: z.number().int().nonnegative().optional(),
});
export type SessionOutputOpenRequest = z.output<typeof SessionOutputOpenRequestSchema>;

export const SubscriptionIdSchema = z
  .string()
  .min(1)
  .max(100)
  .regex(/^[a-z0-9-]+$/i);
export const SessionOutputOpenResultSchema = z.strictObject({
  subscriptionId: SubscriptionIdSchema,
});
export type SessionOutputOpenResult = z.output<typeof SessionOutputOpenResultSchema>;

export const SessionOutputCancelRequestSchema = z.strictObject({
  subscriptionId: SubscriptionIdSchema,
});
export const SessionOutputCancelResultSchema = z.strictObject({ cancelled: z.boolean() });

export const SessionOutputEventSchema = z.discriminatedUnion('type', [
  z.strictObject({
    subscriptionId: SubscriptionIdSchema,
    type: z.literal('record'),
    sessionId: SessionIdSchema,
    index: z.number().int().nonnegative(),
    message: TranscriptMessageSchema,
  }),
  z.strictObject({
    subscriptionId: SubscriptionIdSchema,
    type: z.literal('done'),
    sessionId: SessionIdSchema,
    nextIndex: z.number().int().nonnegative(),
  }),
  z.strictObject({
    subscriptionId: SubscriptionIdSchema,
    type: z.literal('error'),
    sessionId: SessionIdSchema,
    error: SafeErrorSchema,
  }),
]);
export type SessionOutputEvent = z.output<typeof SessionOutputEventSchema>;

// --- Feature creation ---------------------------------------------------------

/** The narrow creation input, validated at both IPC boundaries. */
export const CreateFeatureInputSchema = z.strictObject({
  name: z
    .string()
    .min(1)
    .max(200)
    .refine((value) => value.trim() !== '', { message: 'A feature name is required.' }),
  description: z.string().max(10000),
  /** Repository keys from server workspace discovery. */
  repoKeys: z.array(z.string().min(1).max(200)).min(1).max(32),
  /** Branch choice: reuse the current branch instead of a feature branch. */
  useCurrentBranch: z.boolean(),
});

export type CreateFeatureInput = z.output<typeof CreateFeatureInputSchema>;

export const CreateFeatureResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
});

export type CreateFeatureResult = z.output<typeof CreateFeatureResultSchema>;

export const SetupDispatchResultSchema = z.strictObject({
  result: z.string(),
});

export type SetupDispatchResult = z.output<typeof SetupDispatchResultSchema>;

/**
 * Fresh server-provided creation context: discovered repositories with
 * eligibility, and the defaults the creation contract applies server-side.
 */
export const CreationDefaultsSchema = z.strictObject({
  repositories: z.array(RepositoryStateSchema),
  defaults: z.strictObject({
    pipeline: z.string().optional(),
    inquireness: z.string().optional(),
    /** Per-phase default models, for read-only display. */
    models: z.array(z.strictObject({ phase: z.string(), model: z.string() })),
    /** Server default branch choice: false ⇒ new feature branch. */
    useCurrentBranch: z.boolean(),
  }),
});

export type CreationDefaults = z.output<typeof CreationDefaultsSchema>;

// --- Theme ------------------------------------------------------------------

export const ThemePreferenceSchema = z.enum(['light', 'dark', 'system']);
export type ThemePreference = z.output<typeof ThemePreferenceSchema>;

export const ThemeInfoSchema = z.strictObject({
  preference: ThemePreferenceSchema,
  resolved: z.enum(['light', 'dark']),
});

export type ThemeInfo = z.output<typeof ThemeInfoSchema>;

// --- Settings (app-local presentation/runtime-selection data ONLY) ---------
// Never store features, runs, configuration, credentials, or any
// server-domain snapshot here.

export const SETTINGS_SCHEMA_VERSION = 1;

export const WindowBoundsSchema = z.strictObject({
  x: z.number().int(),
  y: z.number().int(),
  width: z.number().int().min(1),
  height: z.number().int().min(1),
});

/**
 * Wizard presentation preferences ONLY: a path *hint* for preselecting the
 * repository picker and collapsed-help state. Wizard progress is never
 * stored locally — completed gates always derive from the server snapshot.
 */
export const WizardPrefsSchema = z.strictObject({
  collapsedHelp: z.boolean(),
  lastRepositoryPathHint: z.string().max(4096).nullable(),
});

export type WizardPrefs = z.output<typeof WizardPrefsSchema>;

export function defaultWizardPrefs(): WizardPrefs {
  return { collapsedHelp: false, lastRepositoryPathHint: null };
}

/**
 * AMA presentation preferences ONLY. Transcript rows and chat archive live on
 * the server; the app stores only whether the drawer is compact or expanded.
 */
export const AmaPrefsSchema = z.strictObject({
  drawer: z.enum(['compact', 'expanded']),
});

export type AmaPrefs = z.output<typeof AmaPrefsSchema>;

export function defaultAmaPrefs(): AmaPrefs {
  return { drawer: 'compact' };
}

/**
 * Native notification presentation preference ONLY. Preview is off by default
 * so OS notifications contain no domain content unless explicitly enabled.
 */
export const NotificationPrefsSchema = z.strictObject({
  previewEnabled: z.boolean(),
});

export type NotificationPrefs = z.output<typeof NotificationPrefsSchema>;

export function defaultNotificationPrefs(): NotificationPrefs {
  return { previewEnabled: false };
}

/**
 * Open feature tabs: strictly identity plus presentation. The title is a
 * *hint* used only until the authoritative feature loads; feature state,
 * setup progress, and any other server-domain data are never stored here.
 */
export const FeatureTabSchema = z.strictObject({
  featureId: FeatureIdSchema,
  titleHint: z.string().max(200),
  /** Selected sealed run for archive mode; null/absent means current run. */
  selectedRunNumber: z.number().int().nonnegative().nullable().optional(),
});

export type FeatureTab = z.output<typeof FeatureTabSchema>;

export const TabsPrefsSchema = z.strictObject({
  /** Open feature tabs in display order. */
  open: z.array(FeatureTabSchema).max(50),
  /**
   * Active tab; null means the Home tab. The sentinel '__settings__'
   * represents the Settings tab — it passes the feature-id regex but is
   * not a real feature id.
   */
  activeFeatureId: FeatureIdSchema.nullable(),
});

export type TabsPrefs = z.output<typeof TabsPrefsSchema>;

export function defaultTabsPrefs(): TabsPrefs {
  return { open: [], activeFeatureId: null };
}

export const SettingsSchema = z.strictObject({
  schemaVersion: z.literal(SETTINGS_SCHEMA_VERSION),
  runtime: z.strictObject({
    /** Identifier of the user's preferred runtime — never a URL or token. */
    selection: z.string().max(200).nullable(),
  }),
  window: z.strictObject({
    bounds: WindowBoundsSchema.optional(),
  }),
  theme: ThemePreferenceSchema,
  wizard: WizardPrefsSchema.default(defaultWizardPrefs()),
  ama: AmaPrefsSchema.default(defaultAmaPrefs()),
  notifications: NotificationPrefsSchema.default(defaultNotificationPrefs()),
  tabs: TabsPrefsSchema.default(defaultTabsPrefs()),
});

export type Settings = z.output<typeof SettingsSchema>;

export const SettingsPatchSchema = z.strictObject({
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }).optional(),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }).optional(),
  theme: ThemePreferenceSchema.optional(),
  wizard: WizardPrefsSchema.optional(),
  ama: AmaPrefsSchema.optional(),
  notifications: NotificationPrefsSchema.optional(),
  tabs: TabsPrefsSchema.optional(),
});

export type SettingsPatch = z.output<typeof SettingsPatchSchema>;

export function defaultSettings(): Settings {
  return {
    schemaVersion: SETTINGS_SCHEMA_VERSION,
    runtime: { selection: null },
    window: {},
    theme: 'system',
    wizard: defaultWizardPrefs(),
    ama: defaultAmaPrefs(),
    notifications: defaultNotificationPrefs(),
    tabs: defaultTabsPrefs(),
  };
}

// --- Recoverable local review drafts --------------------------------------
// These are intentionally not server review-session snapshots. The app stores
// only a user's unsaved text, its lookup key, and a save timestamp so that a
// renderer can honestly distinguish recovered local text from server state.

export const LOCAL_DRAFT_STORE_SCHEMA_VERSION = 1;
export const MAX_LOCAL_REVIEW_DRAFT_BYTES = 1024 * 1024;

export const ReviewIdSchema = z
  .string()
  .min(1)
  .max(200)
  .regex(/^[a-z0-9._-]+$/i);
export type ReviewId = z.output<typeof ReviewIdSchema>;

export const DraftRevisionSchema = z.string().min(1).max(512);
export type DraftRevision = z.output<typeof DraftRevisionSchema>;

/** An opaque runtime identity, typically the selected runtime's state dir. */
export const RuntimeIdSchema = z.string().min(1).max(4096);
export type RuntimeId = z.output<typeof RuntimeIdSchema>;

/** Fallback runtime identity when no runtime is selected. */
export const DEFAULT_RUNTIME_ID = 'default-runtime';

/** True when a feature status indicates a pending review gate. */
export function isPendingReviewStatus(status: string): boolean {
  return status.endsWith('NeedsReview');
}

/** Human-readable label for a review kind, stripping the status suffix. */
export function reviewKindLabel(reviewKind: string): string {
  return reviewKind.replace(/NeedsReview$/, '');
}

/** The identity of exactly one unsaved review buffer. */
export const ReviewDraftKeySchema = z.strictObject({
  runtimeId: RuntimeIdSchema,
  featureId: FeatureIdSchema,
  reviewId: ReviewIdSchema,
  baseDraftRevision: DraftRevisionSchema,
});
export type ReviewDraftKey = z.output<typeof ReviewDraftKeySchema>;

export const LocalReviewDraftSchema = ReviewDraftKeySchema.extend({
  text: z.string().max(MAX_LOCAL_REVIEW_DRAFT_BYTES),
  savedAt: z.string().datetime({ offset: true }),
});
export type LocalReviewDraft = z.output<typeof LocalReviewDraftSchema>;

export const LocalReviewDraftSaveRequestSchema = ReviewDraftKeySchema.extend({
  text: z.string().max(MAX_LOCAL_REVIEW_DRAFT_BYTES),
});
export type LocalReviewDraftSaveRequest = z.output<typeof LocalReviewDraftSaveRequestSchema>;

/** Finds the most recent unsaved buffer for one review, regardless of its base revision. */
export const LocalReviewDraftLookupRequestSchema = ReviewDraftKeySchema.pick({
  runtimeId: true,
  featureId: true,
  reviewId: true,
}).extend({ baseDraftRevision: DraftRevisionSchema.optional() });
export type LocalReviewDraftLookupRequest = z.output<typeof LocalReviewDraftLookupRequestSchema>;

export const LocalReviewDraftDiscardRequestSchema = ReviewDraftKeySchema;
export type LocalReviewDraftDiscardRequest = z.output<typeof LocalReviewDraftDiscardRequestSchema>;

export const LocalReviewDraftDiscardResultSchema = z.strictObject({ discarded: z.boolean() });
export type LocalReviewDraftDiscardResult = z.output<typeof LocalReviewDraftDiscardResultSchema>;

/** Disk-only envelope. Versioning keeps incompatible persisted content safe. */
export const LocalReviewDraftStoreSchema = z.strictObject({
  schemaVersion: z.literal(LOCAL_DRAFT_STORE_SCHEMA_VERSION),
  drafts: z.array(LocalReviewDraftSchema).max(20),
});
export type LocalReviewDraftStore = z.output<typeof LocalReviewDraftStoreSchema>;

export const ReviewSessionSchema = z.strictObject({
  featureId: FeatureIdSchema,
  reviewId: ReviewIdSchema,
  reviewMode: z.string().min(1).max(200),
  targetPhase: z.string().min(1).max(200),
  runNumber: z.number().int().nonnegative(),
  artifactId: z.string().min(1).max(500),
  text: z.string().max(2 * 1024 * 1024),
  draftRevision: DraftRevisionSchema,
  sourceRevision: DraftRevisionSchema,
  canIterate: z.boolean(),
});
export type ReviewSession = z.output<typeof ReviewSessionSchema>;

export const ReviewReadRequestSchema = z.strictObject({ featureId: FeatureIdSchema });
export type ReviewReadRequest = z.output<typeof ReviewReadRequestSchema>;
export const ReviewSaveRequestSchema = ReviewReadRequestSchema.extend({
  reviewId: ReviewIdSchema,
  baseRevision: DraftRevisionSchema,
  text: z.string().max(2 * 1024 * 1024),
});
export type ReviewSaveRequest = z.output<typeof ReviewSaveRequestSchema>;
export const ReviewValidateRequestSchema = ReviewReadRequestSchema.extend({
  reviewId: ReviewIdSchema,
  text: z.string().max(2 * 1024 * 1024),
});
export type ReviewValidateRequest = z.output<typeof ReviewValidateRequestSchema>;
export const ReviewDecisionRequestSchema = ReviewReadRequestSchema.extend({
  reviewId: ReviewIdSchema,
  baseRevision: DraftRevisionSchema,
  decision: z.enum(['proceed', 'iterate']),
});
export type ReviewDecisionRequest = z.output<typeof ReviewDecisionRequestSchema>;
export const ReviewValidationSchema = z.strictObject({
  applicable: z.boolean(),
  valid: z.boolean(),
  revision: DraftRevisionSchema,
  findings: z
    .array(z.strictObject({ code: z.string().min(1), message: z.string().min(1) }))
    .max(100),
});
export type ReviewValidation = z.output<typeof ReviewValidationSchema>;
export const ReviewConflictSchema = z.strictObject({
  type: z.literal('conflict'),
  expectedRevision: DraftRevisionSchema,
  currentRevision: DraftRevisionSchema,
});
export const ReviewSaveResultSchema = z.discriminatedUnion('type', [
  z.strictObject({ type: z.literal('saved'), session: ReviewSessionSchema }),
  ReviewConflictSchema,
]);
export type ReviewSaveResult = z.output<typeof ReviewSaveResultSchema>;
export const ReviewDecisionResultSchema = z.discriminatedUnion('type', [
  z.strictObject({ type: z.literal('saved'), result: z.string().min(1).max(500) }),
  ReviewConflictSchema,
]);
export type ReviewDecisionResult = z.output<typeof ReviewDecisionResultSchema>;

// --- Editable resources (feature config, runtime config, skills, guidelines) ---

export const ResourceKindSchema = z.enum([
  'feature_config',
  'runtime_config',
  'skill',
  'guideline',
]);
export type ResourceKind = z.output<typeof ResourceKindSchema>;

export const ResourceContentTypeSchema = z.enum(['yaml', 'markdown', 'text']);
export type ResourceContentType = z.output<typeof ResourceContentTypeSchema>;

export const ResourceEffectSchema = z.enum([
  'immediate',
  'next_dispatch',
  'next_session',
  'restart_required',
]);
export type ResourceEffect = z.output<typeof ResourceEffectSchema>;

export const ResourceFindingSchema = z.strictObject({
  code: z.string().min(1),
  message: z.string().min(1),
  field: z.string().optional(),
});
export type ResourceFinding = z.output<typeof ResourceFindingSchema>;

export const ResourceEntrySchema = z.strictObject({
  id: z.string().min(1).max(256),
  kind: ResourceKindSchema,
  label: z.string().min(1).max(500),
  contentType: ResourceContentTypeSchema,
  revision: z.string().min(1).max(128),
  effect: ResourceEffectSchema.optional(),
  validatable: z.boolean(),
  hierarchy: z.array(z.string().max(200)).max(50).optional(),
  featureId: z.string().max(200).optional(),
});
export type ResourceEntry = z.output<typeof ResourceEntrySchema>;

export const ResourceCatalogueSchema = z.strictObject({
  resources: z.array(ResourceEntrySchema).max(5000),
  truncated: z.boolean().optional(),
});
export type ResourceCatalogue = z.output<typeof ResourceCatalogueSchema>;

export const ResourceReadSchema = z.strictObject({
  id: z.string().min(1).max(256),
  kind: ResourceKindSchema,
  label: z.string().min(1).max(500),
  contentType: ResourceContentTypeSchema,
  revision: z.string().min(1).max(128),
  text: z.string().max(2 * 1024 * 1024),
  effect: ResourceEffectSchema.optional(),
  validatable: z.boolean(),
  hierarchy: z.array(z.string().max(200)).max(50).optional(),
  featureId: z.string().max(200).optional(),
});
export type ResourceRead = z.output<typeof ResourceReadSchema>;

export const ResourceValidateRequestSchema = z.strictObject({
  resourceId: z.string().min(1).max(256),
  text: z.string().max(1024 * 1024),
});
export type ResourceValidateRequest = z.output<typeof ResourceValidateRequestSchema>;

export const ResourceValidateResultSchema = z.strictObject({
  id: z.string().min(1).max(256),
  valid: z.boolean(),
  revision: z.string().max(128),
  findings: z.array(ResourceFindingSchema).max(100),
});
export type ResourceValidateResult = z.output<typeof ResourceValidateResultSchema>;

export const ResourceWriteRequestSchema = z.strictObject({
  resourceId: z.string().min(1).max(256),
  baseRevision: z.string().min(1).max(128),
  text: z.string().max(1024 * 1024),
});
export type ResourceWriteRequest = z.output<typeof ResourceWriteRequestSchema>;

export const ResourceWriteResultSchema = z.discriminatedUnion('type', [
  z.strictObject({
    type: z.literal('saved'),
    id: z.string().min(1).max(256),
    revision: z.string().min(1).max(128),
    effect: ResourceEffectSchema.optional(),
  }),
  z.strictObject({
    type: z.literal('conflict'),
    id: z.string().min(1).max(256),
    expectedRevision: z.string().max(128),
    currentRevision: z.string().min(1).max(128),
    currentText: z.string().max(2 * 1024 * 1024),
  }),
]);
export type ResourceWriteResult = z.output<typeof ResourceWriteResultSchema>;

// --- Recoverable local resource drafts (generalized from review drafts) ---

export const LOCAL_RESOURCE_DRAFT_STORE_SCHEMA_VERSION = 1;
export const MAX_LOCAL_RESOURCE_DRAFT_BYTES = 1024 * 1024;

export const ResourceDraftKeySchema = z.strictObject({
  runtimeId: RuntimeIdSchema,
  resourceId: z.string().min(1).max(256),
  baseRevision: DraftRevisionSchema,
});
export type ResourceDraftKey = z.output<typeof ResourceDraftKeySchema>;

export const LocalResourceDraftSchema = ResourceDraftKeySchema.extend({
  text: z.string().max(MAX_LOCAL_RESOURCE_DRAFT_BYTES),
  savedAt: z.string().datetime({ offset: true }),
});
export type LocalResourceDraft = z.output<typeof LocalResourceDraftSchema>;

export const LocalResourceDraftSaveRequestSchema = ResourceDraftKeySchema.extend({
  text: z.string().max(MAX_LOCAL_RESOURCE_DRAFT_BYTES),
});
export type LocalResourceDraftSaveRequest = z.output<typeof LocalResourceDraftSaveRequestSchema>;

export const LocalResourceDraftLookupRequestSchema = ResourceDraftKeySchema.pick({
  runtimeId: true,
  resourceId: true,
}).extend({ baseRevision: DraftRevisionSchema.optional() });
export type LocalResourceDraftLookupRequest = z.output<
  typeof LocalResourceDraftLookupRequestSchema
>;

export const LocalResourceDraftDiscardRequestSchema = ResourceDraftKeySchema;
export type LocalResourceDraftDiscardRequest = z.output<
  typeof LocalResourceDraftDiscardRequestSchema
>;

export const LocalResourceDraftDiscardResultSchema = z.strictObject({ discarded: z.boolean() });
export type LocalResourceDraftDiscardResult = z.output<
  typeof LocalResourceDraftDiscardResultSchema
>;

export const LocalResourceDraftStoreSchema = z.strictObject({
  schemaVersion: z.literal(LOCAL_RESOURCE_DRAFT_STORE_SCHEMA_VERSION),
  drafts: z.array(LocalResourceDraftSchema).max(50),
});
export type LocalResourceDraftStore = z.output<typeof LocalResourceDraftStoreSchema>;

// --- Per-channel contracts ---------------------------------------------------

export interface IpcContract {
  /** Tuple schema for the invoke arguments. */
  request: z.ZodType<readonly unknown[]>;
  /** Schema for the successful response value. */
  response: z.ZodType;
}

export const ipcContracts: Record<IpcChannel, IpcContract> = {
  [IPC_CHANNELS.connectionGetStatus]: {
    request: z.tuple([]),
    response: ConnectionStateSchema,
  },
  [IPC_CHANNELS.connectionRetry]: {
    request: z.tuple([]),
    response: ConnectionStateSchema,
  },
  [IPC_CHANNELS.connectionRestart]: {
    request: z.tuple([]),
    response: ConnectionStateSchema,
  },
  [IPC_CHANNELS.settingsGet]: {
    request: z.tuple([]),
    response: SettingsSchema,
  },
  [IPC_CHANNELS.settingsUpdate]: {
    request: z.tuple([SettingsPatchSchema]),
    response: SettingsSchema,
  },
  [IPC_CHANNELS.themeGet]: {
    request: z.tuple([]),
    response: ThemeInfoSchema,
  },
  [IPC_CHANNELS.themeSet]: {
    request: z.tuple([ThemePreferenceSchema]),
    response: ThemeInfoSchema,
  },
  [IPC_CHANNELS.readinessGet]: {
    request: z.tuple([]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.readinessRefresh]: {
    request: z.tuple([]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.workspacePickDirectory]: {
    request: z.tuple([]),
    response: PickedDirectorySchema,
  },
  [IPC_CHANNELS.workspaceAddRoot]: {
    request: z.tuple([AbsolutePathSchema]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.workspaceRemoveRoot]: {
    request: z.tuple([AbsolutePathSchema]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.workspaceReorderRoots]: {
    request: z.tuple([z.array(AbsolutePathSchema).max(100)]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.workspaceInitRepository]: {
    request: z.tuple([InitRepositoryRequestSchema]),
    response: ReadinessSnapshotSchema,
  },
  [IPC_CHANNELS.repositoriesList]: {
    request: z.tuple([]),
    response: z.array(RepositoryStateSchema),
  },
  [IPC_CHANNELS.featuresList]: {
    request: z.tuple([]),
    response: z.array(FeatureSummaryViewSchema),
  },
  [IPC_CHANNELS.featuresGet]: {
    request: z.tuple([FeatureIdSchema]),
    response: FeatureSnapshotSchema,
  },
  [IPC_CHANNELS.featuresCreate]: {
    request: z.tuple([CreateFeatureInputSchema]),
    response: CreateFeatureResultSchema,
  },
  [IPC_CHANNELS.featuresSetup]: {
    request: z.tuple([FeatureIdSchema]),
    response: SetupDispatchResultSchema,
  },
  [IPC_CHANNELS.featuresDispatchAction]: {
    request: z.tuple([FeatureActionRequestSchema]),
    response: FeatureActionResultSchema,
  },
  [IPC_CHANNELS.attentionGet]: { request: z.tuple([]), response: AttentionSnapshotSchema },
  [IPC_CHANNELS.attentionAnswerPermission]: {
    request: z.tuple([PermissionDecisionRequestSchema]),
    response: AttentionActionResultSchema,
  },
  [IPC_CHANNELS.attentionAnswerQuestions]: {
    request: z.tuple([AskUserAnswerRequestSchema]),
    response: AttentionActionResultSchema,
  },
  [IPC_CHANNELS.attentionSendHelp]: {
    request: z.tuple([HelpAnswerRequestSchema]),
    response: AttentionActionResultSchema,
  },
  [IPC_CHANNELS.attentionSaveGateDraft]: {
    request: z.tuple([GateDraftRequestSchema]),
    response: AttentionActionResultSchema,
  },
  [IPC_CHANNELS.attentionResolveGate]: {
    request: z.tuple([GateResolutionRequestSchema]),
    response: AttentionActionResultSchema,
  },
  [IPC_CHANNELS.chatStart]: {
    request: z.tuple([ChatStartRequestSchema]),
    response: ChatActionResultSchema,
  },
  [IPC_CHANNELS.chatEnd]: {
    request: z.tuple([]),
    response: ChatActionResultSchema,
  },
  [IPC_CHANNELS.sessionsList]: {
    request: z.tuple([]),
    response: z.array(SessionSummarySchema).max(1000),
  },
  [IPC_CHANNELS.sessionsGet]: {
    request: z.tuple([SessionIdSchema]),
    response: SessionDetailSchema,
  },
  [IPC_CHANNELS.sessionsTranscript]: {
    request: z.tuple([SessionTranscriptRequestSchema]),
    response: SessionTranscriptSchema,
  },
  [IPC_CHANNELS.sessionsOutputOpen]: {
    request: z.tuple([SessionOutputOpenRequestSchema]),
    response: SessionOutputOpenResultSchema,
  },
  [IPC_CHANNELS.sessionsOutputCancel]: {
    request: z.tuple([SessionOutputCancelRequestSchema]),
    response: SessionOutputCancelResultSchema,
  },
  [IPC_CHANNELS.creationDefaults]: {
    request: z.tuple([]),
    response: CreationDefaultsSchema,
  },
  [IPC_CHANNELS.reviewDraftsLoad]: {
    request: z.tuple([LocalReviewDraftLookupRequestSchema]),
    response: LocalReviewDraftSchema.nullable(),
  },
  [IPC_CHANNELS.reviewDraftsSave]: {
    request: z.tuple([LocalReviewDraftSaveRequestSchema]),
    response: LocalReviewDraftSchema,
  },
  [IPC_CHANNELS.reviewDraftsDiscard]: {
    request: z.tuple([LocalReviewDraftDiscardRequestSchema]),
    response: LocalReviewDraftDiscardResultSchema,
  },
  [IPC_CHANNELS.reviewsRead]: {
    request: z.tuple([ReviewReadRequestSchema]),
    response: ReviewSessionSchema,
  },
  [IPC_CHANNELS.reviewsOpen]: {
    request: z.tuple([ReviewReadRequestSchema]),
    response: ReviewSessionSchema,
  },
  [IPC_CHANNELS.reviewsSave]: {
    request: z.tuple([ReviewSaveRequestSchema]),
    response: ReviewSaveResultSchema,
  },
  [IPC_CHANNELS.reviewsValidate]: {
    request: z.tuple([ReviewValidateRequestSchema]),
    response: ReviewValidationSchema,
  },
  [IPC_CHANNELS.reviewsDecide]: {
    request: z.tuple([ReviewDecisionRequestSchema]),
    response: ReviewDecisionResultSchema,
  },
  [IPC_CHANNELS.resourcesCatalogue]: {
    request: z.tuple([z.string().max(50).optional()]),
    response: ResourceCatalogueSchema,
  },
  [IPC_CHANNELS.resourcesRead]: {
    request: z.tuple([z.string().min(1).max(256)]),
    response: ResourceReadSchema,
  },
  [IPC_CHANNELS.resourcesValidate]: {
    request: z.tuple([ResourceValidateRequestSchema]),
    response: ResourceValidateResultSchema,
  },
  [IPC_CHANNELS.resourcesWrite]: {
    request: z.tuple([ResourceWriteRequestSchema]),
    response: ResourceWriteResultSchema,
  },
  [IPC_CHANNELS.resourceDraftsLoad]: {
    request: z.tuple([LocalResourceDraftLookupRequestSchema]),
    response: LocalResourceDraftSchema.nullable(),
  },
  [IPC_CHANNELS.resourceDraftsSave]: {
    request: z.tuple([LocalResourceDraftSaveRequestSchema]),
    response: LocalResourceDraftSchema,
  },
  [IPC_CHANNELS.resourceDraftsDiscard]: {
    request: z.tuple([LocalResourceDraftDiscardRequestSchema]),
    response: LocalResourceDraftDiscardResultSchema,
  },
  [IPC_CHANNELS.runsList]: {
    request: z.tuple([RunListRequestSchema]),
    response: RunListResultSchema,
  },
  [IPC_CHANNELS.runsGet]: {
    request: z.tuple([RunGetRequestSchema]),
    response: RunDetailViewSchema,
  },
  [IPC_CHANNELS.runSessionsList]: {
    request: z.tuple([RunGetRequestSchema]),
    response: RunSessionsListResultSchema,
  },
  [IPC_CHANNELS.runArtifactsList]: {
    request: z.tuple([RunArtifactsListRequestSchema]),
    response: RunArtifactsListResultSchema,
  },
  [IPC_CHANNELS.runArtifactContent]: {
    request: z.tuple([RunArtifactContentRequestSchema]),
    response: RunTextContentSchema,
  },
  [IPC_CHANNELS.runLogContent]: {
    request: z.tuple([RunLogContentRequestSchema]),
    response: RunTextContentSchema,
  },
  [IPC_CHANNELS.rewindPreview]: {
    request: z.tuple([RewindPreviewRequestSchema]),
    response: RewindPreviewViewSchema,
  },
  [IPC_CHANNELS.rewindExecute]: {
    request: z.tuple([RewindExecuteRequestSchema]),
    response: FeatureActionResultSchema,
  },
  [IPC_CHANNELS.completionPreflight]: {
    request: z.tuple([CompletionPreflightRequestSchema]),
    response: CompletionPreflightResultSchema,
  },
  [IPC_CHANNELS.repositoryDiff]: {
    request: z.tuple([RepositoryDiffRequestSchema]),
    response: RepositoryDiffResultSchema,
  },
  [IPC_CHANNELS.publishDescription]: {
    request: z.tuple([PublishDescriptionRequestSchema]),
    response: PublishDescriptionResultSchema,
  },
  [IPC_CHANNELS.openExternal]: {
    request: z.tuple([OpenExternalRequestSchema]),
    response: z.strictObject({ ok: z.boolean() }),
  },
  [IPC_CHANNELS.revealPath]: {
    request: z.tuple([RevealPathRequestSchema]),
    response: z.strictObject({ ok: z.boolean() }),
  },
  [IPC_CHANNELS.featuresRebase]: {
    request: z.tuple([RebaseRequestSchema]),
    response: RebaseResultSchema,
  },
  [IPC_CHANNELS.featuresRebasePreflight]: {
    request: z.tuple([RebasePreflightRequestSchema]),
    response: RebasePreflightResultSchema,
  },
  [IPC_CHANNELS.featuresReviewCommentsFetch]: {
    request: z.tuple([ReviewCommentsFetchRequestSchema]),
    response: ReviewCommentsFetchResultSchema,
  },
  [IPC_CHANNELS.featuresReviewCommentsStart]: {
    request: z.tuple([ReviewCommentsStartRequestSchema]),
    response: ReviewCommentsStartResultSchema,
  },
  [IPC_CHANNELS.featuresRefactor]: {
    request: z.tuple([RefactorRequestSchema]),
    response: RefactorResultSchema,
  },
  [IPC_CHANNELS.featuresRefactorPreflight]: {
    request: z.tuple([RefactorPreflightRequestSchema]),
    response: RefactorPreflightResultSchema,
  },
  [IPC_CHANNELS.recoveryScan]: {
    request: z.tuple([]),
    response: RecoverySnapshotSchema,
  },
  [IPC_CHANNELS.recoveryExecute]: {
    request: z.tuple([RecoveryExecuteRequestSchema]),
    response: RecoveryExecuteResultSchema,
  },
  [IPC_CHANNELS.recoveryLogRead]: {
    request: z.tuple([RecoveryLogReadRequestSchema]),
    response: RecoveryLogReadResultSchema,
  },
  [IPC_CHANNELS.bulkPreview]: {
    request: z.tuple([]),
    response: BulkPreviewSchema,
  },
  [IPC_CHANNELS.updatesGet]: {
    request: z.tuple([]),
    response: UpdateStateSchema,
  },
  [IPC_CHANNELS.updatesCheck]: {
    request: z.tuple([]),
    response: UpdateStateSchema,
  },
  [IPC_CHANNELS.updatesInstallWhenIdle]: {
    request: z.tuple([]),
    response: UpdateStateSchema,
  },
  [IPC_CHANNELS.updatesInstallNow]: {
    request: z.tuple([UpdateInstallNowRequestSchema]),
    response: UpdateStateSchema,
  },
  [IPC_CHANNELS.updatesRestart]: {
    request: z.tuple([]),
    response: UpdateStateSchema,
  },
  [IPC_CHANNELS.diagnosticsGet]: {
    request: z.tuple([]),
    response: DiagnosticsSnapshotSchema,
  },
  [IPC_CHANNELS.diagnosticsReveal]: {
    request: z.tuple([]),
    response: z.strictObject({ ok: z.boolean() }),
  },
  [IPC_CHANNELS.diagnosticsClear]: {
    request: z.tuple([]),
    response: DiagnosticsSnapshotSchema,
  },
};

// --- The narrow window API the preload exposes -------------------------------

export interface AgenticoApi {
  getConnectionStatus(): Promise<ConnectionState>;
  retryConnection(): Promise<ConnectionState>;
  restartConnection(): Promise<ConnectionState>;
  onConnectionChanged(listener: (state: ConnectionState) => void): () => void;
  onRouteRequest(listener: (event: AppRouteEvent) => void): () => void;
  getSettings(): Promise<Settings>;
  updateSettings(patch: SettingsPatch): Promise<Settings>;
  getThemePreference(): Promise<ThemeInfo>;
  setThemePreference(preference: ThemePreference): Promise<ThemeInfo>;
  getReadiness(): Promise<ReadinessSnapshot>;
  refreshReadiness(): Promise<ReadinessSnapshot>;
  pickWorkspaceDirectory(): Promise<PickedDirectory>;
  addWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
  removeWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
  reorderWorkspaceRoots(paths: string[]): Promise<ReadinessSnapshot>;
  initRepository(request: InitRepositoryRequest): Promise<ReadinessSnapshot>;
  listRepositories(): Promise<RepositoryState[]>;
  listFeatures(): Promise<FeatureSummaryView[]>;
  getFeature(featureId: string): Promise<FeatureSnapshot>;
  createFeature(input: CreateFeatureInput): Promise<CreateFeatureResult>;
  dispatchFeatureSetup(featureId: string): Promise<SetupDispatchResult>;
  dispatchFeatureAction(request: FeatureActionRequest): Promise<FeatureActionResult>;
  getAttention(): Promise<AttentionSnapshot>;
  answerPermission(request: PermissionDecisionRequest): Promise<AttentionActionResult>;
  answerQuestions(request: AskUserAnswerRequest): Promise<AttentionActionResult>;
  sendHelp(request: HelpAnswerRequest): Promise<AttentionActionResult>;
  saveGateDraft(request: GateDraftRequest): Promise<AttentionActionResult>;
  resolveGate(request: GateResolutionRequest): Promise<AttentionActionResult>;
  startChat(request: ChatStartRequest): Promise<ChatActionResult>;
  endChat(): Promise<ChatActionResult>;
  listSessions(): Promise<SessionSummary[]>;
  getSession(sessionId: string): Promise<SessionDetail>;
  getSessionTranscript(request: SessionTranscriptRequest): Promise<SessionTranscript>;
  openSessionOutput(request: SessionOutputOpenRequest): Promise<SessionOutputOpenResult>;
  cancelSessionOutput(subscriptionId: string): Promise<boolean>;
  onSessionOutput(listener: (event: SessionOutputEvent) => void): () => void;
  getCreationDefaults(): Promise<CreationDefaults>;
  loadLocalReviewDraft(request: LocalReviewDraftLookupRequest): Promise<LocalReviewDraft | null>;
  saveLocalReviewDraft(request: LocalReviewDraftSaveRequest): Promise<LocalReviewDraft>;
  discardLocalReviewDraft(request: LocalReviewDraftDiscardRequest): Promise<boolean>;
  readReview(request: ReviewReadRequest): Promise<ReviewSession>;
  openReview(request: ReviewReadRequest): Promise<ReviewSession>;
  saveReview(request: ReviewSaveRequest): Promise<ReviewSaveResult>;
  validateReview(request: ReviewValidateRequest): Promise<ReviewValidation>;
  decideReview(request: ReviewDecisionRequest): Promise<ReviewDecisionResult>;
  listResources(kind?: string): Promise<ResourceCatalogue>;
  readResource(resourceId: string): Promise<ResourceRead>;
  validateResource(request: ResourceValidateRequest): Promise<ResourceValidateResult>;
  writeResource(request: ResourceWriteRequest): Promise<ResourceWriteResult>;
  loadLocalResourceDraft(
    request: LocalResourceDraftLookupRequest,
  ): Promise<LocalResourceDraft | null>;
  saveLocalResourceDraft(request: LocalResourceDraftSaveRequest): Promise<LocalResourceDraft>;
  discardLocalResourceDraft(request: LocalResourceDraftDiscardRequest): Promise<boolean>;
  listRuns(request: RunListRequest): Promise<RunListResult>;
  getRun(request: RunGetRequest): Promise<RunDetailView>;
  listRunSessions(request: RunGetRequest): Promise<RunSessionsListResult>;
  listRunArtifacts(request: RunArtifactsListRequest): Promise<RunArtifactsListResult>;
  getRunArtifactContent(request: RunArtifactContentRequest): Promise<RunTextContent>;
  getRunLogContent(request: RunLogContentRequest): Promise<RunTextContent>;
  getRewindPreview(request: RewindPreviewRequest): Promise<RewindPreviewView>;
  executeRewind(request: RewindExecuteRequest): Promise<FeatureActionResult>;
  preflightCompletion(request: CompletionPreflightRequest): Promise<CompletionPreflightResult>;
  getRepositoryDiff(request: RepositoryDiffRequest): Promise<RepositoryDiffResult>;
  generatePublishDescription(request: PublishDescriptionRequest): Promise<PublishDescriptionResult>;
  openExternal(request: OpenExternalRequest): Promise<{ ok: boolean }>;
  revealPath(request: RevealPathRequest): Promise<{ ok: boolean }>;
  startRebase(request: RebaseRequest): Promise<RebaseResult>;
  preflightRebase(request: RebasePreflightRequest): Promise<RebasePreflightResult>;
  fetchReviewComments(request: ReviewCommentsFetchRequest): Promise<ReviewCommentsFetchResult>;
  startReviewComments(request: ReviewCommentsStartRequest): Promise<ReviewCommentsStartResult>;
  startRefactor(request: RefactorRequest): Promise<RefactorResult>;
  preflightRefactor(request: RefactorPreflightRequest): Promise<RefactorPreflightResult>;
  scanRecovery(): Promise<RecoverySnapshot>;
  executeRecovery(request: RecoveryExecuteRequest): Promise<RecoveryExecuteResult>;
  readRecoveryLog(request: RecoveryLogReadRequest): Promise<RecoveryLogReadResult>;
  bulkPreview(): Promise<BulkPreview>;
  getUpdates(): Promise<UpdateState>;
  checkForUpdates(): Promise<UpdateState>;
  installUpdateWhenIdle(): Promise<UpdateState>;
  installUpdateNow(request: UpdateInstallNowRequest): Promise<UpdateState>;
  restartToUpdate(): Promise<UpdateState>;
  getDiagnostics(): Promise<DiagnosticsSnapshot>;
  revealDiagnostics(): Promise<{ ok: boolean }>;
  clearDiagnostics(): Promise<DiagnosticsSnapshot>;
  onAppEvent(listener: (event: AppEvent) => void): () => void;
}
