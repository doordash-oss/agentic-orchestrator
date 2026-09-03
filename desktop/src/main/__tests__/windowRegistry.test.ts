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

import { describe, expect, it, vi } from 'vitest';
import {
  RendererCrashRecovery,
  WindowRegistry,
  routeSettingsPane,
  routeWindowPurpose,
} from '../windowRegistry';
import type { AppRouteEvent, WindowPurpose } from '../../shared/ipc';

// --- helpers ---------------------------------------------------------------

interface FakeWindow {
  purpose: WindowPurpose;
  id: number;
  crashed?: boolean;
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
  const reload = vi.fn();
  const registry = new WindowRegistry<FakeWindow>(
    {
      create,
      focus,
      isCrashed: (window) => window.crashed === true,
      reload,
      webContentsId: (window) => window.id,
    },
    trusted,
  );
  return { registry, create, focus, reload, created };
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
        isCrashed: () => false,
        reload: () => {},
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

  it('never reloads a live window on focus', () => {
    const { registry, reload, focus } = makeHarness();

    const main = registry.openOrFocus('main');
    registry.openOrFocus('main');

    expect(reload).not.toHaveBeenCalled();
    expect(focus).toHaveBeenCalledWith(main);
  });

  it('reloads a crashed window before focusing it, instead of raising a blank page', () => {
    const { registry, reload, focus } = makeHarness();

    const main = registry.openOrFocus('main');
    main.crashed = true;
    const raised = registry.openOrFocus('main');

    expect(raised).toBe(main);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(reload).toHaveBeenCalledWith(main);
    expect(focus).toHaveBeenCalledWith(main);
  });

  it('rebuilds fresh after a crashed window was destroyed and evicted', () => {
    const { registry, create, reload } = makeHarness();

    const dead = registry.openOrFocus('main');
    dead.crashed = true;
    // Repeated crashes exhausted the reload budget: the window was destroyed,
    // which fires `closed` and evicts it.
    registry.evict(dead);
    const rebuilt = registry.openOrFocus('main');

    expect(rebuilt).not.toBe(dead);
    expect(create).toHaveBeenCalledTimes(2);
    expect(reload).not.toHaveBeenCalled();
  });
});

// --- renderer crash recovery -------------------------------------------------

describe('RendererCrashRecovery', () => {
  function makeRecovery(maxReloads?: number) {
    const reload = vi.fn();
    const destroy = vi.fn();
    const recovery = new RendererCrashRecovery({ reload, destroy }, maxReloads);
    return { recovery, reload, destroy };
  }

  it('answers a non-clean exit with a reload', () => {
    const { recovery, reload, destroy } = makeRecovery();
    recovery.crashed('crashed');
    expect(reload).toHaveBeenCalledTimes(1);
    expect(destroy).not.toHaveBeenCalled();
  });

  it('ignores a clean exit', () => {
    const { recovery, reload, destroy } = makeRecovery();
    recovery.crashed('clean-exit');
    expect(reload).not.toHaveBeenCalled();
    expect(destroy).not.toHaveBeenCalled();
  });

  it('destroys the window once the reload budget is exhausted', () => {
    const { recovery, reload, destroy } = makeRecovery();
    recovery.crashed('oom');
    recovery.crashed('oom');
    recovery.crashed('oom');
    expect(reload).toHaveBeenCalledTimes(2);
    expect(destroy).toHaveBeenCalledTimes(1);
  });

  it('resets the budget when a load finishes, so later crashes reload again', () => {
    const { recovery, reload, destroy } = makeRecovery();
    recovery.crashed('crashed');
    recovery.crashed('crashed');
    recovery.loadFinished();
    recovery.crashed('crashed');
    expect(reload).toHaveBeenCalledTimes(3);
    expect(destroy).not.toHaveBeenCalled();
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
