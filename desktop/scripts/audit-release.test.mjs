import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

import {
  auditLicenseInventory,
  auditGoReleaserDesktopArtifacts,
  auditGoReleaserReleaseTarget,
  auditReleaseMakefile,
  auditNpmLockfile,
  collectNpmRuntimeInventory,
  hasElectronBuilderProtocolScheme,
  highestGoSeverity,
  parseGoModRequirements,
} from './audit-release.mjs';

describe('release audit lockfile and license checks', () => {
  it('rejects a release config that uploads an AppImage without signing it', () => {
    const config = `
checksum:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
release:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
    - glob: desktop/dist/Agentico-x64.AppImage
`;

    expect(
      auditGoReleaserDesktopArtifacts(config, [
        'Agentico-mac-universal.dmg',
        'Agentico-x64.AppImage',
      ]),
    ).toContain('GoReleaser checksum.extra_files omits Agentico-x64.AppImage');
  });

  it('detects independent checksum and release omissions', () => {
    const config = `
checksum:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
    - glob: desktop/dist/Agentico-x64.AppImage
release:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
    - glob: desktop/dist/Agentico-arm64.AppImage
`;

    expect(
      auditGoReleaserDesktopArtifacts(config, [
        'Agentico-mac-universal.dmg',
        'Agentico-x64.AppImage',
        'Agentico-arm64.AppImage',
      ]),
    ).toEqual(
      expect.arrayContaining([
        'GoReleaser checksum.extra_files omits Agentico-arm64.AppImage',
        'GoReleaser release.extra_files omits Agentico-x64.AppImage',
      ]),
    );
  });

  it('requires the exact desktop artifact directory and versioned Debian paths', () => {
    const config = `
checksum:
  extra_files:
    - glob: wrong/place/Agentico-x64.AppImage
    - glob: desktop/dist/agentico_*_amd64.deb
release:
  extra_files:
    - glob: desktop/dist/Agentico-x64.AppImage
    - glob: wrong/place/agentico_{{ .Version }}_amd64.deb
`;

    expect(
      auditGoReleaserDesktopArtifacts(config, [
        'Agentico-x64.AppImage',
        'agentico_0.150.0_amd64.deb',
      ]),
    ).toEqual(
      expect.arrayContaining([
        'GoReleaser checksum.extra_files omits Agentico-x64.AppImage',
        'GoReleaser checksum.extra_files omits agentico_0.150.0_amd64.deb',
        'GoReleaser release.extra_files omits agentico_0.150.0_amd64.deb',
      ]),
    );
  });

  it('requires each exact desktop path exactly once in each GoReleaser block', () => {
    const config = `
checksum:
  extra_files:
    - glob: desktop/dist/Agentico-x64.AppImage
    - glob: desktop/dist/Agentico-x64.AppImage
release:
  extra_files:
    - glob: desktop/dist/Agentico-x64.AppImage
    - glob: desktop/dist/Agentico-x64.AppImage
`;

    expect(auditGoReleaserDesktopArtifacts(config, ['Agentico-x64.AppImage'])).toEqual(
      expect.arrayContaining([
        'GoReleaser checksum.extra_files has 2 entries for Agentico-x64.AppImage, expected exactly 1',
        'GoReleaser release.extra_files has 2 entries for Agentico-x64.AppImage, expected exactly 1',
      ]),
    );
  });

  it('accepts the actual GoReleaser desktop artifact configuration', () => {
    const config = readFileSync(new URL('../../.goreleaser.yaml', import.meta.url), 'utf8');
    expect(
      auditGoReleaserDesktopArtifacts(config, [
        'Agentico-mac-universal.dmg',
        'Agentico-x64.AppImage',
        'Agentico-arm64.AppImage',
        'agentico_0.150.0_amd64.deb',
        'agentico_0.150.0_arm64.deb',
      ]),
    ).toEqual([]);
  });

  it('requires GoReleaser to pin the release target to the captured commit', () => {
    expect(auditGoReleaserReleaseTarget('release:\n  target_commitish: main\n')).toEqual([
      'GoReleaser release.target_commitish must be exactly "{{ .Commit }}" to prevent publishing another commit',
    ]);
    const config = readFileSync(new URL('../../.goreleaser.yaml', import.meta.url), 'utf8');
    expect(auditGoReleaserReleaseTarget(config)).toEqual([]);
  });

  it('audits only the actual release recipe with exact publication command ordering', () => {
    expect(
      auditReleaseMakefile(
        [
          '# node desktop/scripts/verify-release-publication.mjs preflight --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
          'other:',
          '\tnode desktop/scripts/verify-release-publication.mjs preflight --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
          '\tnode desktop/scripts/release-goreleaser.mjs',
          '\tnpm run release:artifacts:verify --workspace desktop -- manifest',
          '\tnode desktop/scripts/verify-release-publication.mjs verify --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
          '\tnode desktop/scripts/publish-desktop-cask.mjs',
          'release:',
          '\t# A comment must not satisfy the release audit.',
          '\tnode desktop/scripts/publish-desktop-cask.mjs',
        ].join('\n'),
      ),
    ).toEqual(
      expect.arrayContaining([
        expect.stringContaining('remote tag is absent'),
        expect.stringContaining('local manifest then remote publication'),
      ]),
    );
    expect(
      auditReleaseMakefile(
        [
          'release:',
          '\tnode desktop/scripts/verify-release-publication.mjs preflight --commit "$(RELEASE_COMMIT)" --tag "$(RELEASE_TAG)"',
          '\tnode desktop/scripts/release-goreleaser.mjs GORELEASER_FLAGS=--skip=publish',
          '\tnpm run release:artifacts:verify --workspace desktop -- manifest',
          '\tnode desktop/scripts/verify-release-publication.mjs verify --tag "$(RELEASE_TAG)" --commit "wrong"',
          '\tnode desktop/scripts/publish-desktop-cask.mjs',
        ].join('\n'),
      ),
    ).toEqual(
      expect.arrayContaining([
        expect.stringContaining('must not expose GORELEASER_FLAGS'),
        expect.stringContaining('remote tag is absent'),
        expect.stringContaining('exact audited publication commands'),
      ]),
    );
    const makefile = readFileSync(new URL('../../Makefile', import.meta.url), 'utf8');
    expect(auditReleaseMakefile(makefile)).toEqual([]);
  });

  it('rejects an early cask and every duplicate or extra publication-sensitive command', () => {
    const validRecipe = [
      'release:',
      '\tnode desktop/scripts/verify-release-publication.mjs preflight --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
      '\tnpm run release:artifacts:verify --workspace desktop -- packages',
      '\tnode desktop/scripts/release-goreleaser.mjs',
      '\tnpm run release:artifacts:verify --workspace desktop -- manifest',
      '\tnode desktop/scripts/verify-release-publication.mjs verify --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
      '\tnode desktop/scripts/publish-desktop-cask.mjs',
    ];
    for (const extra of [
      '\tnode desktop/scripts/publish-desktop-cask.mjs --early',
      '\tnode desktop/scripts/release-goreleaser.mjs',
      '\tgoreleaser release --clean',
      '\t/opt/homebrew/bin/goreleaser release --clean',
      '\t./bin/goreleaser release --clean',
      '\tGITHUB_TOKEN=token ./bin/goreleaser release --clean',
      '\tenv GITHUB_TOKEN=token /opt/homebrew/bin/goreleaser release --clean',
      '\tnpm run release:artifacts:verify --workspace desktop -- manifest --again',
    ]) {
      const recipe = [...validRecipe];
      recipe.splice(1, 0, extra);
      expect(auditReleaseMakefile(recipe.join('\n'))).toEqual(
        expect.arrayContaining([expect.stringContaining('exact audited publication commands')]),
      );
    }
  });

  it('requires local preflight, a lockfile install, and a provenance recheck before publication', () => {
    const recipe = [
      'release:',
      '\tnode desktop/scripts/verify-release-publication.mjs preflight --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
      '\tnpm run package:verify --workspace desktop',
      '\tnpm run package:linux:release --workspace desktop',
      '\tnpm run release:artifacts:verify --workspace desktop -- packages',
      '\tnode desktop/scripts/release-goreleaser.mjs',
      '\tnpm run release:artifacts:verify --workspace desktop -- manifest',
      '\tnode desktop/scripts/verify-release-publication.mjs verify --tag "$(RELEASE_TAG)" --commit "$(RELEASE_COMMIT)"',
      '\tnode desktop/scripts/publish-desktop-cask.mjs',
    ].join('\n');
    expect(auditReleaseMakefile(recipe)).toEqual(
      expect.arrayContaining([
        expect.stringContaining('local preflight'),
        expect.stringContaining('npm ci'),
        expect.stringContaining('provenance recheck'),
      ]),
    );
  });

  it('collects production npm packages plus explicitly shipped renderer dev assets', () => {
    const lock = {
      lockfileVersion: 3,
      packages: {
        '': { workspaces: ['desktop'] },
        desktop: { dependencies: { 'runtime-lib': '^1.0.0' } },
        'node_modules/runtime-lib': {
          version: '1.0.0',
          license: 'MIT',
          resolved: 'https://registry.npmjs.org/runtime-lib/-/runtime-lib-1.0.0.tgz',
          integrity: 'sha512-abc=',
          dependencies: { transitive: '^2.0.0' },
        },
        'node_modules/transitive': {
          version: '2.0.0',
          license: 'Apache-2.0',
          resolved: 'https://registry.npmjs.org/transitive/-/transitive-2.0.0.tgz',
          integrity: 'sha512-def=',
        },
        'node_modules/@fontsource/example': {
          version: '3.0.0',
          license: 'OFL-1.1',
          resolved: 'https://registry.npmjs.org/@fontsource/example/-/example-3.0.0.tgz',
          integrity: 'sha512-ghi=',
          dev: true,
        },
      },
    };

    const inventory = collectNpmRuntimeInventory(
      lock,
      { dependencies: { 'runtime-lib': '^1.0.0' } },
      { shippedDevDependencies: ['@fontsource/example'] },
    );

    expect(inventory.map((pkg) => pkg.name).sort()).toEqual([
      '@fontsource/example',
      'runtime-lib',
      'transitive',
    ]);
    expect(
      auditNpmLockfile(lock, { dependencies: { 'runtime-lib': '^1.0.0' } }, inventory),
    ).toEqual([]);
  });

  it('resolves npm dependencies through the closest package ancestor before root hoists', () => {
    const lock = {
      lockfileVersion: 3,
      packages: {
        '': { workspaces: ['desktop'] },
        desktop: { dependencies: { parent: '^1.0.0' } },
        'node_modules/parent': {
          version: '1.0.0',
          license: 'MIT',
          resolved: 'https://registry.npmjs.org/parent/-/parent-1.0.0.tgz',
          integrity: 'sha512-parent=',
          dependencies: { child: '^1.0.0' },
        },
        'node_modules/parent/node_modules/child': {
          version: '1.0.0',
          license: 'MIT',
          resolved: 'https://registry.npmjs.org/child/-/child-1.0.0.tgz',
          integrity: 'sha512-child=',
          dependencies: { '@scope/shared': '^2.0.0' },
        },
        'node_modules/parent/node_modules/@scope/shared': {
          version: '2.0.0',
          license: 'Apache-2.0',
          resolved: 'https://registry.npmjs.org/@scope/shared/-/shared-2.0.0.tgz',
          integrity: 'sha512-shared2=',
        },
        'node_modules/@scope/shared': {
          version: '1.0.0',
          license: 'Apache-2.0',
          resolved: 'https://registry.npmjs.org/@scope/shared/-/shared-1.0.0.tgz',
          integrity: 'sha512-shared1=',
        },
      },
    };

    const inventory = collectNpmRuntimeInventory(
      lock,
      { dependencies: { parent: '^1.0.0' } },
      { shippedDevDependencies: [] },
    );

    expect(inventory).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          name: '@scope/shared',
          path: 'node_modules/parent/node_modules/@scope/shared',
          version: '2.0.0',
        }),
      ]),
    );
  });

  it('rejects runtime npm dependencies without registry provenance and sha512 integrity', () => {
    const inventory = [
      {
        source: 'npm',
        name: 'bad-lib',
        path: 'node_modules/bad-lib',
        version: '1.0.0',
        license: 'MIT',
        resolved: 'git+https://example.invalid/bad-lib',
        integrity: null,
      },
    ];

    expect(
      auditNpmLockfile(
        { lockfileVersion: 3, packages: { '': { workspaces: ['desktop'] }, desktop: {} } },
        { dependencies: {} },
        inventory,
      ),
    ).toEqual(
      expect.arrayContaining([
        'runtime npm package has non-registry provenance: node_modules/bad-lib',
        'runtime npm package missing sha512 integrity: node_modules/bad-lib',
      ]),
    );
  });

  it('parses direct and indirect Go requirements', () => {
    expect(
      parseGoModRequirements(
        [
          'module example.test/app',
          'require example.test/direct v1.2.3',
          'require (',
          '  example.test/indirect v0.1.0 // indirect',
          ')',
        ].join('\n'),
      ),
    ).toEqual([
      { path: 'example.test/direct', version: 'v1.2.3', indirect: false },
      { path: 'example.test/indirect', version: 'v0.1.0', indirect: true },
    ]);
  });

  it('requires licenses to be allowed or covered by an active exception', () => {
    const inventory = [
      { source: 'npm', name: 'allowed', version: '1.0.0', license: 'MIT' },
      { source: 'go', name: 'exceptional', version: '1.0.0', license: 'Custom' },
    ];
    const exceptions = {
      allowedLicenses: ['MIT'],
      licenseExceptions: [
        {
          source: 'go',
          name: 'exceptional',
          license: 'Custom',
          reason: 'Reviewed fixture license.',
          expires: '2099-01-01',
        },
      ],
    };

    expect(auditLicenseInventory(inventory, exceptions, new Date('2026-07-20'))).toEqual([]);
  });

  it('fails expired license exceptions', () => {
    expect(
      auditLicenseInventory(
        [{ source: 'npm', name: 'old-lib', version: '1.0.0', license: 'Custom' }],
        {
          allowedLicenses: ['MIT'],
          licenseExceptions: [
            {
              source: 'npm',
              name: 'old-lib',
              license: 'Custom',
              reason: 'Expired fixture.',
              expires: '2024-01-01',
            },
          ],
        },
        new Date('2026-07-20'),
      ),
    ).toEqual(
      expect.arrayContaining([
        'npm package old-lib@1.0.0 uses unreviewed license Custom',
        'license exception for npm:old-lib Custom is expired or has no valid expires date',
      ]),
    );
  });

  it('classifies real OSV CVSS vector severities and fails closed on malformed scores', () => {
    expect(
      highestGoSeverity({
        severity: [
          {
            type: 'CVSS_V3',
            score: 'CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H',
          },
        ],
      }),
    ).toBe('critical');

    expect(
      highestGoSeverity({
        severity: [{ type: 'CVSS_V4', score: 'CVSS:4.0/unknown-fixture' }],
      }),
    ).toBe('high');
  });

  it('requires the agentico protocol scheme under electron-builder protocols', () => {
    expect(
      hasElectronBuilderProtocolScheme(
        [
          'appId: com.doordash.agentico',
          'protocols:',
          '  - name: Agentico',
          '    schemes:',
          '      - "agentico" # app protocol',
        ].join('\n'),
        'agentico',
      ),
    ).toBe(true);

    expect(
      hasElectronBuilderProtocolScheme(
        [
          'appId: com.doordash.agentico',
          'protocols:',
          '  - name: Agentico',
          '    schemes:',
          '      - other',
        ].join('\n'),
        'agentico',
      ),
    ).toBe(false);
  });
});
