import { describe, expect, it, vi } from 'vitest';
import { WindowRegistry, routeSettingsPane, routeWindowPurpose } from '../windowRegistry';
import type { AppRouteEvent, WindowPurpose } from '../../shared/ipc';

// --- helpers ---------------------------------------------------------------

interface FakeWindow {
  purpose: WindowPurpose;
  id: number;
}

function makeHarness(trusted?: Set<number>) {
  let nextId = 1;
  const created: FakeWindow[] = [];
  const create = vi.fn((purpose: WindowPurpose): FakeWindow => {
    const window = { purpose, id: nextId++ };
    created.push(window);
    return window;
  });
  const focus = vi.fn();
  const registry = new WindowRegistry<FakeWindow>(
    { create, focus, webContentsId: (window) => window.id },
    trusted,
  );
  return { registry, create, focus, created };
}

// --- registry ---------------------------------------------------------------

describe('WindowRegistry', () => {
  it('creates a window once and thereafter focuses it instead of duplicating', () => {
    const { registry, create, focus } = makeHarness();

    const first = registry.openOrFocus('settings');
    const second = registry.openOrFocus('settings');

    expect(second).toBe(first);
    expect(create).toHaveBeenCalledTimes(1);
    expect(create).toHaveBeenCalledWith('settings');
    expect(focus).toHaveBeenCalledTimes(1);
    expect(focus).toHaveBeenCalledWith(first);
    expect(registry.all()).toEqual([first]);
  });

  it('keeps one window per purpose and reports them in insertion order', () => {
    const { registry, create } = makeHarness();

    const main = registry.openOrFocus('main');
    const settings = registry.openOrFocus('settings');
    registry.openOrFocus('main');

    expect(create).toHaveBeenCalledTimes(2);
    expect(registry.all()).toEqual([main, settings]);
    expect(registry.peek('main')).toBe(main);
    expect(registry.peek('settings')).toBe(settings);
    expect(registry.purposeOf(settings)).toBe('settings');
  });

  it('has no open window and no trust before anything is opened', () => {
    const { registry } = makeHarness();
    expect(registry.peek('main')).toBeNull();
    expect(registry.peek('settings')).toBeNull();
    expect(registry.all()).toEqual([]);
    expect([...registry.trustedIds()]).toEqual([]);
  });

  it('trusts a window on creation through the caller-supplied set, by reference', () => {
    const trusted = new Set<number>();
    const { registry } = makeHarness(trusted);

    const main = registry.openOrFocus('main');
    const settings = registry.openOrFocus('settings');

    // The TrustedSender handed to the IPC handlers holds this exact set.
    expect(registry.trustedIds()).toBe(trusted);
    expect([...trusted]).toEqual([main.id, settings.id]);
  });

  it('evicts a closed window from the registry and from the trust set', () => {
    const trusted = new Set<number>();
    const { registry } = makeHarness(trusted);

    const main = registry.openOrFocus('main');
    const settings = registry.openOrFocus('settings');
    registry.evict(settings);

    expect(registry.peek('settings')).toBeNull();
    expect(registry.all()).toEqual([main]);
    expect(registry.all()).not.toContain(settings);
    expect(registry.purposeOf(settings)).toBeNull();
    expect(registry.trustedIds().has(settings.id)).toBe(false);
    expect(trusted.has(settings.id)).toBe(false);
    // Its sibling keeps its trust.
    expect(trusted.has(main.id)).toBe(true);
  });

  it('creates a fresh window and re-adds its trust when re-opened after eviction', () => {
    const trusted = new Set<number>();
    const { registry, create, focus } = makeHarness(trusted);

    const first = registry.openOrFocus('settings');
    registry.evict(first);
    const second = registry.openOrFocus('settings');

    expect(second).not.toBe(first);
    expect(create).toHaveBeenCalledTimes(2);
    expect(focus).not.toHaveBeenCalled();
    expect(registry.peek('settings')).toBe(second);
    expect([...trusted]).toEqual([second.id]);
  });

  it('is idempotent when the same window is evicted twice', () => {
    const trusted = new Set<number>();
    const { registry } = makeHarness(trusted);

    const settings = registry.openOrFocus('settings');
    registry.evict(settings);
    expect(() => registry.evict(settings)).not.toThrow();

    expect(registry.peek('settings')).toBeNull();
    expect(registry.all()).toEqual([]);
    expect([...trusted]).toEqual([]);
  });

  it('evicting a window already replaced under the same purpose keeps the replacement', () => {
    const trusted = new Set<number>();
    const { registry } = makeHarness(trusted);

    // A create-during-close: the replacement registers before the closing
    // window's 'closed' handler runs.
    const closing = registry.openOrFocus('settings');
    registry.evict(closing);
    const replacement = registry.openOrFocus('settings');
    registry.evict(closing);

    expect(registry.peek('settings')).toBe(replacement);
    expect(registry.all()).toEqual([replacement]);
    expect(trusted.has(replacement.id)).toBe(true);
    expect(trusted.has(closing.id)).toBe(false);
  });

  /**
   * Eviction is driven by Electron's `closed` event, and by then the window's
   * webContents is destroyed — reading `window.webContents.id` there throws
   * "Object has been destroyed" and takes the main process down with it. The
   * registry must therefore revoke the id it recorded at registration and
   * never touch the window it is being told is gone.
   */
  it('revokes trust without reading anything off the window being evicted', () => {
    const trusted = new Set<number>();
    let destroyed = false;
    const settings: FakeWindow = { purpose: 'settings', id: 42 };
    const registry = new WindowRegistry<FakeWindow>(
      {
        create: () => settings,
        focus: () => {},
        webContentsId: (window) => {
          if (destroyed) throw new Error('Object has been destroyed');
          return window.id;
        },
      },
      trusted,
    );

    registry.openOrFocus('settings');
    expect([...trusted]).toEqual([42]);

    // The window closes: its webContents is gone before `closed` fires.
    destroyed = true;
    expect(() => registry.evict(settings)).not.toThrow();

    expect(registry.peek('settings')).toBeNull();
    expect([...trusted]).toEqual([]);
  });
});

// --- route dispatch ---------------------------------------------------------

describe('routeWindowPurpose', () => {
  it('sends settings routes to the Settings window, with and without a section', () => {
    expect(routeWindowPurpose({ target: 'settings' })).toBe('settings');
    expect(routeWindowPurpose({ target: 'settings', settingsSection: 'updates' })).toBe('settings');
    expect(routeWindowPurpose({ target: 'settings', settingsSection: 'diagnostics' })).toBe(
      'settings',
    );
  });

  it('sends every other route to the main window', () => {
    const targets = ['home', 'attention', 'ama', 'bulk', 'palette', 'help'] as const;
    for (const target of targets) {
      expect(routeWindowPurpose({ target }), target).toBe('main');
    }
  });
});

describe('routeSettingsPane', () => {
  it('returns the deep-linked section for settings routes', () => {
    expect(routeSettingsPane({ target: 'settings', settingsSection: 'updates' })).toBe('updates');
    expect(routeSettingsPane({ target: 'settings', settingsSection: 'diagnostics' })).toBe(
      'diagnostics',
    );
  });

  it('returns null when no section is named, keeping the last-viewed pane', () => {
    expect(routeSettingsPane({ target: 'settings' })).toBeNull();
  });

  it('returns null for every non-settings route, even one carrying a section', () => {
    const targets = ['home', 'attention', 'ama', 'bulk', 'palette', 'help'] as const;
    for (const target of targets) {
      expect(routeSettingsPane({ target }), target).toBeNull();
      const event: AppRouteEvent = { target, settingsSection: 'updates' };
      expect(routeSettingsPane(event), target).toBeNull();
    }
  });
});
