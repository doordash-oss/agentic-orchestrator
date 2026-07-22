import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { FeatureConfigPanel, WorkspaceDefaultsPanel } from './ConfigEditor';
import type { FeatureConfigSnapshot, ModelCatalogue, WorkspaceDefaults } from '../../../shared/ipc';

afterEach(cleanup);

const CATALOGUE: ModelCatalogue = {
  providerOrder: ['claude', 'codex'],
  providerModels: {
    claude: [
      { id: 'sonnet', displayName: 'Sonnet' },
      { id: 'opus', displayName: 'Opus' },
    ],
    codex: [{ id: 'gpt-a', displayName: 'GPT A' }],
  },
  phaseDefaults: { implementation: 'claude:sonnet' },
  phaseProviderModels: {
    implementation: { claude: ['sonnet', 'opus'], codex: ['gpt-a'] },
  },
};

const SNAPSHOT: FeatureConfigSnapshot = {
  featureId: 'feat-1',
  current: {
    models: { implementation: 'claude:opus' },
    inquireness: 'medium',
    checkpoints: {
      inquiryReview: true,
      researchReview: false,
      designReview: false,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: true,
      draftPublish: false,
    },
    pipeline: 'large',
    inputNotifications: 'default',
  },
  defaults: {
    models: { implementation: 'claude:sonnet' },
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
    inputNotifications: 'default',
  },
  manualPublishAvailable: true,
};

describe('FeatureConfigPanel', () => {
  it('renders provider-grouped model options with the effective default named', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(SNAPSHOT);
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(<FeatureConfigPanel featureId="feat-1" />);

    const picker = await screen.findByLabelText('Implementation model');
    expect(picker).toHaveValue('claude:opus');
    const options = within(picker).getAllByRole('option');
    expect(options[0]).toHaveTextContent('Default — claude:sonnet');
    // Two providers detected: values carry the provider prefix, and the
    // phase default carries the recommendation marker.
    const sonnet = options.find((o) => o.textContent?.startsWith('Sonnet'));
    expect(sonnet).toHaveValue('claude:sonnet');
    expect(sonnet).toHaveTextContent('★');
    expect(within(picker).getByRole('group', { name: 'codex' })).toBeInTheDocument();
  });

  it('marks a workspace default as unavailable when it is absent from live discovery', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue({
      ...SNAPSHOT,
      defaults: {
        ...SNAPSHOT.defaults,
        models: { implementation: 'sonnet[200K]' },
      },
    });
    mock.api.getModelCatalogue.mockResolvedValue({
      ...CATALOGUE,
      providerOrder: ['claude'],
      providerModels: { claude: [{ id: 'sonnet[1M]', displayName: 'Sonnet 1M' }] },
      phaseProviderModels: { implementation: { claude: ['sonnet[1M]'] } },
    });
    render(<FeatureConfigPanel featureId="feat-1" />);

    const picker = await screen.findByLabelText('Implementation model');
    expect(within(picker).getAllByRole('option')[0]).toHaveTextContent(
      'Default — sonnet[200K] (unavailable)',
    );
  });

  it('saves the full edited config and reports the saved state', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(SNAPSHOT);
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    mock.api.updateFeatureConfig.mockResolvedValue(SNAPSHOT);
    render(<FeatureConfigPanel featureId="feat-1" />);
    const user = userEvent.setup();

    const save = await screen.findByRole('button', { name: 'Save changes' });
    expect(save).toBeDisabled();

    await user.selectOptions(screen.getByLabelText('Implementation model'), 'claude:sonnet');
    await user.click(screen.getByRole('radio', { name: /High/ }));
    await user.click(screen.getByRole('checkbox', { name: /Research review/ }));
    expect(save).toBeEnabled();
    expect(screen.getByRole('status')).toHaveTextContent('Unsaved changes');

    await user.click(save);
    await waitFor(() => expect(mock.api.updateFeatureConfig).toHaveBeenCalledTimes(1));
    expect(mock.api.updateFeatureConfig).toHaveBeenCalledWith({
      featureId: 'feat-1',
      config: {
        ...SNAPSHOT.current,
        models: { implementation: 'claude:sonnet' },
        inquireness: 'high',
        checkpoints: { ...SNAPSHOT.current.checkpoints, researchReview: true },
      },
    });
    await screen.findByText(/Saved\./);
  });

  it('links roadmap review to phase plan review and hides gates the pipeline drops', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue({
      ...SNAPSHOT,
      current: { ...SNAPSHOT.current, pipeline: 'medium' },
    });
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(<FeatureConfigPanel featureId="feat-1" />);
    const user = userEvent.setup();

    const roadmap = await screen.findByRole('checkbox', { name: /Roadmap review/ });
    // Medium pipeline: inquiry/research/design gates are not applicable.
    expect(screen.queryByRole('checkbox', { name: /Inquiry review/ })).toBeNull();
    expect(screen.queryByRole('checkbox', { name: /Design review/ })).toBeNull();

    const phasePlan = screen.getByRole('checkbox', { name: /Phase plan review/ });
    await user.click(roadmap);
    expect(roadmap).not.toBeChecked();
    expect(phasePlan).not.toBeChecked();
    await user.click(roadmap);
    expect(phasePlan).toBeChecked();
  });

  it('hides the manual publish gate when publishing does not apply', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue({ ...SNAPSHOT, manualPublishAvailable: false });
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(<FeatureConfigPanel featureId="feat-1" />);

    await screen.findByRole('checkbox', { name: /Roadmap review/ });
    expect(screen.queryByRole('checkbox', { name: /Manual publish/ })).toBeNull();
  });

  it('keeps an unavailable persisted model selectable and flagged', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue({
      ...SNAPSHOT,
      current: { ...SNAPSHOT.current, models: { implementation: 'gone:model' } },
    });
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(<FeatureConfigPanel featureId="feat-1" />);

    const picker = await screen.findByLabelText('Implementation model');
    expect(picker).toHaveValue('gone:model');
    expect(
      within(picker).getByRole('option', { name: 'gone:model (unavailable)' }),
    ).toBeInTheDocument();
  });

  it('surfaces load errors with a retry action', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockRejectedValueOnce(new Error('boom'));
    mock.api.getFeatureConfig.mockResolvedValueOnce(SNAPSHOT);
    render(<FeatureConfigPanel featureId="feat-1" />);
    const user = userEvent.setup();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Could not load configuration');
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByRole('button', { name: 'Save changes' });
  });
});

const DEFAULTS: WorkspaceDefaults = {
  models: { planning: 'claude:sonnet' },
  inquireness: 'medium',
  checkpoints: SNAPSHOT.current.checkpoints,
  pipeline: 'large',
  muteFeatureInput: false,
};

describe('WorkspaceDefaultsPanel', () => {
  it('isolates its inquireness radio group from other config editors', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(SNAPSHOT);
    mock.api.getWorkspaceDefaults.mockResolvedValue(DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    render(
      <>
        <FeatureConfigPanel featureId="feat-1" />
        <WorkspaceDefaultsPanel />
      </>,
    );
    const user = userEvent.setup();

    const editors = await screen.findAllByLabelText(/configuration editor|defaults editor/);
    const featureEditor = editors[0]!;
    const defaultsEditor = editors[1]!;

    expect(within(featureEditor).getByRole('radio', { name: /Medium/ })).toBeChecked();
    expect(within(defaultsEditor).getByRole('radio', { name: /Medium/ })).toBeChecked();

    await user.click(within(defaultsEditor).getByRole('radio', { name: /High/ }));

    expect(within(featureEditor).getByRole('radio', { name: /Medium/ })).toBeChecked();
    expect(within(defaultsEditor).getByRole('radio', { name: /High/ })).toBeChecked();
  });

  it('shows the utilities model row and maps input alerts to the mute flag', async () => {
    const mock = installAgenticoMock();
    mock.api.getWorkspaceDefaults.mockResolvedValue(DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(CATALOGUE);
    mock.api.updateWorkspaceDefaults.mockResolvedValue({ ...DEFAULTS, muteFeatureInput: true });
    render(<WorkspaceDefaultsPanel />);
    const user = userEvent.setup();

    await screen.findByLabelText('Utilities model');
    await user.selectOptions(screen.getByLabelText('Input alerts'), 'muted');
    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() => expect(mock.api.updateWorkspaceDefaults).toHaveBeenCalledTimes(1));
    expect(mock.api.updateWorkspaceDefaults).toHaveBeenCalledWith({
      ...DEFAULTS,
      muteFeatureInput: true,
    });
  });
});
