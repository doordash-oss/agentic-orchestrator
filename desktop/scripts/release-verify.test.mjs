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
import { readFileSync } from 'node:fs';

import { verifyLocalReleaseModel } from './release-verify.mjs';

describe('local release verification', () => {
  it('rejects a release recipe that bypasses the isolated audited runner', () => {
    expect(
      verifyLocalReleaseModel({
        makefile:
          'release:\n\tnode desktop/scripts/release-preflight.mjs\n\tnode desktop/scripts/release-goreleaser.mjs\n',
        builder: 'hardenedRuntime: true\nprotocols:\n',
        signingScript: 'ed25519\nRELEASE_PUBLIC_KEY\n',
      }),
    ).toContain('release verification requires node desktop/scripts/release-run.mjs');
  });

  it('accepts the actual local-operator release model without protected-CI warnings', () => {
    expect(verifyLocalReleaseModel()).toEqual([]);
  });

  it('requires a durable checkpoint before remote tag reservation', () => {
    const runner = readFileSync(new URL('./release-run.mjs', import.meta.url), 'utf8');
    expect(
      verifyLocalReleaseModel({
        releaseRunner: runner.replace(
          "saveResume(evidence, snapshot, 'tag-reservation-started')",
          '// reservation checkpoint omitted',
        ),
      }),
    ).toContain(
      "release runner is missing audited step: saveResume(evidence, snapshot, 'tag-reservation-started')",
    );
  });
});
