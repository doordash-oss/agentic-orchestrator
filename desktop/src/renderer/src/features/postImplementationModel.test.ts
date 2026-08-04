import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import { aftercareActions, resolvePostImplementationMode } from './postImplementationModel';

describe('postImplementationModel', () => {
  it.each(['CodeReady', 'Published', 'Done'])('%s resolves to aftercare', (status) => {
    expect(resolvePostImplementationMode(featureSnapshot({ status })).kind).toBe('aftercare');
  });

  it('resolves non-aftercare statuses to regular', () => {
    expect(resolvePostImplementationMode(featureSnapshot({ status: 'Planning' })).kind).toBe(
      'regular',
    );
  });

  it('orders publish before available aftercare actions', () => {
    const snapshot = featureSnapshot({
      status: 'CodeReady',
      actions: ['refactor', 'publish', 'rebase'].map((id) => ({
        id,
        enabled: true,
        disabledReasons: [],
      })),
    });
    expect(aftercareActions(snapshot).map((action) => action.id)).toEqual([
      'publish',
      'rebase',
      'refactor',
    ]);
  });

  it('offers review feedback on the aftercare runway when the catalog enables it', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      actions: [
        { id: 'review-feedback', enabled: true, disabledReasons: [] },
        { id: 'refactor', enabled: true, disabledReasons: [] },
      ],
    });
    const actions = aftercareActions(snapshot);
    expect(actions.map((action) => action.id)).toEqual(['refactor', 'review-feedback']);
    expect(actions[1]).toMatchObject({
      id: 'review-feedback',
      label: 'Address review feedback',
      title: 'Address review feedback',
    });
  });

  it('keeps a disabled review feedback on the runway carrying its reason', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      actions: [
        {
          id: 'review-feedback',
          enabled: false,
          disabledReasons: [{ code: 'no_pull_request', message: 'no PR' }],
        },
      ],
    });
    const actions = aftercareActions(snapshot);
    expect(actions.map((action) => action.id)).toEqual(['review-feedback']);
    expect(actions[0]!.disabledReason).toBe('no PR');
  });

  it('keeps a blocked pass on the runway with its reason', () => {
    const actions = aftercareActions(
      featureSnapshot({
        status: 'CodeReady',
        actions: [
          {
            id: 'refactor',
            enabled: false,
            disabledReasons: [
              { code: 'dirty_parent', message: 'worktree has uncommitted changes' },
            ],
          },
        ],
      }),
    );
    expect(actions.map((action) => action.id)).toEqual(['refactor']);
    expect(actions[0]!.disabledReason).toBe('worktree has uncommitted changes');
  });

  it('omits a disabled publish action rather than showing a blocked card', () => {
    const actions = aftercareActions(
      featureSnapshot({
        status: 'CodeReady',
        actions: [
          { id: 'publish', enabled: false, disabledReasons: [{ code: 'manual_publish_required', message: 'waiting' }] },
        ],
      }),
    );
    expect(actions).toEqual([]);
  });

  it('leads the runway with undelivered publish work', () => {
    const actions = aftercareActions(
      featureSnapshot({
        status: 'CodeReady',
        actions: [{ id: 'refactor', enabled: true, disabledReasons: [] }],
      }),
      {
        publishRepos: [{ repo: 'api', commits: 3, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 }],
        mergeRepos: [],
      },
    );
    expect(actions.map((action) => action.id)).toEqual(['publish-updates', 'refactor']);
    expect(actions[0]!.title).toBe('Publish new commits');
    expect(actions[0]!.label).toBe('Publish updates');
    expect(actions[0]!.description).toBe('Not on the pull-request branch yet: 3 commits.');
  });

  it('offers undelivered merge work', () => {
    const actions = aftercareActions(featureSnapshot({ status: 'Done', actions: [] }), {
      publishRepos: [],
      mergeRepos: [{ repo: 'core', commits: 1, dirty: true, dirtyFiles: [], dirtyFileTotal: 0 }],
    });
    expect(actions.map((action) => action.id)).toEqual(['merge-updates']);
    expect(actions[0]!.title).toBe('Merge new commits');
    expect(actions[0]!.description).toBe(
      'Not in the base branch yet: 1 commit · uncommitted changes.',
    );
  });

  it('omits undelivered cards when nothing is pending', () => {
    const actions = aftercareActions(featureSnapshot({ status: 'Published', actions: [] }), {
      publishRepos: [],
      mergeRepos: [],
    });
    expect(actions).toEqual([]);
  });
});
