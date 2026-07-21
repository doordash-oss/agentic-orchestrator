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
