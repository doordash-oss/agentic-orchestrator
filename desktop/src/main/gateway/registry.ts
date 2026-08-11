/**
 * Central-registry scanner for the runtime gateway. Pure logic: every file,
 * directory, and process touch comes in through RegistryDeps so tests can
 * cover all rejection and pruning paths. Mirrors the Go launcher semantics
 * (resolveRegistryParent in cmd/agentico/main.go and the server publisher in
 * internal/server/discovery.go): the server writes a verbatim copy of its
 * discovery record to <registry-parent>/servers/<sha256-key>.json, and the
 * gateway scans that directory to enumerate running servers.
 *
 * Security invariants:
 * - Only owner-only (0o600), owner-owned records are candidates or pruning
 *   targets. Foreign-owned files are rejected but never deleted.
 * - Pruning is best-effort: a failed removal never breaks the scan.
 * - No reason, log line, or diagnostic ever contains file contents or token
 *   material; failures are reported with fixed redacted strings only.
 */
import { createHash } from 'node:crypto';
import path from 'node:path';
import { assertNoPrototypePollution } from '../../shared/sanitize';
import {
  DiscoveryRecordSchema,
  isLoopbackHttpUrl,
  type DiscoveryDeps,
  type DiscoveryRecord,
} from './discovery';

export const AGENTICO_HOME_DIR = '.agentic-orchestrator';
export const AGENTICO_LEGACY_HOME_DIR = '.agentic-workflow';
export const REGISTRY_SUBDIR = 'servers';
export const REGISTRY_ENTRY_SUFFIX = '.json';

export interface RegistryDeps extends DiscoveryDeps {
  /** Lists entry basenames; null means the directory is absent (may also throw). */
  listDir(dirPath: string): string[] | null;
  /** Removes a registry entry file; may throw — pruning never breaks the scan. */
  removeFile(filePath: string): void;
  /** Current user's home directory (injected for testability). */
  homeDir: string;
  /** Directory existence probe used by the parent fallback rule. */
  dirExists(path: string): boolean;
}

export interface RegistryCandidate {
  /** Entry filename without the .json suffix: the server's registry key. */
  serverKey: string;
  /** The canonical runtime dir declared by the record. */
  runtimeDir: string;
  /** Fully validated discovery record; the token stays in the main process. */
  record: DiscoveryRecord;
}

export interface RegistryRejection {
  serverKey: string;
  /** Fixed redacted reason; never content-derived. */
  reason: string;
}

export interface RegistryScan {
  candidates: RegistryCandidate[];
  /** Entries physically pruned (best-effort; failures included). */
  pruned: number;
  /** Entries rejected but intentionally left on disk (never deleted). */
  rejected: RegistryRejection[];
}

/**
 * Implements the Go resolveRegistryParent rule: prefer the fresh install
 * dir, fall back to the legacy install dir when only it exists, otherwise
 * default to the fresh dir (its parent may simply not exist yet).
 */
export function registryPathForHome(
  homeDir: string,
  dirExists: (dirPath: string) => boolean,
): string {
  const primary = path.join(homeDir, AGENTICO_HOME_DIR);
  if (dirExists(primary)) {
    return primary;
  }
  const legacy = path.join(homeDir, AGENTICO_LEGACY_HOME_DIR);
  if (dirExists(legacy)) {
    return legacy;
  }
  return primary;
}

/**
 * Registry entry key for a canonical (symlink-resolved) runtime dir: the
 * first 32 lowercase hex chars of its sha256, matching the Go publisher.
 * Canonicalization is the caller's responsibility (deps must resolve
 * symlinks before computing this key).
 */
export function registryEntryKey(runtimeDirCanonical: string): string {
  return createHash('sha256').update(runtimeDirCanonical).digest('hex').slice(0, 32);
}

/** Collector for one entry; outcome tags prune versus survive on disk. */
type EntryVerdict =
  | { kind: 'candidate'; candidate: RegistryCandidate }
  | { kind: 'prunable'; reason: string }
  | { kind: 'rejected'; reason: string };

function classifyEntry(serverKey: string, filePath: string, deps: RegistryDeps): EntryVerdict {
  let stat: { mode: number; uid: number } | null;
  try {
    stat = deps.statFile(filePath);
  } catch {
    // Racy or failing stat: never delete what we cannot inspect.
    return { kind: 'rejected', reason: 'unreadable' };
  }
  if (stat === null) {
    return { kind: 'rejected', reason: 'unreadable' };
  }
  if ((stat.mode & 0o077) !== 0) {
    return { kind: 'prunable', reason: 'unsafe permissions' };
  }
  if (deps.euid !== null && stat.uid !== deps.euid) {
    // Foreign-owned files are never physically deleted.
    return { kind: 'rejected', reason: 'foreign owner' };
  }

  let raw: string;
  try {
    raw = deps.readFile(filePath);
  } catch {
    return { kind: 'rejected', reason: 'unreadable' };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { kind: 'prunable', reason: 'corrupt' };
  }
  try {
    assertNoPrototypePollution(parsed);
  } catch {
    return { kind: 'prunable', reason: 'unsafe payload' };
  }
  const record = DiscoveryRecordSchema.safeParse(parsed);
  if (!record.success) {
    return { kind: 'prunable', reason: 'schema mismatch' };
  }
  if (!isLoopbackHttpUrl(record.data.base_url)) {
    return { kind: 'prunable', reason: 'non-loopback base URL' };
  }
  if (!deps.isProcessAlive(record.data.pid)) {
    return { kind: 'prunable', reason: 'dead PID' };
  }
  return {
    kind: 'candidate',
    candidate: { serverKey, runtimeDir: record.data.runtime.runtime_dir, record: record.data },
  };
}

/**
 * Scans the central registry directory. Never throws; one bad entry never
 * breaks the others, and pruning is best-effort. Candidates are sorted by
 * serverKey for deterministic downstream behavior.
 */
export function scanRegistry(deps: RegistryDeps): RegistryScan {
  const registryDir = path.join(registryPathForHome(deps.homeDir, deps.dirExists), REGISTRY_SUBDIR);

  let entries: string[] | null;
  try {
    entries = deps.listDir(registryDir);
  } catch {
    entries = null;
  }
  const scan: RegistryScan = { candidates: [], pruned: 0, rejected: [] };
  if (entries === null) {
    return scan;
  }

  for (const entry of entries) {
    if (!entry.endsWith(REGISTRY_ENTRY_SUFFIX)) {
      continue;
    }
    const serverKey = entry.slice(0, -REGISTRY_ENTRY_SUFFIX.length);
    const verdict = classifyEntry(serverKey, path.join(registryDir, entry), deps);
    if (verdict.kind === 'candidate') {
      scan.candidates.push(verdict.candidate);
    } else if (verdict.kind === 'prunable') {
      scan.pruned += 1;
      try {
        deps.removeFile(path.join(registryDir, entry));
      } catch {
        // Best-effort prune: a failed removal does not affect the scan.
      }
    } else {
      scan.rejected.push({ serverKey, reason: verdict.reason });
    }
  }

  scan.candidates.sort((a, b) =>
    a.serverKey < b.serverKey ? -1 : a.serverKey > b.serverKey ? 1 : 0,
  );
  scan.rejected.sort((a, b) =>
    a.serverKey < b.serverKey ? -1 : a.serverKey > b.serverKey ? 1 : 0,
  );
  return scan;
}
