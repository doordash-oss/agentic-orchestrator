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

import path from 'node:path';
import { describe, expect, it, vi } from 'vitest';

import {
  RENDERER_ORIGIN,
  installRendererProtocol,
  resolveRendererRequest,
} from '../rendererProtocol';

describe('packaged renderer protocol', () => {
  it('maps only bundle-host requests inside the renderer root', () => {
    const root = path.resolve('/app/out/renderer');

    expect(resolveRendererRequest(root, `${RENDERER_ORIGIN}/index.html`)).toBe(
      path.join(root, 'index.html'),
    );
    expect(resolveRendererRequest(root, `${RENDERER_ORIGIN}/assets/app.js?v=1`)).toBe(
      path.join(root, 'assets/app.js'),
    );
    expect(resolveRendererRequest(root, `${RENDERER_ORIGIN}/`)).toBe(path.join(root, 'index.html'));
    expect(resolveRendererRequest(root, 'agentico-app://other/index.html')).toBeNull();
    expect(resolveRendererRequest(root, `${RENDERER_ORIGIN}/%2e%2e%2fmain/index.js`)).toBeNull();
    expect(resolveRendererRequest(root, 'https://bundle/index.html')).toBeNull();
  });

  it('serves resolved files through Electron net.fetch and rejects invalid requests', async () => {
    let handler: ((request: { url: string }) => Promise<Response>) | undefined;
    const protocol = {
      handle: vi.fn((scheme: string, registered: typeof handler) => {
        expect(scheme).toBe('agentico-app');
        handler = registered;
      }),
    };
    const fetchFile = vi.fn(async () => new Response('renderer'));
    installRendererProtocol(protocol, '/app/out/renderer', fetchFile);

    expect(handler).toBeDefined();
    const loaded = await handler!({ url: `${RENDERER_ORIGIN}/index.html` });
    expect(await loaded.text()).toBe('renderer');
    expect(fetchFile).toHaveBeenCalledWith('file:///app/out/renderer/index.html');

    const rejected = await handler!({
      url: `${RENDERER_ORIGIN}/%2e%2e%2fmain/index.js`,
    });
    expect(rejected.status).toBe(404);
  });
});
