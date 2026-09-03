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

import {
  PRODUCTION_FUSE_POLICY,
  auditElectronBuilderFuseConfig,
  auditFuseWire,
  expectedElectronBuilderFuses,
  parseElectronBuilderFuses,
} from './fuse-policy.mjs';

describe('production fuse policy', () => {
  it('requires the builder config to pin every production fuse', () => {
    const config = [
      'asar: true',
      'electronFuses:',
      '  runAsNode: false',
      '  enableCookieEncryption: true',
      '  enableNodeOptionsEnvironmentVariable: false',
      '  enableNodeCliInspectArguments: false',
      '  enableEmbeddedAsarIntegrityValidation: true',
      '  onlyLoadAppFromAsar: true',
      '  loadBrowserProcessSpecificV8Snapshot: false',
      '  grantFileProtocolExtraPrivileges: false',
      'npmRebuild: false',
    ].join('\n');

    expect(parseElectronBuilderFuses(config)).toEqual(expectedElectronBuilderFuses());
    expect(auditElectronBuilderFuseConfig(config)).toEqual([]);
  });

  it('reports missing and mismatched builder fuses', () => {
    const config = ['electronFuses:', '  runAsNode: true'].join('\n');

    expect(auditElectronBuilderFuseConfig(config)).toEqual(
      expect.arrayContaining([
        'electron-builder.yml electronFuses.runAsNode=true, expected false',
        'electron-builder.yml missing electronFuses.onlyLoadAppFromAsar',
      ]),
    );
  });

  it('compares packaged Electron fuse wire values', () => {
    const wire = [];
    for (const fuse of PRODUCTION_FUSE_POLICY) {
      wire[fuse.option] = fuse.expectedWireValue;
    }

    expect(auditFuseWire(wire)).toEqual([]);

    wire[PRODUCTION_FUSE_POLICY[0].option] = 49;
    expect(auditFuseWire(wire)[0]).toContain('RunAsNode');
  });
});
