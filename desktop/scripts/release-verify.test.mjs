import { describe, expect, it } from 'vitest';

import { verifyLocalReleaseModel } from './release-verify.mjs';

describe('local release verification', () => {
  it('rejects a release recipe that omits provenance rechecks before GoReleaser', () => {
    expect(
      verifyLocalReleaseModel({
        makefile:
          'release:\n\tnode desktop/scripts/release-preflight.mjs\n\tnode desktop/scripts/release-goreleaser.mjs\n',
        builder: 'hardenedRuntime: true\nprotocols:\n',
        signingScript: 'ed25519\nRELEASE_PUBLIC_KEY\n',
      }),
    ).toContain(
      'release verification requires provenance verification immediately before GoReleaser',
    );
  });

  it('accepts the actual local-operator release model without protected-CI warnings', () => {
    expect(verifyLocalReleaseModel()).toEqual([]);
  });
});
