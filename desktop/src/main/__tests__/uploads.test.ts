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
import { CanonicalErrorException, safeError, SafeErrorException } from '../../shared/errors';
import type { HttpResult } from '../gateway/runtimeGateway';
import { mapServerError } from '../serverClient';
import { UploadService, UPLOAD_BYTE_LIMITS, type UploadServiceDeps } from '../uploads';

function stageBody(reference: string, name: string): Record<string, unknown> {
  return {
    api_version: 'v1',
    reference,
    kind: 'image',
    name,
    size: 4,
  };
}

function makeService(
  transport: UploadServiceDeps['transport'],
  overrides: Partial<UploadServiceDeps> = {},
): { service: UploadService; deps: UploadServiceDeps } {
  const deps: UploadServiceDeps = {
    transport,
    readFile: vi.fn(() => Promise.resolve(new Uint8Array([1, 2, 3, 4]))),
    statFile: vi.fn(() => Promise.resolve({ size: 4 })),
    serverKey: () => 'server-key-1',
    ...overrides,
  };
  return { service: new UploadService(deps), deps };
}

describe('UploadService.stageFiles', () => {
  it('stages every file with one POST per file and stamps the server identity', async () => {
    const apiUpload = vi.fn((path: string): Promise<HttpResult> => {
      const name = new URLSearchParams(path.split('?')[1] ?? '').get('name') ?? 'x.png';
      return Promise.resolve({ status: 200, body: stageBody(`ref-${name}`, name) });
    });
    const { service, deps } = makeService({ apiUpload });

    const result = await service.stageFiles('image', ['/shots/a.png', '/shots/b.png']);

    expect(apiUpload).toHaveBeenCalledTimes(2);
    const [firstPath, firstBody] = apiUpload.mock.calls[0] as unknown as [string, Uint8Array];
    expect(firstPath).toBe('/api/v1/uploads?kind=image&name=a.png');
    expect([...firstBody]).toEqual([1, 2, 3, 4]);
    expect(result.results).toEqual([
      {
        ok: true,
        name: 'a.png',
        upload: {
          reference: 'ref-a.png',
          kind: 'image',
          name: 'a.png',
          size: 4,
          serverKey: 'server-key-1',
        },
      },
      {
        ok: true,
        name: 'b.png',
        upload: {
          reference: 'ref-b.png',
          kind: 'image',
          name: 'b.png',
          size: 4,
          serverKey: 'server-key-1',
        },
      },
    ]);
    expect(deps.readFile).toHaveBeenCalledWith('/shots/a.png');
  });

  it('reports per-file failures in place so a batch never fails wholesale', async () => {
    let call = 0;
    const apiUpload = vi.fn((): Promise<HttpResult> => {
      call += 1;
      if (call === 2) {
        return Promise.reject(new SafeErrorException(safeError('E_NOT_CONNECTED', 'down')));
      }
      return Promise.resolve({ status: 200, body: stageBody('ref-a', 'a.png') });
    });
    const { service } = makeService({ apiUpload });

    const result = await service.stageFiles('image', ['/shots/a.png', '/shots/b.png']);

    expect(result.results).toHaveLength(2);
    expect(result.results[0]).toMatchObject({ ok: true });
    expect(result.results[1]).toMatchObject({
      ok: false,
      name: 'b.png',
      error: { code: 'E_NOT_CONNECTED' },
    });
  });

  it('maps a 413 request_too_large response to an actionable surface', async () => {
    const body = {
      api_version: 'v1',
      error: {
        code: 'request_too_large',
        class: 'blocking',
        title: 'Request too large',
        summary: 'The request body exceeds the size limit.',
        remediation: { hint: 'Choose a smaller payload and retry.' },
      },
    };
    const apiUpload = vi.fn((): Promise<HttpResult> => Promise.resolve({ status: 413, body }));
    const { service } = makeService(
      { apiUpload },
      { statFile: () => Promise.resolve({ size: UPLOAD_BYTE_LIMITS.image }) },
    );

    // The catalog-rendered rejection crosses as the canonical error with its
    // own remediation; the per-file result carries its code and title text.
    const mapped = mapServerError({ status: 413, body });
    expect(mapped).toBeInstanceOf(CanonicalErrorException);
    expect((mapped as CanonicalErrorException).canonical.code).toBe('request_too_large');
    expect((mapped as CanonicalErrorException).canonical.remediation?.hint).toBe(
      'Choose a smaller payload and retry.',
    );

    const result = await service.stageFiles('image', ['/shots/big.png']);

    expect(result.results[0]).toMatchObject({
      ok: false,
      name: 'big.png',
      error: { code: 'E_UPLOAD', message: expect.stringContaining('request_too_large') },
    });
  });

  it('refuses oversized files before any network work', async () => {
    const apiUpload = vi.fn();
    const readFile = vi.fn();
    const { service } = makeService(
      { apiUpload },
      {
        statFile: () => Promise.resolve({ size: UPLOAD_BYTE_LIMITS.attachment + 1 }),
        readFile,
      },
    );

    const result = await service.stageFiles('attachment', ['/docs/huge.zip']);

    expect(result.results[0]).toMatchObject({ ok: false, error: { code: 'request_too_large' } });
    expect(apiUpload).not.toHaveBeenCalled();
    expect(readFile).not.toHaveBeenCalled();
  });

  it('rejects non-allowlisted image extensions before any network work', async () => {
    const apiUpload = vi.fn();
    const { service } = makeService({ apiUpload });

    const result = await service.stageFiles('image', ['/shots/vector.svg']);

    expect(result.results[0]).toMatchObject({ ok: false, error: { code: 'bad_request' } });
    expect(apiUpload).not.toHaveBeenCalled();
  });

  it('rejects relative and malformed paths via the IPC-grade path schema', async () => {
    const apiUpload = vi.fn();
    const { service } = makeService({ apiUpload });

    const result = await service.stageFiles('image', ['relative/a.png', '/tmp/line\nbreak.png']);

    expect(apiUpload).not.toHaveBeenCalled();
    expect(result.results.every((entry) => !entry.ok)).toBe(true);
  });

  it('fails when no server identity is available (never stages anonymously)', async () => {
    const apiUpload = vi.fn(() =>
      Promise.resolve({ status: 200, body: stageBody('ref-a', 'a.png') }),
    );
    const { service } = makeService({ apiUpload }, { serverKey: () => null });

    const result = await service.stageFiles('image', ['/shots/a.png']);

    expect(result.results[0]).toMatchObject({ ok: false, error: { code: 'E_NOT_CONNECTED' } });
  });

  it('rejects malformed stage responses via the schema gate', async () => {
    const apiUpload = vi.fn((): Promise<HttpResult> =>
      Promise.resolve({ status: 200, body: { api_version: 'v1', kind: 'image' } }),
    );
    const { service } = makeService({ apiUpload });

    const result = await service.stageFiles('image', ['/shots/a.png']);

    expect(result.results[0]).toMatchObject({ ok: false, error: { code: 'E_SCHEMA_MISMATCH' } });
  });

  it('fails closed when the gateway has no upload transport wired', async () => {
    const { service } = makeService({});

    const result = await service.stageFiles('image', ['/shots/a.png']);

    expect(result.results[0]).toMatchObject({
      ok: false,
      error: { code: 'E_UPLOAD_UNAVAILABLE' },
    });
  });
});
