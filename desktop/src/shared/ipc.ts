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
} as const;

export type IpcChannel = (typeof IPC_CHANNELS)[keyof typeof IPC_CHANNELS];

/** Main-to-renderer push events (webContents.send). */
export const IPC_EVENTS = {
  connectionChanged: 'agentico:connection:changed',
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
});

export type Settings = z.output<typeof SettingsSchema>;

export const SettingsPatchSchema = z.strictObject({
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }).optional(),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }).optional(),
  theme: ThemePreferenceSchema.optional(),
});

export type SettingsPatch = z.output<typeof SettingsPatchSchema>;

export function defaultSettings(): Settings {
  return {
    schemaVersion: SETTINGS_SCHEMA_VERSION,
    runtime: { selection: null },
    window: {},
    theme: 'system',
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
}
