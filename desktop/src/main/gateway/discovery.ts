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
 * Discovery-file validation for the runtime gateway. Pure logic: all
 * filesystem and process access comes in through DiscoveryDeps so tests can
 * cover every rejection path. Mirrors the Go launcher's semantics
 * (internal/server/discovery.go): stale records mean "proceed to launch",
 * malformed or insecure records are treated as absent but produce a
 * redacted diagnostic, and only owner-only loopback records for the
 * selected runtime become attach candidates.
 */
import path from 'node:path';
import { z } from 'zod';
import { assertNoPrototypePollution } from '../../shared/sanitize';

export const DISCOVERY_FILENAME = '.agentico-server.json';

/**
 * The subset of the Go DiscoveryRecord the gateway consumes. Unknown fields
 * are stripped (newer servers may add fields), missing required fields fail
 * closed.
 */
export const DiscoveryRecordSchema = z.object({
  schema_version: z.number().int().min(1),
  api_version: z.string().min(1),
  base_url: z.string().min(1),
  auth_token: z.string().optional(),
  runtime: z.object({
    runtime_dir: z.string(),
    state_dir: z.string().min(1),
    config_path: z.string(),
  }),
  pid: z.number().int().positive(),
  started_at: z.string().optional(),
  // Optional operator-assigned display name (server cap: MaxServerNameLength
  // = 64). Informational only; the authoritative read is the health probe.
  name: z.string().max(64).optional(),
});

export type DiscoveryRecord = z.output<typeof DiscoveryRecordSchema>;

export interface DiscoveryDeps {
  /** Reads the discovery file; may throw. */
  readFile(filePath: string): string;
  /** Stats the discovery file; null means it does not exist. */
  statFile(filePath: string): { mode: number; uid: number } | null;
  /** Effective uid of this process; null when the platform has no uids. */
  euid: number | null;
  /** Liveness check for the recorded pid (signal 0 semantics). */
  isProcessAlive(pid: number): boolean;
}

export type DiscoveryOutcome =
  /** No discovery file: launch a server. */
  | { kind: 'absent' }
  /** Record points at a dead process: treat as absent and launch. */
  | { kind: 'stale'; reason: string }
  /**
   * Malformed, insecure, non-loopback, or wrong-runtime record: treat as
   * absent (launch) but surface the redacted reason as a diagnostic. The
   * reason never contains file contents or token material.
   */
  | { kind: 'rejected'; reason: string }
  /** A validated record worth probing over HTTP. */
  | { kind: 'candidate'; record: DiscoveryRecord };

export function discoveryPath(runtimeDir: string): string {
  return path.join(runtimeDir, DISCOVERY_FILENAME);
}

/** Accepts only plain http URLs with a non-empty host. */
export function isPlainHttpUrl(raw: string): boolean {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  return url.protocol === 'http:' && url.hostname !== '';
}

/** Accepts only plain http URLs whose host is loopback. */
export function isLoopbackHttpUrl(raw: string): boolean {
  if (!isPlainHttpUrl(raw)) {
    return false;
  }
  // Safe: isPlainHttpUrl already proved the URL parses.
  return isLoopbackHostname(new URL(raw).hostname);
}

function isLoopbackHostname(hostname: string): boolean {
  const host = hostname.replace(/^\[/, '').replace(/\]$/, '').toLowerCase();
  if (host === 'localhost') {
    return true;
  }
  if (host === '::1' || host === '0:0:0:0:0:0:0:1') {
    return true;
  }
  const mapped = /^::ffff:(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$/.exec(host);
  const ipv4 = mapped?.[1] ?? host;
  const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ipv4);
  if (match === null) {
    return false;
  }
  const octets = match.slice(1).map(Number);
  if (octets.some((octet) => octet > 255)) {
    return false;
  }
  return octets[0] === 127;
}

/**
 * Validates the discovery file for the selected runtime. Never throws; the
 * caller maps outcomes onto the connection lifecycle.
 */
export function evaluateDiscoveryFile(
  runtimeDir: string,
  selectedStateDir: string,
  deps: DiscoveryDeps,
): DiscoveryOutcome {
  const filePath = discoveryPath(runtimeDir);

  let stat: { mode: number; uid: number } | null;
  try {
    stat = deps.statFile(filePath);
  } catch {
    return { kind: 'rejected', reason: 'discovery file could not be inspected' };
  }
  if (stat === null) {
    return { kind: 'absent' };
  }
  if ((stat.mode & 0o077) !== 0) {
    return {
      kind: 'rejected',
      reason: 'discovery file has unsafe permissions (readable beyond owner)',
    };
  }
  if (deps.euid !== null && stat.uid !== deps.euid) {
    return { kind: 'rejected', reason: 'discovery file has a foreign owner' };
  }

  let raw: string;
  try {
    raw = deps.readFile(filePath);
  } catch {
    return { kind: 'rejected', reason: 'discovery file was unreadable' };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { kind: 'rejected', reason: 'discovery file was not valid JSON' };
  }
  try {
    assertNoPrototypePollution(parsed);
  } catch {
    return { kind: 'rejected', reason: 'discovery file carried an unsafe payload' };
  }
  const record = DiscoveryRecordSchema.safeParse(parsed);
  if (!record.success) {
    return { kind: 'rejected', reason: 'discovery file did not match the discovery record schema' };
  }

  if (!isLoopbackHttpUrl(record.data.base_url)) {
    return { kind: 'rejected', reason: 'discovery base URL is not plain loopback http' };
  }
  if (record.data.runtime.state_dir !== selectedStateDir) {
    return { kind: 'rejected', reason: 'discovery record belongs to a different runtime' };
  }
  if (!deps.isProcessAlive(record.data.pid)) {
    return { kind: 'stale', reason: 'discovery record points at a dead process' };
  }
  return { kind: 'candidate', record: record.data };
}
