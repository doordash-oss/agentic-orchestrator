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
 * Security posture of the remote upload channel: it is the one IPC route
 * where local file bytes leave the machine, so its boundary must prove the
 * request is bounded, the response is schema-constrained, and the envelope
 * still rejects foreign senders.
 */
import { describe, expect, it, vi } from 'vitest';
import { registerIpcHandlers, type IpcServices } from '../../src/main/ipcHandlers';
import type { TrustedSender } from '../../src/main/security';
import { IPC_CHANNELS } from '../../src/shared/ipc';

const trusted: TrustedSender = {
  webContentsIds: new Set([1]),
  allowedOrigins: new Set(['file://']),
};

const goodEvent = {
  sender: { id: 1 },
  senderFrame: { url: 'file:///app/out/renderer/index.html' },
};
const foreignEvent = {
  sender: { id: 99 },
  senderFrame: { url: 'https://evil.example.com/' },
};

function register(services: Partial<IpcServices>) {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<unknown>>();
  const ipcMain = {
    handle: vi.fn(
      (channel: string, listener: (event: unknown, ...args: unknown[]) => Promise<unknown>) => {
        handlers.set(channel, listener);
      },
    ),
  };
  registerIpcHandlers(ipcMain, trusted, services as IpcServices);
  return handlers;
}

describe('creationUploadFiles channel', () => {
  it('stages files through the service with validated arguments', async () => {
    const uploadCreationFiles: IpcServices['uploadCreationFiles'] = vi.fn(() =>
      Promise.resolve({
        results: [
          {
            ok: true as const,
            name: 'a.png',
            upload: {
              reference: 'ref-a',
              kind: 'image' as const,
              name: 'a.png',
              size: 4,
              serverKey: 'server-key-1',
            },
          },
        ],
      }),
    );
    const handlers = register({ uploadCreationFiles });

    const result = (await handlers.get(IPC_CHANNELS.creationUploadFiles)!(goodEvent, 'image', [
      '/shots/a.png',
    ])) as { ok: boolean; value?: unknown };

    expect(result.ok).toBe(true);
    expect(uploadCreationFiles).toHaveBeenCalledWith('image', ['/shots/a.png']);
  });

  it('fails closed on out-of-contract arguments before touching the service', async () => {
    const uploadCreationFiles = vi.fn(() => Promise.resolve({ results: [] }));
    const handlers = register({ uploadCreationFiles });
    const channel = handlers.get(IPC_CHANNELS.creationUploadFiles)!;

    const cases: unknown[][] = [
      ['document', ['/shots/a.png']], // unknown kind
      ['image', ['relative/a.png']], // non-absolute path
      ['image', ['/tmp/has\nnewline.png']], // control characters
      ['image', Array.from({ length: 25 }, (_, index) => `/f/${index}.png`)], // beyond bound
      ['image'], // missing paths tuple element
      [{ kind: 'image' }, ['/shots/a.png']], // object kinds rejected
    ];
    for (const args of cases) {
      const result = (await channel(goodEvent, ...args)) as {
        ok: boolean;
        error?: { code: string };
      };
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(uploadCreationFiles).not.toHaveBeenCalled();
  });

  it('fails closed when the service returns an out-of-contract response', async () => {
    const uploadCreationFiles: IpcServices['uploadCreationFiles'] = vi.fn(() =>
      Promise.resolve({ results: [{ ok: true } as never] }),
    );
    const handlers = register({ uploadCreationFiles });

    const result = (await handlers.get(IPC_CHANNELS.creationUploadFiles)!(goodEvent, 'image', [
      '/shots/a.png',
    ])) as { ok: boolean; error?: { code: string } };

    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
  });

  it('never stages a file for a foreign sender', async () => {
    const uploadCreationFiles = vi.fn(() => Promise.resolve({ results: [] }));
    const handlers = register({ uploadCreationFiles });

    const result = (await handlers.get(IPC_CHANNELS.creationUploadFiles)!(foreignEvent, 'image', [
      '/shots/a.png',
    ])) as { ok: boolean; error?: { code: string } };

    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_UNTRUSTED_SENDER');
    expect(uploadCreationFiles).not.toHaveBeenCalled();
  });
});
