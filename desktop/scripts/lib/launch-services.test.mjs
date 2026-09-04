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
import { parseSchemeClaimants, stalenessReason, unregisterAppBundle } from './launch-services.mjs';

const RULE = '-'.repeat(80);

function record(fields) {
  return Object.entries(fields)
    .map(([key, value]) => `${key}:${' '.repeat(28 - key.length - 1)}${value}`)
    .join('\n');
}

describe('parseSchemeClaimants', () => {
  it('returns the unique bundle paths whose claimed schemes include the scheme', () => {
    const dump = [
      record({ 'bundle id': 'Agentico (0x2a3c)', path: '/Applications/Agentico.app (0x1)' }) +
        '\nclaimed schemes:            agentico:',
      record({ 'bundle id': 'Safari (0x1)', path: '/Applications/Safari.app (0x2)' }) +
        '\nclaimed schemes:            http:, https:',
      record({ path: '/tmp/agentico-e2e-security-abc/ro-install/Agentico.app (0x3)' }) +
        '\nclaimed schemes:            agentico:',
      record({ path: '/tmp/agentico-e2e-security-abc/ro-install/Agentico.app (0x3)' }) +
        '\nclaimed schemes:            agentico:',
      'claim id:                   Agentico (0x5fbc)\nbindings:                   agentico:',
    ].join(`\n${RULE}\n`);
    expect(parseSchemeClaimants(dump)).toEqual([
      '/Applications/Agentico.app',
      '/tmp/agentico-e2e-security-abc/ro-install/Agentico.app',
    ]);
  });

  it('keeps paths containing spaces intact', () => {
    const dump =
      record({ path: '/Users/me/Some Dir/Electron.app (0x9)' }) +
      '\nclaimed schemes:            agentico:';
    expect(parseSchemeClaimants(dump)).toEqual(['/Users/me/Some Dir/Electron.app']);
  });
});

describe('stalenessReason', () => {
  const exists = (candidate) => candidate !== '/gone/Agentico.app';
  it('keeps only the installed app', () => {
    expect(stalenessReason('/Applications/Agentico.app', { exists })).toBeNull();
  });
  it('flags missing bundles, dev shells, temp copies, and other builds', () => {
    expect(stalenessReason('/gone/Agentico.app', { exists })).toBe('bundle no longer exists');
    expect(stalenessReason('/repo/node_modules/electron/dist/Electron.app', { exists })).toBe(
      'dev Electron shell',
    );
    expect(
      stalenessReason(
        '/private/var/folders/z5/x/T/agentico-e2e-security-1/ro-install/Agentico.app',
        {
          exists,
        },
      ),
    ).toBe('temporary directory');
    expect(stalenessReason('/home/qa/tmp/agentico-verify-dmg-1/Agentico.app', { exists })).toBe(
      'temporary directory',
    );
    expect(stalenessReason('/repo/desktop/dist/mac-universal/Agentico.app', { exists })).toMatch(
      /not the installed app/,
    );
  });
});

describe('unregisterAppBundle', () => {
  it('is a no-op off macOS and never throws when lsregister fails', () => {
    const calls = [];
    const exec = (...args) => {
      calls.push(args);
      throw new Error('boom');
    };
    expect(unregisterAppBundle('/x/Agentico.app', { platform: 'linux', exec })).toBe(false);
    expect(calls).toHaveLength(0);
    if (process.platform === 'darwin') {
      expect(unregisterAppBundle('/x/Agentico.app', { platform: 'darwin', exec })).toBe(false);
      expect(calls[0][1]).toEqual(['-u', '/x/Agentico.app']);
    }
  });
});
