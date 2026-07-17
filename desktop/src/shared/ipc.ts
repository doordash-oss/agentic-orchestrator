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
  settingsGet: 'agentico:settings:get',
  settingsUpdate: 'agentico:settings:update',
  themeGet: 'agentico:theme:get',
  themeSet: 'agentico:theme:set',
  readinessGet: 'agentico:readiness:get',
  readinessRefresh: 'agentico:readiness:refresh',
  workspacePickDirectory: 'agentico:workspace:pick-directory',
  workspaceAddRoot: 'agentico:workspace:add-root',
  workspaceInitRepository: 'agentico:workspace:init-repository',
  repositoriesList: 'agentico:repositories:list',
  featuresList: 'agentico:features:list',
  featuresGet: 'agentico:features:get',
  featuresCreate: 'agentico:features:create',
  featuresSetup: 'agentico:features:setup',
  featuresDispatchAction: 'agentico:features:dispatch-action',
  attentionGet: 'agentico:attention:get',
  attentionAnswerPermission: 'agentico:attention:answer-permission',
  attentionAnswerQuestions: 'agentico:attention:answer-questions',
  attentionSendHelp: 'agentico:attention:send-help',
  attentionSaveGateDraft: 'agentico:attention:save-gate-draft',
  attentionResolveGate: 'agentico:attention:resolve-gate',
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
} as const;

export type IpcChannel = (typeof IPC_CHANNELS)[keyof typeof IPC_CHANNELS];

/** Main-to-renderer push events (webContents.send). */
export const IPC_EVENTS = {
  connectionChanged: 'agentico:connection:changed',
  appEvent: 'agentico:events:app',
  sessionOutput: 'agentico:sessions:output',
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
  setup: FeatureSetupViewSchema.optional(),
  /** The authoritative server action catalogue (setup/start/…). */
  actions: z.array(FeatureActionViewSchema),
  failure: z
    .strictObject({ type: z.string().optional(), message: z.string().optional() })
    .optional(),
});

export type FeatureSnapshot = z.output<typeof FeatureSnapshotSchema>;

/** Renderer-visible operational actions are limited to the start/stop allowlist. */
export const FeatureOperationalActionSchema = z.enum(['start', 'pause-stop']);
export type FeatureOperationalAction = z.output<typeof FeatureOperationalActionSchema>;

export const FeatureActionRequestSchema = z.strictObject({
  featureId: FeatureIdSchema,
  action: FeatureOperationalActionSchema,
});
export type FeatureActionRequest = z.output<typeof FeatureActionRequestSchema>;

export const FeatureActionResultSchema = z.strictObject({
  featureId: FeatureIdSchema,
  action: FeatureOperationalActionSchema,
  result: z.string().max(500),
  phase: z.string().max(200).optional(),
  sessionIds: z.array(z.string().min(1).max(200)).max(100),
});
export type FeatureActionResult = z.output<typeof FeatureActionResultSchema>;

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
export const AttentionItemSchema = z.discriminatedUnion('kind', [
  AttentionPermissionSchema,
  AttentionQuestionBundleSchema,
  AttentionHelpSchema,
  AttentionGateSchema,
  AttentionReviewSchema,
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

// --- Sessions and bounded transcript/output operations ---------------------

/** Canonical safe URL-segment syntax for server-owned session identifiers. */
export const SESSION_ID_SEGMENT_PATTERN = '[a-z0-9._-]{1,200}';

export const SessionIdSchema = z.string().regex(new RegExp(`^${SESSION_ID_SEGMENT_PATTERN}$`, 'i'));
export type SessionId = z.output<typeof SessionIdSchema>;

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
 * Open feature tabs: strictly identity plus presentation. The title is a
 * *hint* used only until the authoritative feature loads; feature state,
 * setup progress, and any other server-domain data are never stored here.
 */
export const FeatureTabSchema = z.strictObject({
  featureId: FeatureIdSchema,
  titleHint: z.string().max(200),
});

export type FeatureTab = z.output<typeof FeatureTabSchema>;

export const TabsPrefsSchema = z.strictObject({
  /** Open feature tabs in display order. */
  open: z.array(FeatureTabSchema).max(50),
  /** Active tab; null means the Home tab. */
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
  tabs: TabsPrefsSchema.default(defaultTabsPrefs()),
});

export type Settings = z.output<typeof SettingsSchema>;

export const SettingsPatchSchema = z.strictObject({
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }).optional(),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }).optional(),
  theme: ThemePreferenceSchema.optional(),
  wizard: WizardPrefsSchema.optional(),
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
};

// --- The narrow window API the preload exposes -------------------------------

export interface AgenticoApi {
  getConnectionStatus(): Promise<ConnectionState>;
  retryConnection(): Promise<ConnectionState>;
  onConnectionChanged(listener: (state: ConnectionState) => void): () => void;
  getSettings(): Promise<Settings>;
  updateSettings(patch: SettingsPatch): Promise<Settings>;
  getThemePreference(): Promise<ThemeInfo>;
  setThemePreference(preference: ThemePreference): Promise<ThemeInfo>;
  getReadiness(): Promise<ReadinessSnapshot>;
  refreshReadiness(): Promise<ReadinessSnapshot>;
  pickWorkspaceDirectory(): Promise<PickedDirectory>;
  addWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
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
  onAppEvent(listener: (event: AppEvent) => void): () => void;
}
