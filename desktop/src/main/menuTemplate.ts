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
 * The application menu bar as a pure, dependency-injected template builder —
 * the same testability posture as the window registry: nothing Electron-shaped
 * is constructed here (only a type is imported, which erases at build time), so
 * the enablement matrix, labels, order, and accelerators carry direct unit
 * coverage without an Electron runtime.
 *
 * Enablement comes entirely from the main window's pushed UI-state summary, so
 * the menu and the ⌘K palette read the same action catalogue and can never
 * disagree. Inapplicable items are disabled and visible — never hidden.
 */
import type { MenuItemConstructorOptions } from 'electron';
import {
  FEATURE_COMMANDS,
  commandById,
  type CommandId,
  type FeatureCommandId,
} from '../shared/commands';
import type { AppRouteEvent, MainWindowUiState } from '../shared/ipc';

export interface MenuTemplateDeps {
  /** The app's display name, which titles the leading app menu. */
  appName: string;
  uiState: MainWindowUiState;
  showWindow(): void;
  quit(): void;
  route(event: AppRouteEvent): void;
  /**
   * Starts (or attaches to) the app's own bundled runtime and switches to it.
   * A main-process action, not a route: it must stay reachable from a
   * connection error state, where no renderer route target exists.
   */
  startLocalRuntime(): void;
  adjustZoom(delta: number): void;
  setZoom(factor: number): void;
}

/** True when the summary allows any per-feature verb at all. */
function featureMenuLive(uiState: MainWindowUiState): boolean {
  return uiState.runtimeReady && uiState.activeFeatureId !== null;
}

function featureCommandEnabled(uiState: MainWindowUiState, id: FeatureCommandId): boolean {
  return featureMenuLive(uiState) && uiState.featureCommands[id] === true;
}

/** The macOS-convention Show/Hide labels, flipped by the pushed state. */
export function sidebarToggleLabel(uiState: MainWindowUiState): string {
  return uiState.sidebarCollapsed ? 'Show Sidebar' : 'Hide Sidebar';
}

export function inspectorToggleLabel(uiState: MainWindowUiState): string {
  return uiState.inspectorOpen ? 'Hide Inspector' : 'Show Inspector';
}

/**
 * A routed catalogue item. A `rendererAccelerator` command (the ⌘⌃S case) is
 * built with `registerAccelerator: false`, so off macOS the binding is
 * displayed but not registered and the renderer's listener stays the single
 * handler; on macOS the native key equivalent consumes the event instead.
 * Either way the toggle fires exactly once per press.
 */
function routedItem(
  id: CommandId,
  deps: MenuTemplateDeps,
  options: { label?: string; enabled?: boolean } = {},
): MenuItemConstructorOptions {
  const command = commandById(id);
  const target = command.target;
  if (target === undefined) {
    throw new Error(`Command ${id} has no route target.`);
  }
  return {
    id,
    label: options.label ?? command.label,
    ...(command.accelerator === undefined ? {} : { accelerator: command.accelerator }),
    ...(command.rendererAccelerator === true ? { registerAccelerator: false } : {}),
    ...(options.enabled === undefined ? {} : { enabled: options.enabled }),
    click: () => deps.route({ target }),
  };
}

function featureItem(command: (typeof FEATURE_COMMANDS)[number], deps: MenuTemplateDeps) {
  const id = command.id as FeatureCommandId;
  return {
    id,
    label: command.label,
    enabled: featureCommandEnabled(deps.uiState, id),
    // The route carries the command's identity only — the renderer funnel
    // resolves the target from the live selection, so a click racing a
    // selection change can never act on a stale feature.
    click: () => deps.route({ target: 'feature-command', command: id }),
  } satisfies MenuItemConstructorOptions;
}

export function buildApplicationMenuTemplate(deps: MenuTemplateDeps): MenuItemConstructorOptions[] {
  const { uiState } = deps;
  const showCommand = commandById('global.show');
  const quitCommand = commandById('global.quit');
  const closeCommand = commandById('global.close-window');

  const appMenu: MenuItemConstructorOptions = {
    label: deps.appName,
    submenu: [
      {
        id: showCommand.id,
        label: showCommand.label,
        accelerator: showCommand.accelerator,
        click: () => deps.showWindow(),
      },
      { type: 'separator' },
      {
        id: quitCommand.id,
        label: quitCommand.label,
        accelerator: quitCommand.accelerator,
        click: () => deps.quit(),
      },
    ],
  };

  return [
    appMenu,
    // The standard File position, immediately after the app menu. New Feature
    // is shell-level by design — it opens the creation sheet over whatever
    // pane is current — so it is enabled on readiness alone, regardless of
    // selection. `close` is the platform role, so ⌘W always closes whichever
    // window is focused. Settings always closes outright; on macOS the main
    // window closes while the application remains active, while other platforms
    // send a main-window close through the coordinated quit decision.
    {
      label: 'File',
      submenu: [
        routedItem('global.new-feature', deps, { enabled: uiState.runtimeReady }),
        { id: closeCommand.id, role: 'close', label: closeCommand.label },
      ],
    },
    {
      label: 'Navigate',
      submenu: [
        routedItem('global.palette', deps),
        routedItem('global.help', deps),
        { type: 'separator' },
        routedItem('global.home', deps),
        routedItem('global.settings', deps),
        {
          id: 'global.updates',
          label: 'Updates',
          click: () => deps.route({ target: 'settings', settingsSection: 'updates' }),
        },
        routedItem('global.switch-server', deps, { enabled: uiState.runtimeReady }),
        {
          // Always enabled: this is the escape hatch back to the local
          // runtime from a remote connection or a failed link launch.
          id: 'global.start-local-runtime',
          label: 'Start bundled runtime',
          click: () => deps.startLocalRuntime(),
        },
        routedItem('global.attention', deps),
        routedItem('global.ama', deps),
        routedItem('global.bulk', deps),
      ],
    },
    { role: 'editMenu' },
    {
      label: 'View',
      submenu: [
        routedItem('global.toggle-sidebar', deps, {
          label: sidebarToggleLabel(uiState),
          enabled: uiState.runtimeReady,
        }),
        routedItem('global.toggle-inspector', deps, {
          label: inspectorToggleLabel(uiState),
          enabled: uiState.runtimeReady && uiState.inspectorAvailable,
        }),
        { type: 'separator' },
        // Escape hatch for a crashed or wedged renderer: the platform roles
        // reload whichever window is focused.
        { id: 'view.reload', role: 'reload', accelerator: 'CommandOrControl+R' },
        { id: 'view.force-reload', role: 'forceReload', accelerator: 'CommandOrControl+Shift+R' },
        { type: 'separator' },
        {
          id: 'view.zoom-in',
          label: 'Zoom In',
          accelerator: 'CommandOrControl+=',
          click: () => deps.adjustZoom(0.2),
        },
        {
          id: 'view.zoom-out',
          label: 'Zoom Out',
          accelerator: 'CommandOrControl+-',
          click: () => deps.adjustZoom(-0.2),
        },
        {
          id: 'view.actual-size',
          label: 'Actual Size',
          accelerator: 'CommandOrControl+0',
          click: () => deps.setZoom(1),
        },
        { type: 'separator' },
        { role: 'togglefullscreen' },
      ],
    },
    // The HIG app-specific position: between View and Window. Every verb is
    // always present; an inapplicable one is disabled, never missing.
    {
      label: 'Feature',
      submenu: FEATURE_COMMANDS.map((command) => featureItem(command, deps)),
    },
    { role: 'windowMenu' },
  ];
}
