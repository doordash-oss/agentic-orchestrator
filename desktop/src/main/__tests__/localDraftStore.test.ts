import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { LocalDraftStore } from '../localDraftStore';

let dir: string;
let warnings: string[];

const key = {
  runtimeId: 'runtime-state-a',
  featureId: 'feature-a',
  reviewId: 'review-a',
  baseDraftRevision: 'draft-revision-a',
};

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-local-drafts-'));
  warnings = [];
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

function makeStore() {
  return new LocalDraftStore(dir, { warn: (message) => warnings.push(message) });
}

function draftPath() {
  return path.join(dir, 'review-local-drafts.json');
}

describe('LocalDraftStore', () => {
  it('restores a saved draft after creating a fresh store', () => {
    const store = makeStore();
    const saved = store.save({ ...key, text: '# Unsaved review draft' });

    expect(saved).toMatchObject({ ...key, text: '# Unsaved review draft' });
    expect(makeStore().load(key)).toStrictEqual(saved);
  });

  it('keeps drafts for distinct runtime, resource, and base revisions separate', () => {
    const store = makeStore();
    store.save({ ...key, text: 'first' });
    store.save({ ...key, runtimeId: 'runtime-state-b', text: 'second' });
    store.save({ ...key, reviewId: 'review-b', text: 'third' });
    store.save({ ...key, baseDraftRevision: 'draft-revision-b', text: 'fourth' });

    expect(store.load(key)?.text).toBe('first');
    expect(store.load({ ...key, runtimeId: 'runtime-state-b' })?.text).toBe('second');
    expect(store.load({ ...key, reviewId: 'review-b' })?.text).toBe('third');
    expect(store.load({ ...key, baseDraftRevision: 'draft-revision-b' })?.text).toBe('fourth');
  });

  it('discards exactly the requested draft', () => {
    const store = makeStore();
    store.save({ ...key, text: 'remove me' });
    store.save({ ...key, reviewId: 'review-b', text: 'keep me' });

    expect(store.discard(key)).toBe(true);
    expect(store.load(key)).toBeNull();
    expect(store.load({ ...key, reviewId: 'review-b' })?.text).toBe('keep me');
    expect(store.discard(key)).toBe(false);
  });

  it('writes an owner-only file in an owner-only directory', () => {
    makeStore().save({ ...key, text: 'private' });

    expect(fs.statSync(draftPath()).mode & 0o777).toBe(0o600);
    expect(fs.statSync(dir).mode & 0o777).toBe(0o700);
  });

  it('backs up corrupt data and recovers to an empty store', () => {
    fs.writeFileSync(draftPath(), '{"schemaVersion": 1, "drafts": [');

    const store = makeStore();
    expect(store.load(key)).toBeNull();
    expect(fs.existsSync(`${draftPath()}.bak-1`)).toBe(true);
    expect(warnings).toHaveLength(1);
  });

  it('backs up an unsupported schema without claiming a recovered draft', () => {
    fs.writeFileSync(draftPath(), JSON.stringify({ schemaVersion: 99, drafts: [] }));

    expect(makeStore().load(key)).toBeNull();
    expect(fs.existsSync(`${draftPath()}.bak-1`)).toBe(true);
  });
});

describe('LocalDraftStore identity re-key', () => {
  it('re-keys drafts from the legacy identity to the serverKey, idempotently', () => {
    const store = makeStore();
    store.save({ ...key, text: 'legacy draft' });
    store.save({ ...key, runtimeId: 'other-server-key', text: 'not legacy' });

    expect(store.rekeyRuntimeIds('server-key-a', ['runtime-state-a', 'default-runtime'])).toBe(1);
    // The matching draft moved; the foreign draft is untouched.
    expect(store.load({ ...key, runtimeId: 'server-key-a' })?.text).toBe('legacy draft');
    expect(store.load({ ...key, runtimeId: 'other-server-key' })?.text).toBe('not legacy');
    expect(store.load(key)).toBeNull();
    // A repeated migration is a no-op and persists nothing more.
    expect(store.rekeyRuntimeIds('server-key-a', ['runtime-state-a', 'default-runtime'])).toBe(0);
    // The re-key survives a reload.
    expect(makeStore().load({ ...key, runtimeId: 'server-key-a' })?.text).toBe('legacy draft');
  });
});
