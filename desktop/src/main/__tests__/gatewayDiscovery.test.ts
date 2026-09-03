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

import { describe, expect, it } from 'vitest';
import {
  DISCOVERY_FILENAME,
  discoveryPath,
  evaluateDiscoveryFile,
  isLoopbackHttpUrl,
  type DiscoveryDeps,
} from '../gateway/discovery';

const SELECTED_STATE_DIR = '/home/user/.agentic-orchestrator/features';

function record(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1,
    api_version: 'v1',
    base_url: 'http://127.0.0.1:49152',
    auth_token: 'tok-abc123',
    runtime: {
      runtime_dir: '/home/user/.agentic-orchestrator',
      state_dir: SELECTED_STATE_DIR,
      config_path: '/home/user/.agentic-orchestrator/config.yaml',
    },
    launch_policy: { resolved: true, providers: ['claude'], dangerously_skip_permissions: false },
    start_mode: 'server',
    pid: 4242,
    started_at: '2026-07-14T00:00:00Z',
    published_at: '2026-07-14T00:00:00Z',
    owner: { pid: 4242, started_at: '2026-07-14T00:00:00Z' },
    ...overrides,
  };
}

function deps(
  overrides: Partial<DiscoveryDeps> = {},
  content = JSON.stringify(record()),
): DiscoveryDeps {
  return {
    readFile: () => content,
    statFile: () => ({ mode: 0o100600, uid: 501 }),
    euid: 501,
    isProcessAlive: () => true,
    ...overrides,
  };
}

describe('discoveryPath', () => {
  it('joins the runtime dir with the discovery filename', () => {
    expect(discoveryPath('/x/runtime')).toBe(`/x/runtime/${DISCOVERY_FILENAME}`);
    expect(DISCOVERY_FILENAME).toBe('.agentico-server.json');
  });
});

describe('isLoopbackHttpUrl', () => {
  it.each([
    'http://127.0.0.1:8080',
    'http://127.9.8.7:1',
    'http://localhost:9999',
    'http://[::1]:8080',
  ])('accepts loopback %s', (url) => {
    expect(isLoopbackHttpUrl(url)).toBe(true);
  });

  it.each([
    'http://192.168.1.4:8080',
    'http://10.0.0.1:8080',
    'http://example.com:8080',
    'https://127.0.0.1:8080', // https implies a non-standard listener; only plain loopback http is valid
    'http://0.0.0.0:8080',
    'http://[::]:8080',
    'http://localhost.evil.com:8080',
    'ftp://127.0.0.1:21',
    'not a url',
    '',
  ])('rejects %s', (url) => {
    expect(isLoopbackHttpUrl(url)).toBe(false);
  });
});

describe('evaluateDiscoveryFile', () => {
  it('returns absent when there is no discovery file', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({ statFile: () => null }),
    );
    expect(outcome).toEqual({ kind: 'absent' });
  });

  it('accepts an owner-only, loopback, live-pid record for the selected runtime', () => {
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps());
    expect(outcome.kind).toBe('candidate');
    if (outcome.kind === 'candidate') {
      expect(outcome.record.base_url).toBe('http://127.0.0.1:49152');
      expect(outcome.record.pid).toBe(4242);
    }
  });

  it('tolerates the optional bounded server name published by newer servers', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({}, JSON.stringify(record({ name: 'frothy-macchiato' }))),
    );
    expect(outcome.kind).toBe('candidate');
    if (outcome.kind === 'candidate') {
      expect(outcome.record.name).toBe('frothy-macchiato');
    }
  });

  it('rejects group- or world-readable discovery files', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({ statFile: () => ({ mode: 0o100644, uid: 501 }) }),
    );
    expect(outcome).toMatchObject({
      kind: 'rejected',
      reason: expect.stringContaining('permissions'),
    });
  });

  it('rejects discovery files owned by another user', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({ statFile: () => ({ mode: 0o100600, uid: 0 }) }),
    );
    expect(outcome).toMatchObject({ kind: 'rejected', reason: expect.stringContaining('owner') });
  });

  it('rejects malformed JSON without echoing the contents', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({ readFile: () => '{not json, token: super-secret' }),
    );
    expect(outcome.kind).toBe('rejected');
    if (outcome.kind === 'rejected') {
      expect(outcome.reason).not.toContain('super-secret');
    }
  });

  it('rejects prototype-polluting payloads', () => {
    const content = '{"schema_version": 1, "__proto__": {"polluted": true}}';
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps({}, content));
    expect(outcome.kind).toBe('rejected');
  });

  it('rejects records missing required fields', () => {
    const content = JSON.stringify({ schema_version: 1, api_version: 'v1' });
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps({}, content));
    expect(outcome.kind).toBe('rejected');
  });

  it('rejects non-loopback base URLs', () => {
    const content = JSON.stringify(record({ base_url: 'http://192.168.7.7:8080' }));
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps({}, content));
    expect(outcome).toMatchObject({
      kind: 'rejected',
      reason: expect.stringContaining('loopback'),
    });
  });

  it('treats a record for a different runtime as not a match', () => {
    const content = JSON.stringify(
      record({
        runtime: {
          runtime_dir: '/elsewhere',
          state_dir: '/elsewhere/features',
          config_path: '/elsewhere/config.yaml',
        },
      }),
    );
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps({}, content));
    expect(outcome).toMatchObject({ kind: 'rejected', reason: expect.stringContaining('runtime') });
  });

  it('treats a dead pid as stale so launch can proceed', () => {
    const outcome = evaluateDiscoveryFile(
      '/rt',
      SELECTED_STATE_DIR,
      deps({ isProcessAlive: () => false }),
    );
    expect(outcome).toMatchObject({ kind: 'stale' });
  });

  it('never places token material in the rejection reason', () => {
    const content = JSON.stringify(record({ base_url: 'http://evil.example.com:1' }));
    const outcome = evaluateDiscoveryFile('/rt', SELECTED_STATE_DIR, deps({}, content));
    expect(JSON.stringify(outcome)).not.toContain('tok-abc123');
  });
});
