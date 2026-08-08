/**
 * The menu bar's structure and enablement matrix, straight off the pure
 * builder — no Electron runtime involved, the same posture the window registry
 * established.
 */
import { describe, expect, it, vi } from 'vitest';
import type { MenuItemConstructorOptions } from 'electron';
import {
  buildApplicationMenuTemplate,
  inspectorToggleLabel,
  sidebarToggleLabel,
  type MenuTemplateDeps,
} from '../menuTemplate';
import {
  COMMAND_CATALOGUE,
  FEATURE_COMMAND_IDS,
  commandById,
  featureCommandEnablement,
  type FeatureCommandId,
} from '../../shared/commands';
import {
  disabledMainWindowUiState,
  sameMainWindowUiState,
  type AppRouteEvent,
  type MainWindowUiState,
} from '../../shared/ipc';

const FEATURE_ID = 'abcd1234ef567890';

function deps(
  uiState: MainWindowUiState,
  overrides: Partial<MenuTemplateDeps> = {},
): MenuTemplateDeps {
  return {
    appName: 'Agentico',
    uiState,
    showWindow: () => {},
    quit: () => {},
    route: () => {},
    adjustZoom: () => {},
    setZoom: () => {},
    ...overrides,
  };
}

function submenuOf(
  template: MenuItemConstructorOptions[],
  label: string,
): MenuItemConstructorOptions[] {
  const menu = template.find((item) => item.label === label);
  if (menu === undefined) throw new Error(`no ${label} menu`);
  return Array.isArray(menu.submenu) ? menu.submenu : [];
}

function itemById(
  template: MenuItemConstructorOptions[],
  id: string,
): MenuItemConstructorOptions | undefined {
  for (const item of template) {
    if (item.id === id) return item;
    const found = (Array.isArray(item.submenu) ? item.submenu : []).find(
      (child) => child.id === id,
    );
    if (found !== undefined) return found;
  }
  return undefined;
}

/** A summary with a feature selected and an explicit enabled set. */
function selected(
  enabled: readonly string[],
  overrides: Partial<MainWindowUiState> = {},
): MainWindowUiState {
  return {
    ...disabledMainWindowUiState(),
    activeFeatureId: FEATURE_ID,
    runtimeReady: true,
    inspectorAvailable: true,
    featureCommands: featureCommandEnablement(
      enabled.map((id) => ({ id, enabled: true, disabledReasons: [] })),
      { hasSelection: true },
    ),
    ...overrides,
  };
}

describe('application menu structure', () => {
  it('orders the top-level menus with Feature between View and Window', () => {
    const template = buildApplicationMenuTemplate(deps(disabledMainWindowUiState()));
    expect(template.map((item) => item.label ?? item.role)).toEqual([
      'Agentico',
      'File',
      'Navigate',
      'editMenu',
      'View',
      'Feature',
      'windowMenu',
    ]);
  });

  it('gives every catalogue entry a real menu command', () => {
    const template = buildApplicationMenuTemplate(deps(disabledMainWindowUiState()));
    for (const command of COMMAND_CATALOGUE) {
      expect(itemById(template, command.id), `${command.id} is missing from the menu bar`).not.toBe(
        undefined,
      );
    }
  });

  it('carries New Feature above the standard Close Window role in File', () => {
    const file = submenuOf(buildApplicationMenuTemplate(deps(disabledMainWindowUiState())), 'File');
    expect(file.map((item) => [item.id, item.label, item.accelerator, item.role])).toEqual([
      ['global.new-feature', 'New Feature', 'CommandOrControl+N', undefined],
      ['global.close-window', 'Close Window', undefined, 'close'],
    ]);
  });

  it('carries the two toggles above the zoom trio and full screen in View', () => {
    const view = submenuOf(buildApplicationMenuTemplate(deps(disabledMainWindowUiState())), 'View');
    expect(view.map((item) => item.id ?? item.role ?? item.type)).toEqual([
      'global.toggle-sidebar',
      'global.toggle-inspector',
      'separator',
      'view.zoom-in',
      'view.zoom-out',
      'view.actual-size',
      'separator',
      'togglefullscreen',
    ]);
  });

  it('leaves the ⌘⌃S binding displayed but unregistered, so the renderer toggles exactly once', () => {
    const item = itemById(
      buildApplicationMenuTemplate(deps(disabledMainWindowUiState())),
      'global.toggle-sidebar',
    );
    expect(item?.accelerator).toBe('Command+Control+S');
    expect(item?.registerAccelerator).toBe(false);
  });

  it('lists the fifteen feature verbs in catalogue order', () => {
    const feature = submenuOf(
      buildApplicationMenuTemplate(deps(disabledMainWindowUiState())),
      'Feature',
    );
    expect(feature.map((item) => item.id)).toEqual([...FEATURE_COMMAND_IDS]);
    expect(feature.map((item) => item.label)).toEqual(
      FEATURE_COMMAND_IDS.map((id) => commandById(id).label),
    );
  });
});

describe('application menu enablement matrix', () => {
  it('disables every feature verb — visible, never hidden — with no selection', () => {
    const feature = submenuOf(
      buildApplicationMenuTemplate(deps(disabledMainWindowUiState())),
      'Feature',
    );
    expect(feature).toHaveLength(FEATURE_COMMAND_IDS.length);
    expect(feature.every((item) => item.enabled === false)).toBe(true);
  });

  it('disables every feature verb while the runtime is not ready, even with a selection', () => {
    const uiState = selected(['start', 'delete'], { runtimeReady: false });
    const feature = submenuOf(buildApplicationMenuTemplate(deps(uiState)), 'Feature');
    expect(feature.every((item) => item.enabled === false)).toBe(true);
  });

  it('mirrors the pushed enabled map verb by verb', () => {
    const uiState = selected(['start', 'delete', 'rebase']);
    const feature = submenuOf(buildApplicationMenuTemplate(deps(uiState)), 'Feature');
    const enabled = new Map(feature.map((item) => [item.id as FeatureCommandId, item.enabled]));
    expect(enabled.get('feature.start')).toBe(true);
    expect(enabled.get('feature.delete')).toBe(true);
    expect(enabled.get('feature.rebase')).toBe(true);
    // Configuration is a local editor: enabled whenever a feature is selected.
    expect(enabled.get('feature.configuration')).toBe(true);
    expect(enabled.get('feature.pause-stop')).toBe(false);
    expect(enabled.get('feature.merge')).toBe(false);
  });

  it('gates New Feature on readiness alone and the inspector on having a feature', () => {
    const unready = buildApplicationMenuTemplate(deps(disabledMainWindowUiState()));
    expect(itemById(unready, 'global.new-feature')?.enabled).toBe(false);
    expect(itemById(unready, 'global.toggle-sidebar')?.enabled).toBe(false);
    expect(itemById(unready, 'global.toggle-inspector')?.enabled).toBe(false);

    const overview = buildApplicationMenuTemplate(
      deps({ ...disabledMainWindowUiState(), runtimeReady: true }),
    );
    expect(itemById(overview, 'global.new-feature')?.enabled).toBe(true);
    expect(itemById(overview, 'global.toggle-sidebar')?.enabled).toBe(true);
    expect(itemById(overview, 'global.toggle-inspector')?.enabled).toBe(false);

    const feature = buildApplicationMenuTemplate(deps(selected(['start'])));
    expect(itemById(feature, 'global.toggle-inspector')?.enabled).toBe(true);
  });

  it('flips the Show/Hide labels with the pushed chrome state', () => {
    const expanded = selected(['start']);
    expect(sidebarToggleLabel(expanded)).toBe('Hide Sidebar');
    expect(inspectorToggleLabel(expanded)).toBe('Show Inspector');
    const collapsed = selected(['start'], { sidebarCollapsed: true, inspectorOpen: true });
    expect(sidebarToggleLabel(collapsed)).toBe('Show Sidebar');
    expect(inspectorToggleLabel(collapsed)).toBe('Hide Inspector');

    const template = buildApplicationMenuTemplate(deps(collapsed));
    expect(itemById(template, 'global.toggle-sidebar')?.label).toBe('Show Sidebar');
    expect(itemById(template, 'global.toggle-inspector')?.label).toBe('Hide Inspector');
  });
});

describe('application menu dispatch', () => {
  it('routes a feature click by command identity, never by feature id', () => {
    const route = vi.fn<(event: AppRouteEvent) => void>();
    const template = buildApplicationMenuTemplate(deps(selected(['start']), { route }));
    const start = itemById(template, 'feature.start');
    start?.click?.(undefined as never, undefined as never, undefined as never);
    expect(route).toHaveBeenCalledWith({ target: 'feature-command', command: 'feature.start' });
    expect(JSON.stringify(route.mock.calls)).not.toContain(FEATURE_ID);
  });

  it('routes the global items to their catalogue targets and keeps Show and Quit direct', () => {
    const route = vi.fn<(event: AppRouteEvent) => void>();
    const showWindow = vi.fn();
    const quit = vi.fn();
    const template = buildApplicationMenuTemplate(
      deps(selected(['start']), { route, showWindow, quit }),
    );
    for (const [id, target] of [
      ['global.new-feature', 'new-feature'],
      ['global.toggle-sidebar', 'toggle-sidebar'],
      ['global.toggle-inspector', 'toggle-inspector'],
      ['global.home', 'home'],
      ['global.attention', 'attention'],
    ] as const) {
      route.mockClear();
      itemById(template, id)?.click?.(undefined as never, undefined as never, undefined as never);
      expect(route).toHaveBeenCalledWith({ target });
    }
    itemById(template, 'global.show')?.click?.(
      undefined as never,
      undefined as never,
      undefined as never,
    );
    itemById(template, 'global.quit')?.click?.(
      undefined as never,
      undefined as never,
      undefined as never,
    );
    expect(showWindow).toHaveBeenCalledTimes(1);
    expect(quit).toHaveBeenCalledTimes(1);
  });
});

describe('sameMainWindowUiState', () => {
  it('treats an identical summary as no change, so nothing rebuilds the menu', () => {
    expect(sameMainWindowUiState(selected(['start']), selected(['start']))).toBe(true);
    expect(sameMainWindowUiState(disabledMainWindowUiState(), disabledMainWindowUiState())).toBe(
      true,
    );
  });

  it('detects every axis the menu reads', () => {
    const base = selected(['start']);
    expect(sameMainWindowUiState(base, selected(['start', 'delete']))).toBe(false);
    expect(sameMainWindowUiState(base, { ...base, activeFeatureId: null })).toBe(false);
    expect(sameMainWindowUiState(base, { ...base, runtimeReady: false })).toBe(false);
    expect(sameMainWindowUiState(base, { ...base, sidebarCollapsed: true })).toBe(false);
    expect(sameMainWindowUiState(base, { ...base, inspectorOpen: true })).toBe(false);
    expect(sameMainWindowUiState(base, { ...base, inspectorAvailable: false })).toBe(false);
  });
});
