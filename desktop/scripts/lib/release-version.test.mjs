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

// Unit tests for release-version stamping: only a clean release tag may
// override the development version, because both the in-app updater and the
// CLI downgrade guard refuse versions they cannot order.
import { describe, expect, it } from 'vitest';

import { desktopVersionFromExactTag } from './release-version.mjs';

describe('desktopVersionFromExactTag', () => {
  it('strips the leading v from a clean release tag', () => {
    expect(desktopVersionFromExactTag('v0.149.0\n')).toBe('0.149.0');
  });

  it('accepts a bare MAJOR.MINOR.PATCH tag', () => {
    expect(desktopVersionFromExactTag('1.2.3')).toBe('1.2.3');
  });

  it.each([
    ['prerelease suffix', 'v1.2.3-rc.1'],
    ['git-describe distance', 'v1.2.3-5-gabc1234'],
    ['dirty marker', 'v1.2.3-dirty'],
    ['non-release tag', 'nightly'],
    ['two-part version', 'v1.2'],
    ['four-part version', 'v1.2.3.4'],
    ['empty output', ''],
    ['whitespace only', '  \n'],
  ])('rejects %s', (_label, tag) => {
    expect(desktopVersionFromExactTag(tag)).toBeNull();
  });

  it('rejects non-string input', () => {
    expect(desktopVersionFromExactTag(undefined)).toBeNull();
  });
});
