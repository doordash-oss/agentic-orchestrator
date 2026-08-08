/**
 * The main process's single owner of window instances, keyed by purpose.
 *
 * Everything Electron-shaped is injected, so the registry (and the route
 * dispatch below it) carries direct unit coverage without an Electron
 * runtime — the same dependency-injected pattern the gateway, theme, and
 * quit coordinator use.
 *
 * The registry also owns the set of trusted webContents ids: a window joins
 * on creation and leaves on close, and the `TrustedSender` object handed to
 * every IPC handler holds that same set by reference, so the per-call guard
 * always sees the live membership.
 */
import type { AppRouteEvent, SettingsSection, WindowPurpose } from '../shared/ipc';

export interface WindowRegistryDeps<W> {
  /** Builds a brand-new window for a purpose. Called only when none is open. */
  create(purpose: WindowPurpose): W;
  /** Raises an already-open window (restore + show + focus). */
  focus(window: W): void;
  /**
   * The window's webContents id — its membership token in the trust set.
   * Read exactly once, at registration: eviction runs from Electron's
   * `closed` event, by which point the window's webContents is destroyed and
   * reading anything off it throws.
   */
  webContentsId(window: W): number;
}

export class WindowRegistry<W> {
  private readonly windows = new Map<WindowPurpose, W>();
  /**
   * Each registered window's id, remembered from registration so `evict` can
   * revoke trust without touching the window it is being told is gone.
   */
  private readonly registeredIds = new Map<W, number>();

  /**
   * `trustedWebContentsIds` may be supplied by the caller so the
   * `TrustedSender` handed to the IPC handlers — which has to exist before
   * any window does — shares this exact set rather than a copy of it.
   */
  constructor(
    private readonly deps: WindowRegistryDeps<W>,
    private readonly trustedWebContentsIds: Set<number> = new Set(),
  ) {}

  /**
   * The single entry point every open path goes through: creates the window
   * for `purpose` if none is open, otherwise focuses the existing one.
   * Never opens a second window for a purpose.
   */
  openOrFocus(purpose: WindowPurpose): W {
    const existing = this.windows.get(purpose);
    if (existing !== undefined) {
      this.deps.focus(existing);
      return existing;
    }
    const created = this.deps.create(purpose);
    const webContentsId = this.deps.webContentsId(created);
    this.windows.set(purpose, created);
    this.registeredIds.set(created, webContentsId);
    this.trustedWebContentsIds.add(webContentsId);
    return created;
  }

  /** The open window for a purpose, or null. Never creates one. */
  peek(purpose: WindowPurpose): W | null {
    return this.windows.get(purpose) ?? null;
  }

  /** Every open window, in insertion order — the event fan-out's audience. */
  all(): W[] {
    return [...this.windows.values()];
  }

  /**
   * Evicts a closed window. Called from the window's own `closed` event, so it
   * must never read anything off the window itself — the id it revokes is the
   * one remembered at registration. Idempotent, and a no-op when a replacement
   * has already registered under the same purpose, so a create-during-close
   * never drops the new window or its trust.
   */
  evict(window: W): void {
    const purpose = this.purposeOf(window);
    if (purpose !== null) {
      this.windows.delete(purpose);
    }
    const webContentsId = this.registeredIds.get(window);
    if (webContentsId !== undefined) {
      this.registeredIds.delete(window);
      this.trustedWebContentsIds.delete(webContentsId);
    }
  }

  purposeOf(window: W): WindowPurpose | null {
    for (const [purpose, candidate] of this.windows) {
      if (candidate === window) {
        return purpose;
      }
    }
    return null;
  }

  /**
   * The live trust membership, shared by reference with the `TrustedSender`
   * every IPC handler closes over.
   */
  trustedIds(): ReadonlySet<number> {
    return this.trustedWebContentsIds;
  }
}

// --- Route dispatch ----------------------------------------------------------

/** Which window a route targets: Settings routes go to the Settings window. */
export function routeWindowPurpose(event: AppRouteEvent): WindowPurpose {
  return event.target === 'settings' ? 'settings' : 'main';
}

/**
 * The pane a settings-targeted route lands on, or null to keep whichever pane
 * was last viewed. The deep-link section names are pane ids by construction.
 */
export function routeSettingsPane(event: AppRouteEvent): SettingsSection | null {
  if (event.target !== 'settings') {
    return null;
  }
  return event.settingsSection ?? null;
}
