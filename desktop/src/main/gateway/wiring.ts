/**
 * Production wiring for the RuntimeGateway: real filesystem, process, and
 * network dependencies. Everything behavioural lives in the pure modules —
 * this file only binds them to Node/Electron primitives.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { redactText } from '../../shared/errors';
import { assertNoPrototypePollution, assertWithinByteSize } from '../../shared/sanitize';
import type { DiscoveryDeps } from './discovery';
import { RedactedLogBuffer } from './logBuffer';
import { fileIsExecutable, resolveServerBinary } from './resources';
import { RuntimeGateway, type GatewayDeps, type SelectedRuntime } from './runtimeGateway';
import { ManagedServerProcess } from './serverProcess';

/** Mirrors the Go launcher's default runtime parents (cmd/agentico/main.go). */
const DEFAULT_RUNTIME_PARENT = '~/.agentic-orchestrator';
const LEGACY_RUNTIME_PARENT = '~/.agentic-workflow';
const STATE_BASENAME = 'features';
const CONFIG_BASENAME = 'config.yaml';

/** Upper bound for gateway probe responses (health/readiness are tiny). */
const MAX_PROBE_RESPONSE_BYTES = 1024 * 1024;

export interface RuntimeGatewayWiringOptions {
  /** Reads the persisted runtime selection (a runtime parent directory). */
  getRuntimeSelection(): string | null;
  isPackaged: boolean;
  resourcesPath: string;
  /** Repository root in development (…/desktop/out/main → repo). */
  appRoot: string;
  /** Redacted warning sink (defaults to console.warn). */
  warn?: (line: string) => void;
}

export interface WiredRuntimeGateway {
  gateway: RuntimeGateway;
  /** Local redacted diagnostics (child stdio + gateway notes). */
  logBuffer: RedactedLogBuffer;
}

export function createRuntimeGateway(options: RuntimeGatewayWiringOptions): WiredRuntimeGateway {
  const logBuffer = new RedactedLogBuffer(400);
  const warn = options.warn ?? ((line: string) => console.warn(line));

  const discovery: DiscoveryDeps = {
    readFile: (filePath) => fs.readFileSync(filePath, 'utf8'),
    statFile: (filePath) => {
      try {
        const stat = fs.statSync(filePath);
        return { mode: stat.mode, uid: stat.uid };
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
          return null;
        }
        throw err;
      }
    },
    euid: typeof process.geteuid === 'function' ? process.geteuid() : null,
    isProcessAlive: (pid) => {
      try {
        process.kill(pid, 0);
        return true;
      } catch (err) {
        // EPERM means the process exists but belongs to someone else.
        return (err as NodeJS.ErrnoException).code === 'EPERM';
      }
    },
  };

  const deps: GatewayDeps = {
    selectRuntime: () => selectRuntime(options.getRuntimeSelection()),
    discovery,
    fetchJson,
    resolveServerBinary: () =>
      resolveServerBinary(
        {
          platform: process.platform,
          isPackaged: options.isPackaged,
          resourcesPath: options.resourcesPath,
          appRoot: options.appRoot,
          env: process.env,
        },
        { isExecutableFile: fileIsExecutable },
      ),
    spawnServer: (binaryPath, args) =>
      ManagedServerProcess.launch({
        binaryPath,
        args,
        spawn: (file, argv, spawnOptions) => spawn(file, [...argv], spawnOptions),
        log: logBuffer,
      }),
    registerSecret: (secret) => logBuffer.addSecret(secret),
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    log: (line) => {
      const redacted = redactText(line);
      logBuffer.append(`[gateway] ${redacted}\n`);
      warn(`[agentico-gateway] ${redacted}`);
    },
  };

  return { gateway: new RuntimeGateway(deps), logBuffer };
}

/**
 * Resolves the selected runtime parent to the concrete directories the Go
 * CLI uses, canonicalizing symlinks so the state-dir identity comparison
 * matches the server's symlink-free view (Go canonicalizeStateDir).
 */
export function selectRuntime(selection: string | null, homeDir = os.homedir()): SelectedRuntime {
  let parent: string;
  const trimmed = selection?.trim() ?? '';
  if (trimmed !== '') {
    parent = expandHome(trimmed, homeDir);
  } else {
    const modern = expandHome(DEFAULT_RUNTIME_PARENT, homeDir);
    const legacy = expandHome(LEGACY_RUNTIME_PARENT, homeDir);
    parent = exists(modern) ? modern : exists(legacy) ? legacy : modern;
  }
  const stateDir = canonicalize(path.join(canonicalize(parent), STATE_BASENAME));
  const runtimeDir = path.dirname(stateDir);
  return { runtimeDir, stateDir, configPath: path.join(runtimeDir, CONFIG_BASENAME) };
}

function expandHome(candidate: string, homeDir: string): string {
  if (candidate === '~') {
    return homeDir;
  }
  if (candidate.startsWith('~/')) {
    return path.join(homeDir, candidate.slice(2));
  }
  return candidate;
}

function exists(candidate: string): boolean {
  try {
    fs.statSync(candidate);
    return true;
  } catch {
    return false;
  }
}

function canonicalize(candidate: string): string {
  try {
    return fs.realpathSync(candidate);
  } catch {
    return candidate;
  }
}

/**
 * Bounded, sanitized JSON request (GET unless a mutating method is given).
 * The Authorization header is attached here in the main process only; tokens
 * never appear in URLs. Mutations carry the server's CSRF defense header.
 */
async function fetchJson(
  url: string,
  requestOptions: {
    token?: string;
    timeoutMs: number;
    method?: 'GET' | 'POST' | 'PATCH' | 'PUT';
    body?: unknown;
  },
): Promise<{ status: number; body: unknown }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), requestOptions.timeoutMs);
  try {
    const method = requestOptions.method ?? 'GET';
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (requestOptions.token !== undefined) {
      headers['Authorization'] = `Bearer ${requestOptions.token}`;
    }
    let payload: string | undefined;
    if (method !== 'GET') {
      headers['Content-Type'] = 'application/json';
      headers['X-Agentico-Client'] = 'local';
      payload = JSON.stringify(requestOptions.body ?? {});
    }
    const response = await fetch(url, {
      method,
      signal: controller.signal,
      headers,
      redirect: 'error',
      ...(payload === undefined ? {} : { body: payload }),
    });
    const text = await response.text();
    assertWithinByteSize(text, MAX_PROBE_RESPONSE_BYTES);
    let body: unknown;
    if (text !== '') {
      body = JSON.parse(text);
      assertNoPrototypePollution(body);
    }
    return { status: response.status, body };
  } finally {
    clearTimeout(timer);
  }
}
