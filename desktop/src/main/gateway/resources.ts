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

/**
 * Bundled-server binary resolution. Pure candidate computation plus an
 * injectable existence/executability probe so tests can cover packaged
 * layouts (macOS Contents/Resources, Linux AppImage/deb resources/),
 * development fallbacks, read-only installation roots, and paths containing
 * spaces or non-ASCII characters with real temp directories.
 */
import fs from 'node:fs';
import path from 'node:path';

export interface ResourceContext {
  platform: NodeJS.Platform;
  isPackaged: boolean;
  /** Electron's process.resourcesPath (meaningful when packaged). */
  resourcesPath: string;
  /** Repository root in development builds. */
  appRoot: string;
  env: Readonly<Record<string, string | undefined>>;
}

const BINARY_NAME = 'agentico';

function binaryName(platform: NodeJS.Platform): string {
  return platform === 'win32' ? `${BINARY_NAME}.exe` : BINARY_NAME;
}

/**
 * Ordered candidate paths for the bundled server binary. Packaged builds
 * look only inside application resources (never at env overrides); dev
 * builds honor AGENTICO_SERVER_BIN and then the repo's bin/ output.
 */
export function serverBinaryCandidates(ctx: ResourceContext): string[] {
  const exe = binaryName(ctx.platform);
  if (ctx.isPackaged) {
    return [path.join(ctx.resourcesPath, 'bin', exe), path.join(ctx.resourcesPath, exe)];
  }
  const candidates: string[] = [];
  const override = ctx.env['AGENTICO_SERVER_BIN'];
  if (override !== undefined && override !== '') {
    candidates.push(override);
  }
  candidates.push(path.join(ctx.appRoot, 'bin', exe));
  return candidates;
}

export interface ResolveDeps {
  isExecutableFile(candidate: string): boolean;
}

export type ResolveResult = { ok: true; path: string } | { ok: false; tried: string[] };

export function resolveServerBinary(ctx: ResourceContext, deps: ResolveDeps): ResolveResult {
  const candidates = serverBinaryCandidates(ctx);
  for (const candidate of candidates) {
    if (deps.isExecutableFile(candidate)) {
      return { ok: true, path: candidate };
    }
  }
  return { ok: false, tried: candidates };
}

/** Production probe: a regular file this process may execute. */
export function fileIsExecutable(candidate: string): boolean {
  try {
    fs.accessSync(candidate, fs.constants.X_OK);
    return fs.statSync(candidate).isFile();
  } catch {
    return false;
  }
}
