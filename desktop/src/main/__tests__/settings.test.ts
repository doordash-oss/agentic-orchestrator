import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { SettingsStore } from '../settings';
import { defaultSettings } from '../../shared/ipc';
import { SafeErrorException } from '../../shared/errors';

let dir: string;
let warnings: string[];

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-settings-'));
  warnings = [];
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function makeStore() {
  return new SettingsStore(dir, { warn: (msg: string) => warnings.push(msg) });
}

function settingsPath() {
  return path.join(dir, 'settings.json');
}

describe('SettingsStore', () => {
  it('returns defaults when no file exists, without creating a backup', () => {
    const store = makeStore();
    expect(store.get()).toEqual(defaultSettings());
    expect(fs.readdirSync(dir)).toEqual([]);
  });

  it('round-trips settings through save and a fresh load', () => {
    const store = makeStore();
    store.update({ theme: 'dark', runtime: { selection: 'claude' } });
    const reloaded = makeStore();
    expect(reloaded.get().theme).toBe('dark');
    expect(reloaded.get().runtime.selection).toBe('claude');
  });

  it('persists owner-only permissions: file 0600 and directory 0700', () => {
    const store = makeStore();
    store.update({ theme: 'light' });
    const fileMode = fs.statSync(settingsPath()).mode & 0o777;
    const dirMode = fs.statSync(dir).mode & 0o777;
    expect(fileMode).toBe(0o600);
    expect(dirMode).toBe(0o700);
  });

  it('replaces the file atomically and leaves no temp files behind', () => {
    const store = makeStore();
    store.update({ theme: 'dark' });
    store.update({ theme: 'light' });
    const entries = fs.readdirSync(dir);
    expect(entries).toEqual(['settings.json']);
  });

  it('recovers from corrupt JSON: backs up, warns, and returns defaults', () => {
    fs.writeFileSync(settingsPath(), '{"schemaVersion": 1, "runtime": {"sel');
    const store = makeStore();
    expect(store.get()).toEqual(defaultSettings());
    expect(fs.existsSync(path.join(dir, 'settings.json.bak-1'))).toBe(true);
    expect(warnings.length).toBe(1);
  });

  it('recovers from truncation to an empty file', () => {
    fs.writeFileSync(settingsPath(), '');
    const store = makeStore();
    expect(store.get()).toEqual(defaultSettings());
    expect(fs.existsSync(path.join(dir, 'settings.json.bak-1'))).toBe(true);
  });

  it('recovers from an unsupported (newer) schema version', () => {
    fs.writeFileSync(settingsPath(), JSON.stringify({ ...defaultSettings(), schemaVersion: 99 }));
    const store = makeStore();
    expect(store.get()).toEqual(defaultSettings());
    expect(fs.existsSync(path.join(dir, 'settings.json.bak-1'))).toBe(true);
  });

  it('recovers from schema-valid JSON with foreign fields (fail closed)', () => {
    fs.writeFileSync(settingsPath(), JSON.stringify({ ...defaultSettings(), bearerToken: 'nope' }));
    const store = makeStore();
    expect(store.get()).toEqual(defaultSettings());
  });

  it('uses a deterministic backup counter that never overwrites earlier backups', () => {
    fs.writeFileSync(settingsPath(), 'corrupt-one');
    makeStore();
    fs.writeFileSync(settingsPath(), 'corrupt-two');
    makeStore();
    expect(fs.readFileSync(path.join(dir, 'settings.json.bak-1'), 'utf8')).toBe('corrupt-one');
    expect(fs.readFileSync(path.join(dir, 'settings.json.bak-2'), 'utf8')).toBe('corrupt-two');
  });

  it('redacts absolute user paths from recovery warnings', () => {
    fs.writeFileSync(settingsPath(), 'corrupt');
    makeStore();
    expect(warnings.length).toBe(1);
    expect(warnings[0]).not.toContain(os.tmpdir());
    expect(warnings[0]).not.toMatch(/\/(Users|home)\//);
  });

  it('rejects invalid patches with a typed error and persists nothing', () => {
    const store = makeStore();
    expect(() => store.update({ theme: 'neon' } as never)).toThrow(SafeErrorException);
    expect(fs.existsSync(settingsPath())).toBe(false);
  });

  it('merges window bounds updates without dropping other settings', () => {
    const store = makeStore();
    store.update({ theme: 'dark' });
    store.update({ window: { bounds: { x: 1, y: 2, width: 800, height: 600 } } });
    const reloaded = makeStore();
    expect(reloaded.get()).toEqual({
      schemaVersion: 2,
      runtime: { selection: null },
      window: { bounds: { x: 1, y: 2, width: 800, height: 600 } },
      theme: 'dark',
      wizard: { collapsedHelp: false },
      ama: { drawer: 'compact' },
      notifications: { previewEnabled: false },
      shell: { activeFeatureId: null, sidebarCollapsed: false },
    });
  });

  it('persists shell presentation prefs and restores them on reload', () => {
    const store = makeStore();
    store.update({
      shell: { activeFeatureId: 'abcd1234ef567890', sidebarCollapsed: true },
    });
    expect(makeStore().get().shell).toEqual({
      activeFeatureId: 'abcd1234ef567890',
      sidebarCollapsed: true,
    });
  });

  it('rejects shell patches carrying unknown fields', () => {
    const store = makeStore();
    expect(() =>
      store.update({
        shell: {
          activeFeatureId: null,
          sidebarCollapsed: false,
          status: 'Created',
        } as never,
      }),
    ).toThrow(SafeErrorException);
  });

  it('persists wizard presentation prefs and loads pre-wizard files with defaults', () => {
    const store = makeStore();
    store.update({ wizard: { collapsedHelp: true } });
    expect(makeStore().get().wizard).toEqual({ collapsedHelp: true });

    // A document written before the wizard section existed still loads.
    fs.writeFileSync(
      settingsPath(),
      JSON.stringify({
        schemaVersion: 1,
        runtime: { selection: null },
        window: {},
        theme: 'dark',
      }),
    );
    const upgraded = makeStore();
    expect(upgraded.get().theme).toBe('dark');
    expect(upgraded.get().wizard).toEqual({ collapsedHelp: false });
    expect(upgraded.get().ama).toEqual({ drawer: 'compact' });
    expect(upgraded.get().notifications).toEqual({ previewEnabled: false });
  });

  it('drops a retired wizard pref without discarding the rest of the file', () => {
    fs.writeFileSync(
      settingsPath(),
      JSON.stringify({
        schemaVersion: 1,
        runtime: { selection: null },
        window: {},
        theme: 'dark',
        wizard: { collapsedHelp: true, lastRepositoryPathHint: '/work/repo' },
      }),
    );
    const store = makeStore();
    expect(store.get().wizard).toEqual({ collapsedHelp: true });
    expect(store.get().theme).toBe('dark');
    expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);
  });

  it('never throws while loading a corrupt file', () => {
    fs.writeFileSync(settingsPath(), 'corrupt');
    expect(() => makeStore().get()).not.toThrow();
  });

  describe('schema v1 -> v2 migration', () => {
    function v1Fixture(overrides: Record<string, unknown> = {}) {
      return {
        schemaVersion: 1,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded' },
        notifications: { previewEnabled: true },
        tabs: {
          open: [
            { featureId: 'abcd1234ef567890', titleHint: 'Search revamp' },
            {
              featureId: 'ffee0011aa223344',
              titleHint: 'Archived run',
              selectedRunNumber: 7,
            },
          ],
          activeFeatureId: 'ffee0011aa223344',
        },
        ...overrides,
      };
    }

    it('upgrades a realistic v1 document without resetting to defaults', () => {
      fs.writeFileSync(settingsPath(), JSON.stringify(v1Fixture()));
      const store = makeStore();

      expect(store.get()).toEqual({
        schemaVersion: 2,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded' },
        notifications: { previewEnabled: true },
        shell: { activeFeatureId: 'ffee0011aa223344', sidebarCollapsed: false },
      });
      expect(warnings).toEqual([]);
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);

      const onDisk = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
      expect(onDisk.schemaVersion).toBe(2);
      expect(onDisk.shell).toEqual({
        activeFeatureId: 'ffee0011aa223344',
        sidebarCollapsed: false,
      });
      expect(onDisk.tabs).toBeUndefined();
    });

    it('maps the settings-tab sentinel active id to null', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify(
          v1Fixture({
            tabs: {
              open: [{ featureId: 'abcd1234ef567890', titleHint: 'Search revamp' }],
              activeFeatureId: '__settings__',
            },
          }),
        ),
      );
      const store = makeStore();
      expect(store.get().shell).toEqual({ activeFeatureId: null, sidebarCollapsed: false });
    });

    it('still resets to defaults for a corrupt v1-shaped document', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify({
          schemaVersion: 1,
          runtime: { selection: null },
          window: {},
          theme: 'not-a-real-theme',
        }),
      );
      const store = makeStore();
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });

    it('still resets to defaults for an unrecognized version (not 1 or 2)', () => {
      fs.writeFileSync(settingsPath(), JSON.stringify(v1Fixture({ schemaVersion: 99 })));
      const store = makeStore();
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });

    it('still resets to defaults for a version-2-shaped but invalid document', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify({ ...defaultSettings(), shell: { activeFeatureId: null } }),
      );
      const store = makeStore();
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });

    it('round-trips a shell update through the existing update() API', () => {
      const store = makeStore();
      store.update({ shell: { sidebarCollapsed: true, activeFeatureId: 'some-id' } });
      const reloaded = makeStore();
      expect(reloaded.get().shell).toEqual({ sidebarCollapsed: true, activeFeatureId: 'some-id' });
    });
  });
});
