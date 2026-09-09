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
 * agentico:// deep-link routing (main process only).
 *
 * A deep link is either one of the named app routes (updates, diagnostics,
 * servers, servers/add) or a full connection string
 * (agentico://<token>@<host>:<port>[?name=<name>]) — the same string the
 * Settings → Servers add form accepts. The connection-string variant embeds
 * the server's bearer token, so an ExternalRoute must never be logged,
 * serialized into diagnostics, or forwarded to a renderer; only the
 * `app-route` variant's event may leave the main process as an IPC event.
 */
import { toCanonicalError, type CanonicalError } from '../shared/errors';
import {
  type AppRouteEvent,
  type RemoteServerAddRequest,
  type RemoteServerAddResult,
  type SwitchServerRequest,
} from '../shared/ipc';
import { parseConnectionString } from './connectionString';

export type ExternalRoute =
  { kind: 'app-route'; event: AppRouteEvent } | { kind: 'add-server'; connectionString: string };

/** Fallback code when the add pipeline throws something untyped. */
export const E_LINK_ADD_FAILED = 'E_LINK_ADD_FAILED';

export function routeFromArgv(argv: readonly string[]): ExternalRoute | null {
  if (argv.some((arg) => arg === '--agentico-route=updates')) {
    return appRoute({ target: 'settings', settingsSection: 'updates' });
  }
  const url = argv.find((arg) => arg.startsWith('agentico://'));
  return url === undefined ? null : routeFromUrl(url);
}

export function routeFromUrl(raw: string): ExternalRoute | null {
  // A link that is itself a connection string (URL userinfo carrying the
  // bearer token, explicit port) is an add-server intent. The strict parser
  // decides; anything it rejects falls through to the named routes.
  try {
    parseConnectionString(raw);
    return { kind: 'add-server', connectionString: raw };
  } catch {
    // Not a connection string — try the named routes below.
  }
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return null;
  }
  if (parsed.protocol !== 'agentico:') {
    return null;
  }
  if (parsed.hostname === 'updates' || parsed.pathname === '/updates') {
    return appRoute({ target: 'settings', settingsSection: 'updates' });
  }
  if (parsed.hostname === 'diagnostics' || parsed.pathname === '/diagnostics') {
    return appRoute({ target: 'settings', settingsSection: 'diagnostics' });
  }
  // agentico://servers and agentico://servers/add — the latter carries the
  // within-pane intent to focus the Servers pane's add-server form.
  const isServers =
    parsed.hostname === 'servers' ||
    parsed.pathname === '/servers' ||
    parsed.pathname === '/servers/add';
  if (isServers) {
    const addIntent = parsed.pathname === '/add' || parsed.pathname === '/servers/add';
    return appRoute({
      target: 'settings',
      settingsSection: 'servers',
      ...(addIntent ? { settingsFocus: 'add-server' as const } : {}),
    });
  }
  return appRoute({ target: 'settings' });
}

function appRoute(event: AppRouteEvent): ExternalRoute {
  return { kind: 'app-route', event };
}

/**
 * The route a cold start honors. Windows and Linux carry the link in argv;
 * macOS delivers it as an `open-url` event that fires before `ready` and is
 * never replayed, so the caller buffers those raw URLs at module load and
 * hands them over here. argv wins, then the newest parseable buffered link.
 */
export function initialExternalRoute(
  argv: readonly string[],
  bufferedUrls: readonly string[],
): ExternalRoute | null {
  const fromArgv = routeFromArgv(argv);
  if (fromArgv !== null) {
    return fromArgv;
  }
  for (let index = bufferedUrls.length - 1; index >= 0; index -= 1) {
    const route = routeFromUrl(bufferedUrls[index]!);
    if (route !== null) {
      return route;
    }
  }
  return null;
}

export interface AddServerLinkDeps {
  /** The Settings → Servers add pipeline (probe, compat gate, verify, persist). */
  addServer(request: RemoteServerAddRequest): Promise<RemoteServerAddResult>;
  switchServer(request: SwitchServerRequest): Promise<unknown>;
  route(event: AppRouteEvent): void;
  /** Native notification; the body must already be redaction-safe. */
  notify(body: string): void;
  /** Redacted diagnostics sink; never receives the link or the token. */
  log(line: string): void;
}

/**
 * Runs a connection-string deep link through the exact add pipeline the
 * Settings → Servers form uses, then switches the active connection to the
 * added server. `duplicate-local` is success here — the server is already
 * known, so the link just switches to it. Failures land on the Servers pane
 * with the add form focused, the error carried by a native notification
 * (canonical summaries never echo the link or its token).
 *
 * Resolves `added: true` once the server is known (added or already present)
 * and the switch has been attempted; `added: false` carries the canonical
 * error when the add pipeline rejected the link, so a cold start can fail
 * visibly with the real reason.
 */
export type AddServerLinkOutcome = { added: true } | { added: false; error: CanonicalError };

export async function addServerFromLink(
  connectionString: string,
  deps: AddServerLinkDeps,
): Promise<AddServerLinkOutcome> {
  let result: RemoteServerAddResult;
  try {
    result = await deps.addServer({ connectionString });
  } catch (err) {
    const safe = toCanonicalError(err, E_LINK_ADD_FAILED);
    deps.log(`add-server link failed: ${safe.code}`);
    deps.notify(`${safe.title}. ${safe.summary}`);
    deps.route({ target: 'settings', settingsSection: 'servers', settingsFocus: 'add-server' });
    return { added: false, error: safe };
  }
  try {
    await deps.switchServer({ serverKey: result.serverKey });
  } catch {
    // The connection shell owns a switch's failure surface, exactly as the
    // Servers pane treats it.
  }
  if (result.status === 'session-only') {
    deps.notify(
      'Server connected for this session only — the OS keychain is unavailable, so it was not saved.',
    );
  }
  deps.route({ target: 'settings', settingsSection: 'servers' });
  return { added: true };
}
