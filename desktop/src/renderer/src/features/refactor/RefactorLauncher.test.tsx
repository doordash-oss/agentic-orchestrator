import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
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

    await user.click(screen.getByRole('button', { name: 'Launch child' }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
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
        new Error('parent_worktrees_dirty: /work/repo-a: 1 staged, 2 untracked'),
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
    await user.click(screen.getByRole('button', { name: 'Launch child' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('1 staged, 2 untracked');
    expect(screen.getByText(/repo-a, repo-b \(inherited\)/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Launch child' }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
    expect(mock.api.launchRefactorChild).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ description: 'Extract the query engine', riskLevel: 'high' }),
    );
  });

  it('requires a child name before leaving the What step', async () => {
    const { user } = await renderLauncher();
    await user.clear(screen.getByLabelText('Child name'));
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    expect(screen.getByText('Enter a child name.')).toBeVisible();
    expect(document.getElementById('refactor-child-name')).toHaveFocus();
  });
});
