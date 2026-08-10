// Verify the remote GitHub publication before the desktop cask is allowed to ship.
import { fileURLToPath } from 'node:url';

const REPOSITORY = 'doordash-oss/agentic-orchestrator';
const API_ROOT = 'https://api.github.com';
export const REQUIRED_RELEASE_ASSETS = Object.freeze([
  'Agentico-mac-universal.dmg',
  'Agentico-x64.AppImage',
  'Agentico-arm64.AppImage',
  'agentico_{{VERSION}}_amd64.deb',
  'agentico_{{VERSION}}_arm64.deb',
  'checksums.txt',
  'checksums.txt.sig',
]);

function endpoint(path) {
  return `/repos/${REPOSITORY}${path}`;
}

function tagRefPath(tag) {
  return endpoint(`/git/ref/tags/${encodeURIComponent(tag)}`);
}

function tagObjectPath(sha) {
  return endpoint(`/git/tags/${sha}`);
}

function releasePath(tag) {
  return endpoint(`/releases/tags/${encodeURIComponent(tag)}`);
}

function requiredAssetsForTag(tag) {
  const version = tag.replace(/^v/, '');
  return REQUIRED_RELEASE_ASSETS.map((name) => name.replace('{{VERSION}}', version));
}

function responseMessage(response) {
  const message = response.data?.message;
  return typeof message === 'string' && message !== '' ? `: ${message}` : '';
}

function requireStatus(response, expected, path) {
  if (response.status !== expected) {
    throw new Error(
      `GitHub API ${path} returned ${response.status}, expected ${expected}${responseMessage(response)}`,
    );
  }
  return response.data;
}

function isRecord(value) {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/** Dereference either a lightweight or annotated Git tag to its commit SHA. */
export async function dereferenceRemoteTag({ tag, request }) {
  const refPath = tagRefPath(tag);
  const ref = await request(refPath);
  if (ref.status === 404) return null;
  const refData = requireStatus(ref, 200, refPath);
  let object = refData?.object;
  const visited = new Set();
  while (object?.type === 'tag') {
    if (typeof object.sha !== 'string' || object.sha === '' || visited.has(object.sha)) {
      throw new Error(`GitHub tag ${tag} has an invalid annotated-tag chain`);
    }
    visited.add(object.sha);
    const path = tagObjectPath(object.sha);
    object = requireStatus(await request(path), 200, path)?.object;
  }
  if (object?.type !== 'commit' || typeof object.sha !== 'string' || object.sha === '') {
    throw new Error(`GitHub tag ${tag} does not dereference to a commit`);
  }
  return object.sha;
}

/** Fail safely if this version is already owned by a remote Git tag. */
export async function checkRemoteTagPreflight({ tag, request }) {
  const commit = await dereferenceRemoteTag({ tag, request });
  if (commit === null) return { tag, absent: true };
  throw new Error(
    `remote tag ${tag} already exists (dereferences to ${commit}); refusing to publish`,
  );
}

/** Verify GoReleaser published this exact local commit and a complete stable release. */
export async function verifyReleasePublication({ tag, commit, request }) {
  const remoteCommit = await dereferenceRemoteTag({ tag, request });
  if (remoteCommit === null) throw new Error(`remote tag ${tag} is missing after GoReleaser`);
  if (remoteCommit !== commit) {
    throw new Error(
      `remote tag ${tag} dereferences to ${remoteCommit}, expected captured local HEAD ${commit}`,
    );
  }

  const path = releasePath(tag);
  const release = requireStatus(await request(path), 200, path);
  if (!isRecord(release)) throw new Error(`GitHub release ${tag} response must be an object`);
  if (release.tag_name !== tag)
    throw new Error(`GitHub release tag is ${release.tag_name}, expected ${tag}`);
  if (release.draft !== false) {
    throw new Error(
      `GitHub release ${tag} draft must be exactly false, got ${String(release.draft)}`,
    );
  }
  if (release.prerelease !== false) {
    throw new Error(
      `GitHub release ${tag} prerelease must be exactly false, got ${String(release.prerelease)}`,
    );
  }
  if (
    typeof release.published_at !== 'string' ||
    release.published_at === '' ||
    Number.isNaN(Date.parse(release.published_at))
  ) {
    throw new Error(`GitHub release ${tag} published_at must be a valid timestamp`);
  }

  if (!Array.isArray(release.assets))
    throw new Error(`GitHub release ${tag} assets must be an array`);
  const assets = release.assets;
  if (assets.some((asset) => !isRecord(asset))) {
    throw new Error(`GitHub release ${tag} assets must contain only objects`);
  }
  const required = requiredAssetsForTag(tag);
  const invalid = required.flatMap((name) => {
    const matching = assets.filter((asset) => isRecord(asset) && asset.name === name);
    if (matching.length !== 1) {
      return [
        `GitHub release ${tag} has ${matching.length} assets named ${name}, expected exactly 1`,
      ];
    }
    const [asset] = matching;
    if (asset.state !== 'uploaded') {
      return [`GitHub release ${tag} asset ${name} is ${String(asset.state)}, expected uploaded`];
    }
    if (typeof asset.browser_download_url !== 'string' || asset.browser_download_url === '') {
      return [`GitHub release ${tag} asset ${name} has no nonempty browser_download_url`];
    }
    if (!Number.isSafeInteger(asset.size) || asset.size <= 0) {
      return [`GitHub release ${tag} asset ${name} has invalid size ${String(asset.size)}`];
    }
    return [];
  });
  if (invalid.length > 0) throw new Error(invalid.join('; '));
  return { tag, commit, assets: required };
}

/** GitHub REST boundary; tests inject `request` and never make network calls. */
export async function githubRequest(
  path,
  { fetchImpl = globalThis.fetch, token = process.env.GITHUB_TOKEN } = {},
) {
  if (typeof token !== 'string' || token === '') {
    throw new Error('GITHUB_TOKEN is required for GitHub release publication verification');
  }
  let response;
  try {
    response = await fetchImpl(`${API_ROOT}${path}`, {
      headers: {
        Accept: 'application/vnd.github+json',
        Authorization: `Bearer ${token}`,
        'X-GitHub-Api-Version': '2022-11-28',
      },
    });
  } catch (error) {
    throw new Error(
      `GitHub API request ${path} failed: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  let data = {};
  try {
    data = await response.json();
  } catch {
    // Status remains authoritative; the caller will report it rather than masking it as a parse failure.
  }
  return { status: response.status, data };
}

function parseCli(args) {
  const [mode, ...flags] = args;
  if (!['preflight', 'verify'].includes(mode) || flags.length !== 4) return null;
  const values = {};
  for (let index = 0; index < flags.length; index += 2) {
    const [flag, value] = [flags[index], flags[index + 1]];
    if (
      !['--tag', '--commit'].includes(flag) ||
      value === undefined ||
      values[flag] !== undefined
    ) {
      return null;
    }
    values[flag] = value;
  }
  if (typeof values['--tag'] !== 'string' || typeof values['--commit'] !== 'string') return null;
  return { mode, tag: values['--tag'], commit: values['--commit'] };
}

export async function runReleasePublicationCli({
  args = process.argv.slice(2),
  request = githubRequest,
} = {}) {
  const parsed = parseCli(args);
  if (parsed === null) {
    return {
      status: 1,
      message:
        'usage: verify-release-publication.mjs <preflight|verify> --tag vX.Y.Z --commit <sha>',
    };
  }
  try {
    if (parsed.mode === 'preflight') {
      await checkRemoteTagPreflight({ tag: parsed.tag, request });
      return { status: 0, message: `release publication preflight: ${parsed.tag} is available` };
    }
    await verifyReleasePublication({ tag: parsed.tag, commit: parsed.commit, request });
    return { status: 0, message: `release publication verification: ${parsed.tag} is published` };
  } catch (error) {
    return {
      status: 1,
      message: `release publication verification failed: ${error instanceof Error ? error.message : String(error)}`,
    };
  }
}

async function main() {
  const result = await runReleasePublicationCli();
  if (result.status !== 0) {
    console.error(result.message);
    process.exitCode = 1;
    return;
  }
  console.log(result.message);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) void main();
