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

// Stage the matching Go server binary + build-identity.json for packaging.
//
// Builds ./cmd/agentico for the electron-builder target of the current host
// OS (macOS: a lipo'd universal binary; Linux: the target arch, default the
// host's, overridable with AGENTICO_PACKAGE_ARCH=x64|arm64 for cross-arch
// package construction) into desktop/resources/bin/, then writes
// desktop/resources/build-identity.json. electron-builder maps
// desktop/resources/ -> <package>/resources/ via extraResources, which is
// exactly where the packaged app's server resolution
// (src/main/gateway/resources.ts) looks: process.resourcesPath/bin/agentico.
//
// Version/revision identity mirrors the Makefile: the raw `git describe
// --tags --always --dirty` string is injected via
// -X github.com/doordash-oss/agentic-orchestrator/internal/buildinfo.version and
// recorded as server_version; server_revision is the full HEAD SHA the Go
// toolchain also stamps as vcs.revision. built_at prefers SOURCE_DATE_EPOCH,
// falling back to the HEAD commit time, so identical trees produce identical
// identity files.
import { execFileSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { isMainModule } from './lib/main-entry.mjs';
import { cleanGoEnvironment } from './release-workspace.mjs';

import {
  createBuildIdentity,
  parseCompatibilitySchemaVersion,
  parseOpenApiInfoVersion,
} from './lib/identity.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const repoRoot = dirname(desktopDir);
const resourcesDir = join(desktopDir, 'resources');
const binDir = join(resourcesDir, 'bin');

const LDFLAGS_VERSION_SYMBOL =
  'github.com/doordash-oss/agentic-orchestrator/internal/buildinfo.version';
// Injected explicitly (like the Makefile) because the Go toolchain does not
// stamp vcs.revision when building from a linked git worktree.
const LDFLAGS_REVISION_SYMBOL =
  'github.com/doordash-oss/agentic-orchestrator/internal/buildinfo.revision';
/** electron-builder arch name -> GOARCH. */
const GOARCH_BY_ARCH = { x64: 'amd64', arm64: 'arm64' };

function git(...args) {
  return execFileSync('git', args, { cwd: repoRoot, encoding: 'utf8' }).trim();
}

function goBuild({ goos, goarch, version, revision, output }) {
  console.log(`prepare-server: go build GOOS=${goos} GOARCH=${goarch} -> ${output}`);
  execFileSync(
    'go',
    [
      'build',
      '-mod=readonly',
      '-trimpath',
      '-ldflags',
      `-s -w -X ${LDFLAGS_VERSION_SYMBOL}=${version} -X ${LDFLAGS_REVISION_SYMBOL}=${revision}`,
      '-o',
      output,
      './cmd/agentico',
    ],
    {
      cwd: repoRoot,
      stdio: 'inherit',
      env: {
        ...cleanGoEnvironment(process.env),
        GOOS: goos,
        GOARCH: goarch,
        CGO_ENABLED: '0',
      },
    },
  );
}

function resolveTarget() {
  const platform = process.platform;
  if (platform === 'darwin') {
    if (process.env.AGENTICO_PACKAGE_ARCH !== undefined) {
      throw new Error('AGENTICO_PACKAGE_ARCH is linux-only; macOS packages are always universal');
    }
    return { os: 'darwin', arch: 'universal' };
  }
  if (platform === 'linux') {
    const arch = process.env.AGENTICO_PACKAGE_ARCH ?? (process.arch === 'arm64' ? 'arm64' : 'x64');
    if (!(arch in GOARCH_BY_ARCH)) {
      throw new Error(`unsupported AGENTICO_PACKAGE_ARCH=${arch}; expected x64 or arm64`);
    }
    return { os: 'linux', arch };
  }
  throw new Error(`unsupported packaging host platform: ${platform}`);
}

export function main() {
  const target = resolveTarget();
  const version = git('describe', '--tags', '--always', '--dirty');
  const revision = git('rev-parse', 'HEAD');
  const sourceDateEpoch = process.env.SOURCE_DATE_EPOCH;
  const builtAt =
    sourceDateEpoch !== undefined && sourceDateEpoch !== ''
      ? new Date(Number.parseInt(sourceDateEpoch, 10) * 1000).toISOString()
      : git('show', '-s', '--format=%cI', 'HEAD');

  rmSync(resourcesDir, { recursive: true, force: true });
  mkdirSync(binDir, { recursive: true });
  const binaryPath = join(binDir, 'agentico');

  if (target.os === 'darwin') {
    // A true universal binary: build both slices, then lipo them together so
    // the same file ships in both per-arch app bundles electron-builder
    // merges into the universal DMG.
    const sliceDir = mkdtempSync(join(tmpdir(), 'agentico-universal-'));
    try {
      const arm64 = join(sliceDir, 'agentico-arm64');
      const amd64 = join(sliceDir, 'agentico-amd64');
      goBuild({ goos: 'darwin', goarch: 'arm64', version, revision, output: arm64 });
      goBuild({ goos: 'darwin', goarch: 'amd64', version, revision, output: amd64 });
      execFileSync('lipo', ['-create', '-output', binaryPath, arm64, amd64], {
        stdio: 'inherit',
      });
    } finally {
      rmSync(sliceDir, { recursive: true, force: true });
    }
  } else {
    goBuild({
      goos: 'linux',
      goarch: GOARCH_BY_ARCH[target.arch],
      version,
      revision,
      output: binaryPath,
    });
  }
  chmodSync(binaryPath, 0o755);

  const identity = createBuildIdentity({
    // Release builds stamp the tag version through package-build.mjs so the
    // identity file matches the version the packaged app reports.
    desktop_version:
      process.env.AGENTICO_DESKTOP_VERSION ??
      JSON.parse(readFileSync(join(desktopDir, 'package.json'), 'utf8')).version,
    api_version: parseOpenApiInfoVersion(
      readFileSync(join(repoRoot, 'api', 'openapi.yaml'), 'utf8'),
    ),
    schema_version: parseCompatibilitySchemaVersion(
      readFileSync(join(repoRoot, 'internal', 'server', 'compatibility.go'), 'utf8'),
    ),
    server_version: version,
    server_revision: revision,
    os: target.os,
    arch: target.arch,
    built_at: builtAt,
  });
  const identityPath = join(resourcesDir, 'build-identity.json');
  writeFileSync(identityPath, `${JSON.stringify(identity, null, 2)}\n`);
  console.log(`prepare-server: staged ${binaryPath}`);
  console.log(`prepare-server: wrote ${identityPath}`);
  console.log(JSON.stringify(identity, null, 2));
}

if (isMainModule(import.meta.url)) main();
