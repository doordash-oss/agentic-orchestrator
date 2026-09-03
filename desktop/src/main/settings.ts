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

/**
 * Owner-only, schema-versioned, atomically-replaced local settings.
 *
 * Contents are app-local presentation/runtime-selection data (runtime
 * selection, window bounds, theme) plus the multi-server attachment memory
 * (the bounded known-servers list and the last-used pointer). No features,
 * runs, server configuration, credentials, tokens, or server-domain snapshots
 * are ever stored here, and the strict schema fails closed on any foreign
 * field.
 *
 * On unreadable, truncated, or version-incompatible documents the store
 * backs the file up to `settings.json.bak-<n>` (deterministic counter),
 * logs a redacted warning, and continues with in-memory defaults.
 */
import fs from 'node:fs';
import path from 'node:path';
import { z } from 'zod';
import { redactText, SafeErrorException, safeError } from '../shared/errors';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import {
  AmaPrefsSchema,
  FeatureIdSchema,
  MAX_KNOWN_SERVERS,
  NotificationPrefsSchema,
  SettingsPatchSchema,
  SettingsSchema,
  SettingsWindowPrefsSchema,
  ShellPrefsSchema,
  ThemePreferenceSchema,
  WindowBoundsSchema,
  WizardPrefsSchema,
  defaultAmaPrefs,
  defaultNotificationPrefs,
  defaultSettings,
  defaultSettingsWindowPrefs,
  defaultShellPrefs,
  defaultWizardPrefs,
  isPlainHttpBaseUrl,
  applyServersPatch,
  applyShellPatch,
  type Settings,
  type SettingsPatch,
  type ThemePreference,
} from '../shared/ipc';

const FILE_NAME = 'settings.json';
const MAX_SETTINGS_BYTES = 256 * 1024;

/**
 * The sentinel id used by v1's renderer tab model (WorkspaceShell,
 * CommandPalette) to represent the Settings tab, deleted along with that
 * model. It passed the feature-id regex but was not a real feature id, so a
 * v1 `tabs.activeFeatureId` equal to this value migrates to
 * `shell.activeFeatureId: null` rather than being carried over as a bogus
 * "active feature".
 */
const SETTINGS_TAB_SENTINEL = '__settings__';

/**
 * Shape of a schema-version-1 settings document, kept ONLY so `load()` can
 * validate and migrate legacy files still on disk. Not exported: v1's
 * `tabs` concept (open tab list, per-tab selected run) has no v2 equivalent
 * and must never be reintroduced elsewhere in the app.
 */
const LegacyV1OpenFeatureSchema = z.strictObject({
  featureId: FeatureIdSchema,
  titleHint: z.string().max(200),
  selectedRunNumber: z.number().int().nonnegative().nullable().optional(),
});

const LegacyV1OpenListSchema = z.strictObject({
  open: z.array(LegacyV1OpenFeatureSchema).max(50),
  activeFeatureId: FeatureIdSchema.nullable(),
});

const SettingsSchemaV1 = z.strictObject({
  schemaVersion: z.literal(1),
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }),
  theme: ThemePreferenceSchema,
  wizard: WizardPrefsSchema.default(defaultWizardPrefs()),
  ama: AmaPrefsSchema.default(defaultAmaPrefs()),
  notifications: NotificationPrefsSchema.default(defaultNotificationPrefs()),
  tabs: LegacyV1OpenListSchema.default({ open: [], activeFeatureId: null }),
});

type SettingsV1 = z.output<typeof SettingsSchemaV1>;

/**
 * Shape of the pre-v4 shell section (a single global active feature id), kept
 * ONLY so `load()` can validate and migrate v2/v3 files still on disk. v4
 * replaces it with the per-server `featureByServer` map; new code must only
 * ever see the current ShellPrefsSchema.
 */
const ShellPrefsSchemaV3 = z.strictObject({
  activeFeatureId: FeatureIdSchema.nullable(),
  sidebarCollapsed: z.boolean(),
});

type ShellPrefsV3 = z.output<typeof ShellPrefsSchemaV3>;

function defaultShellPrefsV3(): ShellPrefsV3 {
  return { activeFeatureId: null, sidebarCollapsed: false };
}

/**
 * Upgrades a validated v1 document to v2: `tabs.open` (and any per-tab
 * `selectedRunNumber`) is dropped entirely, and `tabs.activeFeatureId` becomes
 * `shell.activeFeatureId` — mapped to `null` when it was the Settings-tab
 * sentinel. Every other field is carried over byte-for-byte; fields introduced
 * in v2 (the Settings window's own bounds and pane) take their defaults.
 */
function migrateSettingsV1ToV2(v1: SettingsV1): SettingsV2 {
  const activeFeatureId =
    v1.tabs.activeFeatureId === SETTINGS_TAB_SENTINEL ? null : v1.tabs.activeFeatureId;
  return {
    schemaVersion: 2,
    runtime: v1.runtime,
    window: v1.window,
    theme: v1.theme,
    wizard: v1.wizard,
    ama: v1.ama,
    notifications: v1.notifications,
    shell: { activeFeatureId, sidebarCollapsed: false },
    settingsWindow: defaultSettingsWindowPrefs(),
  };
}

/**
 * Shape of a v3/v4 known-server entry, kept ONLY so `load()` can validate and
 * migrate files written before the local/remote known-servers split: those
 * versions have no `kind`/`nickname` and their `runtimeDir` was
 * unconditionally required (every pre-v5 entry is by definition a local
 * runtime attachment, so the v4→v5 migration tags them `kind: 'local'`).
 */
const KnownServerSchemaV4 = z.strictObject({
  serverKey: z.string().min(1).max(64),
  name: z.string().max(64),
  baseUrl: z.string().max(256).refine(isPlainHttpBaseUrl, {
    message: 'baseUrl must be a plain http URL',
  }),
  runtimeDir: z.string().max(4096),
  lastSeenAt: z
    .string()
    .max(64)
    .refine((value) => !Number.isNaN(Date.parse(value)), {
      message: 'lastSeenAt must be an ISO 8601 timestamp',
    }),
});

const ServersPrefsSchemaV4 = z.strictObject({
  known: z.array(KnownServerSchemaV4).max(MAX_KNOWN_SERVERS),
  lastUsed: z.string().max(64).nullable(),
});

type ServersPrefsV4 = z.output<typeof ServersPrefsSchemaV4>;

function defaultServersPrefsV4(): ServersPrefsV4 {
  return { known: [], lastUsed: null };
}

/**
 * Shape of a schema-version-3 settings document, kept ONLY so `load()` can
 * validate and migrate files written before settings were scoped per server.
 * Not exported: new code must only ever see the current SettingsSchema.
 */
const SettingsSchemaV3 = z.strictObject({
  schemaVersion: z.literal(3),
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }),
  theme: ThemePreferenceSchema,
  wizard: WizardPrefsSchema.default(defaultWizardPrefs()),
  ama: AmaPrefsSchema.default(defaultAmaPrefs()),
  notifications: NotificationPrefsSchema.default(defaultNotificationPrefs()),
  shell: ShellPrefsSchemaV3.default(defaultShellPrefsV3()),
  settingsWindow: SettingsWindowPrefsSchema.default(defaultSettingsWindowPrefs()),
  servers: ServersPrefsSchemaV4.default(defaultServersPrefsV4()),
});

type SettingsV3 = z.output<typeof SettingsSchemaV3>;

/**
 * Upgrades a validated v3 document to v4: every field is carried over
 * byte-for-byte except `shell.activeFeatureId`, which becomes the
 * `featureByServer` entry for the last-used server when both exist, and is
 * dropped otherwise (there is no server identity to scope it to).
 */
function migrateSettingsV3ToV4(v3: SettingsV3): SettingsV4 {
  const lastUsed = v3.servers.lastUsed;
  const activeFeatureId = v3.shell.activeFeatureId;
  return {
    schemaVersion: 4,
    runtime: v3.runtime,
    window: v3.window,
    theme: v3.theme,
    wizard: v3.wizard,
    ama: v3.ama,
    notifications: v3.notifications,
    shell: {
      featureByServer:
        lastUsed !== null && activeFeatureId !== null ? { [lastUsed]: activeFeatureId } : {},
      sidebarCollapsed: v3.shell.sidebarCollapsed,
    },
    settingsWindow: v3.settingsWindow,
    servers: v3.servers,
  };
}

/**
 * Shape of a schema-version-4 settings document, kept ONLY so `load()` can
 * validate and migrate files written before the local/remote known-servers
 * split. Not exported: new code must only ever see the current
 * SettingsSchema.
 */
const SettingsSchemaV4 = z.strictObject({
  schemaVersion: z.literal(4),
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }),
  theme: ThemePreferenceSchema,
  wizard: WizardPrefsSchema.default(defaultWizardPrefs()),
  ama: AmaPrefsSchema.default(defaultAmaPrefs()),
  notifications: NotificationPrefsSchema.default(defaultNotificationPrefs()),
  shell: ShellPrefsSchema.default(defaultShellPrefs()),
  settingsWindow: SettingsWindowPrefsSchema.default(defaultSettingsWindowPrefs()),
  servers: ServersPrefsSchemaV4.default(defaultServersPrefsV4()),
});

type SettingsV4 = z.output<typeof SettingsSchemaV4>;

/**
 * Upgrades a validated v4 document to v5: every known-server entry gains
 * `kind: 'local'` (v4 predates the local/remote split — every persisted
 * entry was a local runtime attachment) and every other field, including
 * ordering and the last-used pointer, is carried over byte-for-byte.
 */
function migrateSettingsV4ToV5(v4: SettingsV4): Settings {
  return {
    schemaVersion: 5,
    runtime: v4.runtime,
    window: v4.window,
    theme: v4.theme,
    wizard: v4.wizard,
    ama: v4.ama,
    notifications: v4.notifications,
    shell: v4.shell,
    settingsWindow: v4.settingsWindow,
    servers: {
      known: v4.servers.known.map((entry) => ({ ...entry, kind: 'local' as const })),
      lastUsed: v4.servers.lastUsed,
    },
  };
}

/**
 * Shape of a schema-version-2 settings document, kept ONLY so `load()` can
 * validate and migrate files written before the multi-server fields existed.
 * Not exported: new code must only ever see the current SettingsSchema.
 */
const SettingsSchemaV2 = z.strictObject({
  schemaVersion: z.literal(2),
  runtime: z.strictObject({ selection: z.string().max(200).nullable() }),
  window: z.strictObject({ bounds: WindowBoundsSchema.optional() }),
  theme: ThemePreferenceSchema,
  wizard: WizardPrefsSchema.default(defaultWizardPrefs()),
  ama: AmaPrefsSchema.default(defaultAmaPrefs()),
  notifications: NotificationPrefsSchema.default(defaultNotificationPrefs()),
  shell: ShellPrefsSchemaV3.default(defaultShellPrefsV3()),
  settingsWindow: SettingsWindowPrefsSchema.default(defaultSettingsWindowPrefs()),
});

type SettingsV2 = z.output<typeof SettingsSchemaV2>;

/**
 * Upgrades a validated v2 document to v3: every existing field is carried over
 * byte-for-byte and the new `servers` section (known-servers list plus
 * last-used pointer) starts empty.
 */
function migrateSettingsV2ToV3(v2: SettingsV2): SettingsV3 {
  return {
    schemaVersion: 3,
    runtime: v2.runtime,
    window: v2.window,
    theme: v2.theme,
    wizard: v2.wizard,
    ama: v2.ama,
    notifications: v2.notifications,
    shell: v2.shell,
    settingsWindow: v2.settingsWindow,
    servers: defaultServersPrefsV4(),
  };
}

export interface SettingsStoreOptions {
  warn?: (message: string) => void;
}

export class SettingsStore {
  private readonly file: string;
  private readonly warn: (message: string) => void;
  private settings: Settings;

  constructor(
    private readonly dir: string,
    options: SettingsStoreOptions = {},
  ) {
    this.file = path.join(dir, FILE_NAME);
    this.warn = options.warn ?? ((message) => console.warn(message));
    this.settings = this.load();
  }

  get(): Settings {
    return this.settings;
  }

  update(patch: SettingsPatch): Settings {
    const parsed = SettingsPatchSchema.safeParse(patch);
    if (!parsed.success) {
      throw new SafeErrorException(
        safeError(
          'E_INVALID_SETTINGS_PATCH',
          'The settings update was rejected because it did not match the settings schema.',
        ),
      );
    }
    const next: Settings = {
      ...this.settings,
      ...(parsed.data.runtime !== undefined ? { runtime: parsed.data.runtime } : {}),
      ...(parsed.data.window !== undefined ? { window: parsed.data.window } : {}),
      ...(parsed.data.theme !== undefined ? { theme: parsed.data.theme } : {}),
      ...(parsed.data.wizard !== undefined ? { wizard: parsed.data.wizard } : {}),
      ...(parsed.data.ama !== undefined ? { ama: parsed.data.ama } : {}),
      ...(parsed.data.notifications !== undefined
        ? { notifications: parsed.data.notifications }
        : {}),
      ...(parsed.data.shell !== undefined
        ? { shell: applyShellPatch(this.settings.shell, parsed.data.shell) }
        : {}),
      ...(parsed.data.settingsWindow !== undefined
        ? { settingsWindow: parsed.data.settingsWindow }
        : {}),
      ...(parsed.data.servers !== undefined
        ? { servers: applyServersPatch(this.settings.servers, parsed.data.servers) }
        : {}),
      schemaVersion: this.settings.schemaVersion,
    };
    this.persist(next);
    this.settings = next;
    return next;
  }

  setTheme(theme: ThemePreference): Settings {
    return this.update({ theme });
  }

  // --- persistence ----------------------------------------------------------

  private load(): Settings {
    let raw: string;
    try {
      raw = fs.readFileSync(this.file, 'utf8');
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        return defaultSettings();
      }
      this.recover('settings file was unreadable');
      return defaultSettings();
    }

    try {
      assertWithinByteSize(raw, MAX_SETTINGS_BYTES);
      const data: unknown = JSON.parse(raw);
      assertNoPrototypePollution(data);

      const parsed = SettingsSchema.safeParse(data);
      if (parsed.success) {
        return parsed.data;
      }

      const schemaVersion = (data as { schemaVersion?: unknown }).schemaVersion;
      if (schemaVersion === 1) {
        const legacy = SettingsSchemaV1.safeParse(data);
        if (legacy.success) {
          const migrated = migrateSettingsV4ToV5(
            migrateSettingsV3ToV4(migrateSettingsV2ToV3(migrateSettingsV1ToV2(legacy.data))),
          );
          this.persist(migrated);
          return migrated;
        }
      }
      if (schemaVersion === 2) {
        const legacy = SettingsSchemaV2.safeParse(data);
        if (legacy.success) {
          const migrated = migrateSettingsV4ToV5(
            migrateSettingsV3ToV4(migrateSettingsV2ToV3(legacy.data)),
          );
          this.persist(migrated);
          return migrated;
        }
      }
      if (schemaVersion === 3) {
        const legacy = SettingsSchemaV3.safeParse(data);
        if (legacy.success) {
          const migrated = migrateSettingsV4ToV5(migrateSettingsV3ToV4(legacy.data));
          this.persist(migrated);
          return migrated;
        }
      }
      if (schemaVersion === 4) {
        const legacy = SettingsSchemaV4.safeParse(data);
        if (legacy.success) {
          const migrated = migrateSettingsV4ToV5(legacy.data);
          this.persist(migrated);
          return migrated;
        }
      }

      this.recover('settings file did not match the supported schema version');
      return defaultSettings();
    } catch {
      this.recover('settings file was corrupt or truncated');
      return defaultSettings();
    }
  }

  /** Moves the bad file aside to settings.json.bak-<n> without overwriting. */
  private recover(reason: string): void {
    try {
      let counter = 1;
      let backup = `${this.file}.bak-${counter}`;
      while (fs.existsSync(backup)) {
        counter += 1;
        backup = `${this.file}.bak-${counter}`;
      }
      fs.renameSync(this.file, backup);
      this.warn(
        redactText(
          `Recovered local settings: ${reason}; the previous file was saved as ` +
            `settings.json.bak-${counter} and defaults are in effect.`,
        ),
      );
    } catch {
      this.warn(redactText(`Recovered local settings: ${reason}; defaults are in effect.`));
    }
  }

  /** Atomic replace: temp file in the same directory, 0600, fsync, rename. */
  private persist(settings: Settings): void {
    const validated = SettingsSchema.parse(settings);
    fs.mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    fs.chmodSync(this.dir, 0o700);
    const temp = `${this.file}.tmp-${process.pid}`;
    const payload = `${JSON.stringify(validated, null, 2)}\n`;
    const fd = fs.openSync(temp, 'w', 0o600);
    try {
      fs.writeFileSync(fd, payload, 'utf8');
      fs.fchmodSync(fd, 0o600);
      fs.fsyncSync(fd);
    } finally {
      fs.closeSync(fd);
    }
    fs.renameSync(temp, this.file);
  }
}
