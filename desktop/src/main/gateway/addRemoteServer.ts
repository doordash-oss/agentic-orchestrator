/**
 * The "Add Server" flow: paste a connection string, probe the server,
 * guard against adding a known LOCAL runtime by its remote address, verify
 * the pasted token, and persist the remote known-server entry plus its
 * encrypted token.
 *
 * Invariants enforced here:
 *  - NOTHING is persisted on any failure — no settings entry, no token blob.
 *    Persistence happens exactly once, after probe + auth succeed.
 *  - The pasted string and the token never cross into results, log lines, or
 *    events. Every failure is a SafeErrorException with a distinct,
 *    actionable code; messages never echo the raw input.
 *  - The duplicate guard matches on runtime identity (the health payload's
 *    runtime.state_dir), never on host:port — a remote alias for a known
 *    local server steers the user to the existing entry instead of creating
 *    a shadow entry; a test server outside the registry never collides.
 */
import path from 'node:path';

import { z } from 'zod';

import { SafeErrorException, safeError, stripSecrets } from '../../shared/errors';
import type {
  KnownServer,
  RemoteServerAddRequest,
  RemoteServerAddResult,
  ServersPrefs,
} from '../../shared/ipc';
import { parseConnectionString, serverKeyForBaseUrl } from '../connectionString';
import { evaluateCompatibility } from './compatibility';
import type { RegistryScan } from './registry';
import type { SaveResult as TokenSaveResult } from './remoteTokenStore';
import type { HttpResult } from './runtimeGateway';

export const E_REMOTE_UNREACHABLE = 'E_REMOTE_UNREACHABLE';
export const E_REMOTE_INCOMPATIBLE = 'E_REMOTE_INCOMPATIBLE';
export const E_REMOTE_AUTH_REJECTED = 'E_REMOTE_AUTH_REJECTED';

/**
 * Network-bound probe bound: wider than the local attach probe (1.5s) because
 * a remote server may sit behind real latency, still tight enough that a dead
 * host fails fast.
 */
export const ADD_REMOTE_PROBE_MS = 4000;

/** State-dir basename under a runtime dir (mirrors gateway/wiring.ts). */
const STATE_BASENAME = 'features';

export interface AddRemoteServerDeps {
  /** Bounded JSON request; throws on network failure. GET when no method. */
  fetchJson(url: string, options: { token?: string; timeoutMs: number }): Promise<HttpResult>;
  /** Live registry scan: candidates are KNOWN LOCAL servers. */
  scanRegistry(): RegistryScan;
  /** Reads the persisted known-servers view (local entries drive the guard). */
  knownServers(): ServersPrefs;
  /** Persists one remote known-server entry (never carries a token). */
  upsertRemoteEntry(entry: KnownServer): void;
  /** Encrypted remote-token store; save() registers the secret on receipt. */
  remoteTokens: {
    save(serverKey: string, token: string): TokenSaveResult;
  };
  /** Registers the token with the log-redaction secret registry. */
  registerSecret(secret: string): void;
  /** Redacted diagnostics sink (never receives token material). */
  log(line: string): void;
  /** Injectable clock for deterministic tests. */
  now?(): number;
  timeouts?: { healthProbeMs?: number };
}

/** Lenient probe view identical to runtimeGateway's ProbeHealthSchema. */
const ProbeHealthSchema = z.object({
  status: z.string(),
  compatibility: z.unknown().optional(),
  runtime: z.object({ state_dir: z.string() }).optional(),
  // Operator-assigned display name: informational only, never a blocker.
  name: z.string().max(64).optional().catch(undefined),
});

function fail(code: string, message: string, remediation: string): never {
  throw new SafeErrorException(safeError(code, message, remediation));
}

const UNREACHABLE_REMEDIATION =
  'Check that the server is running and reachable from this machine (address, port, firewall), then retry.';

const INCOMPATIBLE_REMEDIATION =
  'Update the Agentico desktop app and the agentico server to matching releases, then retry.';

const AUTH_REMEDIATION =
  'Re-copy the FULL connection string the server printed — the token is the part before the @ sign — and paste it again.';

/**
 * Finds a known LOCAL server with the same runtime identity as the probed
 * server. Registry candidates carry the canonical state dir directly;
 * persisted local entries derive it from runtimeDir. Pure host:port matching
 * is deliberately absent: the e2e loopback path runs a test server outside
 * the registry that must never collide.
 */
function findLocalDuplicate(
  stateDir: string,
  deps: AddRemoteServerDeps,
): { serverKey: string } | null {
  for (const candidate of deps.scanRegistry().candidates) {
    if (candidate.record.runtime.state_dir === stateDir) {
      return { serverKey: candidate.serverKey };
    }
  }
  for (const entry of deps.knownServers().known) {
    if (entry.kind === 'local' && entry.runtimeDir !== undefined) {
      if (path.join(entry.runtimeDir, STATE_BASENAME) === stateDir) {
        return { serverKey: entry.serverKey };
      }
    }
  }
  return null;
}

/** Host-derived display-name fallback, e.g. "10.1.2.3:8080", bounded to the schema cap. */
function fallbackName(baseUrl: string): string {
  try {
    return new URL(baseUrl).host.slice(0, 64);
  } catch {
    return 'remote server';
  }
}

export async function addRemoteServer(
  request: RemoteServerAddRequest,
  deps: AddRemoteServerDeps,
): Promise<RemoteServerAddResult> {
  const now = deps.now ?? (() => Date.now());
  const probeMs = deps.timeouts?.healthProbeMs ?? ADD_REMOTE_PROBE_MS;

  // Parse errors surface as-is: each carries a distinct actionable code.
  const parsed = parseConnectionString(request.connectionString);
  const baseUrl = parsed.baseUrl;

  // (b) Auth-exempt health probe: compatibility is decided before any
  // credential is presented.
  let health: HttpResult;
  try {
    health = await deps.fetchJson(`${baseUrl}/api/v1/health`, { timeoutMs: probeMs });
  } catch {
    deps.log(`add-remote-server failed: ${E_REMOTE_UNREACHABLE} (health probe)`);
    fail(
      E_REMOTE_UNREACHABLE,
      'Could not reach the server at the address in the connection string.',
      UNREACHABLE_REMEDIATION,
    );
  }
  if (health.status !== 200) {
    deps.log(`add-remote-server failed: ${E_REMOTE_UNREACHABLE} (health status)`);
    fail(
      E_REMOTE_UNREACHABLE,
      'The server did not answer its health probe with a healthy status.',
      UNREACHABLE_REMEDIATION,
    );
  }
  const probe = ProbeHealthSchema.safeParse(health.body);
  if (!probe.success || probe.data.status !== 'ok') {
    deps.log(`add-remote-server failed: ${E_REMOTE_INCOMPATIBLE} (unusable health payload)`);
    fail(
      E_REMOTE_INCOMPATIBLE,
      'The server answered, but its health payload is not an Agentico server contract.',
      INCOMPATIBLE_REMEDIATION,
    );
  }
  const verdict = evaluateCompatibility(probe.data.compatibility);
  if (!verdict.compatible) {
    deps.log(`add-remote-server failed: ${E_REMOTE_INCOMPATIBLE} (${verdict.reason})`);
    fail(
      E_REMOTE_INCOMPATIBLE,
      `The server is not compatible with this app: ${verdict.reason}`,
      INCOMPATIBLE_REMEDIATION,
    );
  }

  // (c) Paste-duplication guard: a remote alias for a known LOCAL server is
  // not an error — steer the user to the existing entry. Nothing persisted.
  const stateDir = probe.data.runtime?.state_dir;
  if (stateDir !== undefined && stateDir !== '') {
    const duplicate = findLocalDuplicate(stateDir, deps);
    if (duplicate !== null) {
      deps.log('add-remote-server: probed server matches a known local server; steering');
      return { status: 'duplicate-local', serverKey: duplicate.serverKey };
    }
  }

  // (d) One authenticated readiness call with the pasted token.
  deps.registerSecret(parsed.token);
  let readiness: HttpResult;
  try {
    readiness = await deps.fetchJson(`${baseUrl}/api/v1/readiness`, {
      token: parsed.token,
      timeoutMs: probeMs,
    });
  } catch {
    deps.log(`add-remote-server failed: ${E_REMOTE_UNREACHABLE} (readiness probe)`);
    fail(
      E_REMOTE_UNREACHABLE,
      'Could not reach the server for the authenticated readiness check.',
      UNREACHABLE_REMEDIATION,
    );
  }
  if (readiness.status === 401 || readiness.status === 403) {
    deps.log(`add-remote-server failed: ${E_REMOTE_AUTH_REJECTED}`);
    fail(
      E_REMOTE_AUTH_REJECTED,
      'The server rejected the token from the connection string.',
      AUTH_REMEDIATION,
    );
  }
  if (readiness.status !== 200) {
    deps.log(`add-remote-server failed: ${E_REMOTE_UNREACHABLE} (readiness status)`);
    fail(
      E_REMOTE_UNREACHABLE,
      'The server did not answer its readiness check with a healthy status.',
      UNREACHABLE_REMEDIATION,
    );
  }

  // (e) Persist. The token blob is written first: when the OS keystore is
  // unavailable the entry would be unrecoverable on next launch, so nothing
  // is persisted at all and the outcome says so. The persisted display name
  // is scrubbed: a ?name= crafted to echo the token, or a hostile server
  // reporting it as its name, must never land in settings.json.
  const serverKey = serverKeyForBaseUrl(baseUrl);
  const name = stripSecrets(
    parsed.name !== undefined ? parsed.name : (probe.data.name ?? fallbackName(baseUrl)),
    [parsed.token],
  ).slice(0, 64);
  const saved = deps.remoteTokens.save(serverKey, parsed.token);
  if (saved.status === 'unavailable') {
    deps.log('add-remote-server: OS keychain unavailable; server kept for this session only');
    return { status: 'session-only', serverKey };
  }
  deps.upsertRemoteEntry({
    serverKey,
    kind: 'remote',
    name,
    baseUrl,
    lastSeenAt: new Date(now()).toISOString(),
  });
  return { status: 'added', serverKey };
}
