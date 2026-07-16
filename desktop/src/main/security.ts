/**
 * Main-process security policy. Pure policy functions (unit-tested in
 * test/security) plus `installSecurityPolicies`, which wires them onto the
 * injected Electron `app`/`session` objects.
 *
 * Posture: sandboxed, context-isolated renderer with no node integration;
 * navigation, window creation, webviews, and permissions denied; strict CSP;
 * external links only through an https allowlist.
 */
import path from 'node:path';

// --- Renderer lockdown -------------------------------------------------------

export interface MainWindowWebPreferences {
  preload: string;
  sandbox: true;
  contextIsolation: true;
  nodeIntegration: false;
  nodeIntegrationInWorker: false;
  nodeIntegrationInSubFrames: false;
  webSecurity: true;
  allowRunningInsecureContent: false;
  experimentalFeatures: false;
  enableBlinkFeatures: '';
  webviewTag: false;
  spellcheck: false;
}

export function mainWindowWebPreferences(preloadPath: string): MainWindowWebPreferences {
  return {
    preload: preloadPath,
    sandbox: true,
    contextIsolation: true,
    nodeIntegration: false,
    nodeIntegrationInWorker: false,
    nodeIntegrationInSubFrames: false,
    webSecurity: true,
    allowRunningInsecureContent: false,
    experimentalFeatures: false,
    enableBlinkFeatures: '',
    webviewTag: false,
    spellcheck: false,
  };
}

// --- Content Security Policy -------------------------------------------------

/**
 * Strict CSP for app documents. `style-src` allows inline styles because
 * React style attributes require it; scripts remain self-only with no eval.
 */
export function buildCsp(): string {
  return [
    "default-src 'self'",
    "script-src 'self'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data:",
    "font-src 'self'",
    "connect-src 'self'",
    "object-src 'none'",
    "media-src 'none'",
    "frame-src 'none'",
    "worker-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
    "frame-ancestors 'none'",
  ].join('; ');
}

// --- URL policies -------------------------------------------------------------

/** Returns the comparison origin of a URL; file: URLs collapse to 'file://'. */
export function originOf(url: string): string | null {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return null;
  }
  if (parsed.protocol === 'file:') {
    return 'file://';
  }
  return parsed.origin;
}

/** Navigation is allowed only within the app's own origin. */
export function isAllowedNavigation(targetUrl: string, appOrigin: string): boolean {
  const origin = originOf(targetUrl);
  return origin !== null && origin === appOrigin;
}

/** All window creation is denied; external links go through openExternalSafely. */
export function windowOpenPolicy(): { action: 'deny' } {
  return { action: 'deny' };
}

/** Every Chromium permission request is denied. */
export function permissionRequestPolicy(_permission: string): false {
  return false;
}

/** Hosts the app may hand to shell.openExternal. */
export const EXTERNAL_URL_ALLOWLIST: ReadonlySet<string> = new Set([
  'github.com',
  'www.github.com',
]);

export function isSafeExternalUrl(url: string): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  return (
    parsed.protocol === 'https:' &&
    parsed.username === '' &&
    parsed.password === '' &&
    EXTERNAL_URL_ALLOWLIST.has(parsed.hostname)
  );
}

/**
 * Opens a URL in the system browser only if it passes the allowlist.
 * Returns whether the URL was accepted.
 */
export function openExternalSafely(
  url: string,
  openExternal: (url: string) => Promise<void>,
): boolean {
  if (!isSafeExternalUrl(url)) {
    return false;
  }
  void openExternal(url);
  return true;
}

// --- Safe path resolution ------------------------------------------------------

/**
 * Resolves a request path inside `root`, rejecting traversal (including
 * percent-encoded traversal) and absolute paths. Returns null when unsafe.
 */
export function resolveWithinRoot(root: string, requestPath: string): string | null {
  let decoded: string;
  try {
    decoded = decodeURIComponent(requestPath);
  } catch {
    return null;
  }
  if (decoded.includes('\0') || path.isAbsolute(decoded)) {
    return null;
  }
  const resolvedRoot = path.resolve(root);
  const resolved = path.resolve(resolvedRoot, decoded);
  if (resolved !== resolvedRoot && !resolved.startsWith(resolvedRoot + path.sep)) {
    return null;
  }
  return resolved;
}

// --- Sender validation -----------------------------------------------------------

export interface TrustedSender {
  /** webContents id of the app's main window. */
  webContentsId: number;
  /** Origins the renderer may legitimately be running on. */
  allowedOrigins: ReadonlySet<string>;
}

export interface SenderLikeEvent {
  sender: { id: number; send?(channel: string, payload: unknown): void };
  senderFrame: { url: string } | null;
}

/** Every ipcMain handler must pass its event through this check. */
export function isTrustedSender(event: SenderLikeEvent, trusted: TrustedSender): boolean {
  if (event.sender.id !== trusted.webContentsId) {
    return false;
  }
  if (event.senderFrame === null) {
    return false;
  }
  const origin = originOf(event.senderFrame.url);
  return origin !== null && trusted.allowedOrigins.has(origin);
}

// --- Wiring -----------------------------------------------------------------------

interface AppLike {
  on(
    event: 'web-contents-created',
    handler: (event: unknown, contents: ContentsLike) => void,
  ): void;
}

interface ContentsLike {
  on(event: string, handler: (event: { preventDefault(): void }, ...args: unknown[]) => void): void;
  setWindowOpenHandler(handler: (details: { url: string }) => { action: 'deny' }): void;
}

interface SessionLike {
  setPermissionRequestHandler(
    handler: (contents: unknown, permission: string, callback: (granted: boolean) => void) => void,
  ): void;
  webRequest: {
    onHeadersReceived(
      handler: (
        details: { responseHeaders?: Record<string, string[]> },
        callback: (response: { responseHeaders?: Record<string, string[]> }) => void,
      ) => void,
    ): void;
  };
}

export interface SecurityWiring {
  app: AppLike;
  session: SessionLike;
  /** Origins app documents may live on (dev server origin and/or 'file://'). */
  appOrigins: ReadonlySet<string>;
}

export function installSecurityPolicies({ app, session, appOrigins }: SecurityWiring): void {
  app.on('web-contents-created', (_event, contents) => {
    contents.on('will-navigate', (event, url) => {
      const target = typeof url === 'string' ? url : '';
      const origin = originOf(target);
      if (origin === null || !appOrigins.has(origin)) {
        event.preventDefault();
      }
    });
    contents.on('will-attach-webview', (event) => {
      event.preventDefault();
    });
    contents.setWindowOpenHandler(() => windowOpenPolicy());
  });

  session.setPermissionRequestHandler((_contents, permission, callback) => {
    callback(permissionRequestPolicy(permission));
  });

  session.webRequest.onHeadersReceived((details, callback) => {
    callback({
      responseHeaders: {
        ...details.responseHeaders,
        'Content-Security-Policy': [buildCsp()],
      },
    });
  });
}
