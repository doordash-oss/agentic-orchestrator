import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { SettingsStore } from '../settings';
import {
  MAX_KNOWN_SERVERS,
  defaultAmaGeometry,
  defaultAmaPrefs,
  defaultServersPrefs,
  defaultSettings,
  defaultSettingsWindowPrefs,
  type KnownServer,
} from '../../shared/ipc';
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
      schemaVersion: 4,
      runtime: { selection: null },
      window: { bounds: { x: 1, y: 2, width: 800, height: 600 } },
      theme: 'dark',
      wizard: { collapsedHelp: false },
      ama: defaultAmaPrefs(),
      notifications: { previewEnabled: false },
      shell: { featureByServer: {}, sidebarCollapsed: false },
      settingsWindow: defaultSettingsWindowPrefs(),
      servers: defaultServersPrefs(),
    });
  });

  it('persists the AMA panel geometry and open state across a reload', () => {
    const store = makeStore();
    store.update({
      ama: { drawer: 'expanded', geometry: { right: 96, bottom: 48, width: 520, height: 400 } },
    });
    expect(makeStore().get().ama).toEqual({
      drawer: 'expanded',
      geometry: { right: 96, bottom: 48, width: 520, height: 400 },
    });
  });

  it('loads a v2 document written before the panel geometry existed without resetting', () => {
    fs.writeFileSync(
      settingsPath(),
      JSON.stringify({
        schemaVersion: 2,
        runtime: { selection: 'claude' },
        window: {},
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded' },
        notifications: { previewEnabled: true },
        shell: { activeFeatureId: 'abcd1234ef567890', sidebarCollapsed: true },
      }),
    );
    const store = makeStore();

    expect(store.get().ama).toEqual({ drawer: 'expanded', geometry: defaultAmaGeometry() });
    expect(store.get().theme).toBe('dark');
    // The global active id has no server to scope to (no last-used pointer):
    // the v3→v4 migration drops it.
    expect(store.get().shell).toEqual({
      featureByServer: {},
      sidebarCollapsed: true,
    });
    expect(warnings).toEqual([]);
    expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);
  });

  it('loads a v2 document written before the Settings window without resetting', () => {
    fs.writeFileSync(
      settingsPath(),
      JSON.stringify({
        schemaVersion: 2,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded', geometry: { right: 96, bottom: 48, width: 520, height: 400 } },
        notifications: { previewEnabled: true },
        shell: { activeFeatureId: 'abcd1234ef567890', sidebarCollapsed: true },
      }),
    );
    const store = makeStore();

    expect(store.get().settingsWindow).toEqual(defaultSettingsWindowPrefs());
    expect(store.get().settingsWindow.pane).toBe('workspace-roots');
    // No other preference is reset by the additive field.
    expect(store.get()).toEqual({
      schemaVersion: 4,
      runtime: { selection: 'claude' },
      window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
      theme: 'dark',
      wizard: { collapsedHelp: true },
      ama: { drawer: 'expanded', geometry: { right: 96, bottom: 48, width: 520, height: 400 } },
      notifications: { previewEnabled: true },
      shell: { featureByServer: {}, sidebarCollapsed: true },
      settingsWindow: defaultSettingsWindowPrefs(),
      servers: defaultServersPrefs(),
    });
    expect(warnings).toEqual([]);
    expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);
    // The v2 document is migrated through and rewritten on disk as v4.
    expect(JSON.parse(fs.readFileSync(settingsPath(), 'utf8')).schemaVersion).toBe(4);
  });

  it('persists the Settings window bounds and pane across a reload', () => {
    const store = makeStore();
    store.update({
      settingsWindow: { bounds: { x: 40, y: 60, width: 900, height: 640 }, pane: 'diagnostics' },
    });
    expect(makeStore().get().settingsWindow).toEqual({
      bounds: { x: 40, y: 60, width: 900, height: 640 },
      pane: 'diagnostics',
    });
  });

  it('merges a Settings window patch without dropping other settings', () => {
    const store = makeStore();
    store.update({ theme: 'dark', shell: { sidebarCollapsed: true } });
    store.update({ settingsWindow: { pane: 'appearance' } });
    const reloaded = makeStore();
    expect(reloaded.get().settingsWindow).toEqual({ pane: 'appearance' });
    expect(reloaded.get().theme).toBe('dark');
    expect(reloaded.get().shell).toEqual({ featureByServer: {}, sidebarCollapsed: true });
  });

  it('rejects a Settings window patch with an unknown pane or extra fields', () => {
    const store = makeStore();
    expect(() => store.update({ settingsWindow: { pane: 'nope' } as never })).toThrow(
      SafeErrorException,
    );
    expect(() =>
      store.update({ settingsWindow: { pane: 'appearance', maximized: true } as never }),
    ).toThrow(SafeErrorException);
    expect(fs.existsSync(settingsPath())).toBe(false);
  });

  it('rejects an AMA patch carrying unknown fields', () => {
    const store = makeStore();
    expect(() =>
      store.update({
        ama: { drawer: 'expanded', geometry: defaultAmaGeometry(), docked: true } as never,
      }),
    ).toThrow(SafeErrorException);
  });

  it('persists shell presentation prefs and restores them on reload', () => {
    const store = makeStore();
    store.update({
      shell: {
        sidebarCollapsed: true,
        setActiveFeature: { serverKey: 'a'.repeat(64), featureId: 'abcd1234ef567890' },
      },
    });
    expect(makeStore().get().shell).toEqual({
      featureByServer: { ['a'.repeat(64)]: 'abcd1234ef567890' },
      sidebarCollapsed: true,
    });
  });

  it('scopes the active feature per server and never disturbs other entries', () => {
    const store = makeStore();
    const serverA = 'a'.repeat(64);
    const serverB = 'b'.repeat(64);
    store.update({
      shell: { setActiveFeature: { serverKey: serverA, featureId: 'abcd1234ef567890' } },
    });
    store.update({
      shell: { setActiveFeature: { serverKey: serverB, featureId: 'ffee0011aa223344' } },
    });
    store.update({
      shell: { setActiveFeature: { serverKey: serverA, featureId: 'abcd1234ef567890' } },
    });
    expect(store.get().shell.featureByServer).toEqual({
      [serverA]: 'abcd1234ef567890',
      [serverB]: 'ffee0011aa223344',
    });
    // Most-recent-first: the touched key moves to the front.
    expect(Object.keys(store.get().shell.featureByServer)).toEqual([serverA, serverB]);
    // Clearing one entry leaves the other intact.
    store.update({ shell: { setActiveFeature: { serverKey: serverA, featureId: null } } });
    expect(store.get().shell.featureByServer).toEqual({ [serverB]: 'ffee0011aa223344' });
    expect(makeStore().get().shell.featureByServer).toEqual({ [serverB]: 'ffee0011aa223344' });
  });

  it('evicts the oldest featureByServer entry once the map exceeds the known-servers cap', () => {
    const store = makeStore();
    for (let seed = 1; seed <= MAX_KNOWN_SERVERS; seed += 1) {
      store.update({
        shell: {
          setActiveFeature: {
            serverKey: seed.toString(16).padStart(64, '0'),
            featureId: 'abcd1234ef567890',
          },
        },
      });
    }
    expect(Object.keys(store.get().shell.featureByServer)).toHaveLength(MAX_KNOWN_SERVERS);
    store.update({
      shell: {
        setActiveFeature: {
          serverKey: (MAX_KNOWN_SERVERS + 1).toString(16).padStart(64, '0'),
          featureId: 'abcd1234ef567890',
        },
      },
    });
    const keys = Object.keys(store.get().shell.featureByServer);
    expect(keys).toHaveLength(MAX_KNOWN_SERVERS);
    expect(keys).not.toContain((1).toString(16).padStart(64, '0'));
    expect(keys).toContain((2).toString(16).padStart(64, '0'));
  });

  it('rejects shell patches carrying unknown fields', () => {
    const store = makeStore();
    expect(() =>
      store.update({
        shell: {
          sidebarCollapsed: false,
          status: 'Created',
        } as never,
      }),
    ).toThrow(SafeErrorException);
  });

  it('rejects an empty shell patch section', () => {
    const store = makeStore();
    expect(() => store.update({ shell: {} as never })).toThrow(SafeErrorException);
    expect(fs.existsSync(settingsPath())).toBe(false);
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
    expect(upgraded.get().ama).toEqual(defaultAmaPrefs());
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

  describe('schema v1 -> v3 migration', () => {
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
        schemaVersion: 4,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded', geometry: defaultAmaGeometry() },
        notifications: { previewEnabled: true },
        // No last-used pointer exists to scope the v1 selection to.
        shell: { featureByServer: {}, sidebarCollapsed: false },
        settingsWindow: defaultSettingsWindowPrefs(),
        servers: defaultServersPrefs(),
      });
      expect(warnings).toEqual([]);
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);

      const onDisk = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
      expect(onDisk.schemaVersion).toBe(4);
      expect(onDisk.shell).toEqual({
        featureByServer: {},
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
      expect(store.get().shell).toEqual({ featureByServer: {}, sidebarCollapsed: false });
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

    it('still resets to defaults for a current-version but invalid document', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify({ ...defaultSettings(), shell: { featureByServer: 'nope' } }),
      );
      const store = makeStore();
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });

    it('round-trips a shell update through the existing update() API', () => {
      const store = makeStore();
      store.update({
        shell: {
          sidebarCollapsed: true,
          setActiveFeature: { serverKey: 'c'.repeat(64), featureId: 'abcd1234ef567890' },
        },
      });
      const reloaded = makeStore();
      expect(reloaded.get().shell).toEqual({
        featureByServer: { ['c'.repeat(64)]: 'abcd1234ef567890' },
        sidebarCollapsed: true,
      });
    });
  });

  describe('schema v2 -> v3 migration', () => {
    function v2Fixture(overrides: Record<string, unknown> = {}) {
      return {
        schemaVersion: 2,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded', geometry: { right: 96, bottom: 48, width: 520, height: 400 } },
        notifications: { previewEnabled: true },
        shell: { activeFeatureId: 'abcd1234ef567890', sidebarCollapsed: true },
        settingsWindow: {
          bounds: { x: 40, y: 60, width: 900, height: 640 },
          pane: 'diagnostics',
        },
        ...overrides,
      };
    }

    it('rewrites a v2 file as v4 and preserves every pre-existing field exactly', () => {
      fs.writeFileSync(settingsPath(), JSON.stringify(v2Fixture()));
      const store = makeStore();
      // The v2 global active id has no last-used pointer to scope to: the
      // v3→v4 step drops it while carrying everything else byte-for-byte.
      const migrated = {
        ...v2Fixture(),
        schemaVersion: 4,
        shell: { featureByServer: {}, sidebarCollapsed: true },
        servers: defaultServersPrefs(),
      };

      expect(store.get()).toEqual(migrated);
      expect(warnings).toEqual([]);
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);

      const onDisk = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
      expect(onDisk).toEqual(migrated);
    });

    it('migrates a v2 file that already carries a servers object as an unknown field to defaults', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify(v2Fixture({ servers: { known: [], rogue: true } })),
      );
      const store = makeStore();
      // The v2 shape is strict: any foreign field makes the document
      // unrecoverable, exactly like a corrupt file.
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });
  });

  describe('schema v3 -> v4 migration', () => {
    function v3Fixture(overrides: Record<string, unknown> = {}) {
      return {
        schemaVersion: 3,
        runtime: { selection: 'claude' },
        window: { bounds: { x: 10, y: 20, width: 1024, height: 768 } },
        theme: 'dark',
        wizard: { collapsedHelp: true },
        ama: { drawer: 'expanded', geometry: { right: 96, bottom: 48, width: 520, height: 400 } },
        notifications: { previewEnabled: true },
        shell: { activeFeatureId: 'abcd1234ef567890', sidebarCollapsed: true },
        settingsWindow: {
          bounds: { x: 40, y: 60, width: 900, height: 640 },
          pane: 'diagnostics',
        },
        servers: { known: [], lastUsed: null },
        ...overrides,
      };
    }

    it('seeds the per-server map from lastUsed + activeFeatureId and carries every other field', () => {
      const lastUsed = 'a'.repeat(64);
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify(v3Fixture({ servers: { known: [], lastUsed } })),
      );
      const store = makeStore();
      expect(store.get().shell).toEqual({
        featureByServer: { [lastUsed]: 'abcd1234ef567890' },
        sidebarCollapsed: true,
      });
      expect(store.get().runtime).toEqual({ selection: 'claude' });
      expect(store.get().servers.lastUsed).toBe(lastUsed);
      expect(store.get().settingsWindow.pane).toBe('diagnostics');
      expect(warnings).toEqual([]);
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);
      const onDisk = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
      expect(onDisk.schemaVersion).toBe(4);
      expect(onDisk.shell).toEqual({
        featureByServer: { [lastUsed]: 'abcd1234ef567890' },
        sidebarCollapsed: true,
      });
    });

    it('drops the global selection when there is no last-used pointer', () => {
      fs.writeFileSync(settingsPath(), JSON.stringify(v3Fixture()));
      const store = makeStore();
      expect(store.get().shell).toEqual({ featureByServer: {}, sidebarCollapsed: true });
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(false);
    });

    it('drops a null global selection even with a last-used pointer', () => {
      const lastUsed = 'b'.repeat(64);
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify(
          v3Fixture({
            shell: { activeFeatureId: null, sidebarCollapsed: false },
            servers: { known: [], lastUsed },
          }),
        ),
      );
      const store = makeStore();
      expect(store.get().shell).toEqual({ featureByServer: {}, sidebarCollapsed: false });
    });

    it('still resets to defaults for a v3-shaped but invalid document', () => {
      fs.writeFileSync(
        settingsPath(),
        JSON.stringify(v3Fixture({ shell: { activeFeatureId: 'ok', rogue: true } })),
      );
      const store = makeStore();
      expect(store.get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });
  });

  describe('known servers', () => {
    function knownServer(seed: number, patch: Partial<KnownServer> = {}): KnownServer {
      return {
        serverKey: seed.toString(16).padStart(64, '0'),
        name: `server-${seed}`,
        baseUrl: `http://127.0.0.1:${9000 + seed}`,
        runtimeDir: `/rt/server-${seed}`,
        lastSeenAt: new Date(1_700_000_000_000 + seed * 1000).toISOString(),
        ...patch,
      };
    }

    it('upserts a known server to the front, most-recent-first', () => {
      const store = makeStore();
      store.update({ servers: { upsertKnown: knownServer(1) } });
      store.update({ servers: { upsertKnown: knownServer(2) } });
      expect(store.get().servers.known).toEqual([knownServer(2), knownServer(1)]);
    });

    it('updates an existing serverKey in place, moves it to the front, and never grows the list', () => {
      const store = makeStore();
      store.update({ servers: { upsertKnown: knownServer(1) } });
      store.update({ servers: { upsertKnown: knownServer(2) } });
      const touched = knownServer(2, { name: 'renamed', lastSeenAt: '2026-08-10T00:00:00.000Z' });
      store.update({ servers: { upsertKnown: touched } });
      expect(store.get().servers.known).toEqual([touched, knownServer(1)]);
      expect(makeStore().get().servers.known).toEqual([touched, knownServer(1)]);
    });

    it('evicts the tail entry once the list exceeds MAX_KNOWN_SERVERS', () => {
      const store = makeStore();
      for (let seed = 1; seed <= MAX_KNOWN_SERVERS; seed += 1) {
        store.update({ servers: { upsertKnown: knownServer(seed) } });
      }
      expect(store.get().servers.known).toHaveLength(MAX_KNOWN_SERVERS);
      const newest = knownServer(MAX_KNOWN_SERVERS + 1);
      store.update({ servers: { upsertKnown: newest } });
      const keys = store.get().servers.known.map((entry) => entry.serverKey);
      expect(keys).toHaveLength(MAX_KNOWN_SERVERS);
      expect(keys[0]).toBe(newest.serverKey);
      expect(keys).not.toContain(knownServer(1).serverKey);
      expect(keys).toContain(knownServer(2).serverKey);
    });

    it('sets and clears the last-used pointer through the patch path', () => {
      const store = makeStore();
      const entry = knownServer(1);
      store.update({ servers: { upsertKnown: entry, lastUsed: entry.serverKey } });
      expect(store.get().servers.lastUsed).toBe(entry.serverKey);
      store.update({ servers: { lastUsed: null } });
      expect(store.get().servers.lastUsed).toBeNull();
      expect(makeStore().get().servers.lastUsed).toBeNull();
    });

    it('allows the last-used pointer to name a serverKey not present in known', () => {
      const store = makeStore();
      store.update({ servers: { lastUsed: 'f'.repeat(64) } });
      expect(store.get().servers.lastUsed).toBe('f'.repeat(64));
      expect(store.get().servers.known).toEqual([]);
    });

    it('rejects an upsert whose baseUrl is not a loopback http URL and corrupts nothing', () => {
      const store = makeStore();
      store.update({ servers: { upsertKnown: knownServer(1) } });
      const before = store.get();
      for (const baseUrl of [
        'https://127.0.0.1:9001',
        'http://example.com:9001',
        'http://192.168.1.10:9001',
        'not a url',
        '',
      ]) {
        expect(() =>
          store.update({ servers: { upsertKnown: knownServer(1, { baseUrl }) } }),
        ).toThrow(SafeErrorException);
      }
      expect(store.get()).toEqual(before);
      expect(JSON.parse(fs.readFileSync(settingsPath(), 'utf8')).servers.known).toEqual([
        knownServer(1),
      ]);
    });

    it('accepts every loopback spelling for baseUrl', () => {
      const store = makeStore();
      for (const [seed, baseUrl] of [
        [1, 'http://localhost:9001'],
        [2, 'http://127.0.0.1:9002'],
        [3, 'http://127.34.56.78:9003'],
        [4, 'http://[::1]:9004/ui'],
      ] as const) {
        store.update({ servers: { upsertKnown: knownServer(seed, { baseUrl }) } });
      }
      expect(store.get().servers.known).toHaveLength(4);
    });

    it('rejects token-shaped and otherwise unknown fields on an upsert, fail closed', () => {
      const store = makeStore();
      for (const rogue of [{ token: 'x' }, { authToken: 'x' }, { secret: 'x' }, { port: 9001 }]) {
        expect(() =>
          store.update({
            servers: { upsertKnown: { ...knownServer(1), ...rogue } as never },
          }),
        ).toThrow(SafeErrorException);
      }
      expect(store.get().servers).toEqual(defaultServersPrefs());
      expect(fs.existsSync(settingsPath())).toBe(false);
    });

    it('rejects an empty servers patch section', () => {
      const store = makeStore();
      expect(() => store.update({ servers: {} as never })).toThrow(SafeErrorException);
      expect(fs.existsSync(settingsPath())).toBe(false);
    });

    it('refuses to load a file whose known list exceeds the bound', () => {
      const store = makeStore();
      for (let seed = 1; seed <= MAX_KNOWN_SERVERS; seed += 1) {
        store.update({ servers: { upsertKnown: knownServer(seed) } });
      }
      const onDisk = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
      onDisk.servers.known.push(knownServer(MAX_KNOWN_SERVERS + 1));
      fs.writeFileSync(settingsPath(), JSON.stringify(onDisk));
      expect(makeStore().get()).toEqual(defaultSettings());
      expect(fs.existsSync(`${settingsPath()}.bak-1`)).toBe(true);
    });
  });
});
