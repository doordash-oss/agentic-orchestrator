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

import { render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { useAftercareEvidence } from './useAftercareEvidence';
import type { AftercareEvidence } from './useAftercareEvidence';

afterEach(() => {
  vi.restoreAllMocks();
});

function Probe({
  repos,
  hasPullRequest = true,
  enabled = true,
  onRender,
}: {
  repos: string[];
  hasPullRequest?: boolean;
  enabled?: boolean;
  onRender(evidence: AftercareEvidence): void;
}) {
  onRender(useAftercareEvidence('abcd1234ef567890', repos, hasPullRequest, enabled));
  return null;
}

describe('useAftercareEvidence', () => {
  it('fetches each repository diff and the review feedback once per mount', async () => {
    const { api } = installAgenticoMock();
    const getRepositoryDiff = api.getRepositoryDiff.mockImplementation(
      (request: { featureId: string; repo: string }) =>
        Promise.resolve({ featureId: request.featureId, repo: request.repo, files: [] }),
    );
    const fetchReviewFeedback = api.fetchReviewFeedback.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repos: [],
    });
    let latest: AftercareEvidence = { diffs: [], reviewFeedback: null };
    const view = render(
      <Probe
        repos={['api', 'web']}
        onRender={(evidence) => {
          latest = evidence;
        }}
      />,
    );

    await waitFor(() => expect(latest.diffs).toHaveLength(2));
    await waitFor(() => expect(latest.reviewFeedback).not.toBeNull());
    expect(getRepositoryDiff).toHaveBeenCalledTimes(2);
    expect(fetchReviewFeedback).toHaveBeenCalledTimes(1);

    // A poll cycle re-renders with a fresh repository array; the fetches key on
    // the names, so nothing refetches.
    view.rerender(
      <Probe
        repos={['api', 'web']}
        onRender={(evidence) => {
          latest = evidence;
        }}
      />,
    );
    expect(getRepositoryDiff).toHaveBeenCalledTimes(2);
    expect(fetchReviewFeedback).toHaveBeenCalledTimes(1);
  });

  it('degrades to omission when a fetch rejects', async () => {
    const { api } = installAgenticoMock();
    api.getRepositoryDiff.mockRejectedValue(new Error('worktree reclaimed'));
    api.fetchReviewFeedback.mockRejectedValue(new Error('offline'));
    let latest: AftercareEvidence = { diffs: [], reviewFeedback: null };
    render(
      <Probe
        repos={['api']}
        onRender={(evidence) => {
          latest = evidence;
        }}
      />,
    );
    await waitFor(() => expect(latest.diffs).toEqual([]));
    expect(latest.reviewFeedback).toBeNull();
  });

  it('fetches nothing while the surface is not aftercare, or without a pull request', async () => {
    const { api } = installAgenticoMock();
    const getRepositoryDiff = api.getRepositoryDiff.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repo: 'api',
      files: [],
    });
    const fetchReviewFeedback = api.fetchReviewFeedback;
    render(<Probe repos={['api']} enabled={false} onRender={() => undefined} />);
    expect(getRepositoryDiff).not.toHaveBeenCalled();

    render(<Probe repos={['api']} hasPullRequest={false} onRender={() => undefined} />);
    await waitFor(() => expect(getRepositoryDiff).toHaveBeenCalledTimes(1));
    expect(fetchReviewFeedback).not.toHaveBeenCalled();
  });
});
