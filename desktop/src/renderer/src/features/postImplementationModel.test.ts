import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  aftercareActions,
  cycleFailureDetail,
  cyclePresentation,
  ownsPostImplementationStage,
  receiptForCycleEnd,
  resolvePostImplementationMode,
} from './postImplementationModel';

describe('postImplementationModel', () => {
  it.each(['CodeReady', 'Published', 'Done'])('%s resolves to aftercare', (status) => {
    expect(resolvePostImplementationMode(featureSnapshot({ status })).kind).toBe('aftercare');
  });

  it('lets a running cycle own a published feature', () => {
    const mode = resolvePostImplementationMode(
      featureSnapshot({
        status: 'Published',
        cycle: {
          type: 'rebase',
          status: 'running',
          count: 2,
          iteration: 1,
          phase: 'final_review',
        },
      }),
    );
    expect(mode.kind).toBe('cycle');
  });

  it('keeps an interrupted cycle in its focused paused workspace', () => {
    const snapshot = featureSnapshot({
      status: 'Interrupted',
      cycle: {
        type: 'rebase',
        status: 'interrupted',
        count: 1,
        phase: 'resolve_conflicts',
      },
    });

    expect(resolvePostImplementationMode(snapshot).kind).toBe('cycle');
    expect(resolvePostImplementationMode(snapshot, 'rebase:1:interrupted').kind).toBe('aftercare');
    expect(cyclePresentation(snapshot)).toMatchObject({
      headline: 'Rebase cycle paused',
      current: 'Resolve conflicts',
      next: 'Final review',
    });
  });

  it('uses one ownership predicate for every durable cycle status', () => {
    for (const status of ['running', 'reviewing', 'need_user_input', 'failed', 'interrupted']) {
      expect(ownsPostImplementationStage({ type: 'rebase', status })).toBe(true);
    }
    expect(ownsPostImplementationStage(undefined)).toBe(false);
    expect(ownsPostImplementationStage({ type: 'rebase', status: 'completed' })).toBe(false);
  });

  it('builds completed, failed, and stopped receipts from the prior cycle outcome', () => {
    const atRest = featureSnapshot({ status: 'Published' });
    expect(
      receiptForCycleEnd({ type: 'rebase', status: 'running', count: 2 }, atRest),
    ).toMatchObject({ id: 'rebase', outcome: 'completed', message: 'Rebase cycle complete.' });
    expect(
      receiptForCycleEnd(
        { type: 'rebase', status: 'failed', count: 2, lastError: 'push rejected' },
        atRest,
      ),
    ).toMatchObject({ id: 'rebase', outcome: 'failed', detail: 'push rejected' });
    expect(
      receiptForCycleEnd({ type: 'rebase', status: 'interrupted', count: 2 }, atRest),
    ).toMatchObject({
      id: 'rebase',
      outcome: 'stopped',
      message: 'Cycle stopped · No completion action was dispatched.',
    });
  });

  it('prefers cycle, feature, then repository failure detail', () => {
    expect(
      cycleFailureDetail(
        featureSnapshot({
          cycle: { type: 'rebase', status: 'failed', lastError: 'cycle failed' },
          failure: { type: 'infrastructure', message: 'feature failed' },
          repoStatus: [{ name: 'api', publishable: true, lastError: 'repo failed' }],
        }),
      ),
    ).toBe('cycle failed');
    expect(
      cycleFailureDetail(
        featureSnapshot({
          cycle: { type: 'rebase', status: 'failed' },
          failure: { type: 'infrastructure', message: 'feature failed' },
          repoStatus: [{ name: 'api', publishable: true, lastError: 'repo failed' }],
        }),
      ),
    ).toBe('feature failed');
    expect(
      cycleFailureDetail(
        featureSnapshot({
          cycle: { type: 'rebase', status: 'failed' },
          repoStatus: [{ name: 'api', publishable: true, lastError: 'repo failed' }],
        }),
      ),
    ).toBe('repo failed');
  });

  it('orders publish before available cycle actions', () => {
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

  it('projects the server-owned phase into a truthful cycle spine', () => {
    const presentation = cyclePresentation(
      featureSnapshot({
        cycle: {
          type: 'rebase',
          status: 'reviewing',
          count: 2,
          phase: 'final_review',
        },
      }),
    );
    expect(presentation?.current).toBe('Final review');
    expect(presentation?.next).toBe('Publish');
    expect(presentation?.stages.map((stage) => stage.state)).toEqual([
      'done',
      'done',
      'active',
      'upcoming',
    ]);
  });

  it('makes need-user-input the current cycle state', () => {
    expect(
      cyclePresentation(
        featureSnapshot({
          cycle: { type: 'rebase', status: 'need_user_input', phase: 'inspect_rebase' },
        }),
      ),
    ).toMatchObject({
      headline: 'Agent is waiting for your input',
      current: 'Waiting for input',
      next: 'Final review',
    });
  });

  it('never presents a refactor as an in-feature cycle — it runs as a child pass', () => {
    expect(
      cyclePresentation(
        featureSnapshot({ cycle: { type: 'refactor', status: 'running', phase: 'implement' } }),
      ),
    ).toBeNull();
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

  it('keeps review feedback out of the repo-cycle taxonomy — it runs as a child pass', () => {
    expect(
      cyclePresentation(
        featureSnapshot({
          cycle: { type: 'review-feedback', status: 'running', phase: 'implement' },
        }),
      ),
    ).toBeNull();
  });
});
