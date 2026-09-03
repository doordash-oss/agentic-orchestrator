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

// Release-metadata signer for the in-app updater's trust chain.
//
//   sign <file>    write <file>.sig ("agentico-ed25519:" + base64 Ed25519
//                  signature over the file bytes). goreleaser invokes this on
//                  checksums.txt so the desktop app can verify that release
//                  assets match their signed checksums. Refuses to sign with a
//                  key whose public half differs from the trust root embedded
//                  in src/main/updates.ts — a signature the shipped app would
//                  reject must never reach a release.
//   verify <file>  check <file>.sig against the embedded trust root.
//   keygen         create a new operator keypair (never overwrites) and print
//                  the public key to embed in src/main/updates.ts.
//
// Key material comes from, in order: AGENTICO_RELEASE_SIGNING_KEY (PEM or
// base64-encoded PEM), AGENTICO_RELEASE_SIGNING_KEY_FILE, or the default
// operator path ~/.config/agentico-release/release-key.pem. The private key
// never enters the repo or CI.
import { generateKeyPairSync } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  extractEmbeddedReleasePublicKey,
  privateKeyFromMaterial,
  publicKeyPem,
  signReleasePayload,
  verifyReleasePayload,
} from './lib/release-signing.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const DEFAULT_KEY_FILE = join(homedir(), '.config', 'agentico-release', 'release-key.pem');
const UPDATES_SOURCE = join(desktopDir, 'src', 'main', 'updates.ts');

function fail(message) {
  console.error(`release-sign: ${message}`);
  process.exit(1);
}

function loadPrivateKey() {
  const env = process.env.AGENTICO_RELEASE_SIGNING_KEY;
  if (env !== undefined && env.trim() !== '') return privateKeyFromMaterial(env);
  const file = process.env.AGENTICO_RELEASE_SIGNING_KEY_FILE ?? DEFAULT_KEY_FILE;
  if (!existsSync(file)) {
    fail(
      `no signing key: set AGENTICO_RELEASE_SIGNING_KEY, or create ${file} ` +
        'with `node desktop/scripts/release-sign.mjs keygen`',
    );
  }
  return privateKeyFromMaterial(readFileSync(file, 'utf8'));
}

function embeddedTrustRoot() {
  return extractEmbeddedReleasePublicKey(readFileSync(UPDATES_SOURCE, 'utf8'));
}

function sign(file) {
  if (file === undefined) fail('usage: release-sign.mjs sign <file>');
  const key = loadPrivateKey();
  const derived = publicKeyPem(key);
  const embedded = embeddedTrustRoot();
  if (derived !== embedded) {
    fail(
      'signing key does not match the trust root embedded in src/main/updates.ts; ' +
        'the shipped app would reject this signature. Update RELEASE_PUBLIC_KEY ' +
        'or use the matching key.',
    );
  }
  const payload = readFileSync(file);
  writeFileSync(`${file}.sig`, `${signReleasePayload(payload, key)}\n`);
  console.log(`release-sign: wrote ${file}.sig`);
}

function verify(file) {
  if (file === undefined) fail('usage: release-sign.mjs verify <file>');
  const ok = verifyReleasePayload(
    readFileSync(file),
    readFileSync(`${file}.sig`, 'utf8'),
    embeddedTrustRoot(),
  );
  if (!ok) fail(`${file}.sig does not verify against the embedded trust root`);
  console.log(`release-sign: ${file}.sig verifies against the embedded trust root`);
}

function keygen() {
  if (existsSync(DEFAULT_KEY_FILE)) {
    fail(`${DEFAULT_KEY_FILE} already exists; move it aside before rotating`);
  }
  const { privateKey } = generateKeyPairSync('ed25519');
  mkdirSync(dirname(DEFAULT_KEY_FILE), { recursive: true, mode: 0o700 });
  writeFileSync(DEFAULT_KEY_FILE, privateKey.export({ type: 'pkcs8', format: 'pem' }), {
    mode: 0o600,
  });
  console.log(`release-sign: wrote ${DEFAULT_KEY_FILE}`);
  console.log('Embed this public key as RELEASE_PUBLIC_KEY in desktop/src/main/updates.ts:');
  console.log(publicKeyPem(privateKey));
}

const [command, file] = process.argv.slice(2);
if (command === 'sign') sign(file);
else if (command === 'verify') verify(file);
else if (command === 'keygen') keygen();
else fail('usage: release-sign.mjs <sign|verify|keygen> [file]');
