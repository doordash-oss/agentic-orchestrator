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
import type { ConnectionState } from '../../../shared/ipc';
import {
  creationDefaults,
  installAgenticoMock,
  ipcError,
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
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
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
          { name: 'repo-a', path: '/work/space/repo-a', valid: true },
          { name: 'repo-new', path: '/work/new-root/repo-new', valid: true },
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
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    await user.click(screen.getByRole('button', { name: 'Browse for folder' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));

    expect(await screen.findByRole('checkbox', { name: /repo-new/ })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
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
      expect.objectContaining({ repoKeys: ['repo-a'], query: 'cre' }),
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

describe('the creation sheet on a remote server', () => {
  it('swaps the folder browse for server-validated typed path entry', async () => {
    const mock = installAgenticoMock({
      connection: READY_REMOTE,
      defaults: creationDefaults({ repositories: [] }),
    });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/srv/work/solo', valid: true }],
        repositories: [{ name: 'solo', path: '/srv/work/solo', valid: true }],
      }),
    );
    const { user } = await renderForm(mock);

    // The native dialog is gone: only the server can see its own folders.
    expect(screen.queryByRole('button', { name: 'Browse for folder' })).toBeNull();
    const field = screen.getByLabelText('Folder path on the server');
    expect(screen.getByText(/validated on the server host/)).toBeVisible();

    // A non-absolute entry is refused before the server is ever asked.
    await user.type(field, 'srv/work/solo');
    await user.click(screen.getByRole('button', { name: 'Use this path' }));
    expect(await screen.findByText(/starting with \//)).toHaveClass('field-error');
    expect(field).toHaveAttribute('aria-describedby', 'creation-folder-path-error');
    expect(field).toHaveAttribute('aria-invalid', 'true');
    expect(mock.api.pickWorkspaceDirectory).not.toHaveBeenCalled();
    expect(mock.api.addWorkspaceRoot).not.toHaveBeenCalled();

    // The server's rejection stays beside the field and names the bad path.
    await user.clear(field);
    await user.type(field, '/srv/work/solo');
    await user.click(screen.getByRole('button', { name: 'Use this path' }));
    mock.api.addWorkspaceRoot.mockRejectedValueOnce(
      ipcError('invalid_workspace_root', '/srv/work/solo does not exist on this server'),
    );
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    expect(await screen.findByText('/srv/work/solo does not exist on this server')).toHaveClass(
      'field-error',
    );
    expect(field).toHaveAttribute('aria-describedby', 'creation-folder-path-error');

    // A valid path saves and the discovered repository selects itself.
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    expect(mock.api.addWorkspaceRoot).toHaveBeenLastCalledWith('/srv/work/solo');
    expect(await screen.findByRole('checkbox', { name: /solo/ })).toBeChecked();
    expect(screen.getByText('Added solo and selected it.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('initializes a typed folder as a repository with server errors surfaced inline', async () => {
    const mock = installAgenticoMock({
      connection: READY_REMOTE,
      defaults: creationDefaults({ repositories: [] }),
    });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({ workspaceRoots: [{ path: '/srv/work/fresh', valid: true }] }),
    );
    mock.api.removeWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [{ path: '/srv/work/fresh', valid: true }],
        repositories: [{ name: 'fresh', path: '/srv/work/fresh', valid: true }],
      }),
    );
    mock.api.initRepository
      .mockRejectedValueOnce(
        ipcError('directory_not_empty', 'the directory contains files. Empty it or pick another.'),
      )
      .mockResolvedValue(
        readySnapshot({
          workspaceRoots: [{ path: '/srv/work/fresh', valid: true }],
          repositories: [{ name: 'fresh', path: '/srv/work/fresh', valid: true }],
        }),
      );
    const { user } = await renderForm(mock);

    await user.type(screen.getByLabelText('Folder path on the server'), '/srv/work/fresh');
    await user.click(screen.getByRole('button', { name: 'Use this path' }));
    await user.click(screen.getByRole('button', { name: 'Use this folder' }));
    expect(await screen.findByText(/holds no git repository yet/i)).toBeVisible();
    expect(screen.getByText(/or type a different folder/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: /Initialize it as a repository/ }));
    await user.click(screen.getByRole('button', { name: 'Initialize repository' }));

    // The transient parent root dance is unchanged remotely; the server's
    // rejection stays next to the typed path, recoverable in place.
    expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/srv/work');
    expect(mock.api.initRepository).toHaveBeenCalledWith({
      path: '/srv/work/fresh',
      consent: true,
    });
    expect(await screen.findByText(/the directory contains files/)).toHaveClass('field-error');

    await user.click(screen.getByRole('button', { name: /Initialize it as a repository/ }));
    await user.click(screen.getByRole('button', { name: 'Initialize repository' }));
    expect(await screen.findByRole('checkbox', { name: /fresh/ })).toBeChecked();
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
