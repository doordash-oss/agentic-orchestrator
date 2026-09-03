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
import { SUPPORTED_API_VERSION, assertCompatibleApiVersion } from './apiVersion';
import { CanonicalErrorException } from './errors';

describe('assertCompatibleApiVersion', () => {
  it('accepts the supported major version', () => {
    expect(() => assertCompatibleApiVersion('v1')).not.toThrow();
    expect(() => assertCompatibleApiVersion('v1.2')).not.toThrow();
  });

  it('fails closed on a different major version with an actionable error', () => {
    for (const bad of ['v2', 'v10', 'v0', '1', '', 'weird']) {
      try {
        assertCompatibleApiVersion(bad);
        throw new Error(`expected ${JSON.stringify(bad)} to be rejected`);
      } catch (err) {
        expect(err).toBeInstanceOf(CanonicalErrorException);
        if (!(err instanceof CanonicalErrorException)) throw err;
        expect(err.canonical.code).toBe('E_API_VERSION_INCOMPATIBLE');
        expect(err.canonical.class).toBe('blocking');
        expect(err.canonical.remediation?.hint).toBeTruthy();
        expect(err.canonical.summary).toContain(SUPPORTED_API_VERSION);
      }
    }
  });
});
