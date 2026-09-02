import { describe, expect, it } from 'vitest';
import { RecoverySnapshotSchema } from '../../shared/ipc';
import { RecoveryService } from '../recovery';

describe('RecoveryService.scan', () => {
  it('maps orphan items with canonical errors and redacted diagnostics', async () => {
    const service = new RecoveryService({
      apiRequest: () =>
        Promise.resolve({
          status: 200,
          body: {
            api_version: 'v1',
            snapshot_id: 'snapshot-001',
            items: [
              {
                key: 'feature-alpha:repo-a',
                feature_id: 'alpha1234ef567890',
                feature_name: 'Alpha Feature',
                repo_name: 'repo-a',
                phase: 'implement',
                iteration: 3,
                pid: 412,
                process_alive: true,
                log_available: true,
                allowed_actions: ['resume', 'kill'],
                default_action: 'resume',
                error: {
                  code: 'orphan_session_live',
                  class: 'needs_action',
                  title: 'Orphan session still running',
                  summary:
                    'A session process for this feature is still alive after its run was interrupted.',
                  diagnostics: 'unexpected tail: /Users/dev/orphan.log',
                },
              },
              {
                key: 'feature-beta:repo-b',
                feature_id: 'beta1234ef567890',
                process_alive: false,
                allowed_actions: ['resume', 'skip'],
                default_action: 'skip',
                error: {
                  code: 'orphan_session_stale',
                  class: 'needs_action',
                  title: 'Orphan session no longer running',
                  summary: 'The session process for this feature is no longer alive.',
                },
              },
            ],
          },
        }),
    });

    const snapshot = await service.scan();
    expect(snapshot.snapshotId).toBe('snapshot-001');
    expect(snapshot.items).toHaveLength(2);
    expect(snapshot.items[0]?.error).toMatchObject({
      code: 'orphan_session_live',
      class: 'needs_action',
      title: 'Orphan session still running',
    });
    // Even though the server promises no paths in diagnostics, the value
    // still passes through the boundary redaction defensively.
    expect(snapshot.items[0]?.error.diagnostics).not.toContain('/Users/dev/orphan.log');
    expect(snapshot.items[0]?.error.diagnostics).toContain('[path]');
    expect(snapshot.items[1]?.error.diagnostics).toBeUndefined();
    expect(() => RecoverySnapshotSchema.parse(snapshot)).not.toThrow();
  });
});
