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

// Unit tests for the desktop cask template: the URL, checksum, and the
// quarantine postflight (the interim unsigned-distribution mechanism) must be
// present and correctly parameterized, and malformed inputs must be refused
// rather than pushed to the tap.
import { describe, expect, it } from 'vitest';

import { renderDesktopCask } from './desktop-cask.mjs';

const SHA = 'a'.repeat(64);

describe('renderDesktopCask', () => {
  it('renders version, checksum, release URL, and app stanza', () => {
    const cask = renderDesktopCask({ version: '0.149.0', sha256: SHA });
    expect(cask).toContain('cask "agentico-desktop" do');
    expect(cask).toContain('version "0.149.0"');
    expect(cask).toContain(`sha256 "${SHA}"`);
    expect(cask).toContain(
      'url "https://github.com/doordash-oss/agentic-orchestrator/releases/download/v#{version}/Agentico-mac-universal.dmg"',
    );
    expect(cask).toContain('app "Agentico.app"');
  });

  it('carries the quarantine postflight for unsigned interim releases', () => {
    const cask = renderDesktopCask({ version: '0.149.0', sha256: SHA });
    expect(cask).toContain('postflight do');
    expect(cask).toContain('com.apple.quarantine');
    expect(cask).toContain('#{appdir}/Agentico.app');
  });

  it.each([
    ['tag-prefixed version', 'v0.149.0', SHA],
    ['prerelease version', '0.149.0-rc.1', SHA],
    ['uppercase digest', '0.149.0', 'A'.repeat(64)],
    ['short digest', '0.149.0', 'ab12'],
  ])('rejects %s', (_label, version, sha256) => {
    expect(() => renderDesktopCask({ version, sha256 })).toThrow();
  });
});
