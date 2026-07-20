import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { creationDefaults, installAgenticoMock, readySnapshot } from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

async function renderForm(mock = installAgenticoMock()) {
  const onCreated = vi.fn();
  render(<CreateFeatureForm onCreated={onCreated} />);
  await screen.findByRole('button', { name: 'Next: Where' });
  return { mock, onCreated, user: userEvent.setup() };
}

async function reachReview(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Name'), 'Search revamp');
  await user.click(screen.getByRole('button', { name: 'Next: Where' }));
  await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
  await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
  await user.click(screen.getByRole('radio', { name: /Large/ }));
  await user.click(screen.getByRole('button', { name: 'Next: Review' }));
}

describe('CreateFeatureForm four-step contract', () => {
  it('validates each step, preserves the draft on Back, and focuses the owning field', async () => {
    const { mock, user } = await renderForm();
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    expect(document.getElementById('feature-name')).toHaveFocus();
    expect(mock.api.createFeature).not.toHaveBeenCalled();

    await user.type(document.getElementById('feature-name') as HTMLInputElement, 'Draft survives');
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(document.getElementById('feature-name')).toHaveValue('Draft survives');
  });

  it('rediscovering repositories preserves the current step and chosen run contract', async () => {
    const mock = installAgenticoMock();
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/new-root' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        repositories: [
          { name: 'repo-a', path: '/work/space/repo-a', valid: true },
          { name: 'repo-new', path: '/work/new-root/repo-new', valid: true },
        ],
      }),
    );
    const { user } = await renderForm(mock);

    await user.type(screen.getByLabelText('Name'), 'Preserved draft');
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('radio', { name: /Moonshot/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.selectOptions(screen.getByLabelText('Inquireness'), 'always');
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Add workspace root' }));

    expect(await screen.findByRole('checkbox', { name: /repo-new/ })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    expect(screen.getByRole('radio', { name: /Moonshot/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    expect(screen.getByLabelText('Inquireness')).toHaveValue('always');
  });

  it('adds ordered native-picked files and permits removal without reading their contents', async () => {
    const mock = installAgenticoMock();
    mock.api.pickCreationFiles
      .mockResolvedValueOnce({ paths: ['/safe/one.png', '/safe/two.png'] })
      .mockResolvedValueOnce({ paths: ['/safe/spec.pdf'] });
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('button', { name: 'Choose images' }));
    await user.click(screen.getByRole('button', { name: 'Choose attachments' }));
    expect(screen.getByText('one.png')).toBeVisible();
    expect(screen.getByText('two.png')).toBeVisible();
    expect(screen.getByText('spec.pdf')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Remove one.png' }));
    expect(screen.queryByText('one.png')).toBeNull();
  });

  it('accepts ordered image drop/paste through the narrow preload file seam', async () => {
    const mock = installAgenticoMock();
    mock.api.importDroppedCreationFiles
      .mockReturnValueOnce({ paths: ['/safe/drop-one.png', '/safe/drop-two.png'] })
      .mockReturnValueOnce({ paths: ['/safe/pasted.png'] });
    await renderForm(mock);
    const shelf = screen.getByRole('region', { name: 'Images' });
    fireEvent.drop(shelf, {
      dataTransfer: { files: [new File(['a'], 'a.png', { type: 'image/png' })] },
    });
    fireEvent.paste(shelf, {
      clipboardData: { files: [new File(['b'], 'b.png', { type: 'image/png' })] },
    });
    expect(screen.getByText('drop-one.png')).toBeVisible();
    expect(screen.getByText('drop-two.png')).toBeVisible();
    expect(screen.getByText('pasted.png')).toBeVisible();
  });

  it('cancels stale repository searches and selects bounded relative matches', async () => {
    const mock = installAgenticoMock();
    mock.api.searchCreationFiles.mockImplementation((request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [{ repoKey: 'repo-a', path: 'src/creation.ts' }],
        truncated: false,
        cancelled: false,
      }),
    );
    const { user } = await renderForm(mock);
    await user.type(document.getElementById('feature-name') as HTMLInputElement, 'Indexed');
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.type(screen.getByPlaceholderText('Fuzzy search selected repositories'), 'crt');
    expect(await screen.findByText('src/creation.ts')).toBeVisible();
    expect(mock.api.cancelCreationFileSearch).not.toHaveBeenCalled();
    await user.click(screen.getByRole('checkbox', { name: /repo-a.*src\/creation.ts/ }));
    await user.type(screen.getByPlaceholderText('Fuzzy search selected repositories'), 'x');
    expect(mock.api.cancelCreationFileSearch).toHaveBeenCalled();
  });

  it('caps repository files and prunes them when their repository is deselected', async () => {
    const mock = installAgenticoMock();
    mock.api.searchCreationFiles.mockImplementation((request) =>
      Promise.resolve({
        requestId: request.requestId,
        files: Array.from({ length: 25 }, (_, index) => ({
          repoKey: 'repo-a',
          path: `src/file-${index}.ts`,
        })),
        truncated: false,
        cancelled: false,
      }),
    );
    const { user } = await renderForm(mock);
    await user.type(screen.getByLabelText('Name'), 'Bounded context');
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.type(screen.getByPlaceholderText('Fuzzy search selected repositories'), 'file');
    await screen.findByText('src/file-24.ts');

    const fileChoices = screen.getAllByRole('checkbox');
    for (const choice of fileChoices.slice(0, 24)) await user.click(choice);
    expect(fileChoices[24]).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    const refreshedFirstChoice = await screen.findByRole('checkbox', {
      name: /repo-a.*src\/file-0.ts/,
    });
    expect(refreshedFirstChoice).not.toBeChecked();
  });

  it('submits pipeline, review, skills, files, and one stable idempotency identity', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(
      creationDefaults({
        defaults: {
          pipeline: 'medium',
          inquireness: 'balanced',
          models: [
            { phase: 'Planning', model: 'model-plan' },
            { phase: 'Knowledge base', model: 'model-kb' },
          ],
          useCurrentBranch: false,
        },
      }),
    );
    mock.api.listResources.mockResolvedValue({
      resources: [
        {
          id: 'skill:frontend-design',
          kind: 'skill',
          label: 'Frontend design',
          contentType: 'markdown',
          revision: 'r1',
          validatable: true,
        },
      ],
    });
    mock.api.pickCreationFiles.mockResolvedValueOnce({ paths: ['/safe/screen.png'] });
    const { onCreated, user } = await renderForm(mock);
    await user.click(screen.getByRole('button', { name: 'Choose images' }));
    await reachReview(user);
    await user.selectOptions(screen.getByLabelText('Risk'), 'high');
    await user.click(screen.getByRole('checkbox', { name: 'Frontend design' }));
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'Search revamp',
        repoKeys: ['repo-a'],
        images: ['/safe/screen.png'],
        pipeline: 'large',
        riskLevel: 'high',
        checkpoints: {
          inquiryReview: true,
          researchReview: false,
          designReview: false,
          roadmapReview: true,
          phasePlanReview: true,
          manualPublish: false,
          draftPublish: false,
        },
        models: { planning: 'model-plan', kb_build: 'model-kb' },
        skills: ['skill:frontend-design'],
        idempotencyKey: expect.stringMatching(/^[0-9a-f-]{36}$/),
      }),
    );
    expect(mock.api.dispatchFeatureSetup).toHaveBeenCalledWith('abcd1234ef567890');
  });

  it('keeps the final submit single-flight and retryable after an authoritative error', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(creationDefaults());
    mock.api.createFeature.mockRejectedValueOnce(new Error('conflict: try again'));
    const { user } = await renderForm(mock);
    await reachReview(user);
    await user.click(screen.getByRole('button', { name: 'Create feature' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('conflict');
    expect(screen.getByRole('button', { name: 'Create feature' })).toBeEnabled();
  });
});
