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
 * Production wiring for the RuntimeGateway: real filesystem, process, and
 * network dependencies. Everything behavioural lives in the pure modules —
 * this file only binds them to Node/Electron primitives.
 */
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { redactText, requestTimeoutError, SafeErrorException } from '../../shared/errors';
import type { KnownServer, ServersPrefs } from '../../shared/ipc';
import {
  assertNoPrototypePollution,
  assertWithinByteSize,
  MAX_PAYLOAD_BYTES,
} from '../../shared/sanitize';
import type { DiscoveryDeps } from './discovery';
import type { SseStream } from './events';
import { RedactedLogBuffer } from './logBuffer';
import { RemoteTokenStore, type SafeStorageLike } from './remoteTokenStore';
import { scanRegistry, type RegistryDeps, type RegistryScan } from './registry';
import { fileIsExecutable, resolveServerBinary } from './resources';
import { RuntimeGateway, type GatewayDeps, type SelectedRuntime } from './runtimeGateway';
import { ManagedServerProcess } from './serverProcess';

/** Mirrors the Go launcher's default runtime parent (cmd/agentico/main.go). */
const DEFAULT_RUNTIME_PARENT = '~/.agentic-orchestrator';
const STATE_BASENAME = 'features';
const CONFIG_BASENAME = 'config.yaml';

/**
 * Upper bound a caller may opt into for health/readiness probes, which are
 * tiny. It is deliberately NOT the default: domain responses are governed by
 * the boundary-wide MAX_PAYLOAD_BYTES, so this transport gate can never become
 * a stricter accidental limit than the documented one.
 */
export const MAX_PROBE_RESPONSE_BYTES = 1024 * 1024;

export interface RuntimeGatewayWiringOptions {
  /** Reads the persisted runtime selection (a runtime parent directory). */
  getRuntimeSelection(): string | null;
  /** Reads the persisted known-servers prefs (bounded list + last-used pointer). */
  getServersPrefs(): ServersPrefs;
  /** Persists a successful attach (upsert + last-used pointer). */
  recordAttachedServer(entry: KnownServer): void;
  isPackaged: boolean;
  resourcesPath: string;
  /** Repository root in development (…/desktop/out/main → repo). */
  appRoot: string;
  /** Electron safeStorage primitive backing the encrypted remote token store. */
  safeStorage: SafeStorageLike;
  /** App userData directory; the token store file lives directly inside it. */
  userDataDir: string;
  /** Redacted warning sink (defaults to console.warn). */
  warn?: (line: string) => void;
}

export interface WiredRuntimeGateway {
  gateway: RuntimeGateway;
  /** Local redacted diagnostics (child stdio + gateway notes). */
  logBuffer: RedactedLogBuffer;
  /** Fresh central-registry scan against the same deps the gateway uses. */
  scanRegistry(): RegistryScan;
  /** Encrypted remote-server token store shared with the gateway. */
  remoteTokens: RemoteTokenStore;
}

export function createRuntimeGateway(options: RuntimeGatewayWiringOptions): WiredRuntimeGateway {
  const logBuffer = new RedactedLogBuffer(400);
  const warn = options.warn ?? ((line: string) => console.warn(line));

  // Mirrors the settings-store placement: one file directly in userData.
  const remoteTokens = new RemoteTokenStore(path.join(options.userDataDir, 'remote-tokens.json'), {
    safeStorage: options.safeStorage,
    registerSecret: (secret) => logBuffer.addSecret(secret),
  });

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

  const registry: RegistryDeps = {
    ...discovery,
    listDir: (dirPath) => {
      try {
        return fs.readdirSync(dirPath);
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
          return null;
        }
        throw err;
      }
    },
    removeFile: (filePath) => fs.unlinkSync(filePath),
    homeDir: os.homedir(),
    dirExists: (dirPath) => {
      try {
        return fs.statSync(dirPath).isDirectory();
      } catch {
        return false;
      }
    },
  };

  const deps: GatewayDeps = {
    selectRuntime: () => selectRuntime(options.getRuntimeSelection()),
    discovery,
    fetchJson,
    fetchOctetPost,
    openSse,
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
    readDiagnosticLines: () => logBuffer.snapshot(),
    scanRegistry: () => scanRegistry(registry),
    knownServers: () => options.getServersPrefs(),
    recordAttachedServer: (entry) => options.recordAttachedServer(entry),
    remoteTokens,
  };

  return {
    gateway: new RuntimeGateway(deps),
    logBuffer,
    scanRegistry: () => scanRegistry(registry),
    remoteTokens,
  };
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
    parent = expandHome(DEFAULT_RUNTIME_PARENT, homeDir);
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
 * Exceeding the bound raises the typed timeout error, never the raw DOM abort
 * message — a mutation that outran its bound may still be running server-side.
 */
export async function fetchJson(
  url: string,
  requestOptions: {
    token?: string;
    timeoutMs: number;
    method?: 'GET' | 'POST' | 'PATCH' | 'PUT';
    body?: unknown;
    /** Tighter response bound for probe endpoints; defaults to the boundary cap. */
    maxResponseBytes?: number;
  },
): Promise<{ status: number; body: unknown }> {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, requestOptions.timeoutMs);
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
    assertWithinByteSize(text, requestOptions.maxResponseBytes ?? MAX_PAYLOAD_BYTES);
    let body: unknown;
    if (text !== '') {
      body = JSON.parse(text);
      assertNoPrototypePollution(body);
    }
    return { status: response.status, body };
  } catch (err) {
    if (timedOut) {
      throw new SafeErrorException(requestTimeoutError());
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Bounded binary POST for upload staging: raw octet-stream bytes in, the
 * same bounded/pollution-scanned JSON surface out as fetchJson. Shares
 * fetchJson's invariants — bearer header attached here in the main process
 * only, the CSRF mutation header, the typed timeout error — so a staged
 * upload can never outrun the mutation bound silently.
 */
export async function fetchOctetPost(
  url: string,
  requestOptions: {
    token: string;
    timeoutMs: number;
    body: Uint8Array;
    /** Tighter response bound for probe endpoints; defaults to the boundary cap. */
    maxResponseBytes?: number;
  },
): Promise<{ status: number; body: unknown }> {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, requestOptions.timeoutMs);
  try {
    const response = await fetch(url, {
      method: 'POST',
      signal: controller.signal,
      headers: {
        Accept: 'application/json',
        'Content-Type': 'application/octet-stream',
        Authorization: `Bearer ${requestOptions.token}`,
        'X-Agentico-Client': 'local',
      },
      redirect: 'error',
      body: requestOptions.body,
    });
    const text = await response.text();
    assertWithinByteSize(text, requestOptions.maxResponseBytes ?? MAX_PAYLOAD_BYTES);
    let body: unknown;
    if (text !== '') {
      body = JSON.parse(text);
      assertNoPrototypePollution(body);
    }
    return { status: response.status, body };
  } catch (err) {
    if (timedOut) {
      throw new SafeErrorException(requestTimeoutError());
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

/** Upper bound for one buffered SSE line (invalidation events are tiny). */
const MAX_SSE_LINE_BYTES = 1024 * 1024;

/**
 * Opens a long-lived SSE response. The Authorization header is attached here
 * in the main process only — the token never appears in the URL, so it can
 * never reach server request logs or proxies.
 */
async function openSse(url: string, options: { token: string }): Promise<SseStream> {
  const controller = new AbortController();
  const response = await fetch(url, {
    method: 'GET',
    signal: controller.signal,
    redirect: 'error',
    headers: {
      Accept: 'text/event-stream',
      Authorization: `Bearer ${options.token}`,
    },
  });
  const body = response.body;
  return {
    status: response.status,
    lines: body === null ? emptyLines() : streamLines(body, controller),
    close: () => controller.abort(),
  };
}

async function* emptyLines(): AsyncIterable<string> {
  // No body — the caller sees an immediately-ended stream.
}

/** Decodes a byte stream into newline-separated lines with a size bound. */
async function* streamLines(
  body: ReadableStream<Uint8Array>,
  controller: AbortController,
): AsyncIterable<string> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        return;
      }
      buffer += decoder.decode(value, { stream: true });
      if (buffer.length > MAX_SSE_LINE_BYTES) {
        throw new Error('event stream line exceeded the size bound');
      }
      let newline = buffer.indexOf('\n');
      while (newline >= 0) {
        const line = buffer.slice(0, newline);
        buffer = buffer.slice(newline + 1);
        yield line.endsWith('\r') ? line.slice(0, -1) : line;
        newline = buffer.indexOf('\n');
      }
    }
  } finally {
    controller.abort();
    reader.releaseLock();
  }
}
