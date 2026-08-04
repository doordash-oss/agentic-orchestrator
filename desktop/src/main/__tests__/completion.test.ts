import { describe, expect, it } from 'vitest';
import { CompletionService } from '../completion';

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
