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
  REQUIRED_RELEASE_ASSETS,
  reserveRemoteTag,
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
    digest: `sha256:${String(index + 1)
      .repeat(64)
      .slice(0, 64)}`,
  }));
}

function publishedDigests() {
  return Object.fromEntries(releasedAssets().map(({ name, digest }) => [name, digest.slice(7)]));
}

describe('remote release publication verification', () => {
  it('atomically reserves an absent remote lightweight tag at the captured commit', async () => {
    const calls = [];
    await expect(
      reserveRemoteTag({
        tag: TAG,
        commit: COMMIT,
        request: async (path, options) => {
          calls.push({ path, options });
          return { status: 201, data: { object: { type: 'commit', sha: COMMIT } } };
        },
      }),
    ).resolves.toEqual({ tag: TAG, commit: COMMIT, created: true });
    expect(calls[0]).toMatchObject({
      path: '/repos/doordash-oss/agentic-orchestrator/git/refs',
      options: { method: 'POST', body: { ref: 'refs/tags/v0.150.0', sha: COMMIT } },
    });
  });

  it('accepts a tag-creation race only when the winner points at the captured commit', async () => {
    let calls = 0;
    await expect(
      reserveRemoteTag({
        tag: TAG,
        commit: COMMIT,
        request: async (path) => {
          calls += 1;
          if (calls === 1) return { status: 422, data: { message: 'Reference already exists' } };
          expect(path).toContain('/git/ref/tags/');
          return { status: 200, data: { object: { type: 'commit', sha: COMMIT } } };
        },
      }),
    ).resolves.toEqual({ tag: TAG, commit: COMMIT, created: false });
  });

  it('rejects a reservation race won by another commit and propagates non-race failures', async () => {
    await expect(
      reserveRemoteTag({
        tag: TAG,
        commit: COMMIT,
        request: async (path) =>
          path.endsWith('/git/refs')
            ? { status: 422, data: { message: 'Reference already exists' } }
            : { status: 200, data: { object: { type: 'commit', sha: 'c'.repeat(40) } } },
      }),
    ).rejects.toThrow(/reserved by another commit/);
    await expect(
      reserveRemoteTag({
        tag: TAG,
        commit: COMMIT,
        request: async () => ({ status: 401, data: { message: 'Bad credentials' } }),
      }),
    ).rejects.toThrow(/returned 401/);
    await expect(
      reserveRemoteTag({
        tag: TAG,
        commit: COMMIT,
        request: async (path) =>
          path.endsWith('/git/refs')
            ? { status: 422, data: { message: 'Reference already exists' } }
            : { status: 404, data: { message: 'Not Found' } },
      }),
    ).rejects.toThrow(/reserved by another commit/);
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
      expectedDigests: publishedDigests(),
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
        expectedDigests: publishedDigests(),
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
      [
        {
          draft: false,
          prerelease: false,
          published_at: '2026-08-10T00:00:00Z',
          assets: releasedAssets().filter(({ name }) => name !== 'desktop-release.json'),
        },
        /desktop-release\.json/,
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
        verifyReleasePublication({
          tag: TAG,
          commit: COMMIT,
          expectedDigests: publishedDigests(),
          request: route(responses),
        }),
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
          expectedDigests: publishedDigests(),
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

  it('fails closed on absent, malformed, or locally mismatched GitHub asset digests', async () => {
    const base = releasedAssets();
    for (const assets of [
      base.map((asset) =>
        asset.name === 'Agentico-x64.AppImage' ? { ...asset, digest: null } : asset,
      ),
      base.map((asset) =>
        asset.name === 'checksums.txt' ? { ...asset, digest: 'sha256:not-a-digest' } : asset,
      ),
      base.map((asset) =>
        asset.name === 'checksums.txt.sig'
          ? { ...asset, digest: `sha256:${'f'.repeat(64)}` }
          : asset,
      ),
    ]) {
      await expect(
        verifyReleasePublication({
          tag: TAG,
          commit: COMMIT,
          expectedDigests: publishedDigests(),
          request: route({
            '/repos/doordash-oss/agentic-orchestrator/git/ref/tags/v0.150.0': {
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
                assets,
              },
            },
          }),
        }),
      ).rejects.toThrow(/digest/);
    }
  });
});
