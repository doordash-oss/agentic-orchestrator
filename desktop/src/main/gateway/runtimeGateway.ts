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
 *
 * The class keeps the single public state machine and credential transport.
 * The attach workflows live in attachProfiles.ts (local-attach and
 * remote-attach profiles over one shared pipeline), app-owned child
 * supervision lives in childSupervisor.ts, and API-path authorization lives
 * in apiPaths.ts; each reaches gateway state only through a narrow
 * adapter.
 */
import {
  buildCanonicalError,
  CanonicalErrorException,
  redactText,
  stripSecrets,
  toCanonicalError,
  type BuildCanonicalErrorOptions,
  type CatalogCode,
} from '../../shared/errors';
import type { CanonicalError } from '../../shared/api/parse';
import {
  isConnectionErrorState,
  MAX_SERVER_CHOICE_CANDIDATES,
  type ConnectionState,
  type ChooseServerRequest,
  type KnownServer,
  type ServerChoiceCandidate,
  type ServerRemoveRequest,
  type ServersPrefs,
  type SwitchContext,
  type SwitchServerRequest,
} from '../../shared/ipc';
import { isAllowedApiPath, SESSION_ID_PATTERN } from './apiPaths';
import {
  ProbeHealthSchema,
  runLocalAttach,
  runRemoteAttach,
  trimBase,
  type AttachHost,
  type AttachOutcome,
  type StaleHandling,
} from './attachProfiles';
import { ChildSupervisor, type ChildSupervisorHost } from './childSupervisor';
import { evaluateCompatibility } from './compatibility';
import { evaluateDiscoveryFile, type DiscoveryDeps, type DiscoveryRecord } from './discovery';
import type { SseStream } from './events';
import { registryEntryKey, type RegistryCandidate, type RegistryScan } from './registry';
import type { LoadResult as RemoteTokenLoadResult } from './remoteTokenStore';
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
   * Bounded binary POST (raw octet-stream body, JSON response) for the
   * upload-staging endpoint. Same bearer discipline as fetchJson: the token
   * is attached here in the main process only.
   */
  fetchOctetPost?(
    url: string,
    options: { token: string; timeoutMs: number; body: Uint8Array },
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
  /**
   * Loads an encrypted remote-server bearer token keyed by serverKey. The
   * store performs secret registration itself; the gateway never calls
   * registerSecret for remote tokens. Absent means remote targets land in
   * the re-paste error state.
   */
  remoteTokens?: {
    load(serverKey: string): RemoteTokenLoadResult;
  };
  timeouts?: Partial<GatewayTimeouts>;
  /** Injectable monotonic-enough wall clock for deterministic supervision tests. */
  now?(): number;
}

/**
 * Background re-probe of a lost remote server: first probe after 1s, doubling
 * per missed probe and capped at 15s. The whole loop is bounded by
 * REMOTE_REPROBE_BUDGET_MS of wall-clock probing (~3 minutes), after which the
 * gateway stops and leaves the manual Retry surface. The loop is
 * generation-fenced like all other background work, so a server switch or
 * shutdown quiesces it.
 */
const REMOTE_REPROBE_INITIAL_MS = 1000;
const REMOTE_REPROBE_MAX_MS = 15000;
const REMOTE_REPROBE_BUDGET_MS = 180000;

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
  /**
   * Every credential this gateway has presented. Connection teardown clears
   * `token` (often before an owned-crash error is built), but emitted text
   * must still be scrubbed against the credential the crashed launch used,
   * so the secrets seen here outlive the active connection.
   */
  private readonly knownSecrets: string[] = [];
  /** Base URL of the connected runtime; only set while status is ready. */
  private baseUrl: string | null = null;
  private busy = false;
  private shuttingDown = false;
  private generation = 0;
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
   * The registry snapshot behind an awaiting-server-choice state. Records
   * stay in the main process (tokens included); only the renderer-safe
   * candidate projection crosses IPC. Cleared at the start of every connect
   * cycle and once a choice is consumed.
   */
  private pendingCandidates: RegistryCandidate[] = [];
  /** Supervises the app-owned bundled child and its crash-restart budget. */
  private readonly supervision: ChildSupervisor;
  /**
   * Whether the currently connected server is a registry/discovery local
   * runtime or a persisted remote endpoint. Drives switch candidates and the
   * lost-server surface: only remote connections get a background re-probe.
   */
  private connectedKind: 'local' | 'remote' | null = null;
  /**
   * The persisted known-server entry behind the connected (or most recently
   * lost) remote target. Kept after a remote loss so the background re-probe
   * can re-attach with stored credentials; cleared by every fresh connect
   * cycle, switch, restart, and shutdown. Never carries a token.
   */
  private remoteEntry: KnownServer | null = null;
  /**
   * Generation key of the in-flight remote re-probe loop. Gating on the
   * generation (not a bare boolean) lets a new cycle schedule its own loop
   * even while an older, already-fenced iteration is still unwinding.
   */
  private remoteReprobeGeneration: number | null = null;

  constructor(private readonly deps: GatewayDeps) {
    this.timeouts = { ...DEFAULT_TIMEOUTS, ...deps.timeouts };
    this.supervision = new ChildSupervisor(this.supervisionHost(), {
      shutdownGraceMs: this.timeouts.shutdownGraceMs,
      crashRestartInitialMs: this.timeouts.crashRestartInitialMs,
      crashWindowMs: this.timeouts.crashWindowMs,
    });
  }

  /** The adapter through which the attach profiles affect gateway state. */
  private attachHost(): AttachHost {
    return {
      fetchJson: (url, options) => this.deps.fetchJson(url, options),
      log: (line) => this.deps.log(line),
      registerSecret: (secret) => this.deps.registerSecret(secret),
      knownServers: () => this.deps.knownServers(),
      recordAttachedServer: (entry) => this.deps.recordAttachedServer(entry),
      remoteTokens: this.deps.remoteTokens,
      healthProbeMs: this.timeouts.healthProbeMs,
      now: () => this.now(),
      setState: (next) => this.setState(next),
      cancelled: (generation) => this.cancelled(generation),
      fetchReadiness: (baseUrl) => this.fetchReadiness(baseUrl),
      scrubServerText: (text, secret) => this.scrubServerText(text, secret),
      setToken: (token) => {
        this.token = token;
        if (token !== null) {
          this.rememberSecret(token);
        }
      },
      liveOwnedPid: () => this.supervision.liveChildPid(),
      parkChoiceForDeadRemote: (generation, dead) => this.parkChoiceForDeadRemote(generation, dead),
      beginAttachedConnection: (connection) => {
        this.baseUrl = connection.baseUrl;
        this.serverKey = connection.serverKey;
        this.connectedKind = connection.connectedKind;
        this.remoteEntry = connection.remoteEntry;
        if (connection.connectedKind === 'remote') {
          this.connectedRuntimeDir = null;
        }
      },
      markReady: () => this.supervision.markReady(),
    };
  }

  /** The adapter through which the child supervisor drives gateway state. */
  private supervisionHost(): ChildSupervisorHost {
    return {
      isShuttingDown: () => this.shuttingDown,
      getState: () => this.state,
      setState: (next) => this.setState(next),
      log: (line) => this.deps.log(line),
      sleeper: (ms) => this.deps.sleep(ms),
      clock: () => this.now(),
      resolveServerBinary: () => this.deps.resolveServerBinary(),
      spawnServer: (binaryPath, args) => this.deps.spawnServer(binaryPath, args),
      clearConnectionCredentials: () => {
        this.token = null;
        this.baseUrl = null;
      },
      ownedCrashError: (outcome) => this.ownedError('E_SERVER_CRASHED', { params: { outcome } }),
      ownedCrashLoopError: () => this.ownedError('E_SERVER_CRASH_LOOP'),
      startFromRecovery: () => this.start(),
    };
  }

  getState(): ConnectionState {
    return this.state;
  }

  /**
   * Locality of the active connection, read at call time by main-process
   * locality guards. `null` unless the connection is currently ready — the
   * same rule that stamps `kind` onto the ready ConnectionState. This value
   * is main-process owned; it never enters an IPC payload as an argument.
   */
  get connectedLocality(): 'local' | 'remote' | null {
    return this.state.status === 'ready' ? this.connectedKind : null;
  }

  subscribe(listener: (state: ConnectionState) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  hasOwnedChild(): boolean {
    return this.supervision.hasLiveChild();
  }

  /**
   * Authenticated request against the connected runtime's REST API. The
   * bearer token and base URL never leave this method's closure — callers
   * pass an `/api/v1/...` path and receive status + parsed JSON body only.
   */
  async apiRequest(path: string, init: ApiRequestInit = {}): Promise<HttpResult> {
    if (!isAllowedApiPath(path)) {
      throw new CanonicalErrorException(buildCanonicalError('E_BAD_API_PATH'));
    }
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new CanonicalErrorException(buildCanonicalError('E_NOT_CONNECTED'));
    }
    const method = init.method ?? 'GET';
    return this.deps.fetchJson(`${this.baseUrl}${path}`, {
      token: this.token,
      timeoutMs: init.timeoutMs ?? this.timeouts.apiRequestMs,
      ...(method === 'GET' ? {} : { method, body: init.body ?? {} }),
    });
  }

  /**
   * Authenticated binary POST against `/api/v1/uploads` (raw file bytes,
   * kind/name as bounded query parameters). The bearer token and base URL
   * never leave this method's closure; callers receive status + parsed JSON
   * body only.
   */
  async apiUpload(path: string, body: Uint8Array): Promise<HttpResult> {
    if (!isAllowedApiPath(path)) {
      throw new CanonicalErrorException(buildCanonicalError('E_BAD_API_PATH'));
    }
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new CanonicalErrorException(buildCanonicalError('E_NOT_CONNECTED'));
    }
    const fetchOctetPost = this.deps.fetchOctetPost;
    if (fetchOctetPost === undefined) {
      throw new CanonicalErrorException(buildCanonicalError('E_UPLOAD_UNAVAILABLE'));
    }
    return fetchOctetPost(`${this.baseUrl}${path}`, {
      token: this.token,
      timeoutMs: this.timeouts.apiRequestMs,
      body,
    });
  }

  /**
   * Opens the authenticated global event stream (`GET /api/v1/events`) with
   * optional cursor resume. The bearer token and base URL never leave this
   * method — callers receive status plus a line iterator only.
   */
  async openEventStream(options: { afterSeq?: number; epoch?: string } = {}): Promise<SseStream> {
    if (this.state.status !== 'ready' || this.token === null || this.baseUrl === null) {
      throw new CanonicalErrorException(buildCanonicalError('E_NOT_CONNECTED'));
    }
    const openSse = this.deps.openSse;
    if (openSse === undefined) {
      throw new CanonicalErrorException(buildCanonicalError('E_SSE_UNAVAILABLE'));
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
      throw new CanonicalErrorException(buildCanonicalError('E_NOT_CONNECTED'));
    }
    if (!SESSION_ID_PATTERN.test(sessionId)) {
      throw new CanonicalErrorException(buildCanonicalError('E_BAD_SESSION_ID'));
    }
    const openSse = this.deps.openSse;
    if (openSse === undefined) {
      throw new CanonicalErrorException(buildCanonicalError('E_SSE_UNAVAILABLE'));
    }
    const query = new URLSearchParams();
    if (options.from !== undefined) {
      if (!Number.isSafeInteger(options.from) || options.from < 0) {
        throw new CanonicalErrorException(buildCanonicalError('E_BAD_TRANSCRIPT_CURSOR'));
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
      this.failCycle(err, { stage: this.state.stage });
    } finally {
      this.busy = false;
    }
    return true;
  }

  /** Manual retry from any terminal state; a healthy connection is untouched. */
  async retry(): Promise<ConnectionState> {
    if (this.state.status === 'crashed' && !this.supervision.isRecoveryPending) {
      this.supervision.resetCrashAttempts();
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
    await this.resetConnection({ stopChild: true });
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
      // A remote target never appears in the registry snapshot: resolve it
      // from the persisted known-servers entry instead of rescanning.
      const known = this.deps
        .knownServers()
        .known.find((entry) => entry.serverKey === request.serverKey && entry.kind === 'remote');
      if (known === undefined) {
        this.deps.log('server choice named an unknown candidate; rescanning');
        await this.retry();
        return this.state;
      }
      this.busy = true;
      const generation = ++this.generation;
      try {
        await this.attachRemote(generation, known);
      } catch (err) {
        this.failCycle(err, { stage: 'resolve-runtime' });
      } finally {
        this.busy = false;
      }
      return this.state;
    }
    this.busy = true;
    const generation = ++this.generation;
    try {
      await this.attachCandidate(generation, candidate, 'error');
    } catch (err) {
      this.failCycle(err, { stage: this.state.stage });
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
      await this.resetConnection({ resetCrashAttempts: true });
      if (known !== undefined && known.kind === 'remote') {
        // Remote targets resolve from the persisted known-servers entry only
        // — never the registry, never discovery, never spawn. The generation
        // bump above already fenced any remote re-probe against the previous
        // connection, so switching away leaves no supervision residue.
        const attempted: ServerChoiceCandidate = {
          serverKey: request.serverKey,
          kind: 'remote',
          name: known.nickname ?? known.name,
          runtimeDir: '',
        };
        await this.attachRemote(generation, known, { attempted, previous });
        return this.state;
      }
      const scan = this.deps.scanRegistry();
      const candidate = scan.candidates.find((entry) => entry.serverKey === request.serverKey);
      const attempted: ServerChoiceCandidate = {
        serverKey: request.serverKey,
        kind: 'local',
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
          error:
            previous !== null
              ? buildCanonicalError('E_SWITCH_UNAVAILABLE')
              : // Without a previous server there is no back option, so the
                // catalog hint narrows to the one remaining affordance.
                buildCanonicalError('E_SWITCH_UNAVAILABLE', {
                  remediationHint: 'Use Retry to try again.',
                }),
          switchContext,
        });
        return this.state;
      }
      await this.attachCandidate(generation, candidate, 'error', switchContext);
    } catch (err) {
      const safe = toCanonicalError(err, 'E_GATEWAY');
      this.deps.log(`gateway cycle failed: ${safe.code}: ${safe.summary}`);
      const attempted: ServerChoiceCandidate = {
        serverKey: request.serverKey,
        kind: known?.kind ?? 'local',
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
   * Tears down the connection when the user's Servers pane removed the
   * server it points at, then re-enters the standard startup selection
   * flow (registry scan → picker when multiple servers live → local spawn
   * otherwise) — the same registry startup selection run at launch. Settings must already
   * be updated: the removed entry and any last-used pointer to it are gone
   * by the time this runs, so the fresh connect cycle cannot reattach to
   * the removed server.
   *
   * Callers: the `serverRemove` IPC handler. A no-op when the named server
   * is not the active connection.
   *
   * Deliberately does NOT stop the app-owned child: supervision is
   * decoupled from connection state (same rule as switchServer), and the
   * child may be the very server the new connect cycle attaches to.
   */
  async disconnectServer(request: ServerRemoveRequest): Promise<ConnectionState> {
    if (this.shuttingDown || this.busy) {
      return this.state;
    }
    if (this.serverKey !== request.serverKey) {
      return this.state;
    }
    this.busy = true;
    // The generation bump fences any in-flight attach and any background
    // remote re-probe for the removed connection.
    const generation = ++this.generation;
    try {
      await this.resetConnection();
      this.setState({
        status: 'idle',
        stage: 'resolve-runtime',
        detail: 'Reconnecting after the server was removed.',
        ownership: 'none',
      });
      await this.connect(generation);
    } catch (err) {
      this.failCycle(err, { stage: 'resolve-runtime' });
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
    if (this.serverKey !== null && this.serverKey !== targetKey) {
      if (this.connectedKind === 'remote') {
        return {
          serverKey: this.serverKey,
          kind: 'remote',
          name: this.state.serverName ?? null,
          runtimeDir: '',
        };
      }
      if (this.connectedRuntimeDir !== null) {
        return {
          serverKey: this.serverKey,
          kind: 'local',
          name: this.state.serverName ?? null,
          runtimeDir: this.connectedRuntimeDir,
        };
      }
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
    this.supervision.clearReadySince();
    this.serverKey = null;
    if (this.connectedKind === 'remote' && this.remoteEntry !== null) {
      // Lost remote: never spawn, never consume the crash budget. Surface a
      // warning-class error — the background re-probe below keeps trying and
      // reconnects a server that comes back with its stored credentials.
      const entry = this.remoteEntry;
      this.connectedKind = null;
      this.setState({
        status: 'error',
        stage: 'connect',
        detail: 'The remote Agentico server is no longer reachable.',
        ownership: 'external',
        error: buildCanonicalError('E_REMOTE_SERVER_LOST_REPROBING'),
      });
      this.scheduleRemoteReprobe(this.generation, entry);
      return;
    }
    this.setState({
      status: 'error',
      stage: 'connect',
      detail: 'The externally managed runtime is no longer reachable.',
      ownership: 'external',
      error: buildCanonicalError('E_EXTERNAL_RUNTIME_UNRESPONSIVE'),
    });
  }

  /**
   * App shutdown: gracefully stop the app-owned child (bounded, then
   * SIGKILL via the child's stop()); externally owned servers are left
   * running and are never signalled.
   */
  async shutdown(): Promise<void> {
    this.shuttingDown = true;
    this.generation += 1; // invalidate in-flight connect work
    await this.resetConnection({
      stopChild: true,
      clearConnectedRuntimeDir: true,
      clearPendingCandidates: true,
    });
  }

  // --- connect cycle ---------------------------------------------------------

  private async connect(generation: number): Promise<void> {
    // never leave a stray child from an earlier cycle
    await this.resetConnection({
      stopChild: true,
      clearLaunchContext: true,
      clearPendingCandidates: true,
    });

    // Remote last-used intent: when the last successfully attached server is
    // a persisted remote entry, the cycle attaches to it directly — never the
    // registry, never legacy discovery, never spawn. An unreachable remote
    // lands in the picker when other servers are live, and on its own error
    // surface (never spawn) when nothing else could replace it.
    const lastUsedKey = this.deps.knownServers().lastUsed;
    if (lastUsedKey !== null) {
      const remote = this.deps
        .knownServers()
        .known.find((entry) => entry.serverKey === lastUsedKey && entry.kind === 'remote');
      if (remote !== undefined) {
        this.connectedRuntimeDir = null;
        await this.attachRemote(generation, remote, undefined, { unreachableToChoice: true });
        return;
      }
    }

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
        // The picker lists remote entries alongside the live locals, each
        // probed once so an unreachable remote is visibly marked.
        const candidates = await this.buildChoiceCandidates(scan);
        if (this.cancelled(generation)) {
          return;
        }
        this.setState({
          status: 'awaiting-server-choice',
          stage: 'connect',
          detail: 'Choose which Agentico server to connect to.',
          ownership: 'none',
          candidates,
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
   * The picker's candidate set: registry-located locals (the scan just proved
   * liveness, so no health verdict is carried) union the persisted remote
   * entries, each probed once through the auth-exempt health endpoint with
   * the usual probe bound. A remote known to be dead already (the last-used
   * entry whose probe just failed) skips its probe and is carried as
   * unreachable. Nicknames win over record and stored names. Never a token
   * or a base URL in the result.
   */
  private async buildChoiceCandidates(
    scan: RegistryScan,
    unreachableRemoteKey?: string,
  ): Promise<ServerChoiceCandidate[]> {
    const known = this.deps.knownServers().known;
    const nicknameByKey = new Map(
      known
        .filter((entry) => entry.nickname !== undefined)
        .map((entry) => [entry.serverKey, entry.nickname as string]),
    );
    const candidates: ServerChoiceCandidate[] = scan.candidates.map((candidate) => ({
      kind: 'local' as const,
      serverKey: candidate.serverKey,
      name: nicknameByKey.get(candidate.serverKey) ?? candidate.record.name ?? null,
      runtimeDir: candidate.runtimeDir,
    }));
    const seen = new Set(candidates.map((candidate) => candidate.serverKey));
    const remotes = await Promise.all(
      known
        .filter((entry) => entry.kind === 'remote' && !seen.has(entry.serverKey))
        .map(async (entry): Promise<ServerChoiceCandidate> => {
          const healthy =
            entry.serverKey === unreachableRemoteKey
              ? false
              : await this.probeRemoteLiveness(entry.baseUrl);
          return {
            kind: 'remote' as const,
            serverKey: entry.serverKey,
            name: entry.nickname ?? (entry.name === '' ? null : entry.name),
            health: healthy ? 'healthy' : 'unreachable',
          };
        }),
    );
    return [...candidates, ...remotes].slice(0, MAX_SERVER_CHOICE_CANDIDATES);
  }

  /** One bounded auth-exempt liveness probe; a throw is simply unreachable. */
  private async probeRemoteLiveness(baseUrl: string): Promise<boolean> {
    try {
      const result = await this.deps.fetchJson(`${trimBase(baseUrl)}/api/v1/health`, {
        timeoutMs: this.timeouts.healthProbeMs,
      });
      const probe = result.status === 200 ? ProbeHealthSchema.safeParse(result.body) : null;
      return probe !== null && probe.success && probe.data.status === 'ok';
    } catch {
      return false;
    }
  }

  /**
   * Startup-only fallback for a dead last-used remote: park in the picker
   * when at least one other server (local or remote) can be listed beside
   * it, so the user picks a working server instead of staring at the error
   * surface. Returns false when nothing else is live — the caller then
   * emits the profile-specific lost-server error.
   */
  private async parkChoiceForDeadRemote(generation: number, dead: KnownServer): Promise<boolean> {
    if (this.cancelled(generation)) {
      return true;
    }
    const scan = this.deps.scanRegistry();
    this.pendingCandidates = scan.candidates;
    const candidates = await this.buildChoiceCandidates(scan, dead.serverKey);
    if (this.cancelled(generation)) {
      return true;
    }
    if (candidates.length < 2) {
      return false;
    }
    this.setState({
      status: 'awaiting-server-choice',
      stage: 'connect',
      detail: 'Choose which Agentico server to connect to.',
      ownership: 'none',
      candidates,
    });
    return true;
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
  ): Promise<AttachOutcome> {
    return runLocalAttach(
      this.attachHost(),
      generation,
      selected,
      record,
      stale,
      serverKey,
      switchContext,
    );
  }

  // --- remote attach path ----------------------------------------------------
  //
  // Remote targets come from the persisted known-servers list (kind 'remote',
  // keyed by serverKey) and their encrypted tokens from the RemoteTokenStore.
  // This path never touches the registry, discovery files, PID liveness, or
  // spawn, and never consumes the crash-restart budget. The local
  // runtime-dir/state-dir consistency check does not apply to a process the
  // app does not co-locate with. On the startup last-used path only
  // (`unreachableToChoice`), a dead remote parks in the picker instead of
  // the error surface when other servers are live.

  private async attachRemote(
    generation: number,
    entry: KnownServer,
    switchContext?: SwitchContext,
    options?: { unreachableToChoice?: boolean },
  ): Promise<void> {
    await runRemoteAttach(this.attachHost(), generation, entry, switchContext, options);
  }

  /**
   * Bounded background re-probe of a lost remote server: 1s initial delay,
   * exponential doubling capped at 15s, bounded to REMOTE_REPROBE_BUDGET_MS
   * (~3 minutes) of total probing, then the loop gives up and the manual
   * Retry surface remains. Generation-fenced exactly like the crash-recovery
   * paths: a switch, restart, or shutdown quiesces the loop. When the server
   * answers healthy again the loop hands off to a full fresh attach, which
   * re-loads the stored credentials and re-evaluates compatibility.
   */
  private scheduleRemoteReprobe(generation: number, entry: KnownServer): void {
    if (this.remoteReprobeGeneration === generation || this.shuttingDown) {
      return;
    }
    this.remoteReprobeGeneration = generation;
    const startedAt = this.now();
    const step = async (attempt: number): Promise<void> => {
      const delay = Math.min(REMOTE_REPROBE_INITIAL_MS * 2 ** attempt, REMOTE_REPROBE_MAX_MS);
      await this.deps.sleep(delay);
      if (this.remoteReprobeGeneration !== generation || this.cancelled(generation)) {
        return;
      }
      if (this.now() - startedAt >= REMOTE_REPROBE_BUDGET_MS) {
        this.deps.log('remote re-probe budget exhausted; leaving the manual retry surface');
        this.remoteReprobeGeneration = null;
        return;
      }
      let healthy = false;
      try {
        const health = await this.deps.fetchJson(`${trimBase(entry.baseUrl)}/api/v1/health`, {
          timeoutMs: this.timeouts.healthProbeMs,
        });
        const probe = ProbeHealthSchema.safeParse(health.body);
        healthy = health.status === 200 && probe.success && probe.data.status === 'ok';
      } catch {
        healthy = false;
      }
      if (this.remoteReprobeGeneration !== generation || this.cancelled(generation)) {
        return;
      }
      if (healthy) {
        this.deps.log('remote server is healthy again; re-attaching');
        this.remoteReprobeGeneration = null;
        await this.attachRemote(generation, entry);
        return;
      }
      await step(attempt + 1);
    };
    void step(0).catch((err: unknown) => {
      if (this.remoteReprobeGeneration === generation) {
        this.remoteReprobeGeneration = null;
      }
      const safe = toCanonicalError(err, 'E_RECOVERY_DELAY');
      this.deps.log(`remote re-probe failed: ${safe.summary}`);
    });
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
        error: buildCanonicalError('E_RESOURCES_MISSING'),
      });
      return;
    }

    const args = ['server', '--config', selected.configPath, '--state-dir', selected.stateDir];
    this.launchCommandContext = 'bundled agentico server --config [path] --state-dir [path]';
    let child: ServerChildLike;
    try {
      child = this.deps.spawnServer(resolved.path, args);
    } catch (err) {
      this.deps.log('server spawn failed: E_LAUNCH_FAILED');
      this.setState({
        status: 'launch-failed',
        stage: 'connect',
        detail: 'The bundled runtime could not be started.',
        ownership: 'none',
        error: this.ownedError('E_LAUNCH_FAILED', {
          ...(err instanceof Error && err.message !== '' ? { diagnostics: err.message } : {}),
        }),
      });
      return;
    }
    this.supervision.adopt(child);
    this.supervision.setOwnedSelected(selected);

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
      await this.supervision.stop();
      const error = this.ownedError('E_BUNDLED_INCOMPATIBLE', { params: verdict });
      this.deps.log(`bundled runtime is incompatible: ${error.summary}`);
      this.setState({
        status: 'launch-failed',
        stage: 'wait-health',
        detail: 'The bundled runtime does not match this app build.',
        ownership: 'none',
        error,
      });
      return;
    }

    const token = ready.record.auth_token;
    if (token === undefined || token === '') {
      await this.supervision.stop();
      this.setState({
        status: 'launch-failed',
        stage: 'authenticate',
        detail: 'The launched runtime published no credentials.',
        ownership: 'none',
        error: this.ownedError('E_LAUNCH_NO_TOKEN'),
      });
      return;
    }
    this.token = token;
    this.rememberSecret(token);
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
      this.token = null;
      await this.supervision.stop();
      this.setState({
        status: 'launch-failed',
        stage: 'authenticate',
        detail: 'Could not authenticate with the launched runtime.',
        ownership: 'none',
        error: this.ownedError('E_LAUNCH_AUTH'),
      });
      return;
    }
    this.baseUrl = trimBase(ready.record.base_url);
    this.serverKey = registryEntryKey(selected.runtimeDir);
    this.connectedKind = 'local';
    this.remoteEntry = null;
    this.setState({
      status: 'ready',
      stage: 'ready',
      kind: 'local',
      detail: 'Connected to the app-managed Agentico runtime.',
      ownership: 'app-owned',
      serverBuild: verdict.serverBuild,
      serverName: ready.serverName,
    });
    this.supervision.markReady();
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
        this.supervision.release();
        this.setState({
          status: 'launch-failed',
          stage: 'wait-health',
          detail: 'The runtime exited during startup.',
          ownership: 'none',
          error: this.ownedError('E_SERVER_EXITED'),
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
    await this.supervision.stop(); // bounded SIGTERM→SIGKILL: never leak the child
    this.setState({
      status: 'launch-failed',
      stage: 'wait-health',
      detail: 'The runtime did not become healthy in time.',
      ownership: 'none',
      error: this.ownedError('E_LAUNCH_TIMEOUT'),
    });
    return null;
  }

  // --- connection teardown/reset ----------------------------------------------

  /**
   * Central connection teardown: clears the credential, endpoint, identity,
   * and readiness fields every reset path shares. Per-call-site differences
   * are explicit options: stopping the app-owned child (restart, shutdown,
   * connect), resetting the crash budget (switch), and clearing the launch
   * context, pending picker snapshot, or resolved runtime dir.
   */
  private async resetConnection(
    options: {
      stopChild?: boolean;
      resetCrashAttempts?: boolean;
      clearLaunchContext?: boolean;
      clearPendingCandidates?: boolean;
      clearConnectedRuntimeDir?: boolean;
    } = {},
  ): Promise<void> {
    if (options.stopChild === true) {
      await this.supervision.stop();
    }
    this.token = null;
    this.baseUrl = null;
    this.supervision.clearReadySince();
    this.serverKey = null;
    this.connectedKind = null;
    this.remoteEntry = null;
    if (options.resetCrashAttempts === true) {
      this.supervision.resetCrashAttempts();
    }
    if (options.clearLaunchContext === true) {
      this.launchCommandContext = null;
    }
    if (options.clearPendingCandidates === true) {
      this.pendingCandidates = [];
    }
    if (options.clearConnectedRuntimeDir === true) {
      this.connectedRuntimeDir = null;
    }
  }

  /**
   * Terminal transition for any unexpected connect-cycle throw: logs the
   * redacted failure and parks in the renderer-visible error state with a
   * manual retry path.
   */
  private failCycle(
    err: unknown,
    options: { stage: Extract<ConnectionState, { status: 'error' }>['stage'] },
  ): void {
    const safe = toCanonicalError(err, 'E_GATEWAY');
    this.deps.log(`gateway cycle failed: ${safe.code}: ${safe.summary}`);
    this.setState({
      status: 'error',
      stage: options.stage,
      detail: 'The connection attempt failed unexpectedly.',
      ownership: 'none',
      error: safe,
    });
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
      // Locality is main-process-owned: every ready state carries the kind
      // the active attach established (set before its ready emission; cleared
      // on teardown), so transitional states never stamp a stale value.
      ...(next.status === 'ready' && this.connectedKind !== null
        ? { kind: this.connectedKind }
        : {}),
    } as ConnectionState;
    const sanitized = this.sanitizeState(withIdentity);
    this.state = sanitized;
    for (const listener of [...this.listeners]) {
      listener(sanitized);
    }
  }

  /** Defense in depth: no emitted text ever carries bearer material. */
  private sanitizeState(state: ConnectionState): ConnectionState {
    const scrub = (text: string): string => stripSecrets(redactText(text), this.knownSecrets);
    if (!isConnectionErrorState(state)) {
      return { ...state, detail: scrub(state.detail) };
    }
    return {
      ...state,
      detail: scrub(state.detail),
      error: {
        ...state.error,
        title: scrub(state.error.title),
        summary: scrub(state.error.summary),
        ...(state.error.remediation?.hint === undefined
          ? {}
          : {
              remediation: {
                ...state.error.remediation,
                hint: scrub(state.error.remediation.hint),
              },
            }),
        ...(state.error.diagnostics === undefined
          ? {}
          : { diagnostics: this.scrubOwnedDiagnostics(scrub(state.error.diagnostics)) }),
      },
    };
  }

  /**
   * Re-applies the owned-diagnostics bounding to an already-folded
   * diagnostics string: an optional leading cause line plus the launch
   * command context (≤256 chars each) and the last 20 log lines (≤512 chars
   * each). The newest log line — usually the one carrying the actual launch
   * error — is the tail, so over-length input is trimmed from the middle,
   * never from the end.
   */
  private scrubOwnedDiagnostics(text: string): string {
    const lines = text.split('\n');
    const bounded = lines.length <= 22 ? lines : [...lines.slice(0, 2), ...lines.slice(-20)];
    return bounded
      .map((line, index) => this.scrubDiagnosticLine(line).slice(0, index < 2 ? 256 : 512))
      .join('\n');
  }

  /**
   * Scrubs the presented credential out of server-controlled display text
   * (the health payload's `name`). Nicknames and candidate names are
   * user-controlled and never carry the secret through this seam.
   */
  private scrubServerText(text: string | null, secret: string): string | null {
    if (text === null) {
      return null;
    }
    return stripSecrets(text, [secret]).slice(0, 64);
  }

  /**
   * The bounded, redacted diagnostics string for a failure of the app-owned
   * child: the launch command context (≤256 chars) joined with the last 20
   * log lines (≤512 chars each), scrubbed. Undefined until a launch has
   * published its command context.
   */
  private ownedDiagnosticsText(): string | undefined {
    if (this.launchCommandContext === null) return undefined;
    const lines = this.deps.readDiagnosticLines?.() ?? [];
    return [
      this.launchCommandContext.slice(0, 256),
      ...lines.slice(-20).map((line) => this.scrubDiagnosticLine(line).slice(0, 512)),
    ].join('\n');
  }

  /**
   * Builds the canonical error for an app-owned failure, folding the bounded,
   * redacted launch diagnostics into the error's `diagnostics` string.
   */
  private ownedError<C extends CatalogCode>(
    code: C,
    options: BuildCanonicalErrorOptions<C> = {},
  ): CanonicalError {
    const diagnostics = [options.diagnostics, this.ownedDiagnosticsText()]
      .filter((text): text is string => text !== undefined && text !== '')
      .join('\n');
    return buildCanonicalError(code, {
      ...options,
      ...(diagnostics === '' ? {} : { diagnostics }),
    });
  }

  /** Removes generic absolute paths in addition to shared token/user-path redaction. */
  private scrubDiagnosticLine(line: string): string {
    const out = stripSecrets(redactText(line), this.knownSecrets);
    return out
      .replace(/[A-Za-z]:\\[^\s"']+/g, '[path]')
      .replace(/(^|[\s("'=])\/(?!\/)[^\s"']+/g, '$1[path]');
  }

  /** Records a presented credential so emitted text stays scrubbed after teardown. */
  private rememberSecret(secret: string): void {
    if (secret.length === 0 || this.knownSecrets.includes(secret)) return;
    this.knownSecrets.push(secret);
    if (this.knownSecrets.length > 8) {
      this.knownSecrets.shift();
    }
  }
}
