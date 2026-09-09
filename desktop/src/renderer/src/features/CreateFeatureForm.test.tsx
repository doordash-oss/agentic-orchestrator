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
import type {
  ConnectionState,
  RepositoryOriginStatusRequest,
  RepositoryOriginStatusResult,
  RepositorySourcesRequest,
  RepositorySourcesResult,
  RepositoryUpdateSourceResult,
  RepositorySourceReconcileResult,
} from '../../../shared/ipc';
import {
  creationDefaults,
  installAgenticoMock,
  ipcError,
  mockRepoIdentity,
  readySnapshot,
} from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

const READY_REMOTE: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Runtime ready.',
  ownership: 'external',
  kind: 'remote',
};

async function renderForm(mock = installAgenticoMock()) {
  const onCreated = vi.fn();
  const onClose = vi.fn();
  render(<CreateFeatureForm onCreated={onCreated} onClose={onClose} />);
  await screen.findByRole('button', { name: 'Next: Describe' });
  return { mock, onCreated, onClose, user: userEvent.setup() };
}

async function reachDescribe(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
  await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
}

async function reachContract(user: ReturnType<typeof userEvent.setup>) {
  await reachDescribe(user);
  await user.type(screen.getByLabelText('Name'), 'Search revamp');
  await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
  await user.click(screen.getByRole('radio', { name: /Large/ }));
  await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
}

describe('the creation sheet across its four steps', () => {
  it('starts on Repositories, requires a repository, and searches the discovered list', async () => {
    const { mock, user } = await renderForm();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByText('Select at least one repository.')).toBeVisible();
    expect(mock.api.createFeature).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText('Search repositories'), 'repo-b');
    expect(screen.queryByRole('checkbox', { name: /repo-a/ })).toBeNull();
    expect(screen.getByRole('checkbox', { name: /repo-b/ })).toBeVisible();
    await user.clear(screen.getByLabelText('Search repositories'));
    await user.type(screen.getByLabelText('Search repositories'), 'no-such-repo');
    expect(screen.getByText(/No repositories match/)).toBeVisible();
    await user.clear(screen.getByLabelText('Search repositories'));

    await reachDescribe(user);
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    expect(document.getElementById('feature-name')).toHaveFocus();
    // A missing name is a per-field message: FieldError exposed as the
    // input's description, with no form-level error surface.
    expect(screen.getByText('Enter a feature name.')).toHaveClass('field-error');
    expect(screen.getByText('Enter a feature name.')).toHaveAttribute('id', 'feature-name-error');
    const nameInput = document.getElementById('feature-name') as HTMLElement;
    expect(nameInput).toHaveAttribute('aria-describedby', 'feature-name-error');
    expect(nameInput).toHaveAttribute('aria-invalid', 'true');
    expect(document.querySelector('.error-surface')).toBeNull();
  });

  it('leads an empty workspace with the folder picker instead of a filter miss', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    await renderForm(mock);

    expect(screen.getByRole('heading', { name: 'Add your first repository' })).toBeVisible();
    expect(screen.getByText(/an empty folder to start something new/i)).toBeVisible();
    expect(screen.queryByLabelText('Search repositories')).toBeNull();
    expect(screen.queryByText(/No repositories match/)).toBeNull();
  });

  it('renders a rejected defaults load as a compact ErrorSurface whose Retry reloads', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults
      .mockRejectedValueOnce(
        ipcError('not_ready', 'The runtime is not ready to create a feature.', {
          title: 'Not ready',
        }),
      )
      .mockResolvedValueOnce(creationDefaults());
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);

    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(surface).getByText('not_ready')).toHaveClass('error-surface__code');
    expect(within(surface).getByText('Not ready')).toBeVisible();
    expect(
      within(surface).getByText('The runtime is not ready to create a feature.'),
    ).toBeVisible();

    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Retry' }));

    expect(mock.api.getCreationDefaults).toHaveBeenCalledTimes(2);
    await screen.findByRole('button', { name: 'Next: Describe' });
  });

  it('adopts a folder that is itself a repository and selects it', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/solo' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/solo', valid: true, cloneEligible: true }],
        repositories: [
          {
            name: 'solo',
            path: '/work/solo',
            valid: true,
            featureReady: true,
            identity: mockRepoIdentity('/work/solo'),
          },
        ],
      }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));

    expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/solo');
    expect(mock.api.initRepository).not.toHaveBeenCalled();
    expect(await screen.findByRole('checkbox', { name: /solo/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('offers consented initialization for a folder that holds no repository', async () => {
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories: [] }) });
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space/fresh' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/fresh', valid: true, cloneEligible: true }],
      }),
    );
    mock.api.initRepository.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/fresh', valid: true, cloneEligible: true }],
        repositories: [
          {
            name: 'fresh',
            path: '/work/space/fresh',
            valid: true,
            featureReady: true,
            identity: mockRepoIdentity('/work/space/fresh'),
          },
        ],
      }),
    );
    mock.api.removeWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/fresh', valid: true, cloneEligible: true }],
        repositories: [
          {
            name: 'fresh',
            path: '/work/space/fresh',
            valid: true,
            featureReady: true,
            identity: mockRepoIdentity('/work/space/fresh'),
          },
        ],
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
      readySnapshot({
        workspaceRoots: [{ path: '/work/space/full', valid: true, cloneEligible: true }],
      }),
    );
    mock.api.initRepository.mockRejectedValue(
      ipcError('directory_not_empty', 'The directory is not empty and is not a git repository.', {
        title: 'Directory not empty',
        remediation: 'Choose an empty folder or an existing repository.',
      }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    await user.click(screen.getByRole('button', { name: /Initialize it as a repository/ }));
    await user.click(screen.getByRole('button', { name: 'Initialize repository' }));

    const alert = await screen.findByRole('alert');
    // The form-level card carries the code tag and the remediation hint the
    // canonical object authors.
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
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
          {
            name: 'repo-a',
            path: '/work/space/repo-a',
            valid: true,
            featureReady: true,
            identity: mockRepoIdentity('/work/space/repo-a'),
          },
          {
            name: 'repo-new',
            path: '/work/new-root/repo-new',
            valid: true,
            featureReady: true,
            identity: mockRepoIdentity('/work/new-root/repo-new'),
          },
        ],
      }),
    );
    const { user } = await renderForm(mock);

    await reachDescribe(user);
    await user.type(screen.getByLabelText('Name'), 'Preserved draft');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('radio', { name: /Moonshot/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.selectOptions(screen.getByLabelText('Inquireness'), 'high');
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('radio', { name: 'Current branches' }));
    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));

    expect(await screen.findByRole('checkbox', { name: /repo-new/ })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(screen.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Preserved draft');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    expect(screen.getByRole('radio', { name: /Moonshot/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
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
    await reachDescribe(user);

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
    await reachDescribe(user);

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
    await reachDescribe(user);

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
    await reachDescribe(user);

    await user.type(screen.getByLabelText('Description'), 'Refactor @cre');
    const option = await screen.findByRole('option', { name: /repo-a.*src\/creation\.ts/ });
    await user.click(option);

    expect(screen.getByLabelText('Description')).toHaveValue('Refactor @repo-a/src/creation.ts ');
    expect(
      screen.getByRole('button', { name: 'Remove reference repo-a/src/creation.ts' }),
    ).toBeVisible();
    expect(mock.api.searchCreationFiles).toHaveBeenCalledWith(
      expect.objectContaining({
        repositories: [{ key: 'repo-a', identity: mockRepoIdentity('/work/space/repo-a') }],
        query: 'cre',
      }),
    );

    // Deselecting the repository prunes its referenced files.
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
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
    await reachContract(user);

    const planningPicker = await screen.findByLabelText('Planning model');
    // The model and effort pickers share one trailing unit in the grouped row.
    const picksUnit = planningPicker.closest('.config-editor__phase-row-picks');
    expect(picksUnit).not.toBeNull();
    expect(
      screen.getByLabelText('Planning effort').closest('.config-editor__phase-row-picks'),
    ).toBe(picksUnit);
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
    await reachContract(user);

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
    await reachDescribe(user);
    await user.type(screen.getByLabelText('Name'), 'Medium scope');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));

    expect(screen.queryByRole('checkbox', { name: /Inquiry review/ })).toBeNull();
    expect(screen.getByRole('checkbox', { name: /Phase plan review/ })).toBeChecked();
  });

  it('shows only the model rows the chosen pipeline runs', async () => {
    const { user } = await renderForm();
    await reachDescribe(user);
    await user.type(screen.getByLabelText('Name'), 'Scoped models');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));

    // Medium runs planning, implementation, and review only.
    expect(screen.getByLabelText('Planning model')).toBeVisible();
    expect(screen.queryByLabelText('Clarify model')).toBeNull();
    expect(screen.queryByLabelText('KB Build model')).toBeNull();
    expect(screen.queryByLabelText('Utilities model')).toBeNull();
    expect(screen.queryByLabelText('Auto mode reviewer model')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByLabelText('Clarify model')).toBeVisible();
    expect(screen.getByLabelText('KB Build model')).toBeVisible();
    // Utilities and the automatic reviewer stay workspace-scoped.
    expect(screen.queryByLabelText('Utilities model')).toBeNull();
    expect(screen.queryByLabelText('Auto mode reviewer model')).toBeNull();
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
    await reachDescribe(user);
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
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
    await reachContract(user);

    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await user.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.dispatchFeatureSetup).toHaveBeenCalledWith('abcd1234ef567890');
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it('keeps the final submit single-flight and retryable after an authoritative error', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue(creationDefaults());
    mock.api.createFeature.mockRejectedValueOnce(
      ipcError('conflict', 'Another feature is already running from this branch.', {
        title: 'Feature conflict',
      }),
    );
    const { user } = await renderForm(mock);
    await reachContract(user);
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    // The rejection renders as the compact form-level card, takes focus, and
    // leaves the submit control armed for the retry.
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(surface).getByText('conflict')).toHaveClass('error-surface__code');
    expect(within(surface).getByText('Feature conflict')).toBeVisible();
    expect(surface).toHaveFocus();
    expect(screen.getByRole('button', { name: 'Create and start' })).toBeEnabled();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it('names the four steps, keeps completed steps reachable, and disables the ones ahead', async () => {
    const { user } = await renderForm();
    const rail = screen.getByRole('navigation', { name: 'Creation steps' });

    expect(Array.from(rail.querySelectorAll('button')).map((button) => button.textContent)).toEqual(
      ['Repositories', 'Describe', 'Depth', 'Contract'],
    );
    expect(rail.querySelector('[aria-current="step"]')).toHaveTextContent('Repositories');
    expect(screen.getByRole('button', { name: 'Describe' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Depth' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Contract' })).toBeDisabled();

    await reachDescribe(user);
    expect(rail.querySelector('[aria-current="step"]')).toHaveTextContent('Describe');
    expect(screen.getByRole('button', { name: /Repositories/ })).toBeEnabled();
    // Jumping back to a completed step keeps every entered choice.
    await user.click(screen.getByRole('button', { name: /Repositories/ }));
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
  });

  it('forces phase plan review on with roadmap review and releases it again', async () => {
    const { user } = await renderForm();
    await reachContract(user);

    const roadmap = screen.getByRole('checkbox', { name: /Roadmap review/ });
    const phasePlan = screen.getByRole('checkbox', { name: /Phase plan review/ });
    expect(roadmap).toBeChecked();
    expect(phasePlan).toBeChecked();

    await user.click(roadmap);
    expect(phasePlan).not.toBeChecked();
    await user.click(roadmap);
    expect(phasePlan).toBeChecked();
  });

  it('resets checkpoints to the profile defaults whenever a depth card is selected', async () => {
    const { user } = await renderForm();
    await reachContract(user);

    await user.click(screen.getByRole('checkbox', { name: /Inquiry review/ }));
    expect(screen.getByRole('checkbox', { name: /Inquiry review/ })).not.toBeChecked();

    // Selecting a depth card from the Contract step's own cards restores that
    // profile's checkpoint set rather than keeping the edited one.
    await user.click(screen.getByRole('radio', { name: /Moonshot/ }));
    expect(screen.getByRole('checkbox', { name: /Inquiry review/ })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Manual publish/ })).toBeChecked();
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    expect(screen.getByRole('checkbox', { name: /Manual publish/ })).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Inquiry review/ })).toBeChecked();
  });

  it('shows the live checkpoint and repository summary only on the Contract step', async () => {
    const { user } = await renderForm();
    await reachDescribe(user);
    expect(screen.queryByText(/checkpoints ·/)).toBeNull();

    await user.type(screen.getByLabelText('Name'), 'Summary draft');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('radio', { name: /Large/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));

    expect(screen.getByText('3 checkpoints · 1 repository')).toBeVisible();
    await user.click(screen.getByRole('checkbox', { name: /Inquiry review/ }));
    expect(screen.getByText('2 checkpoints · 1 repository')).toBeVisible();
  });

  it('routes an authoritative name error back to the Describe step and focuses the field', async () => {
    const mock = installAgenticoMock();
    mock.api.createFeature.mockRejectedValueOnce(
      ipcError('bad_request', 'a feature with that name already exists'),
    );
    const { user } = await renderForm(mock);
    await reachContract(user);
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    expect(await screen.findByRole('heading', { name: 'Define the work' })).toBeVisible();
    // A server rejection routed to the field shows the canonical summary as
    // the field message; the code tag stays on the (absent) form-level card.
    expect(screen.getByText(/a feature with that name already exists/)).toHaveClass('field-error');
    const nameInput = document.getElementById('feature-name') as HTMLElement;
    expect(nameInput).toHaveAttribute('aria-describedby', 'feature-name-error');
    expect(nameInput).toHaveFocus();
    expect(document.querySelector('.error-surface')).toBeNull();
  });

  it('submits draftPublish untouched with no control offering it', async () => {
    const mock = installAgenticoMock();
    const { onCreated, user } = await renderForm(mock);
    await reachContract(user);

    expect(screen.queryByRole('checkbox', { name: /draft/i })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({ checkpoints: expect.objectContaining({ draftPublish: false }) }),
    );
  });

  it('cancels a clean sheet immediately and confirms a dirty one', async () => {
    const { onClose, user } = await renderForm();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    expect(onClose).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it('routes Escape to the Cancel path and leaves it to the innermost dialog', async () => {
    const { onClose, user } = await renderForm();
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));

    await user.keyboard('{Escape}');
    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();

    // The discard dialog owns Escape while it is open: the sheet stays.
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: 'Discard feature draft' })).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
  });
});

describe('the creation sheet local source contract', () => {
  it('shows independent branch and detached sources for every selected repository and in review', async () => {
    const repoAIdentity = mockRepoIdentity('/work/space/repo-a');
    const repoBIdentity = mockRepoIdentity('/work/space/repo-b');
    const detachedSha = 'b'.repeat(40);
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [
          {
            name: 'repo-a',
            path: repoAIdentity.path,
            valid: true,
            featureReady: true,
            identity: repoAIdentity,
          },
          {
            name: 'repo-b',
            path: repoBIdentity.path,
            valid: true,
            featureReady: true,
            identity: repoBIdentity,
          },
        ],
      }),
    });
    mock.api.inspectRepositorySources.mockImplementation((request: RepositorySourcesRequest) =>
      Promise.resolve({
        repositories: request.repositories.map((repository) =>
          repository.repoKey === 'repo-a'
            ? {
                ...repository,
                mode: request.mode,
                kind: 'branch' as const,
                branch: 'release/2026/q3',
                observedSha: 'a'.repeat(40),
              }
            : {
                ...repository,
                mode: request.mode,
                kind: 'detached' as const,
                observedSha: detachedSha,
              },
        ),
      }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^repo-b\b/ }));

    expect(await screen.findByText('Source: release/2026/q3')).toBeVisible();
    expect(screen.getByText(`Source: detached ${detachedSha}`)).toBeVisible();
    expect(mock.api.inspectRepositorySources).toHaveBeenLastCalledWith({
      mode: 'default',
      repositories: [
        { repoKey: 'repo-a', identity: repoAIdentity },
        { repoKey: 'repo-b', identity: repoBIdentity },
      ],
    });

    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Independent sources');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));

    expect(
      screen.getByText('repo-a: release/2026/q3, repo-b: detached ' + detachedSha),
    ).toBeVisible();
  });

  it('presents a missing local source and keeps valid repositories usable after deselection', async () => {
    const goodIdentity = mockRepoIdentity('/work/space/good');
    const missingIdentity = mockRepoIdentity('/work/space/missing');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [
          {
            name: 'good',
            path: goodIdentity.path,
            valid: true,
            featureReady: true,
            identity: goodIdentity,
          },
          {
            name: 'missing',
            path: missingIdentity.path,
            valid: true,
            featureReady: true,
            identity: missingIdentity,
          },
        ],
      }),
    });
    mock.api.inspectRepositorySources.mockImplementation((request: RepositorySourcesRequest) =>
      request.repositories.some(({ repoKey }) => repoKey === 'missing')
        ? Promise.reject(
            ipcError('repository_source_missing', 'The selected local source is missing.', {
              title: 'Local source unavailable',
              remediation: 'Repair the repository or deselect it, then retry.',
            }),
          )
        : Promise.resolve({
            repositories: request.repositories.map((repository) => ({
              ...repository,
              mode: request.mode,
              kind: 'branch' as const,
              branch: 'main',
              observedSha: 'a'.repeat(40),
            })),
          }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^good\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^missing\b/ }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Local source unavailable');
    expect(alert).toHaveTextContent('Repair the repository or deselect it, then retry.');
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByText(/refresh or reselect repositories/i)).toBeVisible();

    await user.click(screen.getByRole('checkbox', { name: /^missing\b/ }));
    expect(await screen.findByText('Source: main')).toBeVisible();
    expect(screen.getByRole('checkbox', { name: /^good\b/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('returns a stale submission to source review and refreshes without losing the draft', async () => {
    let inspections = 0;
    const mock = installAgenticoMock();
    mock.api.inspectRepositorySources.mockImplementation((request: RepositorySourcesRequest) => {
      inspections += 1;
      return Promise.resolve({
        repositories: request.repositories.map((repository) => ({
          ...repository,
          mode: request.mode,
          kind: 'branch' as const,
          branch: inspections === 1 ? 'main' : 'release/refreshed',
          observedSha: (inspections === 1 ? 'a' : 'b').repeat(40),
        })),
      });
    });
    mock.api.createFeature.mockRejectedValueOnce(
      ipcError('local_source_stale', 'A selected local repository source changed.', {
        title: 'Local source changed',
        remediation: 'Review the refreshed local source, then submit the feature again.',
      }),
    );
    const { onCreated, user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    expect(await screen.findByText('Source: main')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Retained stale draft');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    expect(await screen.findByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(await screen.findByText('Source: release/refreshed')).toBeVisible();
    expect(screen.getByText('Local source changed')).toBeVisible();
    expect(onCreated).not.toHaveBeenCalled();
    expect(mock.api.inspectRepositorySources).toHaveBeenCalledTimes(2);
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Retained stale draft');
  });

  it('carries a successful offline-probe warning into the created feature view', async () => {
    const warning = {
      code: 'branch_collision_probe_unavailable',
      class: 'warning' as const,
      title: 'Remote branch check unavailable',
      summary: 'The feature branch could not be checked against its origin.',
    };
    const mock = installAgenticoMock();
    mock.api.createFeature.mockResolvedValueOnce({
      featureId: 'created1234abcdef',
      warnings: [warning],
    });
    const { onCreated, user } = await renderForm(mock);
    await reachContract(user);
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    await waitFor(() =>
      expect(onCreated).toHaveBeenCalledWith({
        featureId: 'created1234abcdef',
        name: 'Search revamp',
        warnings: [warning],
      }),
    );
  });

  it('does not let a default-mode reply overwrite a newer current-mode source', async () => {
    let resolveDefault!: (value: RepositorySourcesResult) => void;
    const mock = installAgenticoMock();
    mock.api.inspectRepositorySources.mockImplementation((request: RepositorySourcesRequest) =>
      request.mode === 'default'
        ? new Promise<RepositorySourcesResult>((resolve) => {
            resolveDefault = resolve;
          })
        : Promise.resolve({
            repositories: request.repositories.map((repository) => ({
              ...repository,
              mode: 'current' as const,
              kind: 'branch' as const,
              branch: 'topic/current/source',
              observedSha: 'b'.repeat(40),
            })),
          }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await vi.waitFor(() => expect(mock.api.inspectRepositorySources).toHaveBeenCalledTimes(1));
    expect(screen.getByRole('button', { name: 'Next: Describe' })).toBeDisabled();
    await user.click(screen.getByRole('radio', { name: 'Current branches' }));
    expect(await screen.findByText('Source: topic/current/source')).toBeVisible();

    resolveDefault({
      repositories: [
        {
          repoKey: 'repo-a',
          identity: mockRepoIdentity('/work/space/repo-a'),
          mode: 'default',
          kind: 'branch',
          branch: 'main',
          observedSha: 'a'.repeat(40),
        },
      ],
    });
    await vi.waitFor(() => {
      expect(screen.getByText('Source: topic/current/source')).toBeVisible();
      expect(screen.queryByText('Source: main')).toBeNull();
    });
  });

  it('does not let a previous server reply overwrite the connected server source', async () => {
    let resolvePreviousServer!: (value: RepositorySourcesResult) => void;
    let requestCount = 0;
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverKey: 'server-key-1' },
    });
    mock.api.inspectRepositorySources.mockImplementation((request: RepositorySourcesRequest) =>
      ++requestCount === 1
        ? new Promise<RepositorySourcesResult>((resolve) => {
            resolvePreviousServer = resolve;
          })
        : Promise.resolve({
            repositories: request.repositories.map((repository) => ({
              ...repository,
              mode: request.mode,
              kind: 'branch' as const,
              branch: 'server-two/current',
              observedSha: 'b'.repeat(40),
            })),
          }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await vi.waitFor(() => expect(mock.api.inspectRepositorySources).toHaveBeenCalledTimes(1));
    mock.emitConnection({ ...READY_REMOTE, serverKey: 'server-key-2' });
    expect(await screen.findByText('Source: server-two/current')).toBeVisible();

    resolvePreviousServer({
      repositories: [
        {
          repoKey: 'repo-a',
          identity: mockRepoIdentity('/work/space/repo-a'),
          mode: 'default',
          kind: 'detached',
          observedSha: 'a'.repeat(40),
        },
      ],
    });
    await vi.waitFor(() => {
      expect(screen.getByText('Source: server-two/current')).toBeVisible();
      expect(screen.queryByText(`Source: detached ${'a'.repeat(40)}`)).toBeNull();
    });
  });
});

describe('the creation sheet origin check contract', () => {
  it('shows typed origin statuses per selected row and in review without gating continuation', async () => {
    const repoAIdentity = mockRepoIdentity('/work/space/repo-a');
    const repoBIdentity = mockRepoIdentity('/work/space/repo-b');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [
          {
            name: 'repo-a',
            path: repoAIdentity.path,
            valid: true,
            featureReady: true,
            identity: repoAIdentity,
          },
          {
            name: 'repo-b',
            path: repoBIdentity.path,
            valid: true,
            featureReady: true,
            identity: repoBIdentity,
          },
        ],
      }),
    });
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            repository.repoKey === 'repo-a'
              ? {
                  ...repository,
                  mode: request.mode,
                  kind: 'branch' as const,
                  branch: 'main',
                  localSha: 'a'.repeat(40),
                  originBranch: 'main',
                  status: 'behind' as const,
                  behindCount: 2,
                  fetchedSha: 'c'.repeat(40),
                  updateEligible: true,
                }
              : {
                  ...repository,
                  mode: request.mode,
                  kind: 'branch' as const,
                  branch: 'main',
                  localSha: 'b'.repeat(40),
                  status: 'no_origin' as const,
                },
          ),
        }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^repo-b\b/ }));
    expect(await screen.findByText('Origin: 2 commits behind origin/main')).toBeVisible();
    expect(screen.getByText('Origin: no origin remote configured')).toBeVisible();

    // Origin warnings never gate the repositories step.
    expect(screen.getByRole('button', { name: 'Next: Describe' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Origin statuses');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));

    expect(
      screen.getByText(
        /repo-a: main is 2 commits behind origin\/main; the feature will start from the local source\./,
      ),
    ).toBeVisible();
    expect(
      screen.getByText(/repo-b: no origin remote configured; the feature will start from main\./),
    ).toBeVisible();
  });

  it('polls while a check runs, then serves Check again as a fresh attempt without toggling selection', async () => {
    let calls = 0;
    const mock = installAgenticoMock();
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) => {
        calls += 1;
        return Promise.resolve({
          repositories: request.repositories.map((repository) => ({
            ...repository,
            mode: request.mode,
            kind: 'branch' as const,
            branch: 'main',
            localSha: 'a'.repeat(40),
            originBranch: 'main',
            ...(calls <= 1
              ? { status: 'checking' as const }
              : {
                  status: 'behind' as const,
                  behindCount: 3,
                  fetchedSha: 'c'.repeat(40),
                  checkedAt: '2026-09-09T10:00:00.000Z',
                }),
          })),
        });
      },
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    expect(await screen.findByText('Origin check still running…')).toBeVisible();
    // A running check never blocks continuation.
    expect(screen.getByRole('button', { name: 'Next: Describe' })).toBeEnabled();

    await vi.waitFor(() => expect(calls).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    expect(await screen.findByText(/Origin: 3 commits behind origin\/main/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Check again' }));
    await vi.waitFor(() =>
      expect(mock.api.checkRepositoryOriginStatus).toHaveBeenLastCalledWith(
        expect.objectContaining({ refresh: ['repo-a'] }),
      ),
    );
    // Check again never toggles the selection.
    expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeChecked();
  });

  it('identifies a preserved earlier comparison as stale after a failed retry', async () => {
    const mock = installAgenticoMock();
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) => ({
            ...repository,
            mode: request.mode,
            kind: 'branch' as const,
            branch: 'main',
            localSha: 'a'.repeat(40),
            originBranch: 'main',
            status: 'unknown' as const,
            staleComparison: {
              status: 'behind' as const,
              localSha: 'a'.repeat(40),
              fetchedSha: 'c'.repeat(40),
              originBranch: 'main',
              aheadCount: 0,
              behindCount: 2,
              checkedAt: '2026-09-09T09:00:00.000Z',
            },
          })),
        }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    expect(
      await screen.findByText(
        /Origin check unavailable — creation continues from the local source\./,
      ),
    ).toBeVisible();
    expect(screen.getByText(/Earlier comparison: 2 commits behind \(stale\)\./)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Stale comparison');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(
      screen.getByText(
        /repo-a: the origin check could not complete; the feature will start from main\. An earlier comparison \(2 commits behind\) is preserved but stale\./,
      ),
    ).toBeVisible();
  });

  it('invalidates the previous comparison immediately when the shared mode changes', async () => {
    let resolveCurrent!: (value: RepositoryOriginStatusResult) => void;
    const mock = installAgenticoMock();
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        request.mode === 'default'
          ? Promise.resolve({
              repositories: request.repositories.map((repository) => ({
                ...repository,
                mode: request.mode,
                kind: 'branch' as const,
                branch: 'main',
                localSha: 'a'.repeat(40),
                originBranch: 'main',
                status: 'behind' as const,
                behindCount: 2,
                fetchedSha: 'c'.repeat(40),
              })),
            })
          : new Promise<RepositoryOriginStatusResult>((resolve) => {
              resolveCurrent = resolve;
            }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    expect(await screen.findByText('Origin: 2 commits behind origin/main')).toBeVisible();

    await user.click(screen.getByRole('radio', { name: 'Current branches' }));
    expect(mock.api.checkRepositoryOriginStatus).toHaveBeenLastCalledWith(
      expect.objectContaining({ mode: 'current' }),
    );
    // The default-mode comparison is gone immediately; the current-mode
    // attempt has not replied yet.
    expect(screen.queryByText('Origin: 2 commits behind origin/main')).toBeNull();

    resolveCurrent({
      repositories: [
        {
          repoKey: 'repo-a',
          identity: mockRepoIdentity('/work/space/repo-a'),
          mode: 'current',
          kind: 'branch',
          branch: 'topic/current-work',
          localSha: 'a'.repeat(40),
          originBranch: 'current-work',
          status: 'ahead',
          aheadCount: 1,
          fetchedSha: 'c'.repeat(40),
        },
      ],
    });
    expect(await screen.findByText('Origin: 1 commit ahead of origin/current-work')).toBeVisible();
  });
});

describe('the creation sheet on a remote server', () => {
  it('replaces the native folder picker with the typed server path entry remotely', async () => {
    const mock = installAgenticoMock({
      connection: READY_REMOTE,
      defaults: creationDefaults(),
    });
    const { user } = await renderForm(mock);

    // No native picker remotely: server-side typed entry replaces it.
    expect(screen.queryByRole('button', { name: 'Browse for folder' })).toBeNull();
    expect(screen.getByLabelText('Folder path on the server')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Use this path' })).toBeDisabled();

    // Existing configured repositories are still selectable.
    const repoCheckbox = screen.getByRole('checkbox', { name: /repo-a/ });
    expect(repoCheckbox).toBeVisible();
    await user.click(repoCheckbox);
    expect(repoCheckbox).toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('stages picked files as uploads and submits references, never local paths', async () => {
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverKey: 'server-key-1' },
    });
    mock.api.pickCreationFiles.mockImplementation((kind: string) =>
      Promise.resolve({ paths: kind === 'image' ? ['/safe/one.png'] : ['/safe/spec.pdf'] }),
    );
    mock.api.createFeature.mockResolvedValue({ featureId: 'abcd1234ef567890' });
    const { user } = await renderForm(mock);
    await reachContract(user);

    // Attachment staging happens on Describe; jump back to attach.
    await user.click(screen.getByRole('button', { name: /Describe/ }));
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add files' }));
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', ['/safe/one.png']);
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('attachment', ['/safe/spec.pdf']);
    expect(await screen.findByText(/one\.png/)).toBeVisible();

    // The rail only navigates backward; return forward via the step buttons.
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    const input = mock.api.createFeature.mock.calls[0]?.[0];
    expect(input.imageUploads).toEqual(['ref-onepng']);
    expect(input.attachmentUploads).toEqual(['ref-specpdf']);
    expect(JSON.stringify(input)).not.toContain('/safe/');
  });

  it('blocks creation while a staged upload belongs to another server, until removed', async () => {
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverKey: 'server-key-1' },
    });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/safe/one.png'] });
    const { user } = await renderForm(mock);
    await reachContract(user);

    await user.click(screen.getByRole('button', { name: /Describe/ }));
    await user.click(screen.getByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    expect(await screen.findByText(/one\.png/)).toBeVisible();

    // Switching to another remote identity orphans the staged upload.
    fireEvent(window, new Event('noop'));
    mock.emitConnection({ ...READY_REMOTE, serverKey: 'server-key-2' });
    await screen.findByText('Staged on another server');

    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    const submit = screen.getByRole('button', { name: 'Create and start' });
    expect(submit).toBeDisabled();

    await user.click(screen.getByRole('button', { name: /Describe/ }));
    await user.click(screen.getByRole('button', { name: 'Remove one.png' }));
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByRole('button', { name: 'Create and start' })).toBeEnabled();
  });
});

describe('the creation sheet source update contract', () => {
  function updateableRepositories() {
    const repoAIdentity = mockRepoIdentity('/work/space/repo-a');
    const repoBIdentity = mockRepoIdentity('/work/space/repo-b');
    return {
      repoAIdentity,
      repoBIdentity,
      repositories: [
        {
          name: 'repo-a',
          path: repoAIdentity.path,
          valid: true,
          featureReady: true,
          identity: repoAIdentity,
        },
        {
          name: 'repo-b',
          path: repoBIdentity.path,
          valid: true,
          featureReady: true,
          identity: repoBIdentity,
        },
      ],
    };
  }

  function eligibleBehindRow(
    identity: ReturnType<typeof mockRepoIdentity>,
    overrides: Record<string, unknown> = {},
  ) {
    return {
      repoKey: 'repo-a',
      identity,
      mode: 'default' as const,
      kind: 'branch' as const,
      branch: 'main',
      localSha: 'a'.repeat(40),
      originBranch: 'main',
      fetchedSha: 'c'.repeat(40),
      status: 'behind' as const,
      behindCount: 2,
      updateEligible: true,
      checkoutHeadRef: 'refs/heads/work',
      checkoutHeadSha: 'b'.repeat(40),
      ...overrides,
    };
  }

  function rowItem(name: string): HTMLElement {
    const row = screen.getByText(name).closest('li');
    if (!(row instanceof HTMLElement)) throw new Error(`row ${name} not found`);
    return row;
  }

  it('sends the displayed expectations unchanged, guards conflicting actions while active, and refreshes after success', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    let updateCalls = 0;
    let resolveUpdate!: (value: RepositoryUpdateSourceResult) => void;
    mock.api.updateRepositorySource.mockImplementation(
      () =>
        new Promise<RepositoryUpdateSourceResult>((resolve) => {
          updateCalls += 1;
          resolveUpdate = resolve;
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            repository.repoKey === 'repo-a'
              ? eligibleBehindRow(repoAIdentity)
              : {
                  ...repository,
                  mode: request.mode,
                  kind: 'branch' as const,
                  branch: 'main',
                  localSha: 'd'.repeat(40),
                  status: 'up_to_date' as const,
                  originBranch: 'main',
                  fetchedSha: 'd'.repeat(40),
                },
          ),
        }),
    );
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^repo-b\b/ }));
    expect(await screen.findByText('Origin: 2 commits behind origin/main')).toBeVisible();

    const rowA = within(rowItem('repo-a'));
    expect(
      rowA.getByText(
        'Advances main to origin/main in the original repository on the connected server.',
      ),
    ).toBeVisible();
    await user.click(rowA.getByRole('button', { name: 'Update from origin' }));

    // The displayed expectations crossed unchanged.
    expect(mock.api.updateRepositorySource).toHaveBeenCalledWith({
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      expectedLocalSha: 'a'.repeat(40),
      expectedOriginSha: 'c'.repeat(40),
      checkoutHeadRef: 'refs/heads/work',
      checkoutHeadSha: 'b'.repeat(40),
    });

    // Conflicting controls are guarded while the update is active.
    expect(rowA.getByRole('button', { name: 'Updating…' })).toBeDisabled();
    expect(rowA.getByRole('button', { name: 'Check again' })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeDisabled();
    expect(screen.getByRole('radio', { name: 'Default branches' })).toBeDisabled();
    expect(screen.getByRole('radio', { name: 'Current branches' })).toBeDisabled();
    // Other rows stay usable, and the action never toggles selection.
    const rowB = within(rowItem('repo-b'));
    expect(rowB.getByRole('button', { name: 'Check again' })).toBeEnabled();
    expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeChecked();
    expect(mock.api.createFeature).not.toHaveBeenCalled();

    // Submission waits for the active update.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Update guards');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByRole('button', { name: 'Updating source…' })).toBeDisabled();

    // Back on the repository step, the definitive result lands as a
    // row-scoped status announcement and releases every guard.
    await user.click(screen.getByRole('button', { name: 'Repositories' }));
    resolveUpdate({
      result: 'updated',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      previousSha: 'a'.repeat(40),
      localSha: 'c'.repeat(40),
      fetchedSha: 'c'.repeat(40),
    });
    expect(
      await screen.findByText(
        'Updated main to origin/main on the connected server (now at ccccccc).',
      ),
    ).toBeVisible();
    // The announcement is a polite status region, consistent with the picker.
    expect(within(rowItem('repo-a')).getByRole('status')).toHaveTextContent(
      'Updated main to origin/main on the connected server (now at ccccccc).',
    );
    expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeEnabled();
    expect(screen.getByRole('radio', { name: 'Default branches' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('button', { name: 'Next: Depth' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByRole('button', { name: 'Create and start' })).toBeEnabled();

    // The definitive result refreshed the comparison for this repository.
    await vi.waitFor(() =>
      expect(mock.api.checkRepositoryOriginStatus).toHaveBeenLastCalledWith(
        expect.objectContaining({ refresh: ['repo-a'] }),
      ),
    );
    // The draft survives the update untouched.
    await user.click(screen.getByRole('button', { name: 'Describe' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Update guards');
    expect(updateCalls).toBe(1);
  });

  it('never offers the action for unsafe targets and explains them instead', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverName: 'lab-server', serverKey: 'server-key-1' },
      defaults: creationDefaults({ repositories }),
    });
    const rows: Record<string, Record<string, unknown>> = {
      'repo-a': { checkoutHeadRef: 'refs/heads/main' },
    };
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) => ({
            ...eligibleBehindRow(repository.identity, rows[repository.repoKey] ?? {}),
            repoKey: repository.repoKey,
            identity: repository.identity,
          })),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    const rowA = within(rowItem('repo-a'));
    await screen.findByText('Origin: 2 commits behind origin/main');
    // A clean original-checkout target does not inherit advisory eligibility.
    expect(rowA.queryByRole('button', { name: 'Update from origin' })).toBeNull();
    expect(
      rowA.getByText(
        'main is checked out in the original repository — updating it from here is unavailable in this phase; update that checkout yourself.',
      ),
    ).toBeVisible();

    rows['repo-a'] = {
      updateEligible: false,
      updateBlockers: ['branch_checked_out_in_worktree'],
      checkoutHeadRef: 'refs/heads/work',
    };
    await user.click(rowA.getByRole('button', { name: 'Check again' }));
    expect(
      await rowA.findByText(
        "main is checked out in a linked worktree — update that worktree's checkout on lab-server yourself.",
      ),
    ).toBeVisible();
    expect(rowA.queryByRole('button', { name: 'Update from origin' })).toBeNull();

    rows['repo-a'] = { checkoutHeadRef: undefined, checkoutHeadSha: undefined };
    await user.click(rowA.getByRole('button', { name: 'Check again' }));
    expect(
      await rowA.findByText(
        "main cannot be updated from here — the repository's checkout could not be inspected, so it cannot be proven unoccupied.",
      ),
    ).toBeVisible();
    expect(rowA.queryByRole('button', { name: 'Update from origin' })).toBeNull();
  });

  it('names the exact local and origin branches and the server in the impact copy', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverName: 'lab-server', serverKey: 'server-key-1' },
      defaults: creationDefaults({ repositories }),
    });
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
              branch: 'feature/slashy',
              originBranch: 'tracking-name',
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    expect(
      await within(rowItem('repo-a')).findByText(
        'Advances feature/slashy to origin/tracking-name in the original repository on lab-server.',
      ),
    ).toBeVisible();
  });

  it('treats a typed stale refusal as a warning plus fresh comparison, never an automatic retry', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    let resolveUpdate!: (value: RepositoryUpdateSourceResult) => void;
    mock.api.updateRepositorySource.mockImplementation(
      () =>
        new Promise<RepositoryUpdateSourceResult>((resolve) => {
          resolveUpdate = resolve;
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    resolveUpdate({
      result: 'stale',
      reason: 'origin_tip_changed',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'a'.repeat(40),
      fetchedSha: 'e'.repeat(40),
      status: eligibleBehindRow(repoAIdentity, {
        status: 'behind',
        behindCount: 3,
        fetchedSha: 'e'.repeat(40),
        updateEligible: false,
        updateBlockers: ['comparison_unavailable'],
      }),
    });
    const rowA = within(rowItem('repo-a'));
    expect(
      await rowA.findByText(
        'main was not updated on the connected server — origin/main moved since the comparison. Check again, then update from the fresh comparison.',
      ),
    ).toBeVisible();
    // The refusal's fresh status row replaced the stale comparison.
    expect(rowA.getByText('Origin: 3 commits behind origin/main')).toBeVisible();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);

    // An explicit new action uses the newly displayed expectations.
    await user.click(rowA.getByRole('button', { name: 'Check again' }));
    mock.api.updateRepositorySource.mockResolvedValue({
      result: 'already_up_to_date',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'e'.repeat(40),
      fetchedSha: 'e'.repeat(40),
    });
    const second = within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' });
    await user.click(second);
    expect(mock.api.updateRepositorySource).toHaveBeenLastCalledWith(
      expect.objectContaining({
        expectedOriginSha: 'c'.repeat(40),
      }),
    );
    expect(
      await within(rowItem('repo-a')).findByText(
        'main is already at origin/main on the connected server.',
      ),
    ).toBeVisible();
  });

  it('records an unprovable 503 as outcome unknown, blocks submission until the settlement read, and never retries the mutation', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    mock.api.updateRepositorySource.mockRejectedValue(
      ipcError(
        'source_update_unavailable',
        'The update attempt could not be completed; do not assume it was rolled back.',
      ),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    let settleReconcile!: (value: RepositorySourceReconcileResult) => void;
    mock.api.reconcileSourceUpdate.mockImplementation(
      () =>
        new Promise<RepositorySourceReconcileResult>((resolve) => {
          settleReconcile = resolve;
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    // The outcome is unknown: never a warning that reads as a proved
    // failure, and no second mutation is issued against the same display.
    expect(
      await within(rowItem('repo-a')).findByText(
        /The result of updating main on the connected server is unknown/,
      ),
    ).toBeVisible();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mock.api.reconcileSourceUpdate).toHaveBeenCalledTimes(1));
    expect(mock.api.reconcileSourceUpdate).toHaveBeenCalledWith({
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      expectedLocalSha: 'a'.repeat(40),
      expectedOriginSha: 'c'.repeat(40),
    });

    // Submission stays blocked while the selected source is unsettled.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Unknown outcome continuation');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByRole('button', { name: 'Resolving source update…' })).toBeDisabled();
    expect(
      screen.getByText(/The result of updating main on the connected server is unknown/),
    ).toBeVisible();

    // The settlement read settles: the update did not complete, and the
    // still-valid local source restores warning-based continuation.
    settleReconcile({
      outcome: 'original_tip_remains',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'a'.repeat(40),
    });
    expect(
      await screen.findByText(
        /Reconciled main on the connected server: the update did not complete/,
      ),
    ).toBeVisible();
    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
  });

  it('keeps a definitive rejection warning visible in review even after a later successful check, without blocking submission', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    let checkCalls = 0;
    mock.api.updateRepositorySource.mockRejectedValue(
      ipcError(
        'invalid_repository',
        'The selected repository is no longer present under the expected identity.',
      ),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) => {
        checkCalls += 1;
        return Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
              status: checkCalls > 1 ? ('up_to_date' as const) : ('behind' as const),
              behindCount: checkCalls > 1 ? undefined : 2,
              updateEligible: checkCalls > 1 ? undefined : true,
            }),
          ),
        });
      },
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));
    expect(
      await within(rowItem('repo-a')).findByText(
        'main was not updated on the connected server — The selected repository is no longer present under the expected identity.',
      ),
    ).toBeVisible();

    // A later successful origin check does not clear the failure warning.
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Check again' }));
    await screen.findByText('Origin: up to date with origin/main');
    expect(
      within(rowItem('repo-a')).getByText(/was not updated on the connected server/),
    ).toBeVisible();

    // The warning reaches the final review and submission stays available.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Update failure continuation');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(
      screen.getByText(
        /main was not updated on the connected server — The selected repository is no longer present/,
      ),
    ).toBeVisible();
    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
  });

  it('prevents a conflicting action through a common-directory alias row', async () => {
    // Two catalog entries for one git store: the original checkout and a
    // linked worktree registered as its own repository row.
    const originalIdentity = mockRepoIdentity('/work/space/repo-a');
    const worktreeIdentity = mockRepoIdentity('/work/space/repo-a-worktree', {
      commonDir: originalIdentity.commonDir,
      inode: '909',
    });
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [
          {
            name: 'repo-a',
            path: originalIdentity.path,
            valid: true,
            featureReady: true,
            identity: originalIdentity,
          },
          {
            name: 'repo-a-worktree',
            path: worktreeIdentity.path,
            valid: true,
            featureReady: true,
            identity: worktreeIdentity,
          },
        ],
      }),
    });
    let resolveUpdate!: (value: RepositoryUpdateSourceResult) => void;
    mock.api.updateRepositorySource.mockImplementation(
      () =>
        new Promise<RepositoryUpdateSourceResult>((resolve) => {
          resolveUpdate = resolve;
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(within(rowItem('repo-a')).getByRole('checkbox'));
    await user.click(within(rowItem('repo-a-worktree')).getByRole('checkbox'));
    await screen.findAllByText('Origin: 2 commits behind origin/main');

    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));
    // The alias row cannot start a second, conflicting action.
    expect(
      within(rowItem('repo-a-worktree')).getByRole('button', { name: 'Updating…' }),
    ).toBeDisabled();
    expect(
      within(rowItem('repo-a-worktree')).getByRole('button', { name: 'Check again' }),
    ).toBeDisabled();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);

    resolveUpdate({
      result: 'updated',
      repoKey: 'repo-a',
      identity: originalIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      previousSha: 'a'.repeat(40),
      localSha: 'c'.repeat(40),
      fetchedSha: 'c'.repeat(40),
    });
    await waitFor(() =>
      expect(
        within(rowItem('repo-a-worktree')).getByRole('button', { name: 'Update from origin' }),
      ).toBeEnabled(),
    );
  });

  it('discards a late update result from another server', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverKey: 'server-key-1' },
      defaults: creationDefaults({ repositories }),
    });
    let resolveUpdate!: (value: RepositoryUpdateSourceResult) => void;
    mock.api.updateRepositorySource.mockImplementation(
      () =>
        new Promise<RepositoryUpdateSourceResult>((resolve) => {
          resolveUpdate = resolve;
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    mock.emitConnection({ ...READY_REMOTE, serverKey: 'server-key-2' });
    resolveUpdate({
      result: 'stale',
      reason: 'local_tip_changed',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
    });
    await vi.waitFor(() =>
      expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeEnabled(),
    );
    expect(screen.queryByText(/was not updated/)).toBeNull();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
  });

  it('cannot issue overlapping mutations from duplicate activation', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    mock.api.updateRepositorySource.mockImplementation(
      () => new Promise<RepositoryUpdateSourceResult>(() => undefined),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    const button = within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' });
    await user.click(button);
    // A duplicate activation before the disabled state renders is a no-op.
    fireEvent.click(button);
    fireEvent.click(button);
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
  });

  it('retains uncertainty through a server switch and reconciles only after returning to the originating server', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({
      connection: { ...READY_REMOTE, serverKey: 'server-key-1' },
      defaults: creationDefaults({ repositories }),
    });
    mock.api.updateRepositorySource.mockImplementation(
      () => new Promise<RepositoryUpdateSourceResult>(() => undefined),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));
    await within(rowItem('repo-a')).findByText(/Updating…/);

    // A switch mid-flight fences the result: the outcome is unknown on the
    // originating server, and the new server's sheet never sees it.
    mock.emitConnection({ ...READY_REMOTE, serverKey: 'server-key-2' });
    await vi.waitFor(() =>
      expect(screen.getByRole('checkbox', { name: /^repo-a\b/ })).toBeEnabled(),
    );
    expect(screen.queryByText(/result of updating main/)).toBeNull();
    expect(mock.api.reconcileSourceUpdate).not.toHaveBeenCalled();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);

    // Returning to the originating server reconciles before its source can
    // be accepted; the mutation itself is never retried.
    mock.emitConnection({ ...READY_REMOTE, serverKey: 'server-key-1' });
    await vi.waitFor(() => expect(mock.api.reconcileSourceUpdate).toHaveBeenCalledTimes(1));
    expect(mock.api.reconcileSourceUpdate).toHaveBeenCalledWith(
      expect.objectContaining({ repoKey: 'repo-a', branch: 'main' }),
    );
    expect(
      await within(rowItem('repo-a')).findByText(
        /Reconciled main on the connected server: the update did not complete/,
      ),
    ).toBeVisible();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
  });

  it('settles an expected target present outcome and refreshes the source and comparison', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    mock.api.updateRepositorySource.mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'The request timed out.'),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    mock.api.reconcileSourceUpdate.mockResolvedValue({
      outcome: 'expected_target_present',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'c'.repeat(40),
    });
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    const sourcesBefore = mock.api.inspectRepositorySources.mock.calls.length;
    const checksBefore = mock.api.checkRepositoryOriginStatus.mock.calls.length;
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    expect(
      await within(rowItem('repo-a')).findByText(
        'Reconciled main on the connected server: the update completed (now at ccccccc).',
      ),
    ).toBeVisible();
    // The settled target refreshes the local source and the comparison for
    // an explicit next action; the mutation is never retried.
    await waitFor(() =>
      expect(mock.api.inspectRepositorySources.mock.calls.length).toBeGreaterThan(sourcesBefore),
    );
    await waitFor(() =>
      expect(mock.api.checkRepositoryOriginStatus.mock.calls.length).toBeGreaterThan(checksBefore),
    );
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Reconciled target continuation');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled());
  });

  it('reports a locally changed observation as a warning that needs a fresh explicit action', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    mock.api.updateRepositorySource.mockRejectedValue(
      ipcError('E_SERVER_SWITCHED', 'The app switched servers while the request was running.'),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    mock.api.reconcileSourceUpdate.mockResolvedValue({
      outcome: 'local_state_changed',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'd'.repeat(40),
    });
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    expect(
      await within(rowItem('repo-a')).findByText(
        /Reconciled main on the connected server: it is now at a commit that is neither the tip before the update nor the expected origin tip/,
      ),
    ).toBeVisible();
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);
    // The observation never blocks submission of the still-valid source.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Locally changed continuation');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('checkbox', { name: /Start immediately/ }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled());
  });

  it('keeps the outcome unknown when the settlement read cannot prove it, retrying only on an explicit trigger', async () => {
    const { repositories } = updateableRepositories();
    const mock = installAgenticoMock({ defaults: creationDefaults({ repositories }) });
    mock.api.updateRepositorySource.mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'The request timed out.'),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    let reconcileCalls = 0;
    mock.api.reconcileSourceUpdate.mockImplementation(() => {
      reconcileCalls += 1;
      return Promise.reject(
        ipcError(
          'source_reconcile_unavailable',
          'The connected server could not establish the branch state.',
        ),
      );
    });
    const { user } = await renderForm(mock);
    await user.click(screen.getByRole('checkbox', { name: /^repo-a\b/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));

    expect(
      await within(rowItem('repo-a')).findByText(
        /The result of updating main on the connected server is unknown/,
      ),
    ).toBeVisible();
    await vi.waitFor(() => expect(reconcileCalls).toBe(1));
    // A failed settlement never clears the uncertainty and never loops:
    // submission stays blocked and nothing retries by itself.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Unsettled reconciliation');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    expect(screen.getByRole('button', { name: 'Resolving source update…' })).toBeDisabled();
    expect(reconcileCalls).toBe(1);
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);

    // Check again is the explicit trigger for a new settlement attempt.
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Check again' }));
    await vi.waitFor(() => expect(reconcileCalls).toBe(2));
  });

  it('retains an unsettled attempt through unmount and reconciles from the retained draft', async () => {
    const { repoAIdentity, repositories } = updateableRepositories();
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories }),
      readiness: readySnapshot({ repositories }),
    });
    let resolveUpdate!: (value: RepositoryUpdateSourceResult) => void;
    mock.api.updateRepositorySource.mockImplementation(
      () =>
        new Promise<RepositoryUpdateSourceResult>((resolve) => {
          resolveUpdate = resolve;
        }),
    );
    mock.api.checkRepositoryOriginStatus.mockImplementation(
      (request: RepositoryOriginStatusRequest) =>
        Promise.resolve({
          repositories: request.repositories.map((repository) =>
            eligibleBehindRow(repository.identity, {
              repoKey: repository.repoKey,
              identity: repository.identity,
            }),
          ),
        }),
    );
    let settleReconcile!: (value: RepositorySourceReconcileResult) => void;
    mock.api.reconcileSourceUpdate.mockImplementation(
      () =>
        new Promise<RepositorySourceReconcileResult>((resolve) => {
          settleReconcile = resolve;
        }),
    );
    const onDraftDetach = vi.fn();
    const user = userEvent.setup();
    const { unmount } = render(
      <CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} onDraftDetach={onDraftDetach} />,
    );
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await screen.findByText('Origin: 2 commits behind origin/main');
    await user.click(within(rowItem('repo-a')).getByRole('button', { name: 'Update from origin' }));
    await within(rowItem('repo-a')).findByText(/Updating…/);

    // Unmounting mid-attempt detaches the draft with the attempt recorded
    // as outcome-unknown for its server.
    unmount();
    expect(onDraftDetach).toHaveBeenCalledTimes(1);
    const retained = onDraftDetach.mock.calls.at(0)?.[0];
    expect(retained?.sourceUpdateUncertainty).toHaveLength(1);
    expect(retained.sourceUpdateUncertainty?.[0]).toEqual(
      expect.objectContaining({
        repoKey: 'repo-a',
        branch: 'main',
        expectedLocalSha: 'a'.repeat(40),
        expectedOriginSha: 'c'.repeat(40),
      }),
    );

    // A late result from the dead closure never lands anywhere.
    resolveUpdate({
      result: 'updated',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'c'.repeat(40),
      previousSha: 'a'.repeat(40),
    });
    expect(mock.api.updateRepositorySource).toHaveBeenCalledTimes(1);

    // Reopening the creation view replays the record and reconciles it.
    render(
      <CreateFeatureForm
        onCreated={vi.fn()}
        onClose={vi.fn()}
        retainedDraft={retained}
        onDraftDetach={vi.fn()}
      />,
    );
    await screen.findByRole('button', { name: 'Next: Describe' });
    expect(
      await within(rowItem('repo-a')).findByText(
        /The result of updating main on the connected server is unknown/,
      ),
    ).toBeVisible();
    await vi.waitFor(() => expect(mock.api.reconcileSourceUpdate).toHaveBeenCalledTimes(1));
    settleReconcile({
      outcome: 'original_tip_remains',
      repoKey: 'repo-a',
      identity: repoAIdentity,
      mode: 'default',
      branch: 'main',
      originBranch: 'main',
      localSha: 'a'.repeat(40),
    });
    expect(
      await within(rowItem('repo-a')).findByText(
        /Reconciled main on the connected server: the update did not complete/,
      ),
    ).toBeVisible();
  });
});
