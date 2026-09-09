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

import { BrowserWindow, Menu, Tray, nativeImage, type App } from 'electron';
import { commandById } from '../shared/commands';
import {
  disabledMainWindowUiState,
  sameMainWindowUiState,
  type AppRouteEvent,
  type MainWindowUiState,
} from '../shared/ipc';
import { buildApplicationMenuTemplate } from './menuTemplate';

export interface NativeCommandControllerDeps {
  app: App;
  showWindow(): void;
  route(event: AppRouteEvent): void;
  quit(): void;
  /** Starts (or attaches to) the bundled runtime and switches to it. */
  startLocalRuntime(): void;
}

export interface BackgroundStatus {
  attentionCount: number;
  amaActive: boolean;
}

export interface NativeCommandSnapshot extends BackgroundStatus {
  trayInstalled: boolean;
  trayFallbackActive: boolean;
  platform: NodeJS.Platform;
  /** The summary the menu bar is currently built from. */
  uiState: MainWindowUiState;
  /** Bumped on every menu rebuild, so churn is observable from a test. */
  menuRevision: number;
}

export class NativeCommandController {
  private tray: Tray | null = null;
  private trayFallbackActive = false;
  private status: BackgroundStatus = { attentionCount: 0, amaActive: false };
  /** Everything-disabled until the main window's renderer pushes its first summary. */
  private uiState: MainWindowUiState = disabledMainWindowUiState();
  private menuRevision = 0;

  constructor(private readonly deps: NativeCommandControllerDeps) {}

  install(): void {
    this.applyApplicationMenu();
    try {
      this.tray = new Tray(createTrayIcon(this.status.attentionCount));
      this.tray.on('click', () => this.deps.showWindow());
      this.trayFallbackActive = false;
    } catch {
      this.tray = null;
      this.trayFallbackActive = true;
    }
    this.refreshTray();
  }

  update(status: BackgroundStatus): void {
    this.status = status;
    this.refreshTray();
  }

  /**
   * Accepts the main window's coarse UI-state summary. Identical consecutive
   * summaries are dropped, so nothing rebuilds the menu per poll or per frame.
   * Returns whether the menu was rebuilt.
   */
  updateUiState(next: MainWindowUiState): boolean {
    if (sameMainWindowUiState(this.uiState, next)) {
      return false;
    }
    this.uiState = next;
    this.applyApplicationMenu();
    return true;
  }

  /** The main window is gone: every window- and feature-scoped verb goes dark. */
  resetUiState(): void {
    this.updateUiState(disabledMainWindowUiState());
  }

  destroy(): void {
    this.tray?.destroy();
    this.tray = null;
  }

  snapshot(): NativeCommandSnapshot {
    return {
      ...this.status,
      trayInstalled: this.tray !== null,
      trayFallbackActive: this.trayFallbackActive,
      platform: process.platform,
      uiState: this.uiState,
      menuRevision: this.menuRevision,
    };
  }

  /**
   * Every menu and tray navigation goes straight to the injected dispatcher:
   * raising the right window is that dispatcher's job now, because a
   * settings-targeted route must raise the Settings window rather than the
   * main one.
   */
  private route(target: AppRouteEvent['target']): void {
    this.deps.route({ target });
  }

  private routeEvent(event: AppRouteEvent): void {
    this.deps.route(event);
  }

  private showItem() {
    const command = commandById('global.show');
    return {
      id: command.id,
      label: command.label,
      accelerator: command.accelerator,
      click: () => this.deps.showWindow(),
    };
  }

  private quitItem() {
    const command = commandById('global.quit');
    return {
      id: command.id,
      label: command.label,
      accelerator: command.accelerator,
      click: () => this.deps.quit(),
    };
  }

  /** Rebuilds and installs the menu bar from the current summary. */
  private applyApplicationMenu(): void {
    this.menuRevision += 1;
    Menu.setApplicationMenu(
      Menu.buildFromTemplate(
        buildApplicationMenuTemplate({
          appName: this.deps.app.name,
          uiState: this.uiState,
          showWindow: () => this.deps.showWindow(),
          quit: () => this.deps.quit(),
          route: (event) => this.routeEvent(event),
          startLocalRuntime: () => this.deps.startLocalRuntime(),
          adjustZoom: adjustFocusedZoom,
          setZoom: setFocusedZoom,
        }),
      ),
    );
  }

  private refreshTray(): void {
    if (this.tray === null) return;
    this.tray.setImage(createTrayIcon(this.status.attentionCount));
    this.tray.setToolTip(
      [
        'Agentico',
        `${this.status.attentionCount} attention`,
        this.status.amaActive ? 'AMA active' : 'AMA idle',
      ].join(' - '),
    );
    this.tray.setContextMenu(
      Menu.buildFromTemplate([
        this.showItem(),
        {
          label: `Attention (${this.status.attentionCount})`,
          click: () => this.route('attention'),
        },
        {
          label: this.status.amaActive ? 'AMA (active)' : 'AMA',
          click: () => this.route('ama'),
        },
        {
          label: 'Updates',
          click: () => this.routeEvent({ target: 'settings', settingsSection: 'updates' }),
        },
        { type: 'separator' },
        this.quitItem(),
      ]),
    );
  }
}

function adjustFocusedZoom(delta: number): void {
  const window = BrowserWindow.getFocusedWindow() ?? BrowserWindow.getAllWindows()[0];
  if (window === undefined || window.isDestroyed()) return;
  const next = Math.min(Math.max(window.webContents.getZoomFactor() + delta, 0.25), 5);
  window.webContents.setZoomFactor(next);
}

function setFocusedZoom(factor: number): void {
  const window = BrowserWindow.getFocusedWindow() ?? BrowserWindow.getAllWindows()[0];
  if (window === undefined || window.isDestroyed()) return;
  window.webContents.setZoomFactor(factor);
}

function createTrayIcon(attentionCount: number) {
  const color = attentionCount > 0 ? '#B45309' : '#2563EB';
  const badge = attentionCount > 0 ? '<circle cx="22" cy="6" r="5" fill="#DC2626"/>' : '';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24"><rect width="24" height="24" rx="5" fill="${color}"/><text x="12" y="16" text-anchor="middle" font-family="Arial, sans-serif" font-size="13" font-weight="700" fill="#fff">A</text>${badge}</svg>`;
  const image = nativeImage.createFromDataURL(
    `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`,
  );
  image.setTemplateImage(process.platform === 'darwin' && attentionCount === 0);
  return image;
}
