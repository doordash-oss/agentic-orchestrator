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
 * Servers-pane removal orchestration, kept main-side in full. Order of
 * operations is load-bearing:
 *
 *  1. The token blob is dropped first (remote entries only; locals carry no
 *     remote token) while the entry still names the serverKey.
 *  2. The settings entry is removed (removeKnown), including clearing a
 *     last-used pointer at the removed server.
 *  3. The gateway tears the connection down only if it was pointed at the
 *     removed server — settings already updated, so the fresh connect cycle
 *     can never reattach to the removed entry.
 *
 * The token is never read here: removal needs only the serverKey, and the
 * store deletes by key. Nothing about the removed server (URL, token,
 * runtime dir) lands in logs beyond its kind and a key prefix.
 */
import { buildCanonicalError, CanonicalErrorException } from '../../shared/errors';
import type {
  ConnectionState,
  ServerRemoveRequest,
  ServerTokenStatusRequest,
  ServerTokenStatusResult,
  ServersPrefs,
} from '../../shared/ipc';
import type { LoadResult } from './remoteTokenStore';

export interface RemoveServerDeps {
  /** Persisted known-servers view (bounded list + last-used pointer). */
  knownServers(): ServersPrefs;
  /** Deletes the OS-encrypted token blob for a remote serverKey. */
  removeRemoteToken(serverKey: string): void;
  /** Removes the settings entry (removeKnown + last-used cleanup). */
  removeKnownEntry(serverKey: string): void;
  /**
   * Generation-fenced teardown back into the startup selection flow when
   * the removed server is the active connection; a no-op otherwise.
   */
  disconnectServer(request: ServerRemoveRequest): Promise<ConnectionState>;
  /** Redacted local diagnostics sink. */
  log(line: string): void;
}

export const E_SERVER_UNKNOWN = 'E_SERVER_UNKNOWN';

export async function removeKnownServer(
  request: ServerRemoveRequest,
  deps: RemoveServerDeps,
): Promise<ConnectionState> {
  const entry = deps.knownServers().known.find((item) => item.serverKey === request.serverKey);
  if (entry === undefined) {
    throw new CanonicalErrorException(buildCanonicalError(E_SERVER_UNKNOWN));
  }
  if (entry.kind === 'remote') {
    deps.removeRemoteToken(entry.serverKey);
  }
  deps.removeKnownEntry(entry.serverKey);
  deps.log(`removed ${entry.kind} server ${entry.serverKey.slice(0, 8)}…`);
  // disconnectServer is a no-op when the removed server was not the active
  // connection, so both paths hand back the resulting ConnectionState.
  return deps.disconnectServer(request);
}

export interface ServerTokenStatusDeps {
  /** Persisted known-servers view (bounded list + last-used pointer). */
  knownServers(): ServersPrefs;
  /**
   * Loads the OS-encrypted token blob for a remote serverKey. The loaded
   * token is never returned here — only its storage-health category.
   */
  loadRemoteToken(serverKey: string): LoadResult;
}

/** Stored-credential status for the Servers pane's details affordance. */
export function serverTokenStatus(
  request: ServerTokenStatusRequest,
  deps: ServerTokenStatusDeps,
): ServerTokenStatusResult {
  const entry = deps.knownServers().known.find((item) => item.serverKey === request.serverKey);
  if (entry === undefined) {
    throw new CanonicalErrorException(buildCanonicalError(E_SERVER_UNKNOWN));
  }
  if (entry.kind === 'local') {
    return { status: 'local' };
  }
  const loaded = deps.loadRemoteToken(entry.serverKey);
  if (loaded.status === 'ok') {
    return { status: 'saved' };
  }
  if (loaded.status === 're-paste-required') {
    return { status: 're-paste-required' };
  }
  // A persisted remote entry without a readable blob: session-only until a
  // re-paste restores it.
  return { status: 'session-only' };
}
