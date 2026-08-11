/**
 * Footer-switcher server list: a renderer-safe union of the live registry
 * scan and the persisted known-servers list, with liveness probed only while
 * the switcher popover is open.
 *
 * Invariants enforced here:
 *  - Probing is strictly bounded by the renderer's open/close signal: an
 *    immediate round on open, a light interval while open, nothing when
 *    closed.
 *  - A probe result never mutates the active connection, its token, its
 *    streams, or settings — health is auth-exempt and touches nothing else.
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
  /** Redacted local diagnostics sink (never crosses IPC unfiltered). */
  log(line: string): void;
  /** Timer seams so tests can drive the interval deterministically. */
  setInterval?(fn: () => void, ms: number): unknown;
  clearInterval?(handle: unknown): void;
  /** Per-server probe bound. */
  probeTimeoutMs?: number;
  /** Interval between probe rounds while the popover is open. */
  pollIntervalMs?: number;
}

const HealthStatusSchema = z.object({ status: z.string() });

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
    for (const candidate of this.deps.scanRegistry().candidates) {
      seen.add(candidate.serverKey);
      rows.push({
        serverKey: candidate.serverKey,
        name: candidate.record.name ?? null,
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
        name: entry.name === '' ? null : entry.name,
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
        .then((healthy) => {
          this.pending.delete(target.serverKey);
          // Rows are never hidden for unhealthiness and nothing is deleted
          // from settings: probing only updates the in-memory health view.
          this.health.set(target.serverKey, healthy ? 'healthy' : 'unreachable');
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

  private async probe(baseUrl: string | null): Promise<boolean> {
    if (baseUrl === null) {
      return false;
    }
    try {
      const result = await this.deps.fetchJson(`${baseUrl.replace(/\/+$/, '')}/api/v1/health`, {
        timeoutMs: this.probeTimeoutMs,
      });
      if (result.status !== 200) {
        return false;
      }
      const parsed = HealthStatusSchema.safeParse(result.body);
      return parsed.success && parsed.data.status === 'ok';
    } catch {
      return false;
    }
  }
}
