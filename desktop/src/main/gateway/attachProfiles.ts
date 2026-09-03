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
 * The local-attach and remote-attach profiles behind the runtime gateway.
 *
 * Both profiles share one attach pipeline: an auth-exempt health probe, a
 * fail-closed compatibility evaluation, then a tokened readiness round-trip
 * before the gateway stamps the connection ready. They differ only in input
 * provenance (a discovery/registry record vs. a persisted known-server
 * entry), token source (discovery record vs. encrypted token store), and
 * their terminal-surfaces.
 *
 * Invariants kept from the gateway:
 *  - The bearer token only ever reaches the host's private field; it never
 *    enters a state, payload, URL, or log line.
 *  - The health probe stays auth-exempt: compatibility is evaluated before
 *    any credential is presented.
 *  - Every failure lands in a renderer-visible state with redacted
 *    diagnostics and a manual retry path.
 */
import { z } from 'zod';
import type { ConnectionState, KnownServer, ServersPrefs, SwitchContext } from '../../shared/ipc';
import {
  evaluateCompatibility,
  type BuildIdentity,
  type CompatibilityVerdict,
} from './compatibility';
import type { DiscoveryRecord } from './discovery';
import { registryEntryKey } from './registry';
import type { LoadResult as RemoteTokenLoadResult } from './remoteTokenStore';
import type { HttpResult, SelectedRuntime } from './runtimeGateway';

/**
 * Lenient view of /api/v1/health used while probing possibly-foreign
 * servers: only what the attach decision needs, tolerant of unknown fields
 * and future shapes. The compatibility declaration itself is validated
 * separately (fail-closed) by evaluateCompatibility.
 */
export const ProbeHealthSchema = z.object({
  status: z.string(),
  compatibility: z.unknown().optional(),
  runtime: z.object({ state_dir: z.string() }).optional(),
  // Operator-assigned display name (server cap: MaxServerNameLength = 64).
  // Informational only — an oversized or malformed name is dropped, never
  // treated as an attach blocker.
  name: z.string().max(64).optional().catch(undefined),
});

export type ProbeHealth = z.infer<typeof ProbeHealthSchema>;

export function trimBase(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, '');
}

export const REMOTE_REPASTE_REMEDIATION =
  'Re-enter the remote server token in Settings (paste its connection string again), then use Retry.';

const INCOMPATIBLE_REMEDIATION =
  'Update the Agentico desktop app and the agentico runtime to matching releases, then retry. ' +
  'This app never shuts down a runtime it does not own — close that runtime from wherever it ' +
  'was started if you want this app to manage its own.';

/**
 * What a local attach does when the candidate server turns out unreachable
 * or stale before compatibility is decided: `launch` falls through to the
 * legacy/spawn paths (startup scan race), `error` lands in the visible
 * error/retry state (a server the user explicitly picked died mid-pick).
 */
export type StaleHandling = 'launch' | 'error';

export type AttachOutcome = 'attached' | 'blocked' | 'launch';

/** Connection identity a successful profile hands back to the gateway. */
export interface AttachedConnection {
  baseUrl: string;
  serverKey: string;
  connectedKind: 'local' | 'remote';
  /** The refreshed known-server entry for remote attaches; null for local. */
  remoteEntry: KnownServer | null;
}

/**
 * The narrow gateway surface the profiles run against. All connection-field
 * writes go through these methods so the profiles never touch gateway state
 * directly; state emission and generation fencing stay in the gateway.
 */
export interface AttachHost {
  fetchJson(url: string, options: { token?: string; timeoutMs: number }): Promise<HttpResult>;
  log(line: string): void;
  registerSecret(secret: string): void;
  knownServers(): ServersPrefs;
  recordAttachedServer(entry: KnownServer): void;
  remoteTokens?: {
    load(serverKey: string): RemoteTokenLoadResult;
  };
  healthProbeMs: number;
  now(): number;
  setState(next: ConnectionState): void;
  cancelled(generation: number): boolean;
  /** Tokened readiness probe; uses the host's current bearer. */
  fetchReadiness(baseUrl: string): Promise<boolean>;
  scrubServerText(text: string | null, secret: string): string | null;
  /** Stores/clears the bearer credential (host private field). */
  setToken(token: string | null): void;
  /** PID of the live app-owned child, for launch re-own detection. */
  liveOwnedPid(): number | null;
  parkChoiceForDeadRemote(generation: number, dead: KnownServer): Promise<boolean>;
  /** Stamps the connection identity fields before the ready state emits. */
  beginAttachedConnection(connection: AttachedConnection): void;
  /** Marks the connection ready (crash-budget reset clock). */
  markReady(): void;
}

type HealthProbeResult =
  | { kind: 'healthy'; probe: ProbeHealth }
  | { kind: 'unreachable' }
  | { kind: 'bad-status' }
  | { kind: 'bad-payload' };

/** The shared pipeline's first step: one bounded auth-exempt health probe. */
async function probeAuthExemptHealth(
  host: AttachHost,
  baseUrl: string,
): Promise<HealthProbeResult> {
  let health: HttpResult;
  try {
    health = await host.fetchJson(`${trimBase(baseUrl)}/api/v1/health`, {
      timeoutMs: host.healthProbeMs,
    });
  } catch {
    return { kind: 'unreachable' };
  }
  if (health.status !== 200) {
    return { kind: 'bad-status' };
  }
  const probe = ProbeHealthSchema.safeParse(health.body);
  if (!probe.success || probe.data.status !== 'ok') {
    return { kind: 'bad-payload' };
  }
  return { kind: 'healthy', probe: probe.data };
}

/**
 * The shared pipeline's compatibility gate: evaluates the probed
 * declaration and parks the attach on the incompatible surface. Returns the
 * verdict when compatible, null after parking the terminal state.
 */
function evaluateAttachCompatibility(
  host: AttachHost,
  declaration: unknown,
  logPrefix: string,
  detail: string,
): Extract<CompatibilityVerdict, { compatible: true }> | null {
  const verdict = evaluateCompatibility(declaration);
  if (verdict.compatible) {
    return verdict;
  }
  host.log(`${logPrefix}: ${verdict.reason}`);
  host.setState({
    status: 'incompatible',
    stage: 'connect',
    detail,
    ownership: 'external',
    error: {
      code: 'E_INCOMPATIBLE_SERVER',
      message: verdict.reason,
      remediation: INCOMPATIBLE_REMEDIATION,
    },
  });
  return null;
}

type AuthOutcome = 'authenticated' | 'rejected' | 'cancelled';

/**
 * The shared pipeline's authentication step: presents the stored bearer
 * through the host's readiness probe and maps failure to the profile's own
 * terminal surface.
 */
async function authenticateWithReadiness(
  host: AttachHost,
  generation: number,
  baseUrl: string,
  connecting: {
    detail: string;
    ownership: 'external' | 'app-owned';
    serverBuild: BuildIdentity;
    serverName: string | null;
  },
  failure: {
    detail: string;
    error: { code: string; message: string; remediation: string };
  },
  switchContext?: SwitchContext,
): Promise<AuthOutcome> {
  host.setState({
    status: 'connecting',
    stage: 'authenticate',
    detail: connecting.detail,
    ownership: connecting.ownership,
    serverBuild: connecting.serverBuild,
    serverName: connecting.serverName,
  });
  const authenticated = await host.fetchReadiness(baseUrl);
  if (host.cancelled(generation)) {
    return 'cancelled';
  }
  if (authenticated) {
    return 'authenticated';
  }
  host.setToken(null);
  host.setState({
    status: 'error',
    stage: 'authenticate',
    detail: failure.detail,
    ownership: connecting.ownership,
    serverBuild: connecting.serverBuild,
    serverName: connecting.serverName,
    error: failure.error,
    ...(switchContext !== undefined ? { switchContext } : {}),
  });
  return 'rejected';
}

/**
 * The local-attach profile: attaches to a discovery/registry record for a
 * co-located runtime. Picks up the credential from the published record,
 * re-owns a matching app-owned child, refinishes the known-servers entry,
 * and never touches the remote token store.
 */
export async function runLocalAttach(
  host: AttachHost,
  generation: number,
  selected: SelectedRuntime,
  record: DiscoveryRecord,
  stale: StaleHandling = 'launch',
  serverKey?: string,
  switchContext?: SwitchContext,
): Promise<AttachOutcome> {
  const staleResult = (note: string): AttachOutcome => {
    host.log(note);
    if (stale === 'error') {
      host.setState({
        status: 'error',
        stage: 'connect',
        detail: 'The selected server stopped responding.',
        ownership: 'none',
        error: {
          code: 'E_ATTACH_UNREACHABLE',
          message: 'The selected Agentico server is no longer reachable.',
          remediation: 'Use Retry to rescan the running servers.',
        },
        ...(switchContext !== undefined ? { switchContext } : {}),
      });
      return 'blocked';
    }
    return 'launch';
  };

  host.setState({
    status: 'attaching',
    stage: 'connect',
    detail: 'Checking the running runtime.',
    ownership: 'none',
  });

  const probed = await probeAuthExemptHealth(host, record.base_url);
  if (probed.kind === 'unreachable') {
    return staleResult('discovery candidate did not answer its health probe; treating as stale');
  }
  if (host.cancelled(generation)) {
    return 'blocked';
  }
  if (probed.kind === 'bad-status') {
    return staleResult('discovery candidate returned an unhealthy status; treating as stale');
  }
  if (probed.kind === 'bad-payload') {
    return staleResult('discovery candidate health payload was unusable; treating as stale');
  }
  if (probed.probe.runtime !== undefined && probed.probe.runtime.state_dir !== selected.stateDir) {
    return staleResult('running server reports a different runtime identity; not a match');
  }

  const verdict = evaluateAttachCompatibility(
    host,
    probed.probe.compatibility,
    'external runtime is incompatible',
    'A running Agentico runtime is not compatible with this app.',
  );
  if (verdict === null) {
    return 'blocked';
  }

  // A user-assigned nickname wins over the server-reported name for the
  // displayed identity (footer, connection shell); identity itself is never
  // affected. The raw name is only used before the token is known; once the
  // record's token is in scope, the scrubbed form wins.
  const knownEntry =
    serverKey === undefined
      ? undefined
      : host.knownServers().known.find((entry) => entry.serverKey === serverKey);
  const rawServerName = knownEntry?.nickname ?? probed.probe.name ?? null;
  const ownedPid = host.liveOwnedPid();
  const reOwn = ownedPid !== null && record.pid === ownedPid;
  const attachOwnership: 'external' | 'app-owned' = reOwn ? 'app-owned' : 'external';

  const token = record.auth_token;
  if (token === undefined || token === '') {
    host.setState({
      status: 'error',
      stage: 'authenticate',
      detail: 'The running runtime published no credentials to attach with.',
      ownership: 'external',
      serverBuild: verdict.serverBuild,
      serverName: rawServerName,
      error: {
        code: 'E_ATTACH_NO_TOKEN',
        message: 'The discovery record for the running runtime carries no auth token.',
        remediation: 'Restart that runtime from where it was started, then retry.',
      },
    });
    return 'blocked';
  }
  host.setToken(token);
  host.registerSecret(token);
  // Server-controlled display text: scrub the credential itself out before
  // it can ride a state or a persisted entry (hostile server echoing the
  // token back in its probed name). The token is only in scope after the
  // presence check above, so the scrub lives past it.
  const name = host.scrubServerText(rawServerName, token);

  const authenticated = await authenticateWithReadiness(
    host,
    generation,
    record.base_url,
    {
      detail: 'Authenticating with the running runtime.',
      ownership: attachOwnership,
      serverBuild: verdict.serverBuild,
      serverName: name,
    },
    {
      detail: 'Could not authenticate with the running runtime.',
      error: {
        code: 'E_ATTACH_AUTH',
        message: 'The running runtime rejected the stored credentials.',
        remediation: 'Restart that runtime from where it was started, then retry.',
      },
    },
  );
  if (authenticated === 'cancelled') {
    return 'blocked';
  }
  if (authenticated === 'rejected') {
    return 'blocked';
  }
  const attachedKey = serverKey ?? registryEntryKey(selected.runtimeDir);
  host.beginAttachedConnection({
    baseUrl: trimBase(record.base_url),
    serverKey: attachedKey,
    connectedKind: 'local',
    remoteEntry: null,
  });
  host.setState({
    status: 'ready',
    stage: 'ready',
    kind: 'local',
    detail: reOwn
      ? 'Connected to the app-managed Agentico runtime.'
      : 'Connected to an externally managed Agentico runtime.',
    ownership: attachOwnership,
    serverBuild: verdict.serverBuild,
    serverName: name,
  });
  host.markReady();
  // Every successful attach (registry or legacy discovery) refreshes the
  // known-servers entry and the last-used pointer.
  host.recordAttachedServer({
    serverKey: attachedKey,
    kind: 'local',
    name: name ?? '',
    baseUrl: trimBase(record.base_url),
    runtimeDir: selected.runtimeDir,
    lastSeenAt: new Date(host.now()).toISOString(),
  });
  return 'attached';
}

/**
 * The remote-attach profile: attaches to a persisted known-server entry
 * (kind 'remote'). Loads its credential from the encrypted token store —
 * which registers the secret for log redaction itself — and never touches
 * the registry, discovery files, PID liveness, or spawn, and never consumes
 * the crash-restart budget. The local runtime-dir/state-dir consistency
 * check does not apply to a process the app does not co-locate with. On the
 * startup last-used path only (`unreachableToChoice`), a dead remote parks
 * in the picker instead of the error surface when other servers are live.
 */
export async function runRemoteAttach(
  host: AttachHost,
  generation: number,
  entry: KnownServer,
  switchContext?: SwitchContext,
  options?: { unreachableToChoice?: boolean },
): Promise<AttachOutcome> {
  const baseUrl = trimBase(entry.baseUrl);
  host.setState({
    status: 'attaching',
    stage: 'connect',
    detail: 'Connecting to the remote Agentico server.',
    ownership: 'none',
  });

  // The token store registers the loaded secret for log redaction itself.
  const loaded = host.remoteTokens?.load(entry.serverKey) ?? { status: 'absent' as const };
  if (loaded.status !== 'ok') {
    host.setState({
      status: 'error',
      stage: 'authenticate',
      detail: 'The stored credentials for the remote server must be re-entered.',
      ownership: 'none',
      error: {
        code: 'E_REMOTE_TOKEN_REPASTE',
        message:
          loaded.status === 're-paste-required'
            ? 'The stored token for this remote server could not be decrypted.'
            : 'There is no stored token for this remote server.',
        remediation: REMOTE_REPASTE_REMEDIATION,
      },
      ...(switchContext !== undefined ? { switchContext } : {}),
    });
    return 'blocked';
  }

  const probed = await probeAuthExemptHealth(host, baseUrl);
  if (probed.kind !== 'healthy') {
    if (probed.kind !== 'unreachable' && host.cancelled(generation)) {
      return 'blocked';
    }
    if (options?.unreachableToChoice === true) {
      if (await host.parkChoiceForDeadRemote(generation, entry)) {
        return 'blocked';
      }
    }
    host.setState(
      probed.kind === 'unreachable'
        ? {
            status: 'error',
            stage: 'connect',
            detail: 'The remote Agentico server is not reachable.',
            ownership: 'none',
            error: {
              code: 'E_EXTERNAL_SERVER_LOST',
              message: 'The remote Agentico server did not answer its health probe.',
              remediation: 'Check that the remote server is running and reachable, then use Retry.',
            },
            ...(switchContext !== undefined ? { switchContext } : {}),
          }
        : {
            status: 'error',
            stage: 'connect',
            detail: 'The remote Agentico server is not healthy.',
            ownership: 'none',
            error: {
              code: 'E_EXTERNAL_SERVER_LOST',
              message: 'The remote Agentico server answered with an unhealthy status.',
              remediation: 'Check that the remote server is running and reachable, then use Retry.',
            },
            ...(switchContext !== undefined ? { switchContext } : {}),
          },
    );
    return 'blocked';
  }
  if (host.cancelled(generation)) {
    return 'blocked';
  }

  const verdict = evaluateAttachCompatibility(
    host,
    probed.probe.compatibility,
    'remote server is incompatible',
    'The remote Agentico server is not compatible with this app.',
  );
  if (verdict === null) {
    return 'blocked';
  }
  if (host.cancelled(generation)) {
    return 'blocked';
  }

  // `probeName` is the server-reported name (also the base-name refresh
  // source); the displayed name prefers the user's nickname. Server
  // controlled, so the stored credential itself is scrubbed out before the
  // name can ride a state or a persisted entry (a hostile server echoing
  // the presented token back in its health name must not leak it).
  const probeName = host.scrubServerText(probed.probe.name ?? null, loaded.token);
  const serverName = entry.nickname ?? probeName;
  host.setToken(loaded.token);
  const authenticated = await authenticateWithReadiness(
    host,
    generation,
    baseUrl,
    {
      detail: 'Authenticating with the remote Agentico server.',
      ownership: 'external',
      serverBuild: verdict.serverBuild,
      serverName,
    },
    {
      detail: 'The remote Agentico server rejected the stored credentials.',
      error: {
        code: 'E_REMOTE_TOKEN_REPASTE',
        message: 'The remote Agentico server rejected the stored token.',
        remediation: REMOTE_REPASTE_REMEDIATION,
      },
    },
    switchContext,
  );
  if (authenticated === 'cancelled' || authenticated === 'rejected') {
    return 'blocked';
  }

  host.beginAttachedConnection({
    baseUrl,
    serverKey: entry.serverKey,
    connectedKind: 'remote',
    remoteEntry: null,
  });
  host.setState({
    status: 'ready',
    stage: 'ready',
    kind: 'remote',
    detail: 'Connected to the remote Agentico server.',
    ownership: 'external',
    serverBuild: verdict.serverBuild,
    serverName,
  });
  host.markReady();
  // A successful remote attach refreshes lastSeenAt and the last-used
  // pointer. The stored base name auto-refreshes from the health probe
  // only while no user nickname is set — a nickname locks the rule out.
  const refreshed: KnownServer = {
    serverKey: entry.serverKey,
    kind: 'remote',
    name:
      entry.nickname === undefined && probeName !== null && probeName !== ''
        ? probeName
        : entry.name,
    ...(entry.nickname === undefined ? {} : { nickname: entry.nickname }),
    baseUrl,
    lastSeenAt: new Date(host.now()).toISOString(),
  };
  host.beginAttachedConnection({
    baseUrl,
    serverKey: entry.serverKey,
    connectedKind: 'remote',
    remoteEntry: refreshed,
  });
  host.recordAttachedServer(refreshed);
  return 'attached';
}
