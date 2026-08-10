import { describe, expect, it } from 'vitest';

import {
  REQUIRED_RELEASE_ASSETS,
  checkRemoteTagPreflight,
  githubRequest,
  verifyReleasePublication,
} from './verify-release-publication.mjs';

const TAG = 'v0.150.0';
const COMMIT = 'a'.repeat(40);
const ANNOTATED_TAG = 'b'.repeat(40);

function route(responses) {
  return async (path) => responses[path] ?? { status: 500, data: { message: `unhandled ${path}` } };
}

function releasedAssets() {
  return REQUIRED_RELEASE_ASSETS.map((name, index) => ({
    name: name.replace('{{VERSION}}', '0.150.0'),
    state: 'uploaded',
    browser_download_url: `https://example.test/releases/${index}`,
    size: index + 1,
  }));
}

describe('remote release publication verification', () => {
  it('allows an absent remote tag during the pre-publish check', async () => {
    await expect(
      checkRemoteTagPreflight({
        tag: TAG,
        request: route({
          '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
            status: 404,
            data: { message: 'Not Found' },
          },
        }),
      }),
    ).resolves.toEqual({ tag: TAG, absent: true });
  });

  it('rejects an existing remote tag before GoReleaser can publish', async () => {
    await expect(
      checkRemoteTagPreflight({
        tag: TAG,
        request: route({
          '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
            status: 200,
            data: { object: { type: 'commit', sha: COMMIT } },
          },
        }),
      }),
    ).rejects.toThrow(`remote tag ${TAG} already exists`);
  });

  it('does not treat authentication or transport failures as an absent tag', async () => {
    await expect(
      checkRemoteTagPreflight({
        tag: TAG,
        request: route({
          '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
            status: 401,
            data: { message: 'Bad credentials' },
          },
        }),
      }),
    ).rejects.toThrow(/GitHub API .*401/);
  });

  it('requires GITHUB_TOKEN before it makes a real GitHub API request', async () => {
    await expect(
      githubRequest('/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0', {
        token: '',
        fetchImpl: () => {
          throw new Error('must not fetch');
        },
      }),
    ).rejects.toThrow(/GITHUB_TOKEN is required/);
  });

  it('dereferences an annotated remote tag and verifies its published assets', async () => {
    const result = await verifyReleasePublication({
      tag: TAG,
      commit: COMMIT,
      request: route({
        '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
          status: 200,
          data: { object: { type: 'tag', sha: ANNOTATED_TAG } },
        },
        [`/repos/doordash-oss/agentic-orchestrator/git/tags/${ANNOTATED_TAG}`]: {
          status: 200,
          data: { object: { type: 'commit', sha: COMMIT } },
        },
        '/repos/doordash-oss/agentic-orchestrator/releases/tags/v0.150.0': {
          status: 200,
          data: {
            tag_name: TAG,
            draft: false,
            prerelease: false,
            published_at: '2026-08-10T00:00:00Z',
            assets: releasedAssets(),
          },
        },
      }),
    });

    expect(result).toEqual({
      tag: TAG,
      commit: COMMIT,
      assets: releasedAssets().map(({ name }) => name),
    });
  });

  it('rejects a remote tag pointing at a different commit', async () => {
    await expect(
      verifyReleasePublication({
        tag: TAG,
        commit: COMMIT,
        request: route({
          '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
            status: 200,
            data: { object: { type: 'commit', sha: 'c'.repeat(40) } },
          },
        }),
      }),
    ).rejects.toThrow(/dereferences to .* expected/);
  });

  it('rejects drafts, prereleases, and incomplete asset inventories', async () => {
    const common = {
      '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
        status: 200,
        data: { object: { type: 'commit', sha: COMMIT } },
      },
    };
    const releases = [
      [
        {
          draft: true,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets(),
        },
        /draft/,
      ],
      [
        {
          draft: false,
          prerelease: true,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets(),
        },
        /prerelease/,
      ],
      [
        {
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets().filter(({ name }) => name !== 'checksums.txt.sig'),
        },
        /checksums\.txt\.sig/,
      ],
    ];
    for (const [release, expected] of releases) {
      const responses = {
        ...common,
        '/repos/doordash-oss/agentic-orchestrator/releases/tags/v0.150.0': {
          status: 200,
          data: {
            tag_name: TAG,
            ...release,
          },
        },
      };
      await expect(
        verifyReleasePublication({ tag: TAG, commit: COMMIT, request: route(responses) }),
      ).rejects.toThrow(expected);
    }
  });

  it('fails closed on malformed release data and assets that have not finished uploading', async () => {
    const common = {
      '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
        status: 200,
        data: { object: { type: 'commit', sha: COMMIT } },
      },
    };
    const invalidReleases = [
      [null, /response must be an object/],
      ['not an object', /response must be an object/],
      [
        {
          tag_name: TAG,
          draft: 'false',
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets(),
        },
        /draft must be exactly false/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: null,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets(),
        },
        /prerelease must be exactly false/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: false,
          published_at: 'not a date',
          assets: releasedAssets(),
        },
        /published_at must be a valid timestamp/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: null,
        },
        /assets must be an array/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: [...releasedAssets(), null],
        },
        /assets must contain only objects/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets().map((asset) =>
            asset.name === 'checksums.txt.sig' ? { ...asset, state: 'new' } : asset,
          ),
        },
        /checksums\.txt\.sig is new, expected uploaded/,
      ],
      [
        {
          tag_name: TAG,
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets().map((asset) =>
            asset.name === 'checksums.txt'
              ? { ...asset, browser_download_url: '', size: 0 }
              : asset,
          ),
        },
        /checksums\.txt has no nonempty browser_download_url/,
      ],
    ];
    for (const [release, expected] of invalidReleases) {
      await expect(
        verifyReleasePublication({
          tag: TAG,
          commit: COMMIT,
          request: route({
            ...common,
            '/repos/doordash-oss/agentic-orchestrator/releases/tags/v0.150.0': {
              status: 200,
              data: release,
            },
          }),
        }),
      ).rejects.toThrow(expected);
    }
  });
});
