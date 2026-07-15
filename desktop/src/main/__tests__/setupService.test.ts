import { describe, expect, it, vi } from 'vitest';
import { SafeErrorException } from '../../shared/errors';
import { ReadinessSnapshotSchema } from '../../shared/ipc';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';
import { SetupService, type SetupServiceDeps } from '../setup';

function serverReadiness(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    ready: false,
    probed_at: '2026-07-14T10:00:00Z',
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue: {
          code: 'unauthenticated',
          message: 'The claude CLI is not authenticated.',
          remedy: 'claude login',
        },
      },
      { name: 'codex', installed: false, ready: false },
    ],
    models: { available: false, issue: { code: 'models_unavailable', message: 'No models.' } },
    configuration: { valid: true },
    workspace: {
      roots: [{ path: '/work/space', valid: true }],
      repositories: [{ name: 'repo-a', path: '/work/space/repo-a', valid: true }],
    },
    issues: [
      {
        code: 'unauthenticated',
        message: 'The claude CLI is not authenticated.',
        remedy: 'claude login',
      },
      { code: 'models_unavailable', message: 'No models.' },
    ],
    ...overrides,
  };
}

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function makeService(
  respond: (path: string, init?: ApiRequestInit) => HttpResult,
  pickDirectory: SetupServiceDeps['dialogs']['pickDirectory'] = () => Promise.resolve(null),
): { service: SetupService; calls: Call[]; pick: ReturnType<typeof vi.fn> } {
  const calls: Call[] = [];
  const pick = vi.fn(pickDirectory);
  const service = new SetupService({
    transport: {
      apiRequest: (path, init) => {
        calls.push(init === undefined ? { path } : { path, init });
        return Promise.resolve(respond(path, init));
      },
    },
    dialogs: { pickDirectory: pick },
  });
  return { service, calls, pick };
}

describe('SetupService.getReadiness', () => {
  it('maps the authoritative server snapshot into the strict renderer shape', async () => {
    const { service } = makeService(() => ({ status: 200, body: serverReadiness() }));
    const snapshot = await service.getReadiness();
    expect(ReadinessSnapshotSchema.parse(snapshot)).toEqual(snapshot);
    expect(snapshot.ready).toBe(false);
    expect(snapshot.probedAt).toBe('2026-07-14T10:00:00Z');
    expect(snapshot.providers).toHaveLength(2);
    expect(snapshot.providers[0]?.issue?.remedy).toBe('claude login');
    expect(snapshot.workspaceRoots).toEqual([{ path: '/work/space', valid: true }]);
    expect(snapshot.repositories[0]?.name).toBe('repo-a');
    expect(snapshot.issues.map((issue) => issue.code)).toEqual([
      'unauthenticated',
      'models_unavailable',
    ]);
  });

  it('fails closed when the server payload does not match the schema', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: { api_version: 'v1', ready: 'yes' },
    }));
    await expect(service.getReadiness()).rejects.toMatchObject({
      safe: { code: 'E_SCHEMA_MISMATCH' },
    });
  });

  it('tolerates a snapshot without optional probe time or issues', async () => {
    const body = serverReadiness();
    delete body['probed_at'];
    delete body['issues'];
    const { service } = makeService(() => ({ status: 200, body }));
    const snapshot = await service.getReadiness();
    expect(snapshot.probedAt).toBeUndefined();
    expect(snapshot.issues).toEqual([]);
  });
});

describe('SetupService.refreshReadiness', () => {
  it('re-probes through POST /readiness/refresh and returns the fresh snapshot', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: serverReadiness({ ready: true }),
    }));
    const snapshot = await service.refreshReadiness();
    expect(snapshot.ready).toBe(true);
    expect(calls[0]).toEqual({
      path: '/api/v1/readiness/refresh',
      init: { method: 'POST', body: {} },
    });
  });
});

describe('SetupService.addWorkspaceRoot', () => {
  it('merges the new root into the server config and returns fresh readiness', async () => {
    const { service, calls } = makeService((path) => {
      if (path === '/api/v1/config/runtime') {
        return { status: 200, body: { api_version: 'v1', workspace_roots: ['/existing'] } };
      }
      return { status: 200, body: serverReadiness() };
    });
    await service.addWorkspaceRoot('/work/new-root');
    const patch = calls.find((call) => call.init?.method === 'PATCH');
    expect(patch?.path).toBe('/api/v1/config/runtime');
    expect(patch?.init?.body).toEqual({ workspace_roots: ['/existing', '/work/new-root'] });
    expect(calls.at(-1)?.path).toBe('/api/v1/readiness');
  });

  it('skips the mutation when the root is already configured', async () => {
    const { service, calls } = makeService((path) => {
      if (path === '/api/v1/config/runtime') {
        return { status: 200, body: { api_version: 'v1', workspace_roots: ['/work/space'] } };
      }
      return { status: 200, body: serverReadiness() };
    });
    await service.addWorkspaceRoot('/work/space');
    expect(calls.some((call) => call.init?.method === 'PATCH')).toBe(false);
  });

  it('rejects relative paths fail-closed without contacting the server', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: serverReadiness() }));
    await expect(service.addWorkspaceRoot('relative/path')).rejects.toBeInstanceOf(
      SafeErrorException,
    );
    expect(calls).toHaveLength(0);
  });
});

describe('SetupService.initRepository', () => {
  it('posts the consent-gated init action and returns refreshed discovery', async () => {
    const { service, calls } = makeService((path) => {
      if (path === '/api/v1/workspace/repositories/init') {
        return {
          status: 201,
          body: {
            api_version: 'v1',
            result: 'initialized',
            repository: { name: 'new-repo', path: '/work/space/new-repo' },
          },
        };
      }
      return { status: 200, body: serverReadiness() };
    });
    await service.initRepository({ path: '/work/space/new-repo', consent: true });
    expect(calls[0]).toEqual({
      path: '/api/v1/workspace/repositories/init',
      init: { method: 'POST', body: { path: '/work/space/new-repo', consent: true } },
    });
    expect(calls.at(-1)?.path).toBe('/api/v1/readiness');
  });

  it('refuses to call the server without consent — defense in depth', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: serverReadiness() }));
    await expect(
      service.initRepository({ path: '/work/space/x', consent: false as never }),
    ).rejects.toMatchObject({ safe: { code: 'consent_required' } });
    expect(calls).toHaveLength(0);
  });

  it('maps structured server rejections to safe errors with concrete remediation', async () => {
    const { service } = makeService(() => ({
      status: 409,
      body: {
        api_version: 'v1',
        error: {
          code: 'already_repository',
          message: 'the target is already a git repository',
          status: 409,
        },
      },
    }));
    await expect(
      service.initRepository({ path: '/work/space/existing', consent: true }),
    ).rejects.toMatchObject({
      safe: {
        code: 'already_repository',
        remediation: expect.stringMatching(/select it directly/i),
      },
    });
  });

  it('redacts token or home-path material in server error messages', async () => {
    const { service } = makeService(() => ({
      status: 400,
      body: {
        api_version: 'v1',
        error: {
          code: 'invalid_repository_path',
          message: 'bad path /Users/somebody/secret with Bearer tok-123',
          status: 400,
        },
      },
    }));
    const failure = await service
      .initRepository({ path: '/work/space/x', consent: true })
      .catch((err: unknown) => err as SafeErrorException);
    expect(failure).toBeInstanceOf(SafeErrorException);
    const raw = JSON.stringify((failure as SafeErrorException).safe);
    expect(raw).not.toContain('/Users/somebody');
    expect(raw).not.toContain('tok-123');
  });

  it('degrades to a generic safe error when the error body is unstructured', async () => {
    const { service } = makeService(() => ({ status: 500, body: 'boom' }));
    await expect(
      service.initRepository({ path: '/work/space/x', consent: true }),
    ).rejects.toMatchObject({ safe: { code: 'E_HTTP_500' } });
  });
});

describe('SetupService.listRepositories', () => {
  it('always returns repositories from fresh server discovery', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: serverReadiness() }));
    const repositories = await service.listRepositories();
    expect(repositories).toEqual([{ name: 'repo-a', path: '/work/space/repo-a', valid: true }]);
    expect(calls.map((call) => call.path)).toEqual(['/api/v1/readiness']);
  });
});

describe('SetupService.pickWorkspaceDirectory', () => {
  it('returns null when the user cancels the native dialog', async () => {
    const { service } = makeService(() => ({ status: 200, body: serverReadiness() }));
    await expect(service.pickWorkspaceDirectory()).resolves.toEqual({ path: null });
  });

  it('returns the picked absolute path unchanged', async () => {
    const { service } = makeService(
      () => ({ status: 200, body: serverReadiness() }),
      () => Promise.resolve('/work/space/chosen'),
    );
    await expect(service.pickWorkspaceDirectory()).resolves.toEqual({
      path: '/work/space/chosen',
    });
  });

  it('rejects a non-absolute dialog result without echoing the path', async () => {
    const { service } = makeService(
      () => ({ status: 200, body: serverReadiness() }),
      () => Promise.resolve('sneaky/relative'),
    );
    const failure = await service.pickWorkspaceDirectory().catch((err: unknown) => err);
    expect(failure).toBeInstanceOf(SafeErrorException);
    expect((failure as SafeErrorException).safe.code).toBe('E_INVALID_PATH');
    expect(JSON.stringify((failure as SafeErrorException).safe)).not.toContain('sneaky');
  });
});
