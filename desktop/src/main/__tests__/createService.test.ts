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
import { CreateService } from '../createService';
import type { ServerIdentity, ServerIdentitySource } from '../cloneService';

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function createBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    result: 'created',
    repository: {
      repo_key: 'created',
      path: '/work/space/created',
      has_head: true,
      root: '/work/space',
      identity: {
        path: '/work/space/created',
        common_dir: '/work/space/created/.git',
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
): { service: CreateService; calls: Call[] } {
  const calls: Call[] = [];
  const service = new CreateService({
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

const validRequest = {
  rootPath: '/work/space',
  destination: 'created',
  idempotencyKey: 'key-1-abcdefgh',
  consent: true,
} as const;

describe('CreateService.createRepository', () => {
  it('posts the consented request and maps the authoritative result', async () => {
    const { service, calls } = makeService(
      () => ({ status: 201, body: createBody() }),
      stableIdentity,
    );
    const result = await service.createRepository({ ...validRequest });
    expect(calls).toEqual([
      {
        path: '/api/v1/workspace/repositories/create',
        init: {
          method: 'POST',
          body: {
            root_path: '/work/space',
            destination: 'created',
            idempotency_key: 'key-1-abcdefgh',
            consent: true,
          },
        },
      },
    ]);
    expect(result.repoKey).toBe('created');
    expect(result.path).toBe('/work/space/created');
    expect(result.hasHead).toBe(true);
    expect(result.root).toBe('/work/space');
    expect(result.identity).toEqual({
      path: '/work/space/created',
      commonDir: '/work/space/created/.git',
      device: '16777234',
      inode: '4242',
    });
  });

  it('maps a result whose identity could not be proven without guessing', async () => {
    const { service } = makeService(() => ({
      status: 201,
      body: createBody({ identity: undefined }),
    }));
    const result = await service.createRepository({ ...validRequest });
    expect(result.identity).toBeUndefined();
    expect(result.repoKey).toBe('created');
  });

  it('rejects a request without consent before any transport call — defense in depth', async () => {
    const { service, calls } = makeService(() => ({ status: 201, body: createBody() }));
    // The IPC schema makes `consent: true` the only accepted literal; the
    // service rechecks so a bypassed renderer still cannot create.
    await expect(
      service.createRepository({ ...validRequest, consent: false as unknown as true }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    expect(calls).toHaveLength(0);
  });

  it('rejects malformed input before any transport call', async () => {
    const { service, calls } = makeService(() => ({ status: 201, body: createBody() }));
    await expect(
      service.createRepository({ ...validRequest, destination: 'x'.repeat(129) }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    await expect(
      service.createRepository({ ...validRequest, rootPath: '' }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    await expect(
      service.createRepository({ ...validRequest, idempotencyKey: 'short' }),
    ).rejects.toBeInstanceOf(CanonicalErrorException);
    expect(calls).toHaveLength(0);
  });

  it('fails closed when the response does not match the contract', async () => {
    const { service } = makeService(() => ({ status: 201, body: { result: 'created' } }));
    await expect(service.createRepository({ ...validRequest })).rejects.toBeInstanceOf(
      CanonicalErrorException,
    );
  });

  it('fails closed when the result word is not the creation result', async () => {
    const { service } = makeService(() => ({
      status: 201,
      body: { ...createBody(), result: 'something-else' },
    }));
    await expect(service.createRepository({ ...validRequest })).rejects.toMatchObject({
      canonical: { code: 'E_BAD_API_RESPONSE' },
    });
  });

  it('discards the result when the server switches mid-flight', async () => {
    const identity = vi.fn<() => ServerIdentity>(() => ({ serverKey: 'server-a', generation: 7 }));
    const { service, calls } = makeService((path) => {
      if (path === '/api/v1/workspace/repositories/create') {
        identity.mockImplementation(() => ({ serverKey: 'server-b', generation: 9 }));
      }
      return { status: 201, body: createBody() };
    }, identity);
    await expect(service.createRepository({ ...validRequest })).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
    // The creation did land on its original server; only the stale result
    // is discarded so it can never update the new server's view.
    expect(calls).toHaveLength(1);
  });
});
