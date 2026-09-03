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
 * Footer-switcher server list: a renderer-safe union of the live registry
 * scan and the persisted known-servers list, with liveness probed only while
 * the switcher popover is open.
 *
 * Invariants enforced here:
 *  - Probing is strictly bounded by the renderer's open/close signal: an
 *    immediate round on open, a light interval while open, nothing when
 *    closed.
 *  - Probes are read-only for connection state: a result never mutates the
 *    active connection, its token, or its streams. The ONLY persisted write
 *    a successful probe may cause is the locked name rule: the stored base
 *    name auto-refreshes (only when it changed and the entry has no
 *    nickname — a nickname always wins) and a remote entry's last-seen
 *    timestamp moves forward.
 *  - No token or other credential material ever appears in a row, a log
 *    line, or an IPC payload; base URLs stay in the main process.
 */
import { z } from 'zod';
import {
  MAX_SERVER_CHOICE_CANDIDATES,
  type ServerListRow,
  type ServerListSnapshot,
  type ServersPrefs,
} from '../../shared/ipc';
import type { RegistryScan } from './registry';
import type { HttpResult } from './runtimeGateway';

export interface ServerListServiceDeps {
  /** Fresh registry scan (renderer-safe fields only are ever projected). */
  scanRegistry(): RegistryScan;
  /** Persisted known-servers view (bounded list + last-used pointer). */
  knownServers(): ServersPrefs;
  /** Identity of the currently connected server, when any. */
  currentServerKey(): string | null;
  /** Bounded JSON GET; throws on network failure. Auth-exempt endpoints only. */
  fetchJson(url: string, options: { timeoutMs: number }): Promise<HttpResult>;
  /**
   * Persists probe-derived metadata for a persisted known-server entry
   * (refreshed base name and/or last-seen timestamp). Never tokens.
   */
  recordProbedServer(serverKey: string, patch: { name?: string; lastSeenAt?: string }): void;
  /** Redacted local diagnostics sink (never crosses IPC unfiltered). */
  log(line: string): void;
  /**
   * Scrubs registered secrets out of server-controlled probe text (the
   * health payload's display name) before it can be persisted into settings.
   * A remote server can replay a bearer the app presented earlier into its
   * auth-exempt health name; wired to the log buffer's secret registry so
   * the scrubbed set covers every loaded remote token.
   */
  scrubProbeText?(text: string): string;
  /** Timer seams so tests can drive the interval deterministically. */
  setInterval?(fn: () => void, ms: number): unknown;
  clearInterval?(handle: unknown): void;
  /** Per-server probe bound. */
  probeTimeoutMs?: number;
  /** Interval between probe rounds while the popover is open. */
  pollIntervalMs?: number;
}

const HealthStatusSchema = z.object({
  status: z.string(),
  // Operator-assigned display name (server cap: 64). Informational only —
  // an oversized or malformed name is dropped, never a probe failure.
  name: z.string().max(64).optional().catch(undefined),
});

interface ProbeOutcome {
  healthy: boolean;
  /** Server-reported display name, present only on a healthy probe. */
  name: string | null;
}

export class ServerListService {
  private readonly listeners = new Set<(snapshot: ServerListSnapshot) => void>();
  private readonly probeTimeoutMs: number;
  private readonly pollIntervalMs: number;
  /** Latest liveness verdict per serverKey; absent means never probed. */
  private readonly health = new Map<string, 'healthy' | 'unreachable'>();
  private open = false;
  private interval: unknown = null;
  /** serverKeys with an in-flight probe; a round never doubles one up. */
  private readonly pending = new Set<string>();

  constructor(private readonly deps: ServerListServiceDeps) {
    this.probeTimeoutMs = deps.probeTimeoutMs ?? 1500;
    this.pollIntervalMs = deps.pollIntervalMs ?? 5000;
  }

  subscribe(listener: (snapshot: ServerListSnapshot) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  list(): ServerListSnapshot {
    return this.snapshot();
  }

  /**
   * Bounds probing to the popover's lifetime: opening kicks an immediate
   * probe round and starts the interval; closing stops all polling.
   */
  setOpen(open: boolean): ServerListSnapshot {
    if (open !== this.open) {
      this.open = open;
      if (open) {
        this.probeAll();
        this.startInterval();
      } else {
        this.stopInterval();
      }
    }
    return this.snapshot();
  }

  /** The current-server flag rides connection state; re-emit while open. */
  notifyConnectionChanged(): void {
    if (this.open) {
      this.emit();
    }
  }

  /** Stops polling (window teardown); probing is never leaked. */
  dispose(): void {
    this.open = false;
    this.stopInterval();
  }

  // --- internals -------------------------------------------------------------

  private snapshot(): ServerListSnapshot {
    const currentKey = this.deps.currentServerKey();
    const rows: ServerListRow[] = [];
    const seen = new Set<string>();
    // The persisted nickname wins over both registry-record names and stored
    // base names, on every row kind.
    const nicknameByKey = new Map<string, string>();
    for (const entry of this.deps.knownServers().known) {
      if (entry.nickname !== undefined) {
        nicknameByKey.set(entry.serverKey, entry.nickname);
      }
    }
    const nicknameFor = (serverKey: string): Partial<Pick<ServerListRow, 'nickname'>> => {
      const nickname = nicknameByKey.get(serverKey);
      return nickname === undefined ? {} : { nickname };
    };
    for (const candidate of this.deps.scanRegistry().candidates) {
      seen.add(candidate.serverKey);
      rows.push({
        serverKey: candidate.serverKey,
        kind: 'local',
        name: candidate.record.name ?? null,
        ...nicknameFor(candidate.serverKey),
        runtimeDir: candidate.runtimeDir,
        current: candidate.serverKey === currentKey,
        health: this.health.get(candidate.serverKey) ?? 'probing',
      });
    }
    for (const entry of this.deps.knownServers().known) {
      if (seen.has(entry.serverKey)) {
        continue;
      }
      seen.add(entry.serverKey);
      rows.push({
        serverKey: entry.serverKey,
        kind: entry.kind,
        name: entry.name === '' ? null : entry.name,
        ...nicknameFor(entry.serverKey),
        runtimeDir: entry.runtimeDir,
        current: entry.serverKey === currentKey,
        health: this.health.get(entry.serverKey) ?? 'probing',
      });
    }
    return { rows: rows.slice(0, MAX_SERVER_CHOICE_CANDIDATES) };
  }

  private emit(): void {
    const snapshot = this.snapshot();
    for (const listener of [...this.listeners]) {
      listener(snapshot);
    }
  }

  private startInterval(): void {
    this.stopInterval();
    const setIntervalFn = this.deps.setInterval ?? setInterval;
    this.interval = setIntervalFn(() => {
      this.probeAll();
    }, this.pollIntervalMs);
  }

  private stopInterval(): void {
    if (this.interval !== null) {
      (this.deps.clearInterval ?? clearInterval)(this.interval);
      this.interval = null;
    }
  }

  /**
   * Probes every listed server in parallel against its auth-exempt health
   * endpoint. Each outcome lands independently — one hanging server delays
   * only its own row (bounded by its own timeout), never the others.
   */
  private probeAll(): void {
    for (const target of this.probeTargets()) {
      if (this.pending.has(target.serverKey)) {
        continue;
      }
      this.pending.add(target.serverKey);
      void this.probe(target.baseUrl)
        .then((outcome) => {
          this.pending.delete(target.serverKey);
          // Rows are never hidden for unhealthiness and nothing is deleted
          // from settings: probing only updates the in-memory health view.
          this.health.set(target.serverKey, outcome.healthy ? 'healthy' : 'unreachable');
          if (outcome.healthy) {
            this.persistProbeOutcome(target.serverKey, outcome.name);
          }
          if (this.open) {
            this.emit();
          }
        })
        .catch(() => {
          this.pending.delete(target.serverKey);
        });
    }
  }

  /** serverKey → health-endpoint base URL (registry record wins when live). */
  private probeTargets(): Array<{ serverKey: string; baseUrl: string | null }> {
    const targets: Array<{ serverKey: string; baseUrl: string | null }> = [];
    const seen = new Set<string>();
    for (const candidate of this.deps.scanRegistry().candidates) {
      seen.add(candidate.serverKey);
      targets.push({ serverKey: candidate.serverKey, baseUrl: candidate.record.base_url });
    }
    for (const entry of this.deps.knownServers().known) {
      if (seen.has(entry.serverKey)) {
        continue;
      }
      seen.add(entry.serverKey);
      targets.push({ serverKey: entry.serverKey, baseUrl: entry.baseUrl });
    }
    return targets;
  }

  private async probe(baseUrl: string | null): Promise<ProbeOutcome> {
    if (baseUrl === null) {
      return { healthy: false, name: null };
    }
    try {
      const result = await this.deps.fetchJson(`${baseUrl.replace(/\/+$/, '')}/api/v1/health`, {
        timeoutMs: this.probeTimeoutMs,
      });
      if (result.status !== 200) {
        return { healthy: false, name: null };
      }
      const parsed = HealthStatusSchema.safeParse(result.body);
      const healthy = parsed.success && parsed.data.status === 'ok';
      const rawName = healthy && parsed.success ? (parsed.data.name ?? null) : null;
      const scrub = this.deps.scrubProbeText;
      return {
        healthy,
        name: rawName !== null && scrub !== undefined ? scrub(rawName).slice(0, 64) : rawName,
      };
    } catch {
      return { healthy: false, name: null };
    }
  }

  /**
   * The locked name rule, applied to liveness probes: the stored base name
   * follows the server-reported name only when it actually changed AND the
   * entry carries no nickname — an explicit nickname locks the name write
   * out entirely (the nickname always wins; last-seen still moves). Remote
   * probes also move last-seen forward. No patch means no write: probes
   * must not storm the settings store.
   */
  private persistProbeOutcome(serverKey: string, probeName: string | null): void {
    const entry = this.deps.knownServers().known.find((item) => item.serverKey === serverKey);
    if (entry === undefined) {
      return;
    }
    const name =
      entry.nickname === undefined &&
      probeName !== null &&
      probeName !== '' &&
      probeName !== entry.name
        ? probeName
        : undefined;
    const lastSeenAt = entry.kind === 'remote' ? new Date().toISOString() : undefined;
    if (name === undefined && lastSeenAt === undefined) {
      return;
    }
    this.deps.recordProbedServer(serverKey, {
      ...(name === undefined ? {} : { name }),
      ...(lastSeenAt === undefined ? {} : { lastSeenAt }),
    });
  }
}
