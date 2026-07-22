import { describe, expect, it } from 'vitest';
import { RunHistoryService } from '../runHistory';

describe('RunHistoryService', () => {
  it('maps the bounded authoritative live preview without exposing raw server fields', async () => {
    const paths: string[] = [];
    const service = new RunHistoryService({
      apiRequest: (path) => {
        paths.push(path);
        return Promise.resolve({
          status: 200,
          body: {
            api_version: 'v1',
            feature: {
              id: 'abcd1234ef567890',
              name: 'Operations',
              slug: 'operations',
              status: 'Running',
              current_phase: 'Implement',
              repos: ['repo-a'],
              created_at: '2026-07-20T00:00:00Z',
              active_run: 2,
              run_count: 2,
              progress: {},
            },
            activity: 'Running implementation at /Users/private/worktree',
            context: { percentage: 42 },
            timing: { total_seconds: 73, by_phase: { Implement: 73 } },
            cost: { total_usd: 0.12, by_phase: { Implement: 0.12 } },
            transcript: [],
          },
        });
      },
    });

    await expect(service.getLivePreview('abcd1234ef567890')).resolves.toStrictEqual({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation at [path]',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [],
    });
    expect(paths).toStrictEqual(['/api/v1/features/abcd1234ef567890/live-preview']);
  });

  it('carries the sanitized live-preview transcript tail across IPC', async () => {
    const service = new RunHistoryService({
      apiRequest: () =>
        Promise.resolve({
          status: 200,
          body: {
            api_version: 'v1',
            feature: {
              id: 'abcd1234ef567890',
              name: 'Operations',
              slug: 'operations',
              status: 'Running',
              current_phase: 'Implement',
              repos: ['repo-a'],
              created_at: '2026-07-20T00:00:00Z',
              active_run: 2,
              run_count: 2,
              progress: {},
            },
            activity: 'Running implementation',
            context: { percentage: 42 },
            timing: { total_seconds: 73, by_phase: { Implement: 73 } },
            cost: { total_usd: 0.12, by_phase: { Implement: 0.12 } },
            transcript: [
              { index: 0, block_index: 1, role: 'assistant', type: 'tool_use', tool: 'Read' },
              { index: 1, role: 'assistant', type: 'text', text: 'Applied the change.' },
            ],
          },
        }),
    });

    const preview = await service.getLivePreview('abcd1234ef567890');
    expect(preview.transcript).toStrictEqual([
      { index: 0, blockIndex: 1, role: 'assistant', type: 'tool_use', tool: 'Read' },
      { index: 1, role: 'assistant', type: 'text', text: 'Applied the change.' },
    ]);
  });
});
