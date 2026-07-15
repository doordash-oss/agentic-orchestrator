import { describe, expect, it, vi } from 'vitest';
import { installSecurityPolicies } from '../../src/main/security';

type Handler = (...args: unknown[]) => unknown;

function makeFakes() {
  const appHandlers = new Map<string, Handler>();
  const app = {
    on: vi.fn((name: string, handler: Handler) => {
      appHandlers.set(name, handler);
    }),
  };

  const contentsHandlers = new Map<string, Handler>();
  let windowOpenHandler: Handler | null = null;
  const contents = {
    on: vi.fn((name: string, handler: Handler) => {
      contentsHandlers.set(name, handler);
    }),
    setWindowOpenHandler: vi.fn((handler: Handler) => {
      windowOpenHandler = handler;
    }),
    getURL: () => 'http://localhost:5173/index.html',
  };

  let permissionHandler: Handler | null = null;
  let headersHandler: Handler | null = null;
  const session = {
    setPermissionRequestHandler: vi.fn((handler: Handler) => {
      permissionHandler = handler;
    }),
    webRequest: {
      onHeadersReceived: vi.fn((handler: Handler) => {
        headersHandler = handler;
      }),
    },
  };

  const openExternal = vi.fn();

  return {
    app,
    session,
    contents,
    openExternal,
    appHandlers,
    contentsHandlers,
    getWindowOpenHandler: () => windowOpenHandler,
    getPermissionHandler: () => permissionHandler,
    getHeadersHandler: () => headersHandler,
  };
}

function install(f: ReturnType<typeof makeFakes>) {
  installSecurityPolicies({
    app: f.app as never,
    session: f.session as never,
    appOrigins: new Set(['http://localhost:5173', 'file://']),
  });
  const created = f.appHandlers.get('web-contents-created');
  expect(created).toBeDefined();
  created!({}, f.contents);
}

describe('installSecurityPolicies', () => {
  it('denies navigation away from the app origin', () => {
    const f = makeFakes();
    install(f);
    const willNavigate = f.contentsHandlers.get('will-navigate');
    expect(willNavigate).toBeDefined();

    const denied = { preventDefault: vi.fn() };
    willNavigate!(denied, 'https://evil.example.com/');
    expect(denied.preventDefault).toHaveBeenCalledOnce();

    const allowed = { preventDefault: vi.fn() };
    willNavigate!(allowed, 'http://localhost:5173/route');
    expect(allowed.preventDefault).not.toHaveBeenCalled();
  });

  it('denies webview attachment', () => {
    const f = makeFakes();
    install(f);
    const willAttach = f.contentsHandlers.get('will-attach-webview');
    expect(willAttach).toBeDefined();
    const evt = { preventDefault: vi.fn() };
    willAttach!(evt, {}, {});
    expect(evt.preventDefault).toHaveBeenCalledOnce();
  });

  it('denies all window creation', () => {
    const f = makeFakes();
    install(f);
    const handler = f.getWindowOpenHandler();
    expect(handler).toBeDefined();
    expect(handler!({ url: 'https://github.com/anything' })).toEqual({ action: 'deny' });
    expect(handler!({ url: 'http://localhost:5173/' })).toEqual({ action: 'deny' });
  });

  it('denies all permission requests', () => {
    const f = makeFakes();
    install(f);
    const handler = f.getPermissionHandler();
    expect(handler).toBeDefined();
    const callback = vi.fn();
    handler!(f.contents, 'media', callback);
    expect(callback).toHaveBeenCalledWith(false);
    handler!(f.contents, 'notifications', callback);
    expect(callback).toHaveBeenLastCalledWith(false);
  });

  it('injects a strict CSP on responses', () => {
    const f = makeFakes();
    install(f);
    const handler = f.getHeadersHandler();
    expect(handler).toBeDefined();
    const callback = vi.fn();
    handler!({ responseHeaders: { 'X-Existing': ['1'] } }, callback);
    const arg = callback.mock.calls[0]![0] as {
      responseHeaders: Record<string, string[]>;
    };
    const csp = arg.responseHeaders['Content-Security-Policy'];
    expect(csp).toBeDefined();
    expect(csp![0]).toContain("default-src 'self'");
  });
});
