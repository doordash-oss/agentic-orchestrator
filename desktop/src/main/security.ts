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
 * Main-process security policy. Pure policy functions (unit-tested in
 * test/security) plus `installSecurityPolicies`, which wires them onto the
 * injected Electron `app`/`session` objects.
 *
 * Posture: sandboxed, context-isolated renderer with no node integration;
 * navigation, window creation, webviews, and permissions denied; strict CSP;
 * external links only through the credential-free https policy below.
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
  // Node treats non-special schemes as opaque even when Electron registered
  // them as standard schemes. Reconstruct the tuple origin Electron uses so
  // packaged renderer IPC and navigation checks remain origin-bound.
  if (
    parsed.origin === 'null' &&
    parsed.hostname !== '' &&
    parsed.username === '' &&
    parsed.password === ''
  ) {
    return `${parsed.protocol}//${parsed.host}`;
  }
  return parsed.origin;
}

/** Navigation is allowed only within the app's own origin. */
export function isAllowedNavigation(targetUrl: string, appOrigin: string): boolean {
  const origin = originOf(targetUrl);
  return origin !== null && origin === appOrigin;
}

/** All window creation is denied. */
export function windowOpenPolicy(): { action: 'deny' } {
  return { action: 'deny' };
}

/** Every Chromium permission request is denied. */
export function permissionRequestPolicy(_permission: string): false {
  return false;
}

/**
 * The external-browser trust boundary: only well-formed, absolute https URLs
 * without embedded credentials may be handed to the OS opener. Review
 * feedback links to any host travel this boundary, so the policy is host
 * agnostic; scheme and credential hygiene remain the hard constraints, and
 * every other surface protection (navigation, windows, CSP, permissions)
 * stays unchanged and separate.
 */
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
    parsed.hostname !== ''
  );
}

/**
 * Opens a URL in the system browser only if it passes the policy.
 * Resolves to whether the URL was accepted; rejects if the opener fails so
 * callers can surface the error instead of silently dropping it.
 */
export async function openExternalSafely(
  url: string,
  openExternal: (url: string) => Promise<void>,
): Promise<boolean> {
  if (!isSafeExternalUrl(url)) {
    return false;
  }
  await openExternal(url);
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
  /**
   * webContents ids of the app's own registered windows. A window joins on
   * creation and is evicted on close, so a closed window's id stops being
   * trusted even if it is later reused. Held by reference (the window
   * registry owns the set) so handlers always see live membership.
   */
  webContentsIds: ReadonlySet<number>;
  /** Origins the renderer may legitimately be running on. */
  allowedOrigins: ReadonlySet<string>;
}

export interface SenderLikeEvent {
  sender: { id: number; send?(channel: string, payload: unknown): void };
  senderFrame: { url: string } | null;
}

/** Every ipcMain handler must pass its event through this check. */
export function isTrustedSender(event: SenderLikeEvent, trusted: TrustedSender): boolean {
  if (!trusted.webContentsIds.has(event.sender.id)) {
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
      const allowed = [...appOrigins].some((origin) => isAllowedNavigation(target, origin));
      if (!allowed) {
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
