import { describe, expect, it } from 'vitest';

import { buildFrozenManifest, verifyCutover } from './verify-cutover.mjs';

const matrix = `# Desktop Parity Matrix

## Operations

| Capability | Prior behavior | Desktop interaction | Authoritative contract | Platform scope | Automated evidence | Status |
| --- | --- | --- | --- | --- | --- | --- |
| Watch work | attached terminal view | Open Live activity from the feature cockpit | GET /api/v1/features/{feature_id}/sessions | macOS+Linux | \`desktop/test/e2e/watch.spec.ts\` | delivered |
`;

function completeFixtureManifest() {
  const manifest = buildFrozenManifest(matrix, 'baseline-revision');
  const rowID = manifest.rows[0].id;
  for (const category of Object.keys(manifest.auditCoverage)) {
    manifest.auditCoverage[category] = [rowID];
  }
  return manifest;
}

describe('cutover ledger verifier', () => {
  it('accepts a complete row matching the frozen baseline', () => {
    const manifest = completeFixtureManifest();
    expect(
      verifyCutover({
        matrixText: matrix,
        manifest,
        evidenceExists: (path) => path === 'desktop/test/e2e/watch.spec.ts',
        residueFiles: [],
      }),
    ).toStrictEqual([]);
  });

  it('rejects weakened frozen cells and incomplete delivery evidence', () => {
    const manifest = completeFixtureManifest();
    const weakened = matrix
      .replace('Open Live activity from the feature cockpit', 'pending')
      .replace('`desktop/test/e2e/watch.spec.ts`', '—')
      .replace('| delivered |', '| waived |');
    const failures = verifyCutover({
      matrixText: weakened,
      manifest,
      evidenceExists: () => false,
      residueFiles: [],
    });
    expect(failures).toEqual(
      expect.arrayContaining([
        expect.stringContaining('desktop interaction is incomplete'),
        expect.stringContaining('automated evidence is blank'),
        expect.stringContaining('status is not fully delivered'),
        expect.stringContaining(
          'frozen capability, prior behavior, interaction, contract, or platform cells changed',
        ),
      ]),
    );
  });

  it('rejects stale evidence, duplicate rows, and terminal-client residue', () => {
    const manifest = completeFixtureManifest();
    const duplicated = `${matrix}${matrix.split('\n').find((line) => line.startsWith('| Watch work'))}\n`;
    const failures = verifyCutover({
      matrixText: duplicated,
      manifest,
      evidenceExists: () => false,
      residueFiles: [
        { path: ['internal', 'tui', 'app.go'].join('/'), content: '' },
        { path: 'config.yaml', content: ['ui', 'keyboard', 'layout'].join('_') },
      ],
    });
    expect(failures).toEqual(
      expect.arrayContaining([
        expect.stringContaining('duplicate capability'),
        expect.stringContaining('evidence does not exist'),
        expect.stringContaining('retired terminal-client path'),
        expect.stringContaining('retired token'),
      ]),
    );
  });

  it('rejects an incomplete historical audit taxonomy', () => {
    const manifest = completeFixtureManifest();
    manifest.auditCoverage = { navigation: [manifest.rows[0].id] };
    const failures = verifyCutover({
      matrixText: matrix,
      manifest,
      evidenceExists: () => true,
      residueFiles: [],
    });
    expect(failures).toEqual(
      expect.arrayContaining([expect.stringContaining('audit category creation is missing')]),
    );
  });

  it('keeps pipes inside inline-code evidence in one cell', () => {
    const withPipe = matrix.replace(
      '`desktop/test/e2e/watch.spec.ts`',
      "`go test ./test/integration/... -run 'Watch|Logs'`",
    );
    const manifest = completeFixtureManifest();
    manifest.rows[0].frozenHash = buildFrozenManifest(
      withPipe,
      'baseline-revision',
    ).rows[0].frozenHash;
    expect(
      verifyCutover({
        matrixText: withPipe,
        manifest,
        evidenceExists: () => true,
        residueFiles: [],
      }).some((failure) => failure.includes('columns')),
    ).toBe(false);
  });
});
