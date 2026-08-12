import { describe, expect, it } from 'vitest';
import { CompletionService } from '../completion';
import type { ServerTransport } from '../serverClient';

function pathTransport(response: {
  status: number;
  body: Record<string, unknown>;
}): ServerTransport & { apiRequestCalls: string[] } {
  const apiRequestCalls: string[] = [];
  return {
    apiRequestCalls,
    apiRequest: (path: string) => {
      apiRequestCalls.push(path);
      return Promise.resolve(response);
    },
  };
}

describe('CompletionService.revealPath locality', () => {
  function pathBody(path: string, overrides: Record<string, unknown> = {}) {
    return {
      status: 200,
      body: {
        api_version: 'v1',
        feature_id: 'abcd1234ef567890',
        repo: 'repo-a',
        path,
        ...overrides,
      },
    };
  }

  const requested = { featureId: 'abcd1234ef567890', repo: 'repo-a' };

  it('returns the server-reported path for copy use remotely without local validation', async () => {
    // A host path that does not exist on this machine would be rejected by
    // the local realpath/stat validation; remotely it is returned verbatim.
    const transport = pathTransport(pathBody('/remote-host/worktrees/repo-a'));
    const service = new CompletionService({ transport, locality: () => 'remote' });

    const result = await service.revealPath({ featureId: 'abcd1234ef567890', repo: 'repo-a' });

    expect(result).toStrictEqual({ ok: true, path: '/remote-host/worktrees/repo-a' });
    expect(transport.apiRequestCalls).toEqual([
      `/api/v1/features/abcd1234ef567890/repositories/repo-a/path`,
    ]);
  });

  it('still rejects a remote server answer whose identity does not match the request', async () => {
    const transport = pathTransport(pathBody('/remote-host/worktrees/repo-a', { repo: 'repo-b' }));
    const service = new CompletionService({ transport, locality: () => 'remote' });

    await expect(service.revealPath(requested)).resolves.toStrictEqual({ ok: false });
  });

  it('still rejects non-absolute remote paths before handing them to the renderer', async () => {
    const transport = pathTransport(pathBody('relative/path'));
    const service = new CompletionService({ transport, locality: () => 'remote' });

    await expect(service.revealPath(requested)).resolves.toStrictEqual({ ok: false });
  });

  it('keeps local realpath/stat validation locally: a missing path never reaches the OS', async () => {
    const transport = pathTransport(pathBody('/definitely/not/a/real/worktree-path-xyz3'));
    const service = new CompletionService({ transport, locality: () => 'local' });

    await expect(service.revealPath(requested)).resolves.toStrictEqual({ ok: false });
  });
});
describe('CompletionService.preflightCompletion', () => {
  it('maps pending delivery fields from the transport response', async () => {
    const service = new CompletionService({
      transport: {
        apiRequest: () =>
          Promise.resolve({
            status: 200,
            body: {
              api_version: 'v1',
              feature_id: 'abcd1234ef567890',
              source_revision: 'rev-1',
              repos: [
                {
                  repo: 'repo-a',
                  publishable: true,
                  touched: true,
                  status: 'unpublished_changes',
                  pending_commits: 3,
                  pending_dirty: true,
                  push_mode: 'rewrite',
                  pending_dirty_files: ['a.go', 'b.go'],
                  pending_dirty_file_total: 5,
                },
              ],
            },
          }),
      },
    });

    const result = await service.preflightCompletion({ featureId: 'abcd1234ef567890' });

    expect(result.repos[0]).toMatchObject({
      status: 'unpublished_changes',
      pendingCommits: 3,
      pendingDirty: true,
      pushMode: 'rewrite',
      pendingDirtyFiles: ['a.go', 'b.go'],
      pendingDirtyFileTotal: 5,
    });
  });
});
