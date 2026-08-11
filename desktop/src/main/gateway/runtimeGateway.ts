/**
 * The main-process runtime gateway: resolves the selected runtime, validates
 * owner-only discovery material, and either attaches to a compatible
 * externally owned server or launches the bundled one.
 *
 * Invariants enforced here:
 *  - The bearer token lives only in this class's private field. It is never
 *    placed on a ConnectionState, IPC payload, URL, or log line.
 *  - Compatibility requires the server's explicit declaration (API/schema
 *    contract + runtime policy); a shared API major is never sufficient.
 *  - Externally owned servers are never signalled or terminated — the only
 *    process this gateway ever stops is the child it spawned itself.
 *  - Every failure lands in a renderer-visible connection-shell state with
 *    redacted diagnostics and a manual retry path.
 */
import { SafeErrorException, safeError, toSafeError, redactText } from '../../shared/errors';
import {
  isConnectionErrorState,
  MAX_RUN_CONTENT_BYTES,
  SESSION_ID_SEGMENT_PATTERN,
  type ConnectionDiagnostics,
  type ConnectionState,
  type ChooseServerRequest,
  type KnownServer,
  type ServerChoiceCandidate,
  type ServersPrefs,
  type SwitchContext,
  type SwitchServerRequest,
} from '../../shared/ipc';
import { z } from 'zod';
import { evaluateCompatibility } from './compatibility';
import { evaluateDiscoveryFile, type DiscoveryDeps, type DiscoveryRecord } from './discovery';
import type { SseStream } from './events';
import { registryEntryKey, type RegistryCandidate, type RegistryScan } from './registry';
import type { ResolveResult } from './resources';
import { DEFAULT_STOP_TIMEOUT_MS, type ChildExit } from './serverProcess';

export interface SelectedRuntime {
  runtimeDir: string;
  stateDir: string;
  configPath: string;
}

export interface HttpResult {
  status: number;
  body: unknown;
}

/** Mutating verbs allowed against the connected runtime's REST API. */
export type ApiMethod = 'GET' | 'POST' | 'PATCH' | 'PUT';

export interface ApiRequestInit {
  method?: ApiMethod;
  body?: unknown;
  /** Trusted main-process override for server operations that legitimately run longer. */
  timeoutMs?: number;
}

/** The supervision surface the gateway needs from a spawned server child. */
export interface ServerChildLike {
  pid: number | undefined;
  exited: boolean;
  onExit(listener: (info: ChildExit) => void): () => void;
  stop(options?: { timeoutMs?: number }): Promise<void>;
}

export interface GatewayTimeouts {
  /** Per-request bound for health/readiness probes. */
  healthProbeMs: number;
  /** Total bound for a launched server to publish discovery and turn healthy. */
  launchReadyMs: number;
  /** Poll interval while waiting for a launched server. */
  pollIntervalMs: number;
  /** Grace period for stopping the app-owned child before SIGKILL. */
  shutdownGraceMs: number;
  /**
   * Per-request bound for authenticated API calls once connected. Larger
   * than the probe bound because readiness refresh re-probes provider CLIs.
   */
  apiRequestMs: number;
  /** Initial delay before automatically relaunching an app-owned crash. */
  crashRestartInitialMs: number;
  /** Rolling crash-budget and healthy-reset window. */
  crashWindowMs: number;
}

const DEFAULT_TIMEOUTS: GatewayTimeouts = {
  healthProbeMs: 1500,
  // Server bootstrap performs bounded provider readiness checks before its
  // concurrent model-catalog discovery, whose own ceiling is 45 seconds.
  // Leave enough room for those legitimate stages plus filesystem setup while
  // retaining a finite bound that reaps a genuinely hung bundled child.
  launchReadyMs: 90000,
  pollIntervalMs: 250,
  shutdownGraceMs: DEFAULT_STOP_TIMEOUT_MS,
  apiRequestMs: 30000,
  crashRestartInitialMs: 250,
  crashWindowMs: 60000,
};

export interface GatewayDeps {
  /** Resolves the user's selected runtime to concrete directories. */
  selectRuntime(): SelectedRuntime;
  discovery: DiscoveryDeps;
  /** Bounded JSON request; throws on network failure. GET when no method. */
  fetchJson(
    url: string,
    options: { token?: string; timeoutMs: number; method?: ApiMethod; body?: unknown },
  ): Promise<HttpResult>;
  /**
   * Opens a long-lived SSE response. The bearer travels only as an
   * Authorization header supplied here — never as a URL parameter.
   */
  openSse?(url: string, options: { token: string }): Promise<SseStream>;
  resolveServerBinary(): ResolveResult;
  /** Spawns the bundled server (argv array, never a shell string). */
  spawnServer(binaryPath: string, args: readonly string[]): ServerChildLike;
  /** Registers a secret so the child log buffer scrubs it. */
  registerSecret(secret: string): void;
  sleep(ms: number): Promise<void>;
  /** Local redacted diagnostics sink (never crosses IPC unfiltered). */
  log(line: string): void;
  /** Redacted child-output snapshot. A second IPC-safe scrub is applied before display. */
  readDiagnosticLines?(): readonly string[];
  /**
   * Scans the central server registry before legacy discovery. Registry
   * candidates win over the per-runtime discovery fallback; an empty scan
   * keeps today's behavior byte-identical.
   */
  scanRegistry(): RegistryScan;
  /** Reads the persisted known-servers view (bounded list + last-used pointer). */
  knownServers(): ServersPrefs;
  /**
   * Persists a successful attach (registry or legacy): upserts the
   * known-server entry and moves the last-used pointer.
   */
  recordAttachedServer(entry: KnownServer): void;
  timeouts?: Partial<GatewayTimeouts>;
  /** Injectable monotonic-enough wall clock for deterministic supervision tests. */
  now?(): number;
}

/**
 * Lenient view of /api/v1/health used while probing possibly-foreign
 * servers: only what the attach decision needs, tolerant of unknown fields
 * and future shapes. The compatibility declaration itself is validated
 * separately (fail-closed) by evaluateCompatibility.
 */
const ProbeHealthSchema = z.object({
  status: z.string(),
  compatibility: z.unknown().optional(),
  runtime: z.object({ state_dir: z.string() }).optional(),
  // Operator-assigned display name (server cap: MaxServerNameLength = 64).
  // Informational only — an oversized or malformed name is dropped, never
  // treated as an attach blocker.
  name: z.string().max(64).optional().catch(undefined),
});

const INCOMPATIBLE_REMEDIATION =
  'Update the Agentico desktop app and the agentico runtime to matching releases, then retry. ' +
  'This app never shuts down a runtime it does not own — close that runtime from wherever it ' +
  'was started if you want this app to manage its own.';

type AttachResult = 'attached' | 'blocked' | 'launch';

/**
 * What tryAttach does when the candidate server turns out unreachable or
 * stale before compatibility is decided: `launch` falls through to the
 * legacy/spawn paths (startup scan race), `error` lands in the visible
 * error/retry state (a server the user explicitly picked died mid-pick).
 */
type StaleHandling = 'launch' | 'error';

export class RuntimeGateway {
  private state: ConnectionState = {
    status: 'idle',
    stage: 'resolve-runtime',
    detail: 'Waiting to connect.',
    ownership: 'none',
  };

  private readonly listeners = new Set<(state: ConnectionState) => void>();
  private readonly timeouts: GatewayTimeouts;

  /** Bearer credential; never leaves the main process. */
  private token: string | null = null;
  /** Base URL of the connected runtime; only set while status is ready. */
  private baseUrl: string | null = null;
  private child: ServerChildLike | null = null;
  private childExitUnsubscribe: (() => void) | null = null;
  private busy = false;
  private shuttingDown = false;
  private generation = 0;
  private crashAttempts: number[] = [];
  private readySince: number | null = null;
  private recoveryPending = false;
  private launchCommandContext: string | null = null;
  /**
   * The runtime directory the gateway actually resolved and connected with.
   * Exposed via ConnectionState so the renderer can authoritatively derive
   * restart-pending state by comparing it against settings.runtime.selection.
   */
  private connectedRuntimeDir: string | null = null;
  /**
   * The connected server's identity (the known-servers key). Populated on
   * every successful attach — registry, legacy discovery (derived from the
   * canonical runtime dir), or spawn — and exposed on ConnectionState so the
   * renderer can key per-server UI state. Never credential material.
   */
  private serverKey: string | null = null;
  /**
   * The launch coordinates of the running app-owned child. Supervision is
   * decoupled from connection state: after a switch away, the child keeps
   * running under this identity and its backoff restart budget keeps working
   * silently instead of driving ConnectionState. Cleared whenever the child
   * is deliberately stopped.
   */
  private ownedSelected: SelectedRuntime | null = null;
  /**
   * The registry snapshot behind an awaiting-server-choice state. Records
   * stay in the main process (tokens included); only the renderer-safe
   * candidate projection crosses IPC. Cleared at the start of every connect
   * cycle and once a choice is consumed.
   */
  private pendingCandidates: RegistryCandidate[] = [];

  constructor(private readonly deps: GatewayDeps) {
    this.timeouts = { ...DEFAULT_TIMEOUTS, ...deps.timeouts };
  }

  getState(): ConnectionState {
    return this.state;
  }

  subscribe(listener: (state: ConnectionState) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  hasOwnedChild(): boolean {
    return this.child !== null && !this.child.exited;
  }

  /**
   * Authenticated request against the connected runtime's REST API. The
   * bearer token and base URL never leave this method's closure — callers
   * pass an `/api/v1/...` path and receive status + parsed JSON body only.
   */
  async apiRequest(path: string, init: ApiRequestInit = {}): Promise<HttpResult> {
    if (!isAllowedApiPath(path)) {
      throw new SafeErrorException(
        safeError('E_BAD_API_PATH', 'The requested API path is not allowed.'),
      );
    }
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new SafeErrorException(
        safeError(
          'E_NOT_CONNECTED',
          'The app is not connected to an Agentico runtime.',
          'Wait for the connection to become ready, then retry.',
        ),
      );
    }
    const method = init.method ?? 'GET';
    return this.deps.fetchJson(`${this.baseUrl}${path}`, {
      token: this.token,
      timeoutMs: init.timeoutMs ?? this.timeouts.apiRequestMs,
      ...(method === 'GET' ? {} : { method, body: init.body ?? {} }),
    });
  }

  /**
   * Opens the authenticated global event stream (`GET /api/v1/events`) with
   * optional cursor resume. The bearer token and base URL never leave this
   * method — callers receive status plus a line iterator only.
   */
  async openEventStream(options: { afterSeq?: number; epoch?: string } = {}): Promise<SseStream> {
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new SafeErrorException(
        safeError(
          'E_NOT_CONNECTED',
          'The app is not connected to an Agentico runtime.',
          'Wait for the connection to become ready, then retry.',
        ),
      );
    }
    const openSse = this.deps.openSse;
    if (openSse === undefined) {
      throw new SafeErrorException(
        safeError('E_SSE_UNAVAILABLE', 'This build has no event-stream transport wired.'),
      );
    }
    const query = new URLSearchParams();
    if (options.afterSeq !== undefined && options.afterSeq > 0) {
      query.set('after', String(Math.floor(options.afterSeq)));
      if (options.epoch !== undefined && options.epoch !== '') {
        query.set('epoch', options.epoch);
      }
    }
    const suffix = query.size > 0 ? `?${query.toString()}` : '';
    return openSse(`${this.baseUrl}/api/v1/events${suffix}`, { token: this.token });
  }

  /** Opens one authenticated session stream using only its transcript-row cursor. */
  async openSessionOutputStream(
    sessionId: string,
    options: { from?: number } = {},
  ): Promise<SseStream> {
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new SafeErrorException(
        safeError(
          'E_NOT_CONNECTED',
          'The app is not connected to an Agentico runtime.',
          'Wait for the connection to become ready, then retry.',
        ),
      );
    }
    if (!SESSION_ID_PATTERN.test(sessionId)) {
      throw new SafeErrorException(safeError('E_BAD_SESSION_ID', 'The session ID is not allowed.'));
    }
    const openSse = this.deps.openSse;
    if (openSse === undefined) {
      throw new SafeErrorException(
        safeError('E_SSE_UNAVAILABLE', 'This build has no event-stream transport wired.'),
      );
    }
    const query = new URLSearchParams();
    if (options.from !== undefined) {
      if (!Number.isSafeInteger(options.from) || options.from < 0) {
        throw new SafeErrorException(
          safeError('E_BAD_TRANSCRIPT_CURSOR', 'The transcript cursor is not allowed.'),
        );
      }
      query.set('from', String(options.from));
    }
    const suffix = query.size === 0 ? '' : `?${query.toString()}`;
    return openSse(`${this.baseUrl}/api/v1/sessions/${sessionId}/output/stream${suffix}`, {
      token: this.token,
    });
  }

  /** Runs one full connect cycle. Safe to call repeatedly. */
  async start(): Promise<boolean> {
    if (this.busy || this.shuttingDown || this.state.status === 'ready') {
      return false;
    }
    this.busy = true;
    const generation = ++this.generation;
    try {
      await this.connect(generation);
    } catch (err) {
      const safe = toSafeError(err, 'E_GATEWAY');
      this.deps.log(`gateway cycle failed: ${safe.code}: ${safe.message}`);
      this.setState({
        status: 'error',
        stage: this.state.stage,
        detail: 'The connection attempt failed unexpectedly.',
        ownership: 'none',
        error: safe,
      });
    } finally {
      this.busy = false;
    }
    return true;
  }

  /** Manual retry from any terminal state; a healthy connection is untouched. */
  async retry(): Promise<ConnectionState> {
    if (this.state.status === 'crashed' && !this.recoveryPending) {
      this.crashAttempts = [];
    }
    await this.start();
    return this.state;
  }

  /**
   * Graceful restart from a healthy connection: stops the app-owned child
   * (external servers are never signalled), resets to idle so start()
   * proceeds with the fresh connect cycle, and re-resolves the selected
   * runtime. Used by the restart-pending flow when the user chooses
   * Restart Now after a runtime path change.
   */
  async restart(): Promise<ConnectionState> {
    if (this.shuttingDown) {
      return this.state;
    }
    this.generation += 1;
    await this.stopChild();
    this.token = null;
    this.baseUrl = null;
    this.readySince = null;
    this.serverKey = null;
    this.setState({
      status: 'idle',
      stage: 'resolve-runtime',
      detail: 'Restarting to apply the pending runtime change.',
      ownership: 'none',
    });
    await this.start();
    return this.state;
  }

  /**
   * Picks one candidate from the awaiting-server-choice snapshot and attaches
   * through the existing record-consuming tryAttach path. An unknown key (a
   * stale snapshot) rescans from scratch like a retry; a server that dies
   * mid-pick lands in the visible error state, whose retry rescans.
   */
  async chooseServer(request: ChooseServerRequest): Promise<ConnectionState> {
    if (this.shuttingDown || this.busy || this.state.status !== 'awaiting-server-choice') {
      return this.state;
    }
    const candidate = this.pendingCandidates.find((entry) => entry.serverKey === request.serverKey);
    this.pendingCandidates = [];
    if (candidate === undefined) {
      this.deps.log('server choice named an unknown candidate; rescanning');
      await this.retry();
      return this.state;
    }
    this.busy = true;
    const generation = ++this.generation;
    try {
      await this.attachCandidate(generation, candidate, 'error');
    } catch (err) {
      const safe = toSafeError(err, 'E_GATEWAY');
      this.deps.log(`gateway cycle failed: ${safe.code}: ${safe.message}`);
      this.setState({
        status: 'error',
        stage: this.state.stage,
        detail: 'The connection attempt failed unexpectedly.',
        ownership: 'none',
        error: safe,
      });
    } finally {
      this.busy = false;
    }
    return this.state;
  }

  /**
   * Switches the workspace to another running server without an app restart.
   * Unlike chooseServer this is not gated on awaiting-server-choice: it is
   * invokable from ready (and from a failed switch's error state for its
   * retry/back-to-previous actions). It bumps the generation — fencing
   * in-flight attach/connect work so nothing re-emits state after the
   * switch — and re-aims at the target through the existing record-consuming
   * attachCandidate path (a fresh registry scan, so a restarted target's new
   * port/token are picked up).
   *
   * Critical deviation from restart(): the app-owned child is NOT stopped —
   * supervision is decoupled from connection state, so a left-behind child
   * keeps its backoff restart budget silently and is still unconditionally
   * stopped by shutdown() on quit.
   *
   * A failed switch lands on the error surface carrying the attempted target
   * (Retry) and the previous server's identity (Back to <name>) — never an
   * automatic rollback. settings.runtime.selection stays untouched.
   */
  async switchServer(request: SwitchServerRequest): Promise<ConnectionState> {
    if (this.shuttingDown || this.busy) {
      return this.state;
    }
    if (this.state.status === 'ready' && this.serverKey === request.serverKey) {
      // Already connected to the target: the switch is a no-op.
      return this.state;
    }
    const previous = this.switchPrevious(request.serverKey);
    const known = this.deps
      .knownServers()
      .known.find((entry) => entry.serverKey === request.serverKey);
    this.busy = true;
    const generation = ++this.generation;
    try {
      // Deliberately no stopChild(): the app-owned child survives the switch.
      this.token = null;
      this.baseUrl = null;
      this.readySince = null;
      this.crashAttempts = [];
      this.serverKey = null;
      const scan = this.deps.scanRegistry();
      const candidate = scan.candidates.find((entry) => entry.serverKey === request.serverKey);
      const attempted: ServerChoiceCandidate = {
        serverKey: request.serverKey,
        name: candidate?.record.name ?? known?.name ?? null,
        runtimeDir: candidate?.runtimeDir ?? known?.runtimeDir ?? '',
      };
      const switchContext: SwitchContext = { attempted, previous };
      if (candidate === undefined) {
        this.deps.log('switch target is not in the live registry scan');
        this.setState({
          status: 'error',
          stage: 'connect',
          detail: 'The selected server is not running.',
          ownership: 'none',
          error: {
            code: 'E_SWITCH_UNAVAILABLE',
            message: 'The selected Agentico server is no longer running.',
            remediation:
              previous !== null
                ? 'Use Retry to try again, or go back to the previous server.'
                : 'Use Retry to try again.',
          },
          switchContext,
        });
        return this.state;
      }
      await this.attachCandidate(generation, candidate, 'error', switchContext);
    } catch (err) {
      const safe = toSafeError(err, 'E_GATEWAY');
      this.deps.log(`gateway cycle failed: ${safe.code}: ${safe.message}`);
      const attempted: ServerChoiceCandidate = {
        serverKey: request.serverKey,
        name: known?.name ?? null,
        runtimeDir: known?.runtimeDir ?? '',
      };
      this.setState({
        status: 'error',
        stage: 'connect',
        detail: 'The connection attempt failed unexpectedly.',
        ownership: 'none',
        error: safe,
        switchContext: { attempted, previous },
      });
    } finally {
      this.busy = false;
    }
    return this.state;
  }

  /**
   * The "Back to <name>" identity for a failed switch: the currently
   * connected server when there is one, else the previous entry from the
   * failed switch's own context (never the target itself).
   */
  private switchPrevious(targetKey: string): ServerChoiceCandidate | null {
    if (
      this.serverKey !== null &&
      this.serverKey !== targetKey &&
      this.connectedRuntimeDir !== null
    ) {
      return {
        serverKey: this.serverKey,
        name: this.state.serverName ?? null,
        runtimeDir: this.connectedRuntimeDir,
      };
    }
    if (this.state.status === 'error') {
      const context = this.state.switchContext;
      if (
        context !== undefined &&
        context.previous !== null &&
        context.previous.serverKey !== targetKey
      ) {
        return context.previous;
      }
    }
    return null;
  }

  /**
   * Verifies a stale global stream before dropping an externally owned
   * connection. This never signals, restarts, or adopts the external process.
   */
  async handleGlobalStreamStale(): Promise<void> {
    if (
      this.state.status !== 'ready' ||
      this.state.ownership !== 'external' ||
      this.baseUrl === null
    ) {
      return;
    }
    const baseUrl = this.baseUrl;
    let healthy = false;
    try {
      const result = await this.deps.fetchJson(`${baseUrl}/api/v1/health`, {
        timeoutMs: this.timeouts.healthProbeMs,
      });
      const probe = ProbeHealthSchema.safeParse(result.body);
      healthy = result.status === 200 && probe.success && probe.data.status === 'ok';
    } catch {
      healthy = false;
    }
    if (
      healthy ||
      this.state.status !== 'ready' ||
      this.state.ownership !== 'external' ||
      this.baseUrl !== baseUrl
    ) {
      return;
    }
    this.generation += 1;
    this.token = null;
    this.baseUrl = null;
    this.readySince = null;
    this.serverKey = null;
    this.setState({
      status: 'error',
      stage: 'connect',
      detail: 'The externally managed runtime is no longer reachable.',
      ownership: 'external',
      error: {
        code: 'E_EXTERNAL_SERVER_LOST',
        message: 'The externally managed Agentico runtime stopped responding.',
        remediation: 'Restart it from where it was started, then use Retry.',
      },
    });
  }

  /**
   * App shutdown: gracefully stop the app-owned child (bounded, then
   * SIGKILL via the child's stop()); externally owned servers are left
   * running and are never signalled.
   */
  async shutdown(): Promise<void> {
    this.shuttingDown = true;
    this.recoveryPending = false;
    this.generation += 1; // invalidate in-flight connect work
    await this.stopChild();
    this.token = null;
    this.baseUrl = null;
    this.connectedRuntimeDir = null;
    this.serverKey = null;
    this.pendingCandidates = [];
  }

  // --- connect cycle ---------------------------------------------------------

  private async connect(generation: number): Promise<void> {
    await this.stopChild(); // never leave a stray child from an earlier cycle
    this.token = null;
    this.baseUrl = null;
    this.serverKey = null;
    this.launchCommandContext = null;
    this.pendingCandidates = [];

    this.setState({
      status: 'resolving-runtime',
      stage: 'resolve-runtime',
      detail: 'Resolving the selected runtime.',
      ownership: 'none',
    });
    const selected = this.deps.selectRuntime();
    this.connectedRuntimeDir = selected.runtimeDir;

    this.setState({
      status: 'discovering',
      stage: 'discover',
      detail: 'Looking for a running Agentico runtime.',
      ownership: 'none',
    });

    // Registry-first startup selection: live registry candidates win over
    // the legacy per-runtime discovery fallback. Zero live candidates keeps
    // today's behavior byte-identical (old binaries only publish the
    // per-runtime file). The scan prunes dead/insecure/corrupt entries.
    const scan = this.deps.scanRegistry();
    if (scan.pruned > 0) {
      this.deps.log(
        `registry scan pruned ${scan.pruned} dead or invalid entr${scan.pruned === 1 ? 'y' : 'ies'}`,
      );
    }
    if (scan.rejected.length > 0) {
      this.deps.log(
        `registry scan skipped ${scan.rejected.length} entr${scan.rejected.length === 1 ? 'y' : 'ies'} (left on disk)`,
      );
    }
    if (scan.candidates.length > 0) {
      if (this.cancelled(generation)) {
        return;
      }
      const decided = this.decideRegistryAttach(scan);
      if (decided === null) {
        // Multiple live servers without a last-used match: park in the
        // user-decision state; chooseServer() or retry() continues.
        this.pendingCandidates = scan.candidates;
        this.setState({
          status: 'awaiting-server-choice',
          stage: 'connect',
          detail: 'Choose which Agentico server to connect to.',
          ownership: 'none',
          candidates: scan.candidates.map((candidate) => ({
            serverKey: candidate.serverKey,
            name: candidate.record.name ?? null,
            runtimeDir: candidate.runtimeDir,
          })),
        });
        return;
      }
      const attached = await this.attachCandidate(generation, decided, 'launch');
      if (attached) {
        return;
      }
      // The chosen candidate died between scan and probe; fall through to
      // the legacy discovery/spawn path exactly as a stale discovery would.
      if (this.cancelled(generation)) {
        return;
      }
    }

    const outcome = evaluateDiscoveryFile(
      selected.runtimeDir,
      selected.stateDir,
      this.deps.discovery,
    );
    if (outcome.kind === 'rejected' || outcome.kind === 'stale') {
      this.deps.log(`discovery ignored: ${outcome.reason}`);
    }
    if (outcome.kind === 'candidate') {
      const attach = await this.tryAttach(generation, selected, outcome.record);
      if (attach !== 'launch') {
        return;
      }
    }
    if (this.cancelled(generation)) {
      return;
    }
    await this.launch(generation, selected);
  }

  /**
   * Picks the candidate for a silent attach from a non-empty registry scan:
   * a live candidate matching the last-used pointer when there is one,
   * otherwise the single candidate when exactly one is live. Returns null
   * when the user must pick (multiple live, no usable last-used).
   */
  private decideRegistryAttach(scan: RegistryScan): RegistryCandidate | null {
    const lastUsed = this.deps.knownServers().lastUsed;
    if (lastUsed !== null) {
      const remembered = scan.candidates.find((candidate) => candidate.serverKey === lastUsed);
      if (remembered !== undefined) {
        return remembered;
      }
    }
    if (scan.candidates.length === 1) {
      return scan.candidates[0] ?? null;
    }
    return null;
  }

  /**
   * Attaches to one registry candidate through the existing record-consuming
   * tryAttach path. Returns whether the cycle reached a terminal attach
   * decision (attached or its own terminal state); false means the candidate
   * was stale at probe time and the caller decides the fallback.
   */
  private async attachCandidate(
    generation: number,
    candidate: RegistryCandidate,
    stale: StaleHandling,
    switchContext?: SwitchContext,
  ): Promise<boolean> {
    const record = candidate.record;
    const selected: SelectedRuntime = {
      runtimeDir: record.runtime.runtime_dir,
      stateDir: record.runtime.state_dir,
      configPath: record.runtime.config_path,
    };
    // Attaching elsewhere re-aims connectedRuntimeDir but never rewrites
    // settings.runtime.selection — the last-used pointer is the reconnection
    // authority.
    this.connectedRuntimeDir = selected.runtimeDir;
    const result = await this.tryAttach(
      generation,
      selected,
      record,
      stale,
      candidate.serverKey,
      switchContext,
    );
    if (result === 'launch') {
      return false;
    }
    return true;
  }

  private async tryAttach(
    generation: number,
    selected: SelectedRuntime,
    record: DiscoveryRecord,
    stale: StaleHandling = 'launch',
    serverKey?: string,
    switchContext?: SwitchContext,
  ): Promise<AttachResult> {
    const staleResult = (note: string): AttachResult => {
      this.deps.log(note);
      if (stale === 'error') {
        this.setState({
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

    this.setState({
      status: 'attaching',
      stage: 'connect',
      detail: 'Checking the running runtime.',
      ownership: 'none',
    });

    // Health is auth-exempt by design: compatibility is evaluated before any
    // credential is presented.
    let health: HttpResult;
    try {
      health = await this.deps.fetchJson(`${trimBase(record.base_url)}/api/v1/health`, {
        timeoutMs: this.timeouts.healthProbeMs,
      });
    } catch {
      return staleResult('discovery candidate did not answer its health probe; treating as stale');
    }
    if (this.cancelled(generation)) {
      return 'blocked';
    }
    if (health.status !== 200) {
      return staleResult('discovery candidate returned an unhealthy status; treating as stale');
    }
    const probe = ProbeHealthSchema.safeParse(health.body);
    if (!probe.success || probe.data.status !== 'ok') {
      return staleResult('discovery candidate health payload was unusable; treating as stale');
    }
    if (probe.data.runtime !== undefined && probe.data.runtime.state_dir !== selected.stateDir) {
      return staleResult('running server reports a different runtime identity; not a match');
    }

    const verdict = evaluateCompatibility(probe.data.compatibility);
    if (!verdict.compatible) {
      this.deps.log(`external runtime is incompatible: ${verdict.reason}`);
      this.setState({
        status: 'incompatible',
        stage: 'connect',
        detail: 'A running Agentico runtime is not compatible with this app.',
        ownership: 'external',
        error: {
          code: 'E_INCOMPATIBLE_SERVER',
          message: verdict.reason,
          remediation: INCOMPATIBLE_REMEDIATION,
        },
      });
      return 'blocked';
    }

    const serverName = probe.data.name ?? null;
    // Re-owning: switching back to the server this app spawned (still our
    // supervised child, same pid) makes the attach app-owned again so its
    // supervision drives ConnectionState as before the switch.
    const reOwn = this.child !== null && !this.child.exited && record.pid === this.child.pid;
    const attachOwnership: 'external' | 'app-owned' = reOwn ? 'app-owned' : 'external';

    const token = record.auth_token;
    if (token === undefined || token === '') {
      this.setState({
        status: 'error',
        stage: 'authenticate',
        detail: 'The running runtime published no credentials to attach with.',
        ownership: 'external',
        serverBuild: verdict.serverBuild,
        serverName,
        error: {
          code: 'E_ATTACH_NO_TOKEN',
          message: 'The discovery record for the running runtime carries no auth token.',
          remediation: 'Restart that runtime from where it was started, then retry.',
        },
      });
      return 'blocked';
    }
    this.token = token;
    this.deps.registerSecret(token);

    this.setState({
      status: 'connecting',
      stage: 'authenticate',
      detail: 'Authenticating with the running runtime.',
      ownership: attachOwnership,
      serverBuild: verdict.serverBuild,
      serverName,
    });
    const authenticated = await this.fetchReadiness(record.base_url);
    if (this.cancelled(generation)) {
      return 'blocked';
    }
    if (!authenticated) {
      this.token = null;
      this.setState({
        status: 'error',
        stage: 'authenticate',
        detail: 'Could not authenticate with the running runtime.',
        ownership: attachOwnership,
        serverBuild: verdict.serverBuild,
        serverName,
        error: {
          code: 'E_ATTACH_AUTH',
          message: 'The running runtime rejected the stored credentials.',
          remediation: 'Restart that runtime from where it was started, then retry.',
        },
      });
      return 'blocked';
    }
    this.baseUrl = trimBase(record.base_url);
    this.serverKey = serverKey ?? registryEntryKey(selected.runtimeDir);
    this.setState({
      status: 'ready',
      stage: 'ready',
      detail: reOwn
        ? 'Connected to the app-managed Agentico runtime.'
        : 'Connected to an externally managed Agentico runtime.',
      ownership: attachOwnership,
      serverBuild: verdict.serverBuild,
      serverName,
    });
    this.readySince = this.now();
    // Every successful attach (registry or legacy discovery) refreshes the
    // known-servers entry and the last-used pointer.
    this.deps.recordAttachedServer({
      serverKey: this.serverKey,
      name: serverName ?? '',
      baseUrl: trimBase(record.base_url),
      runtimeDir: selected.runtimeDir,
      lastSeenAt: new Date(this.now()).toISOString(),
    });
    return 'attached';
  }

  // --- launch path -----------------------------------------------------------

  private async launch(generation: number, selected: SelectedRuntime): Promise<void> {
    this.setState({
      status: 'launching',
      stage: 'connect',
      detail: 'Starting the bundled Agentico runtime.',
      ownership: 'none',
    });

    const resolved = this.deps.resolveServerBinary();
    if (!resolved.ok) {
      this.deps.log(`bundled server binary not found (tried ${resolved.tried.length} locations)`);
      this.setState({
        status: 'resources-missing',
        stage: 'connect',
        detail: 'The bundled runtime binary is missing from the application resources.',
        ownership: 'none',
        error: {
          code: 'E_RESOURCES_MISSING',
          message: 'The bundled agentico server binary was not found in the application resources.',
          remediation:
            'Reinstall the application. In development, run "make build" or point ' +
            'AGENTICO_SERVER_BIN at a built agentico binary, then retry.',
        },
      });
      return;
    }

    const args = ['server', '--config', selected.configPath, '--state-dir', selected.stateDir];
    this.launchCommandContext = 'bundled agentico server --config [path] --state-dir [path]';
    let child: ServerChildLike;
    try {
      child = this.deps.spawnServer(resolved.path, args);
    } catch (err) {
      const safe = toSafeError(err, 'E_LAUNCH_FAILED');
      this.deps.log(`server spawn failed: ${safe.message}`);
      this.setState({
        status: 'launch-failed',
        stage: 'connect',
        detail: 'The bundled runtime could not be started.',
        ownership: 'none',
        error: {
          code: 'E_LAUNCH_FAILED',
          message: safe.message,
          remediation: 'Check that the application files are intact and executable, then retry.',
        },
        ...this.ownedDiagnosticsField(),
      });
      return;
    }
    this.adoptChild(child);
    this.ownedSelected = selected;

    this.setState({
      status: 'waiting-health',
      stage: 'wait-health',
      detail: 'Waiting for the runtime to become healthy.',
      ownership: 'app-owned',
    });

    const ready = await this.waitForOwnedServer(generation, selected, child);
    if (ready === null || this.cancelled(generation)) {
      return;
    }

    const verdict = evaluateCompatibility(ready.compatibility);
    if (!verdict.compatible) {
      // Packaging bug: the app's own bundled server disagrees with the app.
      // This child is app-owned, so stopping it is permitted.
      await this.stopChild();
      this.deps.log(`bundled runtime is incompatible: ${verdict.reason}`);
      this.setState({
        status: 'launch-failed',
        stage: 'wait-health',
        detail: 'The bundled runtime does not match this app build.',
        ownership: 'none',
        error: {
          code: 'E_BUNDLED_INCOMPATIBLE',
          message: verdict.reason,
          remediation: 'Reinstall the application so the bundled runtime matches the app.',
        },
        ...this.ownedDiagnosticsField(),
      });
      return;
    }

    const token = ready.record.auth_token;
    if (token === undefined || token === '') {
      await this.stopChild();
      this.setState({
        status: 'launch-failed',
        stage: 'authenticate',
        detail: 'The launched runtime published no credentials.',
        ownership: 'none',
        error: {
          code: 'E_LAUNCH_NO_TOKEN',
          message: 'The launched runtime did not publish an auth token in its discovery record.',
          remediation: 'Retry. If this persists, reinstall the application.',
        },
        ...this.ownedDiagnosticsField(),
      });
      return;
    }
    this.token = token;
    this.deps.registerSecret(token);

    this.setState({
      status: 'connecting',
      stage: 'authenticate',
      detail: 'Authenticating with the runtime.',
      ownership: 'app-owned',
      serverBuild: verdict.serverBuild,
      serverName: ready.serverName,
    });
    const authenticated = await this.fetchReadiness(ready.record.base_url);
    if (this.cancelled(generation)) {
      return;
    }
    if (!authenticated) {
      const diagnostics = this.ownedDiagnosticsField();
      this.token = null;
      await this.stopChild();
      this.setState({
        status: 'launch-failed',
        stage: 'authenticate',
        detail: 'Could not authenticate with the launched runtime.',
        ownership: 'none',
        error: {
          code: 'E_LAUNCH_AUTH',
          message: 'The launched runtime rejected its own published credentials.',
          remediation: 'Retry. If this persists, reinstall the application.',
        },
        ...diagnostics,
      });
      return;
    }
    this.baseUrl = trimBase(ready.record.base_url);
    this.serverKey = registryEntryKey(selected.runtimeDir);
    this.setState({
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to the app-managed Agentico runtime.',
      ownership: 'app-owned',
      serverBuild: verdict.serverBuild,
      serverName: ready.serverName,
    });
    this.readySince = this.now();
  }

  private async waitForOwnedServer(
    generation: number,
    selected: SelectedRuntime,
    child: ServerChildLike,
  ): Promise<{
    record: DiscoveryRecord;
    compatibility: unknown;
    serverName: string | null;
  } | null> {
    const attempts = Math.max(
      1,
      Math.ceil(this.timeouts.launchReadyMs / Math.max(1, this.timeouts.pollIntervalMs)),
    );
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      if (this.cancelled(generation)) {
        return null;
      }
      if (child.exited) {
        this.releaseChild();
        this.setState({
          status: 'launch-failed',
          stage: 'wait-health',
          detail: 'The runtime exited during startup.',
          ownership: 'none',
          error: {
            code: 'E_SERVER_EXITED',
            message: 'The bundled runtime exited during startup.',
            remediation: 'Retry. Local diagnostics were recorded for this launch attempt.',
          },
          ...this.ownedDiagnosticsField(),
        });
        return null;
      }
      const outcome = evaluateDiscoveryFile(
        selected.runtimeDir,
        selected.stateDir,
        this.deps.discovery,
      );
      if (outcome.kind === 'candidate' && outcome.record.pid === child.pid) {
        try {
          const health = await this.deps.fetchJson(
            `${trimBase(outcome.record.base_url)}/api/v1/health`,
            { timeoutMs: this.timeouts.healthProbeMs },
          );
          if (health.status === 200) {
            const probe = ProbeHealthSchema.safeParse(health.body);
            if (probe.success && probe.data.status === 'ok') {
              return {
                record: outcome.record,
                compatibility: probe.data.compatibility,
                serverName: probe.data.name ?? null,
              };
            }
          }
        } catch {
          // Not listening yet — keep polling within the bound.
        }
      }
      await this.deps.sleep(this.timeouts.pollIntervalMs);
    }

    if (this.cancelled(generation)) {
      return null;
    }
    await this.stopChild(); // bounded SIGTERM→SIGKILL: never leak the child
    this.setState({
      status: 'launch-failed',
      stage: 'wait-health',
      detail: 'The runtime did not become healthy in time.',
      ownership: 'none',
      error: {
        code: 'E_LAUNCH_TIMEOUT',
        message: 'The launched runtime did not become healthy within the startup bound.',
        remediation: 'Retry. If this persists, inspect the runtime log in the runtime directory.',
      },
      ...this.ownedDiagnosticsField(),
    });
    return null;
  }

  // --- child supervision -----------------------------------------------------

  private adoptChild(child: ServerChildLike): void {
    this.child = child;
    this.childExitUnsubscribe = child.onExit((info) => this.handleChildExit(info));
  }

  private handleChildExit(info: ChildExit): void {
    // Supervision is decoupled from connection state: an unexpected exit only
    // drives ConnectionState while the child is the connected app-owned
    // server. A left-behind child (we switched elsewhere, or it died before
    // attach) keeps its backoff restart budget silently instead of hijacking
    // the connection surface.
    const attached = this.state.status === 'ready' && this.state.ownership === 'app-owned';
    const detached: SelectedRuntime | null =
      this.state.ownership === 'app-owned' ? null : this.ownedSelected;
    this.releaseChild();
    if (info.expected || this.shuttingDown) {
      return;
    }
    this.deps.log(
      `app-owned runtime exited unexpectedly (code ${String(info.code)}, signal ${String(info.signal)})`,
    );
    if (!attached) {
      if (detached !== null) {
        this.scheduleBackgroundRecovery(detached);
      }
      // Startup-phase exits are reported by the launch loop with more context.
      return;
    }
    const diagnostics = this.ownedDiagnosticsField();
    this.token = null;
    this.baseUrl = null;
    const now = this.now();
    if (this.readySince !== null && now - this.readySince >= this.timeouts.crashWindowMs) {
      this.crashAttempts = [];
    }
    this.readySince = null;
    this.setState({
      status: 'crashed',
      stage: 'connect',
      detail: 'The app-managed runtime exited unexpectedly.',
      ownership: 'none',
      error: {
        code: 'E_SERVER_CRASHED',
        message: 'The app-managed Agentico runtime exited unexpectedly.',
        remediation:
          'Agentico will try to restart it automatically. Local diagnostics were recorded.',
      },
      ...diagnostics,
    });
    this.scheduleAutomaticRecovery();
  }

  /** Relaunches at most three times in the rolling crash window. */
  private scheduleAutomaticRecovery(): void {
    if (this.recoveryPending || this.shuttingDown) {
      return;
    }
    const now = this.now();
    this.crashAttempts = this.crashAttempts.filter(
      (attemptedAt) => now - attemptedAt < this.timeouts.crashWindowMs,
    );
    if (this.crashAttempts.length >= 3) {
      this.setState({
        status: 'crashed',
        stage: 'connect',
        detail: 'The app-managed runtime stopped repeatedly.',
        ownership: 'none',
        error: {
          code: 'E_SERVER_CRASH_LOOP',
          message: 'Three automatic restart attempts failed within one minute.',
          remediation:
            'Inspect the redacted local diagnostics, then use Retry to start a fresh cycle.',
        },
        ...this.ownedDiagnosticsField(),
      });
      return;
    }
    const attempt = this.crashAttempts.length;
    this.crashAttempts.push(now);
    this.recoveryPending = true;
    const delay = this.timeouts.crashRestartInitialMs * 2 ** attempt;
    void this.deps
      .sleep(delay)
      .then(async () => {
        if (this.shuttingDown) {
          this.recoveryPending = false;
          return;
        }
        this.recoveryPending = false;
        const started = await this.start();
        if (!started) {
          this.crashAttempts.pop();
          return;
        }
        if (this.state.status !== 'ready' && !this.shuttingDown) {
          this.setState({
            status: 'crashed',
            stage: 'connect',
            detail: 'The app-managed runtime could not be recovered.',
            ownership: 'none',
            error: {
              code: 'E_SERVER_CRASHED',
              message: 'The automatic runtime restart did not reach a healthy state.',
              remediation: 'Agentico will retry within the bounded crash budget.',
            },
            ...this.ownedDiagnosticsField(),
          });
          this.scheduleAutomaticRecovery();
        }
      })
      .catch((err: unknown) => {
        this.recoveryPending = false;
        this.crashAttempts.pop();
        const safe = toSafeError(err, 'E_RECOVERY_DELAY');
        this.deps.log(`automatic recovery delay failed: ${safe.message}`);
        if (this.shuttingDown) return;
        this.setState({
          status: 'crashed',
          stage: 'connect',
          detail: 'Automatic recovery could not be scheduled.',
          ownership: 'none',
          error: {
            code: 'E_SERVER_CRASHED',
            message: 'The automatic runtime restart could not be scheduled.',
            remediation: 'Use Retry to start a fresh supervised cycle.',
          },
          ...this.ownedDiagnosticsField(),
        });
      });
  }

  /**
   * Relaunches a left-behind app-owned child after an unexpected exit, within
   * the same rolling budget as connected supervision, but silently: no
   * ConnectionState change while a different server is active. The relaunched
   * server republishes its discovery/registry record, so switching back
   * re-attaches through the standard scan.
   */
  private scheduleBackgroundRecovery(selected: SelectedRuntime): void {
    if (this.recoveryPending || this.shuttingDown) {
      return;
    }
    const now = this.now();
    this.crashAttempts = this.crashAttempts.filter(
      (attemptedAt) => now - attemptedAt < this.timeouts.crashWindowMs,
    );
    if (this.crashAttempts.length >= 3) {
      this.deps.log('detached app-owned server exhausted its restart budget; leaving it stopped');
      return;
    }
    const attempt = this.crashAttempts.length;
    this.crashAttempts.push(now);
    this.recoveryPending = true;
    const delay = this.timeouts.crashRestartInitialMs * 2 ** attempt;
    void this.deps
      .sleep(delay)
      .then(() => {
        this.recoveryPending = false;
        if (this.shuttingDown || (this.child !== null && !this.child.exited)) {
          return;
        }
        const resolved = this.deps.resolveServerBinary();
        if (!resolved.ok) {
          this.deps.log('bundled server binary not found for background relaunch');
          return;
        }
        try {
          const child = this.deps.spawnServer(resolved.path, [
            'server',
            '--config',
            selected.configPath,
            '--state-dir',
            selected.stateDir,
          ]);
          this.adoptChild(child);
          this.deps.log('detached app-owned server relaunched silently');
        } catch (err) {
          const safe = toSafeError(err, 'E_LAUNCH_FAILED');
          this.deps.log(`background server relaunch failed: ${safe.message}`);
        }
      })
      .catch((err: unknown) => {
        this.recoveryPending = false;
        const safe = toSafeError(err, 'E_RECOVERY_DELAY');
        this.deps.log(`background recovery delay failed: ${safe.message}`);
      });
  }

  private releaseChild(): void {
    this.childExitUnsubscribe?.();
    this.childExitUnsubscribe = null;
    this.child = null;
  }

  /** Stops the app-owned child (if any). Never touches external processes. */
  private async stopChild(): Promise<void> {
    const child = this.child;
    if (child === null) {
      return;
    }
    // A deliberate stop ends silent supervision: nothing relaunches a child
    // the gateway itself stopped.
    this.ownedSelected = null;
    if (child.exited) {
      this.releaseChild();
      return;
    }
    try {
      await child.stop({ timeoutMs: this.timeouts.shutdownGraceMs });
    } finally {
      if (this.child === child) {
        this.releaseChild();
      }
    }
  }

  // --- helpers ---------------------------------------------------------------

  private async fetchReadiness(baseUrl: string): Promise<boolean> {
    if (this.token === null) {
      return false;
    }
    try {
      const result = await this.deps.fetchJson(`${trimBase(baseUrl)}/api/v1/readiness`, {
        token: this.token,
        // The first readiness request may perform bounded provider CLI probes.
        // It is authenticated API work, not the tiny liveness health check.
        timeoutMs: this.timeouts.apiRequestMs,
      });
      return result.status === 200;
    } catch {
      return false;
    }
  }

  private cancelled(generation: number): boolean {
    return generation !== this.generation || this.shuttingDown;
  }

  private now(): number {
    return this.deps.now?.() ?? Date.now();
  }

  private setState(next: ConnectionState): void {
    const withIdentity = {
      ...next,
      ...(this.connectedRuntimeDir !== null
        ? { connectedRuntimeDir: this.connectedRuntimeDir }
        : {}),
      ...(this.serverKey !== null ? { serverKey: this.serverKey } : {}),
    } as ConnectionState;
    const sanitized = this.sanitizeState(withIdentity);
    this.state = sanitized;
    for (const listener of [...this.listeners]) {
      listener(sanitized);
    }
  }

  /** Defense in depth: no emitted text ever carries bearer material. */
  private sanitizeState(state: ConnectionState): ConnectionState {
    const scrub = (text: string): string => {
      let out = redactText(text);
      if (this.token !== null && this.token.length > 0) {
        out = out.split(this.token).join('[redacted]');
      }
      return out;
    };
    if (!isConnectionErrorState(state)) {
      return { ...state, detail: scrub(state.detail) };
    }
    return {
      ...state,
      detail: scrub(state.detail),
      ...(!('diagnostics' in state) || state.diagnostics === undefined
        ? {}
        : {
            diagnostics: {
              commandContext: scrub(state.diagnostics.commandContext).slice(0, 256),
              logTail: state.diagnostics.logTail
                .slice(-20)
                .map((line) => this.scrubDiagnosticLine(scrub(line)).slice(0, 512)),
            },
          }),
      error: {
        code: state.error.code,
        message: scrub(state.error.message),
        ...(state.error.remediation === undefined
          ? {}
          : { remediation: scrub(state.error.remediation) }),
      },
    };
  }

  private ownedDiagnosticsField(): { diagnostics?: ConnectionDiagnostics } {
    if (this.launchCommandContext === null) return {};
    const lines = this.deps.readDiagnosticLines?.() ?? [];
    return {
      diagnostics: {
        commandContext: this.launchCommandContext,
        logTail: lines.slice(-20).map((line) => this.scrubDiagnosticLine(line).slice(0, 512)),
      },
    };
  }

  /** Removes generic absolute paths in addition to shared token/user-path redaction. */
  private scrubDiagnosticLine(line: string): string {
    let out = redactText(line);
    if (this.token !== null && this.token.length > 0) {
      out = out.split(this.token).join('[redacted]');
    }
    return out
      .replace(/[A-Za-z]:\\[^\s"']+/g, '[path]')
      .replace(/(^|[\s("'=])\/(?!\/)[^\s"']+/g, '$1[path]');
  }
}

const SESSION_ID_PATTERN = new RegExp(`^${SESSION_ID_SEGMENT_PATTERN}$`, 'i');
const QUERYLESS_API_PATH_PATTERN = /^\/api\/v1(\/[a-z0-9_-]+)*$/i;
const QUERYLESS_SESSION_API_PATH_PATTERN = new RegExp(
  `^/api/v1/sessions/${SESSION_ID_SEGMENT_PATTERN}(?:/[a-z0-9_-]+)*$`,
  'i',
);
const SESSION_TRANSCRIPT_PATH_PATTERN = new RegExp(
  `^/api/v1/sessions/${SESSION_ID_SEGMENT_PATTERN}/transcript$`,
  'i',
);
const SAFE_API_SEGMENT = '[a-z0-9_-]+';
const RUN_LIST_PATH_PATTERN = new RegExp(`^/api/v1/features/${SAFE_API_SEGMENT}/runs$`, 'i');
const RUN_CONTENT_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/runs/\\d+/(?:artifacts|logs)/${SAFE_API_SEGMENT}$`,
  'i',
);
const REWIND_PREVIEW_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/rewind/preview$`,
  'i',
);
const REPOSITORY_DIFF_PATH_PATTERN = new RegExp(
  `^/api/v1/features/${SAFE_API_SEGMENT}/repositories/${SAFE_API_SEGMENT}/diff$`,
  'i',
);

function isAllowedApiPath(path: string): boolean {
  const parts = path.split('?');
  const pathname = parts[0] ?? '';
  if (pathname.split('/').some((segment) => segment === '.' || segment === '..')) return false;
  if (parts.length === 1) {
    return (
      QUERYLESS_API_PATH_PATTERN.test(pathname) || QUERYLESS_SESSION_API_PATH_PATTERN.test(pathname)
    );
  }
  if (parts.length === 2 && RUN_LIST_PATH_PATTERN.test(pathname)) {
    return hasBoundedIntegerQuery(parts[1] ?? '', {
      page: { min: 1 },
      page_size: { min: 1, max: 100 },
    });
  }
  if (parts.length === 2 && RUN_CONTENT_PATH_PATTERN.test(pathname)) {
    return hasBoundedIntegerQuery(parts[1] ?? '', {
      offset: { min: 0 },
      limit: { min: 1, max: MAX_RUN_CONTENT_BYTES },
    });
  }
  if (parts.length === 2 && REWIND_PREVIEW_PATH_PATTERN.test(pathname)) {
    return hasRewindPreviewQuery(parts[1] ?? '');
  }
  if (parts.length === 2 && REPOSITORY_DIFF_PATH_PATTERN.test(pathname)) {
    return hasRepositoryDiffQuery(parts[1] ?? '');
  }
  if (parts.length !== 2 || !SESSION_TRANSCRIPT_PATH_PATTERN.test(pathname)) {
    return false;
  }
  return hasBoundedIntegerQuery(parts[1] ?? '', {
    offset: { min: 0 },
    limit: { min: 1, max: 500 },
  });
}

function hasBoundedIntegerQuery(
  rawQuery: string,
  allowed: Record<string, { min: number; max?: number }>,
): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    const bounds = allowed[key];
    if (bounds === undefined || seen.has(key) || !/^\d+$/.test(value)) return false;
    const parsed = Number(value);
    if (!Number.isSafeInteger(parsed) || parsed < bounds.min) return false;
    if (bounds.max !== undefined && parsed > bounds.max) return false;
    seen.add(key);
  }
  return seen.size > 0;
}

function hasRewindPreviewQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key)) return false;
    seen.add(key);
    if (key === 'target_phase' && /^[a-z][a-z0-9_-]{0,199}$/i.test(value)) continue;
    if (key === 'roadmap_phase' && /^\d+$/.test(value) && Number(value) >= 1) continue;
    if (key === 'upgrade_pipeline' && /^[a-z][a-z0-9_-]{0,199}$/i.test(value)) continue;
    return false;
  }
  return seen.has('target_phase');
}

function hasRepositoryDiffQuery(rawQuery: string): boolean {
  if (rawQuery === '') return false;
  const seen = new Set<string>();
  for (const [key, value] of new URLSearchParams(rawQuery)) {
    if (seen.has(key) || key !== 'file_path') return false;
    seen.add(key);
    if (value === '' || value.length > 4096) return false;
    if (value.startsWith('/') || value.startsWith('\\') || value.includes('\\')) return false;
    const segments = value.split('/');
    if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) {
      return false;
    }
  }
  return seen.has('file_path');
}

function trimBase(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, '');
}
