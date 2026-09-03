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

// Unit coverage for the packaged-layout helpers in package-layout.mjs.
import { describe, expect, it } from 'vitest';

import { findDisallowedFontAssets, unpackedExecutablePath } from './package-layout.mjs';

describe('unpackedExecutablePath', () => {
  it('resolves the universal macOS app binary', () => {
    expect(unpackedExecutablePath('/repo/desktop', 'darwin', 'arm64')).toBe(
      '/repo/desktop/dist/mac-universal/Agentico.app/Contents/MacOS/Agentico',
    );
  });

  it('resolves per-arch Linux unpacked dirs', () => {
    expect(unpackedExecutablePath('/repo/desktop', 'linux', 'x64')).toBe(
      '/repo/desktop/dist/linux-unpacked/agentico',
    );
    expect(unpackedExecutablePath('/repo/desktop', 'linux', 'arm64')).toBe(
      '/repo/desktop/dist/linux-arm64-unpacked/agentico',
    );
  });

  it('rejects unsupported hosts', () => {
    expect(() => unpackedExecutablePath('/repo/desktop', 'win32', 'x64')).toThrow(
      /unsupported packaged app host: win32/,
    );
  });
});

describe('findDisallowedFontAssets', () => {
  it('accepts a system-text-faces-only payload', () => {
    expect(
      findDisallowedFontAssets([
        '/out/main/index.js',
        '/out/preload/index.cjs',
        '/out/renderer/index.html',
        '/out/renderer/assets/index-abc123.css',
        '/out/renderer/assets/index-abc123.js',
      ]),
    ).toEqual([]);
  });

  it('permits only monaco-editor codicon glyph fonts', () => {
    expect(
      findDisallowedFontAssets([
        '/node_modules/monaco-editor/esm/vs/base/browser/ui/codicons/codicon/codicon.ttf',
        '/out/renderer/assets/codicon-ngg6Pgfi.ttf',
      ]),
    ).toEqual([]);
    expect(findDisallowedFontAssets(['/out/renderer/assets/codicon-ngg6Pgfi.woff2'])).toEqual([
      '/out/renderer/assets/codicon-ngg6Pgfi.woff2',
    ]);
  });

  it('flags every bundled font format, anywhere in the archive', () => {
    expect(
      findDisallowedFontAssets([
        '/out/renderer/index.html',
        '/out/renderer/assets/display-latin-500-normal-abc.woff2',
        '/out/renderer/assets/text-latin-400-normal-def.woff',
        '/out/renderer/assets/mono-latin-400-normal-ghi.ttf',
        '/out/renderer/assets/legacy-jkl.otf',
        '/node_modules/some-pkg/legacy-mno.eot',
      ]),
    ).toEqual([
      '/out/renderer/assets/display-latin-500-normal-abc.woff2',
      '/out/renderer/assets/text-latin-400-normal-def.woff',
      '/out/renderer/assets/mono-latin-400-normal-ghi.ttf',
      '/out/renderer/assets/legacy-jkl.otf',
      '/node_modules/some-pkg/legacy-mno.eot',
    ]);
  });

  it('matches font extensions case-insensitively', () => {
    expect(findDisallowedFontAssets(['/out/renderer/assets/Display.WOFF2'])).toEqual([
      '/out/renderer/assets/Display.WOFF2',
    ]);
  });
});
