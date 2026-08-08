/**
 * Owner-only, schema-versioned, atomically-replaced local settings.
 *
 * Contents are strictly app-local presentation/runtime-selection data
 * (runtime selection, window bounds, theme). No features, runs, server
 * configuration, credentials, or server-domain snapshots are ever stored
 * here, and the strict schema fails closed on any foreign field.
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
  NotificationPrefsSchema,
  SettingsPatchSchema,
  SettingsSchema,
  ThemePreferenceSchema,
  WindowBoundsSchema,
  WizardPrefsSchema,
  defaultAmaPrefs,
  defaultNotificationPrefs,
  defaultSettings,
  defaultSettingsWindowPrefs,
  defaultWizardPrefs,
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
 * Upgrades a validated v1 document to v2 in place: `tabs.open` (and any
 * per-tab `selectedRunNumber`) is dropped entirely, and `tabs.activeFeatureId`
 * becomes `shell.activeFeatureId` — mapped to `null` when it was the
 * Settings-tab sentinel. Every other field is carried over byte-for-byte;
 * fields introduced after v2 (the Settings window's own bounds and pane) take
 * their defaults, exactly as they do for an untouched v2 document.
 */
function migrateSettingsV1ToV2(v1: SettingsV1): Settings {
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
      ...(parsed.data.shell !== undefined ? { shell: parsed.data.shell } : {}),
      ...(parsed.data.settingsWindow !== undefined
        ? { settingsWindow: parsed.data.settingsWindow }
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

      const isSchemaVersionOne =
        typeof data === 'object' &&
        data !== null &&
        (data as { schemaVersion?: unknown }).schemaVersion === 1;
      if (isSchemaVersionOne) {
        const legacy = SettingsSchemaV1.safeParse(data);
        if (legacy.success) {
          const migrated = migrateSettingsV1ToV2(legacy.data);
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
