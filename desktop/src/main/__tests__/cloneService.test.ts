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
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';
import { CloneService, type ServerIdentitySource } from '../cloneService';
import { CanonicalErrorException } from '../../shared/errors';

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function baseOperation(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 'clone-0123456789abcdef',
    state: 'accepted',
    stage: 'preparing',
    remote_url: 'https://example.com/acme/widget.git',
    root_path: '/work/space',
    destination: 'widget',
    destination_path: '/work/space/widget',
    idempotency_key: 'key-1-abcdefgh',
    cancel_requested: false,
    created_at: '2026-09-04T12:00:00Z',
    updated_at: '2026-09-04T12:00:00Z',
    ...overrides,
  };
}

function cloneActionBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    result: 'accepted',
    operation: baseOperation(overrides),
  };
}

function makeService(
  respond: (path: string, init?: ApiRequestInit) => HttpResult,
  identity?: ServerIdentitySource,
): { service: CloneService; calls: Call[] } {
  const calls: Call[] = [];
  const service = new CloneService({
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

describe('CloneService.startClone', () => {
  it('posts the validated request and maps the accepted snapshot', async () => {
    const { service, calls } = makeService(
      () => ({ status: 202, body: cloneActionBody() }),
      stableIdentity,
    );
    const operation = await service.startClone({
      remoteUrl: 'https://example.com/acme/widget.git',
      rootPath: '/work/space',
      destination: 'widget',
      idempotencyKey: 'key-1-abcdefgh',
    });
    expect(calls).toEqual([
      {
        path: '/api/v1/workspace/repositories/clone',
        init: {
          method: 'POST',
          body: {
            remote_url: 'https://example.com/acme/widget.git',
            root_path: '/work/space',
            destination: 'widget',
            idempotency_key: 'key-1-abcdefgh',
          },
        },
      },
    ]);
    expect(operation.id).toBe('clone-0123456789abcdef');
    expect(operation.state).toBe('accepted');
    expect(operation.remoteUrl).toBe('https://example.com/acme/widget.git');
    expect(operation.destinationPath).toBe('/work/space/widget');
  });

  it('rejects oversized or malformed input before any transport call', async () => {
    const { service, calls } = makeService(() => ({ status: 202, body: cloneActionBody() }));
    await expect(
      service.startClone({
        remoteUrl: '',
        rootPath: '/work/space',
        destination: 'widget',
        idempotencyKey: 'key-1-abcdefgh',
      }),
    ).rejects.toMatchObject({ canonical: { code: 'E_SCHEMA_MISMATCH' } });
    await expect(
      service.startClone({
        remoteUrl: 'https://example.com/acme/widget.git',
        rootPath: '/work/space',
        destination: 'widget',
        idempotencyKey: 'short',
      }),
    ).rejects.toMatchObject({ canonical: { code: 'E_SCHEMA_MISMATCH' } });
    expect(calls).toHaveLength(0);
  });

  it('passes canonical server rejections through unchanged', async () => {
    const { service } = makeService(
      () => ({
        status: 409,
        body: {
          api_version: 'v1',
          error: {
            code: 'clone_destination_reserved',
            class: 'blocking',
            title: 'Destination reserved',
            summary: 'Another clone operation currently holds this destination.',
          },
        },
      }),
      stableIdentity,
    );
    await expect(
      service.startClone({
        remoteUrl: 'https://example.com/acme/widget.git',
        rootPath: '/work/space',
        destination: 'widget',
        idempotencyKey: 'key-1-abcdefgh',
      }),
    ).rejects.toMatchObject({ canonical: { code: 'clone_destination_reserved' } });
  });
});

describe('CloneService request fencing', () => {
  it('discards responses that raced a server switch', async () => {
    let identity: { serverKey: string; generation: number } = {
      serverKey: 'server-a',
      generation: 7,
    };
    let responded = 0;
    const { service } = makeService(
      () => {
        responded += 1;
        if (responded >= 1) {
          identity = { serverKey: 'server-b', generation: 8 };
        }
        return { status: 200, body: cloneActionBody() };
      },
      () => identity,
    );
    await expect(service.listCloneOperations()).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });

  it('discards responses that raced a same-server reconnect generation bump', async () => {
    let identity: { serverKey: string; generation: number } = {
      serverKey: 'server-a',
      generation: 7,
    };
    let responded = 0;
    const { service } = makeService(
      () => {
        responded += 1;
        if (responded >= 1) {
          identity = { serverKey: 'server-a', generation: 9 };
        }
        return { status: 200, body: cloneActionBody() };
      },
      () => identity,
    );
    await expect(service.getCloneOperation('clone-0123456789abcdef')).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });

  it('accepts responses when the identity is unchanged', async () => {
    const { service } = makeService(
      () => ({ status: 200, body: cloneActionBody() }),
      stableIdentity,
    );
    const operation = await service.cancelCloneOperation('clone-0123456789abcdef');
    expect(operation.id).toBe('clone-0123456789abcdef');
  });

  it('treats an unknown identity source as unfenced', async () => {
    const { service } = makeService(() => ({ status: 200, body: cloneActionBody() }));
    const operation = await service.retryCloneOperation('clone-0123456789abcdef');
    expect(operation.id).toBe('clone-0123456789abcdef');
  });
});

describe('CloneService operation routes', () => {
  it('GETs one operation, lists with the bound, and posts actions', async () => {
    const { service, calls } = makeService((path) => {
      if (path.includes('?limit=')) {
        return {
          status: 200,
          body: {
            api_version: 'v1',
            operations: [cloneActionBody().operation],
            next_page_token: '2|1|clone-fedcba',
          },
        };
      }
      if (path.endsWith('/clone-0123456789abcdef')) {
        return { status: 200, body: { api_version: 'v1', operation: cloneActionBody().operation } };
      }
      return {
        status: 200,
        body: cloneActionBody({ state: 'cancelling', cancel_requested: true }),
      };
    }, stableIdentity);

    const snapshot = await service.getCloneOperation('clone-0123456789abcdef');
    expect(snapshot.state).toBe('accepted');

    const list = await service.listCloneOperations();
    expect(list.operations).toHaveLength(1);
    expect(list.nextPageToken).toBe('2|1|clone-fedcba');

    const cancelling = await service.cancelCloneOperation('clone-0123456789abcdef');
    expect(cancelling.state).toBe('cancelling');
    expect(cancelling.cancelRequested).toBe(true);

    await service.retryCloneCleanup('clone-0123456789abcdef');
    await service.retryCloneOperation('clone-0123456789abcdef');

    expect(calls.map((call) => `${call.init?.method ?? 'GET'} ${call.path}`)).toEqual([
      'GET /api/v1/workspace/repositories/clone/clone-0123456789abcdef',
      'GET /api/v1/workspace/repositories/clone?limit=200',
      'POST /api/v1/workspace/repositories/clone/clone-0123456789abcdef/cancel',
      'POST /api/v1/workspace/repositories/clone/clone-0123456789abcdef/cleanup',
      'POST /api/v1/workspace/repositories/clone/clone-0123456789abcdef/retry',
    ]);
  });

  it('rejects malformed operation identifiers without a transport call', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: cloneActionBody() }));
    for (const bad of ['', 'UPPER', '../escape', 'clone-' + 'x'.repeat(100), 'a b']) {
      await expect(service.getCloneOperation(bad)).rejects.toMatchObject({
        canonical: { code: 'E_BAD_API_PATH' },
      });
      await expect(service.cancelCloneOperation(bad)).rejects.toMatchObject({
        canonical: { code: 'E_BAD_API_PATH' },
      });
    }
    expect(calls).toHaveLength(0);
  });

  it('fails closed on schema-mismatched operation payloads', async () => {
    const { service } = makeService(
      () => ({ status: 200, body: { api_version: 'v1', operation: { id: 'x' } } }),
      stableIdentity,
    );
    await expect(service.getCloneOperation('clone-0123456789abcdef')).rejects.toMatchObject({
      canonical: { code: 'E_SCHEMA_MISMATCH' },
    });
  });

  it('maps optional snapshot fields without echoing internal state', async () => {
    const operation = baseOperation({
      state: 'cleanup_pending',
      pending_outcome: 'failed',
      cleanup_issue: 'staging ownership could not be verified',
      cancel_requested: true,
      cancel_requested_at: '2026-09-04T12:01:00Z',
      terminal_at: '2026-09-04T12:01:00Z',
      error: {
        code: 'clone_execution_failed',
        class: 'blocking',
        title: 'Clone failed',
        summary: 'The clone command failed on the server.',
      },
      published: {
        repo_key: 'widget',
        path: '/work/space/widget',
        has_head: true,
        published_at: '2026-09-04T12:02:00Z',
      },
    });
    const { service } = makeService(
      () => ({ status: 200, body: { api_version: 'v1', operation } }),
      stableIdentity,
    );
    const snapshot = await service.getCloneOperation('clone-0123456789abcdef');
    expect(snapshot.state).toBe('cleanup_pending');
    expect(snapshot.pendingOutcome).toBe('failed');
    expect(snapshot.cleanupIssue).toBe('staging ownership could not be verified');
    expect(snapshot.cancelRequestedAt).toBe('2026-09-04T12:01:00Z');
    expect(snapshot.terminalAt).toBe('2026-09-04T12:01:00Z');
    expect(snapshot.error?.code).toBe('clone_execution_failed');
    expect(snapshot.published?.repoKey).toBe('widget');
    expect(snapshot.published?.hasHead).toBe(true);
  });
});

describe('CloneService idempotency keys', () => {
  it('generates unique, bound-length keys', () => {
    const { service } = makeService(() => ({ status: 200, body: cloneActionBody() }));
    const a = service.newIdempotencyKey();
    const b = service.newIdempotencyKey();
    expect(a).not.toEqual(b);
    expect(a).toMatch(/^[0-9a-f-]{8,36}$/);
  });
});

describe('CloneService canonical error plumbing', () => {
  it('surfaces transport failures as canonical exceptions', async () => {
    const { service } = makeService(
      () => ({ status: 500, body: { api_version: 'v1', unexpected: true } }),
      stableIdentity,
    );
    const promise = service.listCloneOperations();
    await expect(promise).rejects.toBeInstanceOf(CanonicalErrorException);
    await expect(promise).rejects.toMatchObject({ canonical: { code: 'E_HTTP_REJECTED' } });
  });
});
