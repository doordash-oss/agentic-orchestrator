import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  aftercareHeadline,
  aftercareRepositories,
  availableAftercareCycles,
} from './aftercareModel';

describe('aftercareHeadline', () => {
  it.each([
    ['CodeReady', 'Implementation complete', 'Code ready'],
    ['Published', 'Published and ready for what comes next', 'Published'],
    ['Done', 'Work complete', 'Done'],
  ])('maps %s to terminal handoff copy', (status, heading, statusLabel) => {
    expect(aftercareHeadline(status)).toMatchObject({ heading, statusLabel });
  });
});

describe('availableAftercareCycles', () => {
  it('returns only enabled server-advertised maintenance cycles in runway order', () => {
    const snapshot = featureSnapshot({
      repos: ['repo-a', 'repo-b'],
      actions: [
        { id: 'refactor', enabled: true, disabledReasons: [] },
        { id: 'review-comments', enabled: false, disabledReasons: [] },
        { id: 'rebase', enabled: true, disabledReasons: [] },
        { id: 'restart', enabled: true, disabledReasons: [] },
      ],
    });

    expect(availableAftercareCycles(snapshot)).toEqual([
      expect.objectContaining({
        id: 'rebase',
        title: 'Rebase onto target branches',
        scope: '2 repositories',
        verb: 'Prepare rebase',
      }),
      expect.objectContaining({
        id: 'refactor',
        title: 'Plan another pass',
        scope: 'Choose one or all repositories',
        verb: 'Plan refactor',
      }),
    ]);
  });
});

describe('aftercareRepositories', () => {
  it('keeps feature repository order and reports only authoritative readiness data', () => {
    const snapshot = featureSnapshot({
      repos: ['repo-b', 'repo-a'],
      repoStatus: [
        {
          name: 'repo-a',
          publishable: true,
          freshness: 'in sync',
          prUrl: 'https://example.test/pr/12',
          cycleType: 'rebase',
          cycleStatus: 'completed',
        },
      ],
    });

    expect(aftercareRepositories(snapshot)).toEqual([
      {
        name: 'repo-b',
        freshness: 'Freshness unavailable',
        pullRequest: 'PR unavailable',
        publishability: 'Publishability unavailable',
      },
      {
        name: 'repo-a',
        freshness: 'In sync',
        pullRequest: 'PR open',
        publishability: 'Publishable',
        prUrl: 'https://example.test/pr/12',
        cycle: 'Rebase completed',
      },
    ]);
  });
});
