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

import { describe, expect, it, vi } from 'vitest';
import { CanonicalErrorException } from '../../shared/errors';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';
import { InitializeService } from '../initializeService';
import type { ServerIdentity, ServerIdentitySource } from '../cloneService';

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function initializeBody(
  result: 'initialized' | 'already_initialized',
  overrides: Record<string, unknown> = {},
): Record<string, unknown> {
  return {
    api_version: 'v1',
    result,
    repository: {
      repo_key: 'unborn',
      path: '/work/space/unborn',
      has_head: true,
      root: '/work/space',
      identity: {
        path: '/work/space/unborn',
        common_dir: '/work/space/unborn/.git',
        device: '16777234',
        inode: '4242',
      },
      ...overrides,
    },
  };
}

function makeService(
  respond: (path: string, init?: ApiRequestInit) => HttpResult,
  identity?: ServerIdentitySource,
): { service: InitializeService; calls: Call[] } {
  const calls: Call[] = [];
  const service = new InitializeService({
    transport: {
      apiRequest: (path, init) => {
        calls.push(init === undefined ? { path } : { path, init });
        return Promise.resolve(respond(path, init));
      },
    },
    identity,
  });
  return { service, calls };
}

const stableIdentity: ServerIdentitySource = () => ({
  serverKey: 'server-a',
  generation: 7,
});

const identity = {
  path: '/work/space/unborn',
  commonDir: '/work/space/unborn/.git',
  device: '16777234',
  inode: '4242',
};

const validRequest = {
  repoKey: 'unborn',
  identity,
  consent: true,
} as const;

describe('InitializeService.initializeRepository', () => {
  it('posts the consented selector and maps the authoritative result', async () => {
    const { service, calls } = makeService(
      () => ({ status: 200, body: initializeBody('initialized') }),
      stableIdentity,
    );
    const result = await service.initializeRepository({ ...validRequest });
    expect(calls).toEqual([
      {
        path: '/api/v1/workspace/repositories/initialize',
        init: {
          method: 'POST',
          body: {
            repo_key: 'unborn',
            identity: {
              path: '/work/space/unborn',
              common_dir: '/work/space/unborn/.git',
              device: '16777234',
              inode: '4242',
            },
            consent: true,
          },
        },
      },
    ]);
    expect(result.result).toBe('initialized');
    expect(result.repoKey).toBe('unborn');
    expect(result.path).toBe('/work/space/unborn');
    expect(result.hasHead).toBe(true);
    expect(result.root).toBe('/work/space');
    expect(result.identity).toEqual(identity);
  });

  it('sends the optional path comparison when the request carries one', async () => {
    const { service, calls } = makeService(
      () => ({ status: 200, body: initializeBody('already_initialized') }),
      stableIdentity,
    );
    const result = await service.initializeRepository({
      ...validRequest,
      path: '/work/space/unborn',
    });
    expect(calls[0]?.init?.body).toMatchObject({ path: '/work/space/unborn' });
    // A refresh-only result is a success, never an error.
    expect(result.result).toBe('already_initialized');
    expect(result.hasHead).toBe(true);
  });

  it('rejects a request without consent before any transport call — defense in depth', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: initializeBody('initialized'),
    }));
    await expect(
      service.initializeRepository({ ...validRequest, consent: false as unknown as true }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    expect(calls).toHaveLength(0);
  });

  it('rejects malformed input before any transport call', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: initializeBody('initialized'),
    }));
    await expect(
      service.initializeRepository({ ...validRequest, repoKey: '' }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    await expect(
      service.initializeRepository({ ...validRequest, identity: { ...identity, inode: 'abc' } }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    expect(calls).toHaveLength(0);
  });

  it('fails closed when the response does not match the contract', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: { result: 'initialized' },
    }));
    await expect(service.initializeRepository({ ...validRequest })).rejects.toBeInstanceOf(
      CanonicalErrorException,
    );
    const { service: wordService } = makeService(() => ({
      status: 200,
      body: initializeBody('initialized', { has_head: false }),
    }));
    await expect(wordService.initializeRepository({ ...validRequest })).rejects.toMatchObject({
      canonical: { code: 'E_BAD_API_RESPONSE' },
    });
  });

  it('discards the result when the server switches mid-flight', async () => {
    const identitySource = vi.fn<() => ServerIdentity>(() => ({
      serverKey: 'server-a',
      generation: 7,
    }));
    const { service, calls } = makeService((path) => {
      if (path === '/api/v1/workspace/repositories/initialize') {
        identitySource.mockImplementation(() => ({ serverKey: 'server-b', generation: 9 }));
      }
      return { status: 200, body: initializeBody('initialized') };
    }, identitySource);
    await expect(service.initializeRepository({ ...validRequest })).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
    // The mutation did land on its original server; only the stale result
    // is discarded so it can never update the new server's view.
    expect(calls).toHaveLength(1);
  });

  it('discards the result when the server disconnects mid-flight', async () => {
    const identitySource = vi.fn<() => ServerIdentity>(() => ({
      serverKey: 'server-a',
      generation: 7,
    }));
    const { service } = makeService((path) => {
      if (path === '/api/v1/workspace/repositories/initialize') {
        identitySource.mockImplementation(() => ({ serverKey: null, generation: 8 }));
      }
      return { status: 200, body: initializeBody('initialized') };
    }, identitySource);

    await expect(service.initializeRepository({ ...validRequest })).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });
});
