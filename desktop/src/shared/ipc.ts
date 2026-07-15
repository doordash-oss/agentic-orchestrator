/**
 * The complete IPC surface between renderer and main. Channel names are
 * centralized here with a zod request/response contract per channel; there is
 * deliberately no generic invoke passthrough anywhere in the app.
 */
import { z } from 'zod';

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
} as const;

export type IpcChannel = (typeof IPC_CHANNELS)[keyof typeof IPC_CHANNELS];

/** Main-to-renderer push events (webContents.send). */
export const IPC_EVENTS = {
  connectionChanged: 'agentico:connection:changed',
  appEvent: 'agentico:events:app',
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

export const CONNECTION_STATUSES = [
  'idle',
  'resolving-runtime',
  'discovering',
  'attaching',
  'launching',
  'waiting-health',
  'connecting',
  'ready',
  'incompatible',
  'resources-missing',
  'launch-failed',
  'crashed',
  'error',
] as const;

export const ConnectionStatusSchema = z.enum(CONNECTION_STATUSES);
export type ConnectionStatus = z.output<typeof ConnectionStatusSchema>;

/** Terminal states that surface the error panel and a manual retry path. */
export const CONNECTION_ERROR_STATUSES: readonly ConnectionStatus[] = [
  'incompatible',
  'resources-missing',
  'launch-failed',
  'crashed',
  'error',
];

export function isConnectionErrorStatus(status: ConnectionStatus): boolean {
  return CONNECTION_ERROR_STATUSES.includes(status);
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

/**
 * The renderer-visible connection model. Strict by design: any foreign
 * field — in particular anything token-shaped — fails validation at the
 * preload and IPC boundaries. Credentials never appear here.
 */
export const ConnectionStateSchema = z.strictObject({
  status: ConnectionStatusSchema,
  stage: ConnectionStageSchema,
  detail: z.string(),
  ownership: ServerOwnershipSchema,
  serverBuild: ServerBuildInfoSchema.optional(),
  error: SafeErrorSchema.optional(),
});

export type ConnectionState = z.output<typeof ConnectionStateSchema>;

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
});

export type Settings = z.output<typeof SettingsSchema>;

export const SettingsPatchSchema = z.strictObject({
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }).optional(),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }).optional(),
  theme: ThemePreferenceSchema.optional(),
  wizard: WizardPrefsSchema.optional(),
});

export type SettingsPatch = z.output<typeof SettingsPatchSchema>;

export function defaultSettings(): Settings {
  return {
    schemaVersion: SETTINGS_SCHEMA_VERSION,
    runtime: { selection: null },
    window: {},
    theme: 'system',
    wizard: defaultWizardPrefs(),
  };
}

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
}
