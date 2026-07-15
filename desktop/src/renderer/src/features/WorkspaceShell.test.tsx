import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { defaultSettings, type Settings } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { WorkspaceShell } from './WorkspaceShell';

afterEach(cleanup);

const FEATURE_ID = 'abcd1234ef567890';

function settingsWithTab(): Settings {
  return {
    ...defaultSettings(),
    tabs: {
      open: [{ featureId: FEATURE_ID, titleHint: 'Search revamp' }],
      activeFeatureId: FEATURE_ID,
    },
  };
}

describe('WorkspaceShell tabs', () => {
  it('shows the Home tab with the creation form and the authoritative feature list', async () => {
    installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
          name: 'Search revamp',
          status: 'Created',
          currentPhase: 'Plan',
          repos: ['repo-a'],
          createdAt: '2026-07-14T10:00:00Z',
        },
      ],
    });
    render(<WorkspaceShell />);

    expect(await screen.findByRole('tab', { name: 'Home' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    expect(within(listRegion).getByText('Search revamp')).toBeInTheDocument();
  });

  it('opens a persistent tab after creation and stores only identity/presentation', async () => {
    const mock = installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();
    await screen.findByRole('form', { name: /create a feature/i });

    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    const tab = await screen.findByRole('tab', { name: 'Search revamp' });
    expect(tab).toHaveAttribute('aria-selected', 'true');
    expect(mock.api.updateSettings).toHaveBeenCalledWith({
      tabs: {
        open: [{ featureId: FEATURE_ID, titleHint: 'Search revamp' }],
        activeFeatureId: FEATURE_ID,
      },
    });
    // The cockpit itself always loads from the server.
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
  });

  it('restores tabs from local settings on restart and refetches from the server', async () => {
    const mock = installAgenticoMock({ settings: settingsWithTab() });
    render(<WorkspaceShell />);

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
    expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeInTheDocument();
  });

  it('shows a no-longer-exists state for a restored tab whose feature vanished', async () => {
    const mock = installAgenticoMock({ settings: settingsWithTab() });
    mock.api.getFeature.mockRejectedValue(new Error('not_found: feature not found'));
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    expect(
      await screen.findByText('This feature no longer exists on the server.'),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Close tab' }));
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'Search revamp' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      tabs: { open: [], activeFeatureId: null },
    });
    // Back on Home without crashing.
    expect(screen.getByRole('tab', { name: 'Home' })).toHaveAttribute('aria-selected', 'true');
  });

  it('opens an existing server feature from the Home list', async () => {
    const mock = installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
          name: 'Search revamp',
          status: 'Created',
          currentPhase: 'Plan',
          repos: ['repo-a'],
          createdAt: '2026-07-14T10:00:00Z',
        },
      ],
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();
    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    await user.click(within(listRegion).getByRole('button', { name: 'Open' }));

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
  });

  it('refetches the feature list on feature invalidations and resync', async () => {
    const mock = installAgenticoMock();
    render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Existing features' });
    const base = mock.api.listFeatures.mock.calls.length;

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.listFeatures.mock.calls.length).toBe(base + 1));

    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });
    await waitFor(() => expect(mock.api.listFeatures.mock.calls.length).toBe(base + 2));

    mock.emitAppEvent({ type: 'invalidated', kind: 'session.updated' });
    expect(mock.api.listFeatures.mock.calls.length).toBe(base + 2);
  });

  it('supports keyboard navigation across the tab strip', async () => {
    installAgenticoMock({ settings: settingsWithTab() });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    const home = await screen.findByRole('tab', { name: 'Home' });
    const featureTab = screen.getByRole('tab', { name: 'Search revamp' });
    featureTab.focus();
    await user.keyboard('{ArrowLeft}');
    expect(home).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(featureTab).toHaveFocus();
  });
});
