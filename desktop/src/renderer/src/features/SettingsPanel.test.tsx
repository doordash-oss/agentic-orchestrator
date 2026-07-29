import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type {
  ModelCatalogue,
  ProviderModelRefreshResult,
  WorkspaceDefaults,
} from '../../../shared/ipc';
import { installAgenticoMock, readySnapshot } from '../test/agenticoMock';
import { SettingsPanel } from './SettingsPanel';

afterEach(cleanup);

const WORKSPACE_DEFAULTS: WorkspaceDefaults = {
  models: { implementation: 'claude:old' },
  effort: {},
  inquireness: 'medium',
  checkpoints: {
    inquiryReview: false,
    researchReview: false,
    designReview: false,
    roadmapReview: true,
    phasePlanReview: true,
    manualPublish: true,
    draftPublish: false,
  },
  pipeline: 'large',
  muteFeatureInput: false,
  automaticReviewEnabled: false,
};

const OLD_CATALOGUE: ModelCatalogue = {
  providerOrder: ['claude', 'codex'],
  providerModels: {
    claude: [{ id: 'old', displayName: 'Old Claude' }],
    codex: [{ id: 'gpt', displayName: 'GPT' }],
  },
  phaseDefaults: { implementation: 'claude:old' },
  phaseProviderModels: {
    implementation: { claude: ['old'], codex: ['gpt'] },
  },
};

describe('SettingsPanel provider refresh', () => {
  it('always shows row actions and applies the selected provider refresh to model pickers', async () => {
    const initialReadiness = readySnapshot({
      providers: [
        { name: 'claude', installed: true, version: '2.1.220', ready: true },
        { name: 'codex', installed: true, version: '0.145.0', ready: true },
      ],
    });
    const refreshed: ProviderModelRefreshResult = {
      readiness: {
        ...initialReadiness,
        models: { available: true, models: ['new', 'gpt'] },
      },
      catalogue: {
        ...OLD_CATALOGUE,
        providerModels: {
          ...OLD_CATALOGUE.providerModels,
          claude: [{ id: 'new', displayName: 'New Claude' }],
        },
        phaseDefaults: { implementation: 'claude:new' },
        phaseProviderModels: {
          implementation: { claude: ['new'], codex: ['gpt'] },
        },
      },
    };
    let resolveRefresh: ((value: ProviderModelRefreshResult) => void) | undefined;
    const refreshPromise = new Promise<ProviderModelRefreshResult>((resolve) => {
      resolveRefresh = resolve;
    });
    const mock = installAgenticoMock({ readiness: initialReadiness });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(OLD_CATALOGUE);
    mock.api.refreshProviderModels.mockReturnValue(refreshPromise);

    render(<SettingsPanel />);

    const claudeRow = (await screen.findByText('claude')).closest('li');
    const codexRow = (await screen.findByText('codex')).closest('li');
    expect(claudeRow).toBeDefined();
    expect(codexRow).toBeDefined();
    expect(within(claudeRow!).getByRole('button', { name: 'Recheck' })).toBeVisible();
    expect(within(codexRow!).getByRole('button', { name: 'Recheck' })).toBeVisible();

    await userEvent.click(within(claudeRow!).getByRole('button', { name: 'Recheck' }));

    expect(mock.api.refreshProviderModels).toHaveBeenCalledWith('claude');
    expect(within(claudeRow!).getByRole('button', { name: 'Rechecking…' })).toBeDisabled();
    expect(within(codexRow!).getByRole('button', { name: 'Recheck' })).toBeEnabled();

    await act(async () => {
      resolveRefresh!(refreshed);
      await refreshPromise;
    });

    const picker = await screen.findByLabelText('Implementation model');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: /New Claude/ })).toHaveValue('claude:new'),
    );
  });

  it('shows a safe error and preserves the row after refresh fails', async () => {
    const readiness = readySnapshot();
    const mock = installAgenticoMock({ readiness });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(OLD_CATALOGUE);
    mock.api.refreshProviderModels.mockRejectedValue(
      new Error('E_PROVIDER_MODEL_REFRESH: Model refresh failed. Retry the provider.'),
    );

    render(<SettingsPanel />);
    const recheck = await screen.findByRole('button', { name: 'Recheck' });
    await userEvent.click(recheck);

    expect(await screen.findByRole('alert')).toHaveTextContent('Model refresh failed');
    expect(screen.getByRole('button', { name: 'Recheck' })).toBeEnabled();
    expect(screen.getByText('claude')).toBeVisible();
  });
});
