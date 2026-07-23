import { describe, it, expect } from 'vitest';
import { completionBarModel, type CompletionVerb } from './completionBarModel';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const ALL: ReadonlySet<CompletionVerb> = new Set(['publish', 'merge', 'mark-done', 'cleanup']);
function pf(over: Partial<CompletionPreflightResult>): CompletionPreflightResult {
  return { featureId: 'f', sourceRevision: 'r', canMarkDone: false, repos: [], ...over };
}

describe('completionBarModel', () => {
  it('omits verbs the server does not offer', () => {
    const model = completionBarModel(
      pf({
        repos: [
          { repo: 'a', publishable: true, touched: true, status: 'eligible' },
          { repo: 'b', publishable: false, touched: true, status: 'eligible' },
        ],
      }),
      new Set(['merge']),
    );
    expect(model.map((m) => m.verb)).toEqual(['merge']);
  });

  it('omits merge when offered but nothing is mergeable', () => {
    const model = completionBarModel(
      pf({ repos: [{ repo: 'a', publishable: true, touched: true, status: 'eligible' }] }),
      new Set(['merge']),
    );
    expect(model.find((m) => m.verb === 'merge')).toBeUndefined();
  });

  it('publish is available with an eligible repo and is primary', () => {
    const model = completionBarModel(
      pf({ repos: [{ repo: 'a', publishable: true, touched: true, status: 'eligible' }] }),
      ALL,
    );
    const publish = model.find((m) => m.verb === 'publish')!;
    expect(publish.state).toBe('available');
    expect(publish.label).toBe('Publish');
    expect(publish.primary).toBe(true);
  });

  it('publish is done (chip) when all publishable repos are already published', () => {
    const model = completionBarModel(
      pf({ repos: [{ repo: 'a', publishable: true, touched: true, status: 'already_published' }] }),
      ALL,
    );
    const publish = model.find((m) => m.verb === 'publish')!;
    expect(publish.state).toBe('done');
    expect(publish.label).toBe('Published');
    expect(publish.primary).toBe(false);
  });

  it('after publish, merge becomes the primary available verb', () => {
    const model = completionBarModel(
      pf({
        repos: [
          { repo: 'a', publishable: true, touched: true, status: 'already_published' },
          { repo: 'b', publishable: false, touched: true, status: 'eligible' },
        ],
      }),
      ALL,
    );
    expect(model.find((m) => m.verb === 'merge')!.state).toBe('available');
    expect(model.find((m) => m.verb === 'merge')!.primary).toBe(true);
  });

  it('mark-done is blocked with its blocker text when canMarkDone is false', () => {
    const model = completionBarModel(
      pf({ canMarkDone: false, markDoneBlocker: 'publish first' }),
      ALL,
    );
    const done = model.find((m) => m.verb === 'mark-done')!;
    expect(done.state).toBe('blocked');
    expect(done.blocker).toBe('publish first');
  });

  it('cleanup is available whenever offered', () => {
    const model = completionBarModel(pf({}), new Set(['cleanup']));
    expect(model).toEqual([
      { verb: 'cleanup', label: 'Clean up', state: 'available', primary: true },
    ]);
  });
});
