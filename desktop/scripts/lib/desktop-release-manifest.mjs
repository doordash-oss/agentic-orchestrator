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

// Canonical, signed desktop release envelope published beside package artifacts.
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';

import {
  expectedDesktopArtifacts,
  readArtifactEvidence,
  releaseVersionFromTag,
} from './release-artifacts.mjs';
import { verifyReleasePayload } from './release-signing.mjs';

export const DESKTOP_RELEASE_MANIFEST = 'desktop-release.json';
export const DESKTOP_RELEASE_SIGNATURE = 'desktop-release.json.sig';

function canonicalManifest({ tag, commit, artifactDir }) {
  if (!/^[0-9a-f]{40}$/.test(commit ?? '')) {
    throw new Error('desktop release manifest requires a lowercase 40-hex commit');
  }
  return {
    schema_version: 1,
    tag,
    version: releaseVersionFromTag(tag),
    commit,
    artifacts: expectedDesktopArtifacts(tag).map(({ name }) => {
      const evidence = readArtifactEvidence(join(artifactDir, name));
      return { name, sha256: evidence.sha256, size: evidence.size };
    }),
  };
}

function canonicalBytes(manifest) {
  return Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
}

/** Generate the exact schema-v1 manifest from the already verified package set. */
export function createDesktopReleaseManifest({ tag, commit, artifactDir, manifestPath }) {
  if (basename(manifestPath) !== DESKTOP_RELEASE_MANIFEST) {
    throw new Error(`desktop release manifest must be named ${DESKTOP_RELEASE_MANIFEST}`);
  }
  const manifest = canonicalManifest({ tag, commit, artifactDir });
  writeFileSync(manifestPath, canonicalBytes(manifest), { flag: 'wx', mode: 0o400 });
  return manifest;
}

/** Verify canonical bytes, trust-root signature, identity, order, and live artifact digests. */
export function verifyDesktopReleaseManifest({
  tag,
  commit,
  artifactDir,
  manifestPath,
  publicKey,
}) {
  const bytes = readFileSync(manifestPath);
  const signaturePath = `${manifestPath}.sig`;
  if (!verifyReleasePayload(bytes, readFileSync(signaturePath, 'utf8'), publicKey)) {
    throw new Error('desktop release manifest signature does not verify against the trust root');
  }
  const expected = canonicalManifest({ tag, commit, artifactDir });
  if (!bytes.equals(canonicalBytes(expected))) {
    throw new Error('desktop release manifest is noncanonical or artifact evidence changed');
  }
  chmodSync(manifestPath, 0o400);
  chmodSync(signaturePath, 0o400);
  return expected;
}
