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

import { join } from 'node:path';

// The renderer draws text exclusively with system faces (--bench-font-* in
// tokens.css), so no text webfont payload may ship inside app.asar.
// verify-package.mjs enforces that as a packaged invariant; this predicate is
// the shared, pure half so it can be unit-tested without building a package.
const FONT_PAYLOAD_EXTENSIONS = Object.freeze(['.woff2', '.woff', '.ttf', '.otf', '.eot']);

// The single permitted exception: monaco-editor's `codicon` glyph font, which
// is an icon set (editor gutter/widget glyphs), not a text face, and is a
// runtime dependency of the bundled editor. Vite content-hashes it into
// out/renderer/assets/, and monaco's ESM tree carries the original copy.
const ALLOWED_FONT_ASSETS = Object.freeze([/(^|\/)codicon(-[A-Za-z0-9_-]+)?\.ttf$/i]);

/**
 * Disallowed font payloads among ASAR entry paths (normalized to forward
 * slashes). Empty means the package ships system text faces only.
 */
export function findDisallowedFontAssets(entries) {
  return entries.filter(
    (entry) =>
      FONT_PAYLOAD_EXTENSIONS.some((extension) => entry.toLowerCase().endsWith(extension)) &&
      !ALLOWED_FONT_ASSETS.some((allowed) => allowed.test(entry)),
  );
}

export function unpackedExecutablePath(
  desktopDir,
  platform = process.platform,
  arch = process.arch,
) {
  if (platform === 'darwin') {
    return join(
      desktopDir,
      'dist',
      'mac-universal',
      'Agentico.app',
      'Contents',
      'MacOS',
      'Agentico',
    );
  }
  if (platform === 'linux') {
    const unpackedDir = arch === 'arm64' ? 'linux-arm64-unpacked' : 'linux-unpacked';
    return join(desktopDir, 'dist', unpackedDir, 'agentico');
  }
  throw new Error(`unsupported packaged app host: ${platform}`);
}
