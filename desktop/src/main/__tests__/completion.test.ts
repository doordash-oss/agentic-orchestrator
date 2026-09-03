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

import { describe, expect, it } from 'vitest';
import { RepositoryDiffResultSchema } from '../../shared/ipc';
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

  it('crosses a repository publish-failure record as the canonical error with redacted diagnostics', async () => {
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
                  error: {
                    code: 'publish_rebase_conflict',
                    class: 'needs_action',
                    title: 'Pull-rebase conflict',
                    summary:
                      'The pull rebase for repository "repo-a" (branch "feature/f") onto "main" conflicted.',
                    remediation: {
                      hint: 'Resolve the conflict in the worktree or run a rebase pass, then retry.',
                      actions: ['publish'],
                    },
                    context: {
                      repositories: [
                        {
                          name: 'repo-a',
                          branch: 'feature/f',
                          rebase_target: 'main',
                        },
                      ],
                    },
                    diagnostics: 'git rebase: CONFLICT at /Users/dev/worktrees/repo-a',
                  },
                },
              ],
            },
          }),
      },
    });

    const result = await service.preflightCompletion({ featureId: 'abcd1234ef567890' });

    const repo = result.repos[0];
    expect(repo).toBeDefined();
    if (repo == null) return;
    expect(repo.error).toMatchObject({
      code: 'publish_rebase_conflict',
      class: 'needs_action',
      title: 'Pull-rebase conflict',
    });
    expect(repo.error?.context?.repositories?.[0]).toEqual({
      name: 'repo-a',
      branch: 'feature/f',
      rebase_target: 'main',
    });
    // Raw diagnostics cross IPC only through the same redaction as every
    // other raw text the renderer receives.
    expect(repo.error?.diagnostics).not.toContain('/Users/dev/worktrees/repo-a');
    expect(repo.error?.diagnostics).toContain('[path]');
    expect(Reflect.get(repo, 'lastError')).toBeUndefined();
  });
});

describe('CompletionService.getRepositoryDiff', () => {
  it('maps the canonical partial-inspection error with redacted diagnostics', async () => {
    const service = new CompletionService({
      transport: pathTransport({
        status: 200,
        body: {
          api_version: 'v1',
          feature_id: 'abcd1234ef567890',
          repo: 'repo-a',
          source_revision: 'rev-1',
          files: [],
          error: {
            code: 'repository_inspection_partial',
            class: 'warning',
            title: 'Repository inspection incomplete',
            summary: 'One repository could not be fully inspected for changes.',
            diagnostics: 'git status failed under /Users/dev/repo-a: bearer tok-live-secret',
          },
        },
      }),
    });

    const result = await service.getRepositoryDiff({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
    });

    expect(result.files).toEqual([]);
    expect(result.error).toMatchObject({
      code: 'repository_inspection_partial',
      class: 'warning',
      title: 'Repository inspection incomplete',
    });
    expect(result.error?.diagnostics).not.toContain('/Users/dev/repo-a');
    expect(result.error?.diagnostics).not.toContain('tok-live-secret');
    expect(result.error?.diagnostics).toContain('[path]');
    expect(result.error?.diagnostics).toContain('[redacted]');
    expect(Reflect.get(result, 'partialFailure')).toBeUndefined();
    expect(() => RepositoryDiffResultSchema.parse(result)).not.toThrow();
  });
});
