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

// Unit tests proving the signer and the desktop updater agree: signatures
// produced here must verify with the same node:crypto primitives updates.ts
// uses, and the embedded-trust-root extraction must find the real constant.
import { generateKeyPairSync } from 'node:crypto';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import {
  SIGNATURE_PREFIX,
  extractEmbeddedReleasePublicKey,
  extractGoReleasePublicKeySPKI,
  privateKeyFromMaterial,
  publicKeyPem,
  signReleasePayload,
  spkiBase64FromPem,
  verifyReleasePayload,
} from './release-signing.mjs';

const desktopDir = dirname(dirname(dirname(fileURLToPath(import.meta.url))));
const repoRoot = dirname(desktopDir);
const GO_TRUST_SOURCE = join(repoRoot, 'internal', 'selfupdate', 'trust.go');
const UPDATES_SOURCE = join(desktopDir, 'src', 'main', 'updates.ts');
const FIXTURE_SPKI_BASE64 = 'MCowBQYDK2VwAyEAmhM+TNlJSPzGSFwd/DakW3G6MzxCpouletrsW4WAezE=';
const FIXTURE_PUBLIC_KEY_PEM = `-----BEGIN PUBLIC KEY-----\n${FIXTURE_SPKI_BASE64}\n-----END PUBLIC KEY-----`;
// Anchor proving the repo-root resolution: if the layout moves, the parity
// tests must fail loudly rather than silently read some other checkout's files.
const repoRootAnchored = existsSync(resolve(repoRoot, 'go.mod'));

function newKeyPem() {
  return generateKeyPairSync('ed25519')
    .privateKey.export({ type: 'pkcs8', format: 'pem' })
    .toString();
}

function fixturePayload() {
  return readFileSync(join(repoRoot, 'test', 'release-trust-vectors', 'payload.bin'));
}

function fixtureSignature() {
  return readFileSync(join(repoRoot, 'test', 'release-trust-vectors', 'payload.sig'), 'utf8');
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

describe('release trust-root parity', () => {
  it('anchors the repo root at go.mod', () => {
    expect(repoRootAnchored).toBe(true);
  });

  it('extracts the SPKI constant from the real internal/selfupdate/trust.go', () => {
    expect(extractGoReleasePublicKeySPKI(readFileSync(GO_TRUST_SOURCE, 'utf8'))).toMatch(
      /^[A-Za-z0-9+/=]+$/,
    );
  });

  it('decodes the Go SPKI constant to the same DER bytes as the desktop trust root', () => {
    const goSpki = extractGoReleasePublicKeySPKI(readFileSync(GO_TRUST_SOURCE, 'utf8'));
    const desktopPem = extractEmbeddedReleasePublicKey(readFileSync(UPDATES_SOURCE, 'utf8'));
    const goDer = Buffer.from(goSpki, 'base64');
    const desktopDer = Buffer.from(spkiBase64FromPem(desktopPem), 'base64');
    expect(Buffer.isBuffer(goDer)).toBe(true);
    expect(Buffer.isBuffer(desktopDer)).toBe(true);
    expect(goDer.equals(desktopDer)).toBe(true);
  });

  it('throws when the Go trust-root constant is missing or malformed', () => {
    expect(() => extractGoReleasePublicKeySPKI('package selfupdate\n')).toThrow(
      /productionReleasePublicKeySPKI/,
    );
    expect(() =>
      extractGoReleasePublicKeySPKI('const productionReleasePublicKeySPKI = not-a-string'),
    ).toThrow(/productionReleasePublicKeySPKI/);
  });

  it('verifies the shared fixture vectors with the fixture public key', () => {
    expect(verifyReleasePayload(fixturePayload(), fixtureSignature(), FIXTURE_PUBLIC_KEY_PEM)).toBe(
      true,
    );
  });

  it('rejects a tampered fixture payload', () => {
    const tampered = Buffer.from(fixturePayload());
    tampered[0] ^= 0x01;
    expect(verifyReleasePayload(tampered, fixtureSignature(), FIXTURE_PUBLIC_KEY_PEM)).toBe(false);
  });

  it('rejects a tampered fixture signature', () => {
    // Swap one base64 character in the signature body (not the prefix).
    const body = fixtureSignature().trim().slice(SIGNATURE_PREFIX.length);
    const flipped = `${body.charAt(0) === 'A' ? 'B' : 'A'}${body.slice(1)}`;
    expect(
      verifyReleasePayload(
        fixturePayload(),
        `${SIGNATURE_PREFIX}${flipped}`,
        FIXTURE_PUBLIC_KEY_PEM,
      ),
    ).toBe(false);
  });

  it('rejects the fixture signature under the production trust root', () => {
    const productionPem = extractEmbeddedReleasePublicKey(readFileSync(UPDATES_SOURCE, 'utf8'));
    expect(verifyReleasePayload(fixturePayload(), fixtureSignature(), productionPem)).toBe(false);
  });

  it('reduces a PUBLIC KEY PEM block to its base64 body', () => {
    expect(spkiBase64FromPem(FIXTURE_PUBLIC_KEY_PEM)).toBe(FIXTURE_SPKI_BASE64);
    expect(() => spkiBase64FromPem('no markers here')).toThrow(/PUBLIC KEY/);
  });
});
