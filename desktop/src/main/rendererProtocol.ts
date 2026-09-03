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

import { pathToFileURL } from 'node:url';
import { resolveWithinRoot } from './security';

export const RENDERER_SCHEME = 'agentico-app';
export const RENDERER_HOST = 'bundle';
export const RENDERER_ORIGIN = `${RENDERER_SCHEME}://${RENDERER_HOST}`;
export const RENDERER_ENTRY_URL = `${RENDERER_ORIGIN}/index.html`;

interface ProtocolRequest {
  url: string;
}

interface ProtocolLike {
  handle(
    scheme: string,
    handler: (request: ProtocolRequest) => Promise<Response>,
  ): void | Promise<void>;
}

export function resolveRendererRequest(rendererRoot: string, requestUrl: string): string | null {
  let parsed: URL;
  try {
    parsed = new URL(requestUrl);
  } catch {
    return null;
  }
  if (
    parsed.protocol !== `${RENDERER_SCHEME}:` ||
    parsed.hostname !== RENDERER_HOST ||
    parsed.username !== '' ||
    parsed.password !== '' ||
    parsed.port !== ''
  ) {
    return null;
  }
  const requestPath = parsed.pathname === '/' ? 'index.html' : parsed.pathname.replace(/^\/+/, '');
  return resolveWithinRoot(rendererRoot, requestPath);
}

/** Serve the packaged renderer without granting legacy file:// privileges. */
export function installRendererProtocol(
  protocol: ProtocolLike,
  rendererRoot: string,
  fetchFile: (fileUrl: string) => Promise<Response>,
): void {
  void protocol.handle(RENDERER_SCHEME, async (request) => {
    const filePath = resolveRendererRequest(rendererRoot, request.url);
    if (filePath === null) {
      return new Response('Not found', { status: 404 });
    }
    return fetchFile(pathToFileURL(filePath).toString());
  });
}
