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

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { fetchJson, MAX_PROBE_RESPONSE_BYTES, selectRuntime } from '../gateway/wiring';
import { MAX_PAYLOAD_BYTES } from '../../shared/sanitize';

/** Installs a fetch that never answers until the caller's signal aborts. */
function withStalledFetch(run: () => Promise<void>): Promise<void> {
  const original = globalThis.fetch;
  globalThis.fetch = ((_url: string, init: { signal: AbortSignal }) =>
    new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => {
        const abort = new Error('This operation was aborted');
        abort.name = 'AbortError';
        reject(abort);
      });
    })) as unknown as typeof fetch;
  return run().finally(() => {
    globalThis.fetch = original;
  });
}

describe('fetchJson', () => {
  it('raises the typed timeout error when a request outruns its bound', async () => {
    await withStalledFetch(async () => {
      await expect(
        fetchJson('http://127.0.0.1:9/api/v1/features/f/actions/publish', {
          timeoutMs: 1,
          method: 'POST',
          body: {},
        }),
      ).rejects.toMatchObject({ canonical: { code: 'E_REQUEST_TIMEOUT' } });
    });
  });

  it('gates responses on the boundary cap, not the tighter probe bound', async () => {
    const original = globalThis.fetch;
    const body = JSON.stringify({ pad: 'x'.repeat(MAX_PROBE_RESPONSE_BYTES) });
    globalThis.fetch = (() =>
      Promise.resolve({
        status: 200,
        text: () => Promise.resolve(body),
      })) as unknown as typeof fetch;
    try {
      // Larger than the probe bound but well inside MAX_PAYLOAD_BYTES.
      await expect(
        fetchJson('http://127.0.0.1:9/api/v1/features', { timeoutMs: 1000 }),
      ).resolves.toMatchObject({ status: 200 });
      // A caller that opts into the probe bound still gets the tighter gate.
      await expect(
        fetchJson('http://127.0.0.1:9/api/v1/health', {
          timeoutMs: 1000,
          maxResponseBytes: MAX_PROBE_RESPONSE_BYTES,
        }),
      ).rejects.toMatchObject({ canonical: { code: 'E_PAYLOAD_TOO_LARGE' } });
    } finally {
      globalThis.fetch = original;
    }
  });

  it('rejects responses past the boundary cap', async () => {
    const original = globalThis.fetch;
    const body = JSON.stringify({ pad: 'x'.repeat(MAX_PAYLOAD_BYTES) });
    globalThis.fetch = (() =>
      Promise.resolve({
        status: 200,
        text: () => Promise.resolve(body),
      })) as unknown as typeof fetch;
    try {
      await expect(
        fetchJson('http://127.0.0.1:9/api/v1/features', { timeoutMs: 1000 }),
      ).rejects.toMatchObject({ canonical: { code: 'E_PAYLOAD_TOO_LARGE' } });
    } finally {
      globalThis.fetch = original;
    }
  });

  it('leaves other transport failures unmapped', async () => {
    const original = globalThis.fetch;
    globalThis.fetch = (() =>
      Promise.reject(new Error('connect ECONNREFUSED'))) as unknown as typeof fetch;
    try {
      await expect(
        fetchJson('http://127.0.0.1:9/api/v1/health', { timeoutMs: 1000 }),
      ).rejects.toThrow('connect ECONNREFUSED');
    } finally {
      globalThis.fetch = original;
    }
  });
});

describe('selectRuntime', () => {
  it('uses the current runtime parent when only the legacy parent exists', () => {
    const homeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-runtime-'));
    fs.mkdirSync(path.join(homeDir, '.agentic-workflow'));

    try {
      expect(selectRuntime(null, homeDir)).toStrictEqual({
        runtimeDir: path.join(homeDir, '.agentic-orchestrator'),
        stateDir: path.join(homeDir, '.agentic-orchestrator', 'features'),
        configPath: path.join(homeDir, '.agentic-orchestrator', 'config.yaml'),
      });
    } finally {
      fs.rmSync(homeDir, { recursive: true, force: true });
    }
  });

  it('preserves an explicit legacy-named runtime selection', () => {
    const homeDir = path.join(path.sep, 'home', 'agentico-user');

    expect(selectRuntime('~/.agentic-workflow', homeDir)).toStrictEqual({
      runtimeDir: path.join(homeDir, '.agentic-workflow'),
      stateDir: path.join(homeDir, '.agentic-workflow', 'features'),
      configPath: path.join(homeDir, '.agentic-workflow', 'config.yaml'),
    });
  });
});
