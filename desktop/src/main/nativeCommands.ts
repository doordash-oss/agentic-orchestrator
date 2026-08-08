import {
  BrowserWindow,
  Menu,
  Tray,
  nativeImage,
  type App,
  type MenuItemConstructorOptions,
} from 'electron';
import { commandById, type CommandId } from '../shared/commands';
import type { AppRouteEvent } from '../shared/ipc';

export interface NativeCommandControllerDeps {
  app: App;
  showWindow(): void;
  route(event: AppRouteEvent): void;
  quit(): void;
}

export interface BackgroundStatus {
  attentionCount: number;
  amaActive: boolean;
}

export interface NativeCommandSnapshot extends BackgroundStatus {
  trayInstalled: boolean;
  trayFallbackActive: boolean;
  platform: NodeJS.Platform;
}

export class NativeCommandController {
  private tray: Tray | null = null;
  private trayFallbackActive = false;
  private status: BackgroundStatus = { attentionCount: 0, amaActive: false };

  constructor(private readonly deps: NativeCommandControllerDeps) {}

  install(): void {
    Menu.setApplicationMenu(this.buildApplicationMenu());
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

  private commandItem(id: CommandId): MenuItemConstructorOptions {
    const command = commandById(id);
    const target = command.target;
    if (target === undefined) {
      throw new Error(`Command ${id} has no route target.`);
    }
    return {
      id,
      label: command.label,
      accelerator: command.accelerator,
      click: () => this.route(target),
    };
  }

  private showItem(): MenuItemConstructorOptions {
    const command = commandById('global.show');
    return {
      id: command.id,
      label: command.label,
      accelerator: command.accelerator,
      click: () => this.deps.showWindow(),
    };
  }

  private quitItem(): MenuItemConstructorOptions {
    const command = commandById('global.quit');
    return {
      id: command.id,
      label: command.label,
      accelerator: command.accelerator,
      click: () => this.deps.quit(),
    };
  }

  private buildApplicationMenu(): Menu {
    const appMenu: MenuItemConstructorOptions = {
      label: this.deps.app.name,
      submenu: [this.showItem(), { type: 'separator' }, this.quitItem()],
    };

    return Menu.buildFromTemplate([
      appMenu,
      // The standard File position, immediately after the app menu. `close`
      // is the platform role, so ⌘W always closes whichever window is
      // focused — the Settings window closes outright, the main window goes
      // through its own close handler and the quit decision behind it.
      {
        label: 'File',
        submenu: [{ role: 'close', label: 'Close Window' }],
      },
      {
        label: 'Navigate',
        submenu: [
          this.commandItem('global.palette'),
          this.commandItem('global.help'),
          { type: 'separator' },
          this.commandItem('global.home'),
          this.commandItem('global.settings'),
          {
            id: 'global.updates',
            label: 'Updates',
            click: () => this.routeEvent({ target: 'settings', settingsSection: 'updates' }),
          },
          this.commandItem('global.attention'),
          this.commandItem('global.ama'),
          this.commandItem('global.bulk'),
        ],
      },
      { role: 'editMenu' },
      {
        label: 'View',
        submenu: [
          {
            label: 'Zoom In',
            accelerator: 'CommandOrControl+=',
            click: () => adjustFocusedZoom(0.2),
          },
          {
            label: 'Zoom Out',
            accelerator: 'CommandOrControl+-',
            click: () => adjustFocusedZoom(-0.2),
          },
          {
            label: 'Actual Size',
            accelerator: 'CommandOrControl+0',
            click: () => setFocusedZoom(1),
          },
        ],
      },
      { role: 'windowMenu' },
    ]);
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
