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

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { RewindJourney } from './RewindJourney';

afterEach(cleanup);

const FEATURE_ID = 'abcd1234ef567890';

function journeyProps(overrides: Partial<Parameters<typeof RewindJourney>[0]> = {}) {
  return {
    featureId: FEATURE_ID,
    featureName: 'Search revamp',
    validPhaseOptions: ['implement'],
    onClose: vi.fn(),
    onRewindComplete: vi.fn(),
    ...overrides,
  };
}

describe('RewindJourney error surfaces', () => {
  it('renders a rejected preview as a compact ErrorSurface', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockRejectedValue(
      ipcError('E_INTERNAL', 'The rewind preview could not be computed.'),
    );
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('E_INTERNAL')).toHaveClass('error-surface__code');
    expect(within(alert).getByText('The rewind preview could not be computed.')).toBeVisible();
    // The legacy step-error markup is gone.
    expect(document.querySelector('.rewind-journey__error')).toBeNull();
  });

  it('renders validation findings as FieldError elements described by the target radiogroup', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue({
      eligible: false,
      sourceRevision: 'rev-1',
      targetPhase: 'implement',
      effectivePhase: 'implement',
      validationFindings: ['The worktree for repository "repo-a" has uncommitted changes.'],
    });
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));

    const findings = await screen.findByLabelText('Validation findings');
    const fieldError = within(findings).getByText(
      'The worktree for repository "repo-a" has uncommitted changes.',
    );
    expect(fieldError).toHaveClass('field-error');
    expect(fieldError).toHaveAttribute('id', 'rewind-finding-0');
    // The findings are wired to the step's input.
    const radiogroup = screen.getByRole('radiogroup');
    expect(radiogroup).toHaveAttribute('aria-describedby', 'rewind-finding-0');
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled();
    expect(document.querySelector('.rewind-journey__findings')).toBeNull();
  });

  it('renders the unavailable-targets notice as a compact ErrorSurface', () => {
    installAgenticoMock();
    render(<RewindJourney {...journeyProps({ validPhaseOptions: [] })} />);

    const alert = screen.getByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('E_REWIND_TARGETS_UNAVAILABLE')).toHaveClass(
      'error-surface__code',
    );
    expect(within(alert).getByText('Rewind targets are no longer available.')).toBeVisible();
    expect(within(alert).getByText('Refresh the feature and try again.')).toHaveClass(
      'error-surface__remediation-hint',
    );
  });

  it('renders a failed rewind as a compact ErrorSurface whose Retry returns to the target step', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue({
      eligible: true,
      sourceRevision: 'rev-1',
      sourceRunNumber: 3,
      targetPhase: 'implement',
      effectivePhase: 'implement',
    });
    mock.api.executeRewind.mockRejectedValue(ipcError('E_INTERNAL', 'The rewind was refused.'));
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.type(screen.getByLabelText(/Type REWIND to confirm/), 'REWIND');
    await user.click(screen.getByRole('button', { name: /^Rewind$/ }));

    // The fork did not advance (getFeature reports the same run), so the
    // terminal failure result renders as one compact ErrorSurface.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(alert).toHaveTextContent('Rewind could not be completed');
    expect(within(alert).getByText('The rewind was refused.')).toBeVisible();
    expect(within(alert).getByRole('button', { name: 'Retry' })).toHaveClass(
      'error-surface__action',
    );
    expect(document.querySelector('.rewind-journey__error-result')).toBeNull();

    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    // Retry returns to the target step with the failure cleared.
    expect(await screen.findByRole('radio', { name: 'Implement' })).toBeVisible();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    // The legacy error-result markup and its "Try again" label are gone.
    expect(document.querySelector('.rewind-journey__retry')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });
});

function stackedPreviewConsequences() {
  return {
    eligible: true,
    sourceRevision: 'rev-stack-1',
    sourceRunNumber: 3,
    targetPhase: 'implement',
    effectivePhase: 'implement',
    roadmapPhase: 3,
    prConsequences: [
      {
        repo: 'repo-a',
        position: 1,
        title: 'Core',
        branch: 'feature/ws/1-core',
        prUrl: 'https://github.example/repo-a/pull/1',
        prState: 'open',
        verdict: 'keep',
        deleteRemoteBranch: false,
      },
      {
        repo: 'repo-a',
        position: 2,
        title: 'Extension',
        branch: 'feature/ws/2-ext',
        prUrl: 'https://github.example/repo-a/pull/2',
        prState: 'open',
        verdict: 'close',
        deleteRemoteBranch: true,
      },
      {
        repo: 'repo-a',
        position: 3,
        title: 'Cleanup',
        branch: 'feature/ws/3-cleanup',
        prUrl: 'https://github.example/repo-a/pull/3',
        prState: 'merged',
        verdict: 'merged',
        deleteRemoteBranch: false,
      },
      {
        repo: 'repo-b',
        position: 1,
        title: 'Core',
        branch: 'feature/ws/1-core',
        prState: 'none',
        verdict: 'none',
        deleteRemoteBranch: false,
      },
      {
        repo: 'repo-b',
        position: 2,
        title: 'Extension',
        branch: 'feature/ws/2-ext',
        prUrl: 'https://github.example/repo-b/pull/4',
        prState: 'open',
        verdict: 'close',
        deleteRemoteBranch: true,
      },
      {
        repo: 'repo-b',
        position: 3,
        title: 'Cleanup',
        branch: 'feature/ws/3-cleanup',
        prState: 'none',
        verdict: 'close',
        deleteRemoteBranch: true,
      },
    ],
  };
}

/** Asserts the per-repository stack rows render exactly once per step. */
function expectStackRowsVisible() {
  const repoA = screen.getByRole('list', { name: 'Stack pull requests for repo-a' });
  const repoB = screen.getByRole('list', { name: 'Stack pull requests for repo-b' });
  expect(within(repoA).getAllByRole('listitem')).toHaveLength(3);
  expect(within(repoB).getAllByRole('listitem')).toHaveLength(3);
  expect(within(repoA).getByText('Layer 1')).toBeVisible();
  expect(within(repoA).getByText('Keeps open')).toHaveAttribute('data-verdict', 'keep');
  expect(within(repoA).getByText('Will close')).toHaveAttribute('data-verdict', 'close');
  expect(within(repoA).getByText('Merged and left as is')).toHaveAttribute(
    'data-verdict',
    'merged',
  );
  expect(within(repoB).getByText('No pull request')).toHaveAttribute('data-verdict', 'none');
  expect(within(repoB).getAllByText('Will close')).toHaveLength(2);
  // Links appear only on rows that carry a pull request URL.
  expect(within(repoA).getAllByRole('button', { name: 'Open pull request' })).toHaveLength(3);
  expect(within(repoB).getAllByRole('button', { name: 'Open pull request' })).toHaveLength(1);
  // The deletion fact lists every flagged repository and branch.
  expect(
    screen.getByText(
      'repo-a (feature/ws/2-ext), repo-b (feature/ws/2-ext), repo-b (feature/ws/3-cleanup)',
    ),
  ).toBeVisible();
}

describe('RewindJourney stack consequences', () => {
  it('renders per-repository layer rows with verdict badges, external links, and the deletion fact', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue(stackedPreviewConsequences());
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());

    expectStackRowsVisible();

    // Clicking a row's link opens the pull request externally.
    const links = screen.getAllByRole('button', { name: 'Open pull request' });
    await user.click(links[1]!);
    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.example/repo-a/pull/2',
    });
  });

  it('omits the deletion fact when no remote branch is flagged', async () => {
    const mock = installAgenticoMock();
    const preview = stackedPreviewConsequences();
    for (const entry of preview.prConsequences) {
      entry.deleteRemoteBranch = false;
    }
    mock.api.getRewindPreview.mockResolvedValue(preview);
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());

    expect(screen.getByRole('list', { name: 'Stack pull requests for repo-a' })).toBeVisible();
    expect(screen.queryByText('Remote branches deleted')).not.toBeInTheDocument();
  });

  it('shows the same layer rows on the confirmation step as the preview step', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue(stackedPreviewConsequences());
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Continue' }));

    // The confirmation step repeats the preview's consequence facts through
    // the same shared renderer.
    expectStackRowsVisible();
  });
});
