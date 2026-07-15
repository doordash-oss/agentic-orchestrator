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
  featuresList: 'agentico:features:list',
  featuresGet: 'agentico:features:get',
  featuresCreate: 'agentico:features:create',
  featuresSetup: 'agentico:features:setup',
  creationDefaults: 'agentico:creation:defaults',
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
  setup: FeatureSetupViewSchema.optional(),
  /** The authoritative server action catalogue (setup/start/…). */
  actions: z.array(FeatureActionViewSchema),
  failure: z
    .strictObject({ type: z.string().optional(), message: z.string().optional() })
    .optional(),
});

export type FeatureSnapshot = z.output<typeof FeatureSnapshotSchema>;

// --- Feature creation ---------------------------------------------------------

/** The narrow Phase 1 creation input, validated at both IPC boundaries. */
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
  [IPC_CHANNELS.creationDefaults]: {
    request: z.tuple([]),
    response: CreationDefaultsSchema,
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
  getCreationDefaults(): Promise<CreationDefaults>;
  onAppEvent(listener: (event: AppEvent) => void): () => void;
}
