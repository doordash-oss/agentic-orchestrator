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
import { redactText, SafeErrorException, safeError } from '../shared/errors';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import {
  SettingsPatchSchema,
  SettingsSchema,
  defaultSettings,
  type Settings,
  type SettingsPatch,
  type ThemePreference,
} from '../shared/ipc';

const FILE_NAME = 'settings.json';
const MAX_SETTINGS_BYTES = 256 * 1024;

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
      ...(parsed.data.tabs !== undefined ? { tabs: parsed.data.tabs } : {}),
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
      if (!parsed.success) {
        this.recover('settings file did not match the supported schema version');
        return defaultSettings();
      }
      return parsed.data;
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
