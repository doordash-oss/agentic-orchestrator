import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { creationDefaults, installAgenticoMock, readySnapshot } from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

async function renderForm(mock = installAgenticoMock()) {
  const onCreated = vi.fn();
  render(<CreateFeatureForm onCreated={onCreated} />);
  await screen.findByRole('button', { name: 'Next: What' });
  return { mock, onCreated, user: userEvent.setup() };
}

async function reachWhat(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
  await user.click(screen.getByRole('button', { name: 'Next: What' }));
}

async function reachReview(user: ReturnType<typeof userEvent.setup>) {
  await reachWhat(user);
  await user.type(screen.getByLabelText('Name'), 'Search revamp');
  await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
  await user.click(screen.getByRole('radio', { name: /Large/ }));
  await user.click(screen.getByRole('button', { name: 'Next: Review' }));
}

describe('CreateFeatureForm repository-first contract', () => {
  it('starts on Where, requires a repository, and searches the discovered list', async () => {
    const { mock, user } = await renderForm();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    expect(screen.getByText('Select at least one repository.')).toBeVisible();
    expect(mock.api.createFeature).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText('Search repositories'), 'repo-b');
    expect(screen.queryByRole('checkbox', { name: /repo-a/ })).toBeNull();
    expect(screen.getByRole('checkbox', { name: /repo-b/ })).toBeVisible();
    await user.clear(screen.getByLabelText('Search repositories'));
    await user.type(screen.getByLabelText('Search repositories'), 'no-such-repo');
    expect(screen.getByText(/No repositories match/)).toBeVisible();
    await user.clear(screen.getByLabelText('Search repositories'));

    await reachWhat(user);
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    expect(document.getElementById('feature-name')).toHaveFocus();
    expect(screen.getByText('Enter a feature name.')).toBeVisible();
  });

  it('leads an empty workspace with the folder picker instead of a filter miss', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    await renderForm(mock);

    expect(screen.getByRole('heading', { name: 'Add your first repository' })).toBeVisible();
    expect(screen.getByText(/an empty folder to start something new/i)).toBeVisible();
    expect(screen.queryByLabelText('Search repositories')).toBeNull();
    expect(screen.queryByText(/No repositories match/)).toBeNull();
  });

  it('adopts a folder that is itself a repository and selects it', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/solo' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/solo', valid: true }],
        repositories: [{ name: 'solo', path: '/work/solo', valid: true }],
      }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));

    expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/solo');
    expect(mock.api.initRepository).not.toHaveBeenCalled();
    expect(await screen.findByRole('checkbox', { name: /solo/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('offers consented initialization for a folder that holds no repository', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space/fresh' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({ workspaceRoots: [{ path: '/work/space/fresh', valid: true }] }),
    );
    mock.api.initRepository.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/fresh', valid: true }],
        repositories: [{ name: 'fresh', path: '/work/space/fresh', valid: true }],
      }),
    );
    mock.api.removeWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/fresh', valid: true }],
        repositories: [{ name: 'fresh', path: '/work/space/fresh', valid: true }],
      }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    expect(await screen.findByText(/holds no git repository yet/i)).toBeVisible();
    expect(mock.api.initRepository).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: /Initialize it as a repository/ }));
    const dialog = await screen.findByRole('dialog', { name: /initialize a new repository/i });
    expect(dialog).toHaveTextContent('/work/space/fresh');
    await user.click(screen.getByRole('button', { name: 'Initialize repository' }));

    // The parent is configured only for the call the server requires it for,
    // leaving the folder itself as the only root behind.
    expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/space');
    expect(mock.api.initRepository).toHaveBeenCalledWith({
      path: '/work/space/fresh',
      consent: true,
    });
    expect(mock.api.removeWorkspaceRoot).toHaveBeenCalledWith('/work/space');
    expect(await screen.findByRole('checkbox', { name: /fresh/ })).toBeChecked();
  });

  it('rolls the transient parent root back when initialization fails', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space/full' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({ workspaceRoots: [{ path: '/work/space/full', valid: true }] }),
    );
    mock.api.initRepository.mockRejectedValue(
      new Error(
        'directory_not_empty: the directory contains files. Choose an empty folder, a new folder name, or an existing git repository instead.',
      ),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    await user.click(screen.getByRole('button', { name: /Initialize it as a repository/ }));
    await user.click(screen.getByRole('button', { name: 'Initialize repository' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('directory_not_empty');
    expect(alert).toHaveTextContent(/choose an empty folder/i);
    expect(mock.api.removeWorkspaceRoot).toHaveBeenCalledWith('/work/space');
    // The choice stays recoverable without reopening the picker.
    expect(screen.getByText('/work/space/full')).toBeVisible();
    expect(screen.getByRole('button', { name: /Initialize it as a repository/ })).toBeEnabled();
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

    await reachWhat(user);
    await user.type(screen.getByLabelText('Name'), 'Preserved draft');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('radio', { name: /Moonshot/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.selectOptions(screen.getByLabelText('Inquireness'), 'high');
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));

    expect(await screen.findByRole('checkbox', { name: /repo-new/ })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Preserved draft');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    expect(screen.getByRole('radio', { name: /Moonshot/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    expect(screen.getByLabelText('Inquireness')).toHaveValue('high');
  });

  it('attaches photos and files from the composer attach menu and permits removal', async () => {
    const mock = installAgenticoMock();
    mock.api.pickCreationFiles.mockImplementation((kind: string) =>
      Promise.resolve({
        paths: kind === 'image' ? ['/safe/one.png', '/safe/two.png'] : ['/safe/spec.pdf'],
      }),
    );
    const { user } = await renderForm(mock);
    await reachWhat(user);

    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    expect(mock.api.pickCreationFiles).toHaveBeenCalledWith('image');
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add files' }));
    expect(mock.api.pickCreationFiles).toHaveBeenCalledWith('attachment');

    expect(await screen.findByText(/one\.png/)).toBeVisible();
    expect(screen.getByText(/two\.png/)).toBeVisible();
    expect(screen.getByText(/spec\.pdf/)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Remove one.png' }));
    expect(screen.queryByText(/one\.png/)).toBeNull();
  });

  it('splits pasted and dropped clipboard files into images and attachments', async () => {
    const mock = installAgenticoMock();
    mock.api.importDroppedCreationFiles.mockImplementation((kind: string) => ({
      paths: kind === 'image' ? ['/safe/pasted.png'] : ['/safe/notes.pdf'],
    }));
    const { user } = await renderForm(mock);
    await reachWhat(user);

    fireEvent.paste(screen.getByLabelText('Description'), {
      clipboardData: {
        files: [
          new File(['i'], 'pasted.png', { type: 'image/png' }),
          new File(['d'], 'notes.pdf', { type: 'application/pdf' }),
        ],
        items: [{ type: 'image/png' }],
      },
    });
    expect(mock.api.importDroppedCreationFiles).toHaveBeenCalledWith('image', expect.any(Array));
    expect(mock.api.importDroppedCreationFiles).toHaveBeenCalledWith(
      'attachment',
      expect.any(Array),
    );
    expect(screen.getByText(/pasted\.png/)).toBeVisible();
    expect(screen.getByText(/notes\.pdf/)).toBeVisible();
  });

  it('materializes pasted clipboard bitmaps that have no filesystem path', async () => {
    const mock = installAgenticoMock();
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    const { user } = await renderForm(mock);
    await reachWhat(user);

    fireEvent.paste(screen.getByLabelText('Description'), {
      clipboardData: {
        files: [new File(['image'], 'image.png', { type: 'image/png' })],
        items: [{ type: 'image/png' }],
      },
    });

    expect(mock.api.readClipboardImage).toHaveBeenCalledOnce();
    expect(await screen.findByText(/clipboard-image\.png/)).toBeVisible();
  });

  it('offers @ file mentions scoped to the selected repositories', async () => {
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
    await reachWhat(user);

    await user.type(screen.getByLabelText('Description'), 'Refactor @cre');
    const option = await screen.findByRole('option', { name: /repo-a.*src\/creation\.ts/ });
    await user.click(option);

    expect(screen.getByLabelText('Description')).toHaveValue('Refactor @repo-a/src/creation.ts ');
    expect(
      screen.getByRole('button', { name: 'Remove reference repo-a/src/creation.ts' }),
    ).toBeVisible();
    expect(mock.api.searchCreationFiles).toHaveBeenCalledWith(
      expect.objectContaining({ repoKeys: ['repo-a'], query: 'cre' }),
    );

    // Deselecting the repository prunes its referenced files.
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    expect(
      screen.queryByRole('button', { name: 'Remove reference repo-a/src/creation.ts' }),
    ).toBeNull();
  });

  it('lets discovery-backed models be chosen per phase and submits the selection', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(creationDefaults());
    mock.api.getModelCatalogue.mockResolvedValue({
      providerOrder: ['claude'],
      providerModels: {
        claude: [
          { id: 'claude-opus', effortCapabilities: ['low', 'medium', 'high', 'max'] },
          { id: 'claude-sonnet-4-5', effortCapabilities: ['low', 'medium', 'high'] },
        ],
      },
      phaseDefaults: { planning: 'model-plan' },
      phaseProviderModels: { planning: { claude: ['claude-opus', 'claude-sonnet-4-5'] } },
    });
    const { onCreated, user } = await renderForm(mock);
    await reachReview(user);

    const planningPicker = await screen.findByLabelText('Planning model');
    await user.selectOptions(planningPicker, 'claude-opus');
    await user.selectOptions(screen.getByLabelText('Planning effort'), 'max');
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({
        models: { planning: 'claude-opus' },
        effort: { planning: 'max' },
      }),
    );
  });

  it('lets checkpoints be toggled, scoped to the pipeline profile', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(creationDefaults());
    const { onCreated, user } = await renderForm(mock);
    await reachReview(user);

    // Large profile: inquiry gate is applicable and on by default; turn it off
    // and turn manual publish on.
    const inquiryGate = screen.getByRole('checkbox', { name: /Inquiry review/ });
    expect(inquiryGate).toBeChecked();
    await user.click(inquiryGate);
    await user.click(screen.getByRole('checkbox', { name: /Manual publish/ }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({
        checkpoints: {
          inquiryReview: false,
          researchReview: false,
          designReview: false,
          roadmapReview: true,
          phasePlanReview: true,
          manualPublish: true,
          draftPublish: false,
        },
      }),
    );
  });

  it('hides gates the medium pipeline does not support', async () => {
    const { user } = await renderForm();
    await reachWhat(user);
    await user.type(screen.getByLabelText('Name'), 'Medium scope');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));

    expect(screen.queryByRole('checkbox', { name: /Inquiry review/ })).toBeNull();
    expect(screen.getByRole('checkbox', { name: /Phase plan review/ })).toBeChecked();
  });

  it('shows only the model rows the chosen pipeline runs', async () => {
    const { user } = await renderForm();
    await reachWhat(user);
    await user.type(screen.getByLabelText('Name'), 'Scoped models');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));

    // Medium runs planning, implementation, and review only.
    expect(screen.getByLabelText('Planning model')).toBeVisible();
    expect(screen.queryByLabelText('Clarify model')).toBeNull();
    expect(screen.queryByLabelText('KB Build model')).toBeNull();
    expect(screen.queryByLabelText('Utilities model')).toBeNull();
    expect(screen.queryByLabelText('Automatic review model')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    expect(screen.getByLabelText('Clarify model')).toBeVisible();
    expect(screen.getByLabelText('KB Build model')).toBeVisible();
    // Utilities and the automatic reviewer stay workspace-scoped.
    expect(screen.queryByLabelText('Utilities model')).toBeNull();
    expect(screen.queryByLabelText('Automatic review model')).toBeNull();
  });

  it('submits the contract without untouched model defaults, auto-starts the feature, and keeps one idempotency identity', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(
      creationDefaults({
        defaults: {
          pipeline: 'medium',
          inquireness: 'medium',
          models: [
            { phase: 'Planning', model: 'model-plan' },
            { phase: 'Knowledge base', model: 'model-kb' },
          ],
          effort: [{ phase: 'Planning', effort: 'auto' }],
          useCurrentBranch: false,
        },
      }),
    );
    mock.api.pickCreationFiles.mockResolvedValueOnce({ paths: ['/safe/screen.png'] });
    const { onCreated, user } = await renderForm(mock);
    await reachWhat(user);
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.selectOptions(screen.getByLabelText('Risk'), 'high');
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

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
        models: {},
        idempotencyKey: expect.stringMatching(/^[0-9a-f-]{36}$/),
      }),
    );
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: 'abcd1234ef567890',
      action: 'start',
    });
  });

  it('only queues setup when Start immediately is opted out', async () => {
    const mock = installAgenticoMock();
    const { onCreated, user } = await renderForm(mock);
    await reachReview(user);

    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.dispatchFeatureSetup).toHaveBeenCalledWith('abcd1234ef567890');
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it('keeps the final submit single-flight and retryable after an authoritative error', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(creationDefaults());
    mock.api.createFeature.mockRejectedValueOnce(new Error('conflict: try again'));
    const { user } = await renderForm(mock);
    await reachReview(user);
    await user.click(screen.getByRole('button', { name: 'Create and start' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('conflict');
    expect(screen.getByRole('button', { name: 'Create and start' })).toBeEnabled();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });
});
