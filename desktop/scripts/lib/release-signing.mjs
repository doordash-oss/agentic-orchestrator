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

// Ed25519 release-metadata signing shared by release-sign.mjs (the producer
// goreleaser invokes for checksums.txt) and its tests. The output is exactly
// what the desktop updater verifies (src/main/updates.ts parseSignature):
// "agentico-ed25519:" + base64 detached Ed25519 signature over the raw
// checksum-file bytes. Pure functions only: no fs, no child_process.
import { createPrivateKey, createPublicKey, sign, verify } from 'node:crypto';

export const SIGNATURE_PREFIX = 'agentico-ed25519:';

/**
 * Accept private-key material as PEM or as base64-encoded PEM (the
 * env-variable-friendly single-line form) and return a KeyObject. Throws on
 * anything that does not decode to an Ed25519 private key.
 */
export function privateKeyFromMaterial(material) {
  const text = material.trim();
  const pem = text.includes('-----BEGIN') ? text : Buffer.from(text, 'base64').toString('utf8');
  const key = createPrivateKey(pem);
  if (key.asymmetricKeyType !== 'ed25519') {
    throw new Error(`release signing key must be ed25519, got ${key.asymmetricKeyType}`);
  }
  return key;
}

/** Export the SPKI PEM public half of a private key. */
export function publicKeyPem(privateKey) {
  return createPublicKey(privateKey).export({ type: 'spki', format: 'pem' }).toString().trim();
}

/** Produce the updater's signature format over the raw payload bytes. */
export function signReleasePayload(payload, privateKey) {
  return `${SIGNATURE_PREFIX}${sign(null, payload, privateKey).toString('base64')}`;
}

/** Verify a signature produced by signReleasePayload against an SPKI PEM. */
export function verifyReleasePayload(payload, signatureText, publicKeyPemText) {
  const text = signatureText.trim();
  if (!text.startsWith(SIGNATURE_PREFIX)) return false;
  try {
    return verify(
      null,
      payload,
      createPublicKey(publicKeyPemText),
      Buffer.from(text.slice(SIGNATURE_PREFIX.length), 'base64'),
    );
  } catch {
    return false;
  }
}

/**
 * Extract the embedded production trust root from src/main/updates.ts source
 * text, so the signer can refuse to produce signatures the shipped app will
 * not accept. Throws when the constant cannot be located.
 */
export function extractEmbeddedReleasePublicKey(updatesSource) {
  const match =
    /const RELEASE_PUBLIC_KEY = `(-----BEGIN PUBLIC KEY-----[^`]+-----END PUBLIC KEY-----)`/.exec(
      updatesSource,
    );
  if (match === null) {
    throw new Error('could not locate RELEASE_PUBLIC_KEY in src/main/updates.ts');
  }
  return match[1].trim();
}
