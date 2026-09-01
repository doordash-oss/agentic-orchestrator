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

import { describe, expect, it, vi } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';
import { CanonicalErrorException, SafeErrorException } from '../../shared/errors';
import { ReadinessSnapshotSchema } from '../../shared/ipc';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';
import { SetupService, type SetupServiceDeps } from '../setup';
import { CreationFilesService } from '../creationFiles';

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

describe('CreationFilesService', () => {
  it('applies the shared kind-specific picker limits', async () => {
    const service = new CreationFilesService({
      pickFiles: () => Promise.resolve(Array.from({ length: 30 }, (_, index) => `/safe/${index}`)),
      readReadiness: () => Promise.reject(new Error('not used')),
    });
    await expect(service.pickFiles('image')).resolves.toMatchObject({ paths: { length: 12 } });
    await expect(service.pickFiles('attachment')).resolves.toMatchObject({ paths: { length: 24 } });
  });

  it('searches only eligible repository files, skips generated trees, and resolves regular files', async () => {
    const root = fs.mkdtempSync(path.join(process.env['TMPDIR'] ?? '/tmp', 'agentico-index-'));
    fs.mkdirSync(path.join(root, 'src'), { recursive: true });
    fs.mkdirSync(path.join(root, 'node_modules', 'hidden'), { recursive: true });
    fs.writeFileSync(path.join(root, 'src', 'creation.ts'), 'export const creation = true;');
    fs.writeFileSync(path.join(root, 'src', 'creation-context.md'), '# context');
    fs.writeFileSync(path.join(root, 'node_modules', 'hidden', 'creation.ts'), 'hidden');
    const { service: setup } = makeService(() => ({
      status: 200,
      body: serverReadiness({
        workspace: {
          roots: [{ path: root, valid: true }],
          repositories: [{ name: 'repo-a', path: root, valid: true }],
        },
      }),
    }));
    const service = new CreationFilesService({
      pickFiles: () => Promise.resolve([]),
      readReadiness: () => setup.getReadiness(),
    });
    try {
      const requestId = crypto.randomUUID();
      const result = await service.search({
        requestId,
        repoKeys: ['repo-a'],
        query: 'creation context',
      });
      expect(result.files).toEqual([{ repoKey: 'repo-a', path: 'src/creation-context.md' }]);
      await expect(service.resolve(result.files)).resolves.toEqual([
        path.join(fs.realpathSync(root), 'src', 'creation-context.md'),
      ]);
      await expect(
        service.resolve([{ repoKey: 'repo-a', path: '../outside' }]),
      ).rejects.toMatchObject({ safe: { code: 'E_INVALID_REPOSITORY_FILE' } });
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
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
    ).rejects.toMatchObject({
      safe: { code: 'consent_required', remediation: expect.stringContaining('consent') },
    });
    expect(calls).toHaveLength(0);
  });

  it.each([
    {
      code: 'consent_required',
      status: 400,
      errorClass: 'needs_action',
      title: 'Consent required',
      summary: 'Explicit consent is required to initialize a repository.',
      hint: 'Confirm the initialization consent, then retry.',
    },
    {
      code: 'invalid_repository_path',
      status: 400,
      errorClass: 'blocking',
      title: 'Invalid repository path',
      summary: 'The repository path "/work/space/x" is not valid.',
      hint: 'Choose an absolute folder inside a configured workspace root.',
    },
    {
      code: 'path_outside_workspace_root',
      status: 400,
      errorClass: 'blocking',
      title: 'Path outside workspace root',
      summary: 'The path "/work/space/x" is not strictly inside a configured workspace root.',
      hint: 'Choose a folder inside a workspace root, or add its parent as a root first.',
    },
    {
      code: 'already_repository',
      status: 409,
      errorClass: 'blocking',
      title: 'Already a repository',
      summary: 'The selected path is already a git repository.',
      hint: 'Select the existing repository instead of initializing it.',
    },
    {
      code: 'directory_not_empty',
      status: 409,
      errorClass: 'blocking',
      title: 'Directory not empty',
      summary: 'The directory is not empty and is not a git repository.',
      hint: 'Choose an empty folder or an existing repository.',
    },
    {
      code: 'not_ready',
      status: 409,
      errorClass: 'needs_action',
      title: 'Runtime not ready',
      summary: 'The runtime is not ready to create features: Unauthenticated; Missing executable.',
      hint: 'Complete the outstanding setup steps, then try again.',
    },
  ])(
    'maps the canonical $code rejection with its catalog remedy',
    async ({ code, status, errorClass, title, summary, hint }) => {
      const { service } = makeService(() => ({
        status,
        body: {
          api_version: 'v1',
          error: { code, class: errorClass, title, summary, remediation: { hint } },
        },
      }));
      const failure = await service
        .initRepository({ path: '/work/space/x', consent: true })
        .catch((e: unknown) => e);
      expect(failure).toBeInstanceOf(CanonicalErrorException);
      const canonical = (failure as CanonicalErrorException).canonical;
      expect(canonical.code).toBe(code);
      expect(canonical.class).toBe(errorClass);
      expect(canonical.title).toBe(title);
      expect(canonical.summary).toBe(summary);
      expect(canonical.remediation?.hint).toBe(hint);
    },
  );

  it('redacts token or home-path material in canonical diagnostics', async () => {
    const { service } = makeService(() => ({
      status: 400,
      body: {
        api_version: 'v1',
        error: {
          code: 'invalid_repository_path',
          class: 'blocking',
          title: 'Invalid repository path',
          summary: 'The repository path "/work/space/x" is not valid.',
          remediation: { hint: 'Choose an absolute folder inside a configured workspace root.' },
          diagnostics: 'bad path /Users/somebody/secret with Bearer tok-123',
        },
      },
    }));
    const failure = await service
      .initRepository({ path: '/work/space/x', consent: true })
      .catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(CanonicalErrorException);
    const diagnostics = (failure as CanonicalErrorException).canonical.diagnostics ?? '';
    expect(diagnostics).toContain('[path]');
    expect(diagnostics).toContain('[redacted]');
    expect(diagnostics).not.toContain('/Users/somebody');
    expect(diagnostics).not.toContain('tok-123');
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
