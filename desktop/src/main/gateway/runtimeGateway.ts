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
import {
  SafeErrorException,
  safeError,
  toSafeError,
  redactText,
  type SafeError,
} from '../../shared/errors';
import type { ConnectionState } from '../../shared/ipc';
import { z } from 'zod';
import { evaluateCompatibility } from './compatibility';
import { evaluateDiscoveryFile, type DiscoveryDeps, type DiscoveryRecord } from './discovery';
import type { ResolveResult } from './resources';
import type { ChildExit } from './serverProcess';

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
}

const DEFAULT_TIMEOUTS: GatewayTimeouts = {
  healthProbeMs: 1500,
  launchReadyMs: 20000,
  pollIntervalMs: 250,
  shutdownGraceMs: 5000,
  apiRequestMs: 30000,
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
  resolveServerBinary(): ResolveResult;
  /** Spawns the bundled server (argv array, never a shell string). */
  spawnServer(binaryPath: string, args: readonly string[]): ServerChildLike;
  /** Registers a secret so the child log buffer scrubs it. */
  registerSecret(secret: string): void;
  sleep(ms: number): Promise<void>;
  /** Local redacted diagnostics sink (never crosses IPC unfiltered). */
  log(line: string): void;
  timeouts?: Partial<GatewayTimeouts>;
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
});

const INCOMPATIBLE_REMEDIATION =
  'Update the Agentico desktop app and the agentico runtime to matching releases, then retry. ' +
  'This app never shuts down a runtime it does not own — close that runtime from wherever it ' +
  'was started if you want this app to manage its own.';

type AttachResult = 'attached' | 'blocked' | 'launch';

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
    if (!/^\/api\/v1(\/[a-z0-9_-]+)*$/i.test(path)) {
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
      timeoutMs: this.timeouts.apiRequestMs,
      ...(method === 'GET' ? {} : { method, body: init.body ?? {} }),
    });
  }

  /** Runs one full connect cycle. Safe to call repeatedly. */
  async start(): Promise<void> {
    if (this.busy || this.shuttingDown || this.state.status === 'ready') {
      return;
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
  }

  /** Manual retry from any terminal state; a healthy connection is untouched. */
  async retry(): Promise<ConnectionState> {
    await this.start();
    return this.state;
  }

  /**
   * App shutdown: gracefully stop the app-owned child (bounded, then
   * SIGKILL via the child's stop()); externally owned servers are left
   * running and are never signalled.
   */
  async shutdown(): Promise<void> {
    this.shuttingDown = true;
    this.generation += 1; // invalidate in-flight connect work
    await this.stopChild();
    this.token = null;
    this.baseUrl = null;
  }

  // --- connect cycle ---------------------------------------------------------

  private async connect(generation: number): Promise<void> {
    await this.stopChild(); // never leave a stray child from an earlier cycle
    this.token = null;
    this.baseUrl = null;

    this.setState({
      status: 'resolving-runtime',
      stage: 'resolve-runtime',
      detail: 'Resolving the selected runtime.',
      ownership: 'none',
    });
    const selected = this.deps.selectRuntime();

    this.setState({
      status: 'discovering',
      stage: 'discover',
      detail: 'Looking for a running Agentico runtime.',
      ownership: 'none',
    });
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

  private async tryAttach(
    generation: number,
    selected: SelectedRuntime,
    record: DiscoveryRecord,
  ): Promise<AttachResult> {
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
      this.deps.log('discovery candidate did not answer its health probe; treating as stale');
      return 'launch';
    }
    if (this.cancelled(generation)) {
      return 'blocked';
    }
    if (health.status !== 200) {
      this.deps.log('discovery candidate returned an unhealthy status; treating as stale');
      return 'launch';
    }
    const probe = ProbeHealthSchema.safeParse(health.body);
    if (!probe.success || probe.data.status !== 'ok') {
      this.deps.log('discovery candidate health payload was unusable; treating as stale');
      return 'launch';
    }
    if (probe.data.runtime !== undefined && probe.data.runtime.state_dir !== selected.stateDir) {
      this.deps.log('running server reports a different runtime identity; not a match');
      return 'launch';
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

    const token = record.auth_token;
    if (token === undefined || token === '') {
      this.setState({
        status: 'error',
        stage: 'authenticate',
        detail: 'The running runtime published no credentials to attach with.',
        ownership: 'external',
        serverBuild: verdict.serverBuild,
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
      ownership: 'external',
      serverBuild: verdict.serverBuild,
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
        ownership: 'external',
        serverBuild: verdict.serverBuild,
        error: {
          code: 'E_ATTACH_AUTH',
          message: 'The running runtime rejected the stored credentials.',
          remediation: 'Restart that runtime from where it was started, then retry.',
        },
      });
      return 'blocked';
    }
    this.baseUrl = trimBase(record.base_url);
    this.setState({
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to an externally managed Agentico runtime.',
      ownership: 'external',
      serverBuild: verdict.serverBuild,
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
      });
      return;
    }
    this.adoptChild(child);

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
    });
    const authenticated = await this.fetchReadiness(ready.record.base_url);
    if (this.cancelled(generation)) {
      return;
    }
    if (!authenticated) {
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
      });
      return;
    }
    this.baseUrl = trimBase(ready.record.base_url);
    this.setState({
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to the app-managed Agentico runtime.',
      ownership: 'app-owned',
      serverBuild: verdict.serverBuild,
    });
  }

  private async waitForOwnedServer(
    generation: number,
    selected: SelectedRuntime,
    child: ServerChildLike,
  ): Promise<{ record: DiscoveryRecord; compatibility: unknown } | null> {
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
              return { record: outcome.record, compatibility: probe.data.compatibility };
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
    });
    return null;
  }

  // --- child supervision -----------------------------------------------------

  private adoptChild(child: ServerChildLike): void {
    this.child = child;
    this.childExitUnsubscribe = child.onExit((info) => this.handleChildExit(info));
  }

  private handleChildExit(info: ChildExit): void {
    const wasReady = this.state.status === 'ready';
    this.releaseChild();
    if (info.expected || this.shuttingDown) {
      return;
    }
    this.deps.log(
      `app-owned runtime exited unexpectedly (code ${String(info.code)}, signal ${String(info.signal)})`,
    );
    if (!wasReady) {
      // Startup-phase exits are reported by the launch loop with more context.
      return;
    }
    this.token = null;
    this.baseUrl = null;
    this.setState({
      status: 'crashed',
      stage: 'connect',
      detail: 'The app-managed runtime exited unexpectedly.',
      ownership: 'none',
      error: {
        code: 'E_SERVER_CRASHED',
        message: 'The app-managed Agentico runtime exited unexpectedly.',
        remediation: 'Retry to start it again. Local diagnostics were recorded.',
      },
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
        timeoutMs: this.timeouts.healthProbeMs,
      });
      return result.status === 200;
    } catch {
      return false;
    }
  }

  private cancelled(generation: number): boolean {
    return generation !== this.generation || this.shuttingDown;
  }

  private setState(next: ConnectionState): void {
    const sanitized = this.sanitizeState(next);
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
    const error: SafeError | undefined =
      state.error === undefined
        ? undefined
        : {
            code: state.error.code,
            message: scrub(state.error.message),
            ...(state.error.remediation === undefined
              ? {}
              : { remediation: scrub(state.error.remediation) }),
          };
    return {
      ...state,
      detail: scrub(state.detail),
      ...(error === undefined ? {} : { error }),
    };
  }
}

function trimBase(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, '');
}
