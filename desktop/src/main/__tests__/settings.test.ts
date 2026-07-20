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
      schemaVersion: 1,
      runtime: { selection: null },
      window: { bounds: { x: 1, y: 2, width: 800, height: 600 } },
      theme: 'dark',
      wizard: { collapsedHelp: false, lastRepositoryPathHint: null },
      ama: { drawer: 'compact' },
      notifications: { previewEnabled: false },
      tabs: { open: [], activeFeatureId: null },
    });
  });

  it('persists tab identity/presentation prefs and restores them on reload', () => {
    const store = makeStore();
    store.update({
      tabs: {
        open: [{ featureId: 'abcd1234ef567890', titleHint: 'Search revamp' }],
        activeFeatureId: 'abcd1234ef567890',
      },
    });
    expect(makeStore().get().tabs).toEqual({
      open: [{ featureId: 'abcd1234ef567890', titleHint: 'Search revamp' }],
      activeFeatureId: 'abcd1234ef567890',
    });
  });

  it('rejects tab entries carrying server-domain state beyond identity/presentation', () => {
    const store = makeStore();
    expect(() =>
      store.update({
        tabs: {
          open: [
            {
              featureId: 'abcd1234ef567890',
              titleHint: 'Search',
              status: 'Created',
            } as never,
          ],
          activeFeatureId: null,
        },
      }),
    ).toThrow(SafeErrorException);
  });

  it('persists wizard presentation prefs and loads pre-wizard files with defaults', () => {
    const store = makeStore();
    store.update({ wizard: { collapsedHelp: true, lastRepositoryPathHint: '/work/repo' } });
    expect(makeStore().get().wizard).toEqual({
      collapsedHelp: true,
      lastRepositoryPathHint: '/work/repo',
    });

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
    expect(upgraded.get().wizard).toEqual({ collapsedHelp: false, lastRepositoryPathHint: null });
    expect(upgraded.get().ama).toEqual({ drawer: 'compact' });
    expect(upgraded.get().notifications).toEqual({ previewEnabled: false });
  });

  it('never throws while loading a corrupt file', () => {
    fs.writeFileSync(settingsPath(), 'corrupt');
    expect(() => makeStore().get()).not.toThrow();
  });
});
