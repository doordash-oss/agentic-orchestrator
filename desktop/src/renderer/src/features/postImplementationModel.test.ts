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

  it('drops review feedback from the runway when the catalog disables it', () => {
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
    expect(aftercareActions(snapshot).map((action) => action.id)).not.toContain('review-feedback');
  });

  it('leads the runway with undelivered publish work', () => {
    const actions = aftercareActions(
      featureSnapshot({
        status: 'CodeReady',
        actions: [{ id: 'refactor', enabled: true, disabledReasons: [] }],
      }),
      { publishRepos: [{ repo: 'api', commits: 3, dirty: false }], mergeRepos: [] },
    );
    expect(actions.map((action) => action.id)).toEqual(['publish-updates', 'refactor']);
    expect(actions[0]!.title).toBe('Publish new commits');
    expect(actions[0]!.label).toBe('Publish updates');
    expect(actions[0]!.description).toBe('Not on the pull-request branch yet: 3 commits.');
  });

  it('offers undelivered merge work', () => {
    const actions = aftercareActions(featureSnapshot({ status: 'Done', actions: [] }), {
      publishRepos: [],
      mergeRepos: [{ repo: 'core', commits: 1, dirty: true }],
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
