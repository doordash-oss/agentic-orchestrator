// Unit tests proving the signer and the desktop updater agree: signatures
// produced here must verify with the same node:crypto primitives updates.ts
// uses, and the embedded-trust-root extraction must find the real constant.
import { generateKeyPairSync } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import {
  SIGNATURE_PREFIX,
  extractEmbeddedReleasePublicKey,
  privateKeyFromMaterial,
  publicKeyPem,
  signReleasePayload,
  verifyReleasePayload,
} from './release-signing.mjs';

const desktopDir = dirname(dirname(dirname(fileURLToPath(import.meta.url))));

function newKeyPem() {
  return generateKeyPairSync('ed25519')
    .privateKey.export({ type: 'pkcs8', format: 'pem' })
    .toString();
}

describe('release signing', () => {
  it('produces signatures the matching public key verifies', () => {
    const pem = newKeyPem();
    const key = privateKeyFromMaterial(pem);
    const payload = Buffer.from('abc123  Agentico-mac-universal.dmg\n');
    const signature = signReleasePayload(payload, key);
    expect(signature.startsWith(SIGNATURE_PREFIX)).toBe(true);
    expect(verifyReleasePayload(payload, signature, publicKeyPem(key))).toBe(true);
  });

  it('rejects a tampered payload and a foreign key', () => {
    const key = privateKeyFromMaterial(newKeyPem());
    const payload = Buffer.from('payload');
    const signature = signReleasePayload(payload, key);
    expect(verifyReleasePayload(Buffer.from('payload2'), signature, publicKeyPem(key))).toBe(false);
    const otherKey = privateKeyFromMaterial(newKeyPem());
    expect(verifyReleasePayload(payload, signature, publicKeyPem(otherKey))).toBe(false);
  });

  it('accepts base64-encoded PEM key material', () => {
    const pem = newKeyPem();
    const fromBase64 = privateKeyFromMaterial(Buffer.from(pem).toString('base64'));
    expect(publicKeyPem(fromBase64)).toBe(publicKeyPem(privateKeyFromMaterial(pem)));
  });

  it('rejects non-ed25519 keys', () => {
    const rsa = generateKeyPairSync('rsa', { modulusLength: 2048 })
      .privateKey.export({ type: 'pkcs8', format: 'pem' })
      .toString();
    expect(() => privateKeyFromMaterial(rsa)).toThrow(/ed25519/);
  });

  it('extracts the trust root actually embedded in updates.ts', () => {
    const source = readFileSync(join(desktopDir, 'src', 'main', 'updates.ts'), 'utf8');
    const embedded = extractEmbeddedReleasePublicKey(source);
    expect(embedded).toContain('BEGIN PUBLIC KEY');
    // The production trust root must never be the committed fixture key.
    expect(embedded).not.toContain('MCowBQYDK2VwAyEAmhM+TNlJSPzGSFwd/DakW3G6MzxCpouletrsW4WAezE=');
  });

  it('throws when the trust root constant is missing', () => {
    expect(() => extractEmbeddedReleasePublicKey('nothing here')).toThrow(/RELEASE_PUBLIC_KEY/);
  });
});
