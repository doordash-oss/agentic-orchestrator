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
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import {
  changesFact,
  phasesFact,
  pullRequestNumber,
  pullRequestRows,
  verificationFact,
} from './aftercareReceipt';

function diff(repo: string, files: RepositoryDiffResult['files']): RepositoryDiffResult {
  return { featureId: 'abcd1234ef567890', repo, files };
}

function preflight(repos: CompletionPreflightResult['repos']): CompletionPreflightResult {
  return { featureId: 'abcd1234ef567890', sourceRevision: 'rev', canMarkDone: true, repos };
}

describe('changesFact', () => {
  it('aggregates files and line totals across every repository diff', () => {
    const fact = changesFact(
      [
        diff('api', [{ path: 'a.go', operation: 'modify', addedLines: 10, removedLines: 4 }]),
        diff('web', [
          { path: 'b.ts', operation: 'add', addedLines: 90 },
          { path: 'c.ts', operation: 'delete', removedLines: 6 },
        ]),
      ],
      null,
    );
    expect(fact).toEqual({ files: 3, additions: 100, deletions: 10 });
  });

  it('is null while no diff data is available', () => {
    expect(changesFact([], null)).toBeNull();
    expect(changesFact([diff('api', [])], null)).toBeNull();
  });

  it('adds the undelivered-commit phrase only where the preflight carries the field', () => {
    const files = [{ path: 'a.go', operation: 'modify', addedLines: 1 }];
    expect(
      changesFact(
        [diff('api', files)],
        preflight([
          { repo: 'api', publishable: true, touched: true, status: 'eligible', pendingCommits: 1 },
          { repo: 'web', publishable: true, touched: true, status: 'eligible', pendingCommits: 5 },
        ]),
      )?.commitPhrase,
    ).toBe('6 commits not delivered yet');
    expect(
      changesFact(
        [diff('api', files)],
        preflight([{ repo: 'api', publishable: true, touched: true, status: 'eligible' }]),
      )?.commitPhrase,
    ).toBeUndefined();
    expect(
      changesFact(
        [diff('api', files)],
        preflight([
          { repo: 'api', publishable: true, touched: true, status: 'eligible', pendingCommits: 0 },
        ]),
      )?.commitPhrase,
    ).toBeUndefined();
  });
});

describe('verificationFact', () => {
  it('counts passed checks and lists their names', () => {
    expect(
      verificationFact([
        { name: 'lint', state: 'passed' },
        { name: 'unit', state: 'passed' },
        { name: 'e2e', state: 'failed' },
      ]),
    ).toEqual({ summary: '2 of 3 checks passed', names: ['lint', 'unit', 'e2e'] });
  });

  it('is null when the snapshot carries no check states', () => {
    expect(verificationFact(undefined)).toBeNull();
    expect(verificationFact([])).toBeNull();
  });
});

describe('pullRequestRows', () => {
  it('emits one row per PR with repository, position, title, and plain-language state', () => {
    const rows = pullRequestRows(
      featureSnapshot({
        repos: ['api', 'web', 'local'],
        repoStatus: [
          {
            name: 'api',
            publishable: true,
            pullRequests: [
              {
                position: 1,
                title: 'Bootstrap',
                branch: 'feature/x/1-bootstrap',
                url: 'https://github.com/x/api/pull/412',
                state: 'open',
                noCommits: false,
                pushedUpToDate: true,
              },
              {
                position: 2,
                title: 'Empty layer',
                state: 'none',
                noCommits: true,
                pushedUpToDate: false,
              },
              {
                position: 3,
                title: 'Search revamp',
                url: 'https://github.com/x/api/pull/415',
                state: 'open',
                noCommits: false,
                pushedUpToDate: false,
              },
            ],
            freshness: 'in sync',
          },
          {
            name: 'web',
            publishable: true,
            pullRequests: [
              {
                position: 1,
                title: 'Bootstrap',
                branch: 'feature/x/1-bootstrap',
                url: 'https://github.com/x/web/pull/9',
                state: 'open',
                noCommits: false,
                pushedUpToDate: false,
              },
            ],
          },
          { name: 'local', publishable: false },
        ],
      }),
      preflight([
        { repo: 'api', publishable: true, touched: true, status: 'already_published' },
        { repo: 'web', publishable: true, touched: true, status: 'unpublished_changes' },
      ]),
      {
        featureId: 'abcd1234ef567890',
        revision: 1,
        snapshotId: 'snap-1',
        repos: [
          {
            repo: 'api',
            pullRequests: [
              {
                position: 1,
                title: 'Bootstrap',
                url: 'https://github.com/x/api/pull/412',
                comments: [
                  { stableRef: 'api:review:1', selected: true, repo: 'api', id: 1, type: 'review' },
                  { stableRef: 'api:issue:2', selected: true, repo: 'api', id: 2, type: 'issue' },
                ],
              },
              {
                position: 3,
                title: 'Search revamp',
                url: 'https://github.com/x/api/pull/413',
                comments: [
                  { stableRef: 'api:review:3', selected: true, repo: 'api', id: 3, type: 'review' },
                ],
              },
            ],
          },
        ],
      },
    );
    expect(rows).toEqual([
      {
        repo: 'api',
        position: 1,
        title: 'Bootstrap',
        url: 'https://github.com/x/api/pull/412',
        number: '#412',
        clauses: ['Published from this run', 'in sync', '3 unresolved comments'],
      },
      {
        repo: 'api',
        position: 3,
        title: 'Search revamp',
        url: 'https://github.com/x/api/pull/415',
        number: '#415',
        clauses: ['Published from this run', 'in sync', '3 unresolved comments'],
      },
      {
        repo: 'web',
        position: 1,
        title: 'Bootstrap',
        url: 'https://github.com/x/web/pull/9',
        number: '#9',
        clauses: ['New commits not pushed yet', 'no unresolved comments'],
      },
    ]);
  });

  it('falls back to preflight entries for repositories the snapshot does not cover', () => {
    const rows = pullRequestRows(
      featureSnapshot({ repoStatus: [{ name: 'api', publishable: true, freshness: 'in sync' }] }),
      preflight([
        {
          repo: 'api',
          publishable: true,
          touched: true,
          status: 'already_published',
          pullRequests: [
            {
              position: 1,
              title: 'Bootstrap',
              url: 'https://github.com/x/api/pull/7',
              state: 'open',
              noCommits: false,
              pushedUpToDate: true,
            },
          ],
        },
        {
          repo: 'web',
          publishable: true,
          touched: true,
          status: 'unpublished_changes',
          pullRequests: [
            {
              position: 2,
              title: 'Search revamp',
              url: 'https://github.com/x/web/pull/8',
              state: 'open',
              noCommits: false,
              pushedUpToDate: false,
            },
          ],
        },
      ]),
      null,
    );
    // The snapshot repository without entries falls back to the preflight's;
    // the preflight-only repository ships its PR too.
    expect(rows).toEqual([
      {
        repo: 'api',
        position: 1,
        title: 'Bootstrap',
        url: 'https://github.com/x/api/pull/7',
        number: '#7',
        clauses: ['Published from this run', 'in sync'],
      },
      {
        repo: 'web',
        position: 2,
        title: 'Search revamp',
        url: 'https://github.com/x/web/pull/8',
        number: '#8',
        clauses: ['New commits not pushed yet'],
      },
    ]);
  });

  it('omits the unresolved clause entirely when the fetch did not resolve', () => {
    const rows = pullRequestRows(
      featureSnapshot({
        repoStatus: [
          {
            name: 'api',
            publishable: true,
            pullRequests: [
              {
                position: 1,
                title: 'Bootstrap',
                branch: 'feature/x/1-bootstrap',
                url: 'https://github.com/x/api/pull/1',
                state: 'open',
                noCommits: false,
                pushedUpToDate: true,
              },
            ],
          },
        ],
      }),
      null,
      null,
    );
    expect(rows[0]!.clauses).toEqual([]);
  });

  it('says nothing about unknown freshness', () => {
    const rows = pullRequestRows(
      featureSnapshot({
        repoStatus: [
          {
            name: 'api',
            publishable: true,
            pullRequests: [
              {
                position: 1,
                title: 'Bootstrap',
                branch: 'feature/x/1-bootstrap',
                url: 'https://github.com/x/api/pull/1',
                state: 'open',
                noCommits: false,
                pushedUpToDate: true,
              },
            ],
            freshness: 'unknown',
          },
        ],
      }),
      null,
      null,
    );
    expect(rows[0]!.clauses).toEqual([]);
  });

  it('has no rows without a pull request', () => {
    expect(
      pullRequestRows(
        featureSnapshot({ repoStatus: [{ name: 'local', publishable: false, touched: true }] }),
        null,
        null,
      ),
    ).toEqual([]);
  });
});

describe('pullRequestNumber', () => {
  it('parses the number and falls back to the bare path', () => {
    expect(pullRequestNumber('https://github.com/x/y/pull/412')).toBe('#412');
    expect(pullRequestNumber('https://example.test/x/y/pulls/7')).toBe('#7');
    expect(pullRequestNumber('https://example.test/x/y/merge_requests/3')).toBe(
      'example.test/x/y/merge_requests/3',
    );
  });
});

describe('phasesFact', () => {
  it('counts the phases the run detail actually recorded', () => {
    const snapshot = featureSnapshot({ pipeline: 'medium' });
    const fact = phasesFact(snapshot, {
      runNumber: 8,
      artifactCount: 0,
      timing: { totalSeconds: 100, byPhase: { Plan: 10, Implement: 60 } },
    });
    expect(fact.summary).toBe(`2 of ${fact.stages} phases recorded`);
  });

  it('makes no claim when the run detail carries no per-phase record', () => {
    expect(phasesFact(featureSnapshot({ pipeline: 'medium' }), null).summary).toBeNull();
    expect(
      phasesFact(featureSnapshot({ pipeline: 'medium' }), { runNumber: 8, artifactCount: 0 })
        .summary,
    ).toBeNull();
  });
});
