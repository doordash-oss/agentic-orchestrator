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
