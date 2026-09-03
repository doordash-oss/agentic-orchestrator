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

import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  featureConfigSnapshot,
  featureSnapshot,
  installAgenticoMock,
} from '../../test/agenticoMock';
import { RefactorLauncher } from './RefactorLauncher';

afterEach(cleanup);

const PARENT_ID = 'abcd1234ef567890';

async function renderLauncher({
  mock = installAgenticoMock(),
  snapshot = featureSnapshot({ repos: ['repo-a', 'repo-b'] }),
  onCancel = vi.fn(),
  onDispatched = vi.fn(),
} = {}) {
  mock.api.getFeatureConfig.mockResolvedValue(
    featureConfigSnapshot({
      current: {
        pipeline: 'large',
        inquireness: 'high',
        models: { planning: 'model-plan-parent' },
        effort: { planning: 'high' },
        checkpoints: {
          inquiryReview: true,
          researchReview: false,
          designReview: false,
          roadmapReview: true,
          phasePlanReview: true,
          manualPublish: true,
          draftPublish: false,
        },
      },
      defaults: { models: { planning: 'model-plan' }, effort: { planning: 'medium' } },
    }),
  );
  render(
    <RefactorLauncher
      featureId={PARENT_ID}
      snapshot={snapshot}
      onCancel={onCancel}
      onDispatched={onDispatched}
    />,
  );
  await screen.findByLabelText('Child name');
  return { mock, onCancel, onDispatched, user: userEvent.setup() };
}

describe('RefactorLauncher', () => {
  it('seeds every review axis from the parent and submits the full run contract', async () => {
    const { mock, onDispatched, onCancel, user } = await renderLauncher({
      snapshot: featureSnapshot({
        repos: ['repo-a', 'repo-b'],
        riskLevel: 'high',
        exitCriteria: 'No behavior change.',
      }),
    });
    mock.api.launchRefactorChild.mockResolvedValue({
      childId: 'child1234ef567890',
      parentId: PARENT_ID,
      result: 'created',
    });

    // Where is inherited, not chosen.
    expect(screen.getByText(/Inherited from Search revamp/)).toBeVisible();
    expect(screen.getByLabelText('Child name')).toHaveValue('Refactor Search revamp');
    await user.type(screen.getByLabelText('Brief'), 'Extract the query engine');

    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    // The child's pipeline starts at the parent's, as its own choice.
    expect(screen.getByRole('radio', { name: /Large/ })).toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    expect(screen.getByLabelText('Inquireness')).toHaveValue('high');
    expect(screen.getByLabelText('Risk')).toHaveValue('high');
    expect(screen.getByDisplayValue('No behavior change.')).toBeVisible();
    expect(screen.getByRole('checkbox', { name: /Inquiry review/ })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Manual publish/ })).toBeChecked();
    expect(screen.getByLabelText('Planning model')).toBeVisible();
    // The model and effort pickers share one trailing unit in the grouped row.
    const picksUnit = screen
      .getByLabelText('Planning model')
      .closest('.config-editor__phase-row-picks');
    expect(picksUnit).not.toBeNull();
    expect(
      screen.getByLabelText('Planning effort').closest('.config-editor__phase-row-picks'),
    ).toBe(picksUnit);

    expect(screen.getByRole('checkbox', { name: /Start immediately/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Launch and start' }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
    expect(onDispatched).toHaveBeenCalledWith({ childId: 'child1234ef567890', autoStart: true });
    expect(mock.api.launchRefactorChild).toHaveBeenCalledWith({
      parentId: PARENT_ID,
      name: 'Refactor Search revamp',
      description: 'Extract the query engine',
      pipeline: 'large',
      riskLevel: 'high',
      inquireness: 'high',
      exitCriteria: 'No behavior change.',
      models: { planning: 'model-plan-parent' },
      effort: { planning: 'high' },
      checkpoints: {
        inquiryReview: true,
        researchReview: false,
        designReview: false,
        roadmapReview: true,
        phasePlanReview: true,
        manualPublish: true,
        draftPublish: false,
      },
    });
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('keeps the parent-seeded checkpoints when the child picks another pipeline', async () => {
    const { user } = await renderLauncher();

    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('radio', { name: /Moonshot/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));

    // The moonshot profile would enable research review; the parent seed wins.
    expect(screen.getByRole('checkbox', { name: /Research review/ })).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Manual publish/ })).toBeChecked();
  });

  it('imports pasted images into the brief composer', async () => {
    const { mock } = await renderLauncher();
    mock.api.importDroppedCreationFiles.mockImplementation((kind: string) => ({
      paths: kind === 'image' ? ['/safe/pasted.png'] : [],
    }));

    fireEvent.paste(screen.getByLabelText('Brief'), {
      clipboardData: {
        files: [new File(['i'], 'pasted.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });

    expect(mock.api.importDroppedCreationFiles).toHaveBeenCalledWith('image', expect.any(Array));
    expect(await screen.findByText(/pasted\.png/)).toBeVisible();
  });

  it('offers @ file mentions scoped to the inherited repositories', async () => {
    const { mock, user } = await renderLauncher();
    mock.api.searchCreationFiles.mockImplementation((request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [{ repoKey: 'repo-a', path: 'src/query.ts' }],
        truncated: false,
        cancelled: false,
      }),
    );

    await user.type(screen.getByLabelText('Brief'), 'Refactor @que');
    const option = await screen.findByRole('option', { name: /repo-a.*src\/query\.ts/ });
    await user.click(option);

    expect(screen.getByLabelText('Brief')).toHaveValue('Refactor @repo-a/src/query.ts ');
    expect(mock.api.searchCreationFiles).toHaveBeenCalledWith(
      expect.objectContaining({ repoKeys: ['repo-a', 'repo-b'], query: 'que' }),
    );
  });

  it('preserves the full draft after a dirty-parent launch rejection and retries it unchanged', async () => {
    const { mock, onDispatched, user } = await renderLauncher();
    mock.api.launchRefactorChild
      .mockRejectedValueOnce(
        Object.assign(
          new Error(
            "parent_worktrees_dirty: The parent feature's worktrees have uncommitted changes.",
          ),
          {
            canonical: {
              code: 'parent_worktrees_dirty',
              class: 'needs_action',
              title: 'Parent worktrees are dirty',
              summary: "The parent feature's worktrees have uncommitted changes.",
              remediation: {
                hint: 'Commit or stash the listed changes in each repository, then retry.',
              },
              context: {
                repositories: [{ name: 'repo-a', dirty_files: ['src/one.ts', 'src/two.ts'] }],
              },
            },
          },
        ),
      )
      .mockResolvedValueOnce({
        childId: 'child1234ef567890',
        parentId: PARENT_ID,
        result: 'created',
      });

    await user.type(screen.getByLabelText('Brief'), 'Extract the query engine');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.selectOptions(screen.getByLabelText('Risk'), 'high');
    await user.click(screen.getByRole('button', { name: 'Launch and start' }));

    // The canonical needs-action surface carries the class label and hint, and
    // the dirty repository list rides the compact disclosure (presence, not
    // visibility — it is folded by default).
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface--needs-action');
    expect(screen.getByText('Needs your action')).toBeVisible();
    expect(
      screen.getByText('Commit or stash the listed changes in each repository, then retry.'),
    ).toBeVisible();
    expect(within(surface).getByText('repo-a')).toBeInTheDocument();
    expect(within(surface).getByText('src/one.ts')).toBeInTheDocument();
    expect(within(surface).getByText('src/two.ts')).toBeInTheDocument();
    expect(screen.getByText('parent_worktrees_dirty')).toHaveClass('error-surface__code');
    // The rejection banner still takes focus through the forwarded root ref.
    expect(surface).toHaveFocus();
    expect(screen.getByText(/repo-a, repo-b \(inherited\)/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Launch and start' }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
    expect(mock.api.launchRefactorChild).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ description: 'Extract the query engine', riskLevel: 'high' }),
    );
  });

  it('launches without auto-start when Start immediately is unchecked', async () => {
    const { mock, onDispatched, user } = await renderLauncher();
    mock.api.launchRefactorChild.mockResolvedValue({
      childId: 'child1234ef567890',
      parentId: PARENT_ID,
      result: 'created',
    });

    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));

    await user.click(screen.getByRole('button', { name: 'Launch child' }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
    expect(onDispatched).toHaveBeenCalledWith({ childId: 'child1234ef567890', autoStart: false });
  });

  it('requires a child name before leaving the What step', async () => {
    const { user } = await renderLauncher();
    await user.clear(screen.getByLabelText('Child name'));
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    expect(screen.getByText('Enter a child name.')).toBeVisible();
    // Client-side validation is a per-field message exposed as the input's
    // description — no form-level error surface.
    expect(screen.getByText('Enter a child name.')).toHaveClass('field-error');
    expect(screen.getByText('Enter a child name.')).toHaveAttribute(
      'id',
      'refactor-child-name-error',
    );
    const input = document.getElementById('refactor-child-name') as HTMLElement;
    expect(input).toHaveAttribute('aria-describedby', 'refactor-child-name-error');
    expect(input).toHaveAttribute('aria-invalid', 'true');
    expect(document.querySelector('.error-surface')).toBeNull();
    expect(input).toHaveFocus();
  });
});
