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
import { createHash } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  RemoteTokenStore,
  type RemoteTokenStoreDeps,
  type SafeStorageLike,
} from '../gateway/remoteTokenStore';

const KEY_A = createHash('sha256').update('server-a').digest('hex').slice(0, 32);
const KEY_B = createHash('sha256').update('server-b').digest('hex').slice(0, 32);
const TOKEN = 'tok_secret_value_0123456789';

/** Deterministic fake safeStorage: XOR-free reversible transform. */
class FakeSafeStorage implements SafeStorageLike {
  available = true;
  throws = false;
  /** Set of buffer bytes decrypted by this instance, simulating OS keyring identity. */
  private readonly decrypted = new Set<string>();

  isEncryptionAvailable(): boolean {
    return this.available;
  }

  encryptString(plain: string): Buffer {
    if (this.throws) {
      throw new Error('encrypt failed');
    }
    const blob = Buffer.from(`enc::${plain}`, 'utf8');
    this.decrypted.add(blob.toString('base64'));
    return blob;
  }

  decryptString(encrypted: Buffer): string {
    if (this.throws || !this.decrypted.has(encrypted.toString('base64'))) {
      throw new Error('decrypt failed');
    }
    const raw = encrypted.toString('utf8');
    if (!raw.startsWith('enc::')) {
      throw new Error('decrypt failed');
    }
    return raw.slice('enc::'.length);
  }
}

let dir: string;
let file: string;
let safeStorage: FakeSafeStorage;
let registerSecret: ReturnType<typeof vi.fn<(secret: string) => void>>;
let deps: RemoteTokenStoreDeps;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-remote-tokens-'));
  file = path.join(dir, 'remote-server-tokens.json');
  safeStorage = new FakeSafeStorage();
  registerSecret = vi.fn<(secret: string) => void>();
  deps = { safeStorage, registerSecret };
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('RemoteTokenStore', () => {
  it('round-trips a saved token through a fresh store instance', () => {
    const first = new RemoteTokenStore(file, deps);
    expect(first.save(KEY_A, TOKEN)).toEqual({ status: 'saved' });

    const second = new RemoteTokenStore(file, deps);
    expect(second.load(KEY_A)).toEqual({ status: 'ok', token: TOKEN });
    expect(registerSecret).toHaveBeenCalledTimes(2);
    expect(registerSecret).toHaveBeenNthCalledWith(1, TOKEN);
    expect(registerSecret).toHaveBeenNthCalledWith(2, TOKEN);
  });

  it('persists only ciphertext: no plaintext token appears in the on-disk bytes', () => {
    const store = new RemoteTokenStore(file, deps);
    store.save(KEY_A, TOKEN);
    const raw = fs.readFileSync(file, 'utf8');
    expect(raw).not.toContain(TOKEN);
    const doc = JSON.parse(raw) as { version: number; tokens: Record<string, string> };
    expect(doc.version).toBe(1);
    expect(Object.keys(doc.tokens)).toEqual([KEY_A]);
  });

  it('returns unavailable on save when safeStorage is unavailable, and writes no file', () => {
    safeStorage.available = false;
    const store = new RemoteTokenStore(file, deps);
    expect(store.save(KEY_A, TOKEN)).toEqual({ status: 'unavailable' });
    expect(fs.existsSync(file)).toBe(false);
    // The token was still registered for session-only redaction on receipt.
    expect(registerSecret).toHaveBeenCalledWith(TOKEN);
  });

  it('returns absent when no blob exists for the server key', () => {
    const store = new RemoteTokenStore(file, deps);
    expect(store.load(KEY_A)).toEqual({ status: 'absent' });
    expect(registerSecret).not.toHaveBeenCalled();
  });

  it('returns re-paste-required on a corrupted blob without deleting it, and recovers on save', () => {
    const store = new RemoteTokenStore(file, deps);
    store.save(KEY_A, TOKEN);
    const before = fs.readFileSync(file, 'utf8');

    // Tamper the ciphertext: the fake decryptString rejects unknown blobs.
    const doc = JSON.parse(before) as { tokens: Record<string, string> };
    const blob = doc.tokens[KEY_A] ?? '';
    doc.tokens[KEY_A] = blob.slice(0, -1) + (blob.endsWith('A') ? 'B' : 'A');
    fs.writeFileSync(file, `${JSON.stringify(doc, null, 2)}\n`, 'utf8');
    const tampered = fs.readFileSync(file, 'utf8');

    expect(store.load(KEY_A)).toEqual({ status: 're-paste-required' });
    // The blob and file bytes are untouched by the failed load.
    expect(fs.readFileSync(file, 'utf8')).toBe(tampered);
    expect(tampered).not.toBe(before);
    const after = JSON.parse(tampered) as { tokens: Record<string, string> };
    expect(after.tokens[KEY_A]).toBe(doc.tokens[KEY_A]);

    // A later successful save overwrites the bad blob.
    expect(store.save(KEY_A, TOKEN)).toEqual({ status: 'saved' });
    expect(new RemoteTokenStore(file, deps).load(KEY_A)).toEqual({
      status: 'ok',
      token: TOKEN,
    });
  });

  it('returns re-paste-required when the stored file is not parseable', () => {
    fs.writeFileSync(file, 'not json {{{', 'utf8');
    const store = new RemoteTokenStore(file, deps);
    expect(store.load(KEY_A)).toEqual({ status: 're-paste-required' });
    expect(fs.readFileSync(file, 'utf8')).toBe('not json {{{');
  });

  it('returns re-paste-required when safeStorage is unavailable at load time', () => {
    const store = new RemoteTokenStore(file, deps);
    store.save(KEY_A, TOKEN);
    safeStorage.available = false;
    expect(new RemoteTokenStore(file, deps).load(KEY_A)).toEqual({
      status: 're-paste-required',
    });
  });

  it('removes exactly the requested blob and no-ops on absent keys', () => {
    const store = new RemoteTokenStore(file, deps);
    store.save(KEY_A, TOKEN);
    store.save(KEY_B, 'other-token');
    store.remove(KEY_A);
    const doc = JSON.parse(fs.readFileSync(file, 'utf8')) as {
      tokens: Record<string, string>;
    };
    expect(Object.keys(doc.tokens)).toEqual([KEY_B]);
    expect(store.load(KEY_A)).toEqual({ status: 'absent' });
    expect(store.load(KEY_B)).toEqual({ status: 'ok', token: 'other-token' });
    // Removing an absent key is a no-op.
    store.remove(KEY_A);
    expect(Object.keys(doc.tokens)).toEqual([KEY_B]);
  });

  it('registers every handled token with the secret registry before use', () => {
    const store = new RemoteTokenStore(file, deps);
    store.save(KEY_A, TOKEN);
    expect(registerSecret).toHaveBeenCalledTimes(1);
    expect(registerSecret).toHaveBeenNthCalledWith(1, TOKEN);
    expect(store.load(KEY_A)).toEqual({ status: 'ok', token: TOKEN });
    expect(registerSecret).toHaveBeenCalledTimes(2);
  });

  it('returns unavailable on save when encryption itself throws', () => {
    safeStorage.throws = true;
    const store = new RemoteTokenStore(file, deps);
    expect(store.save(KEY_A, TOKEN)).toEqual({ status: 'unavailable' });
    expect(fs.existsSync(file)).toBe(false);
  });
});
