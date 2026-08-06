import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { defaultSettings } from '../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { CommandPalette } from './CommandPalette';

afterEach(cleanup);

describe('CommandPalette', () => {
  it('targets the selected feature tab when persisted settings lag behind', async () => {
    const activeFeatureId = 'active1234abcd5678';
    const staleFeatureId = 'stale1234abcd5678';
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: { activeFeatureId: staleFeatureId, sidebarCollapsed: false },
      },
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(
        featureSnapshot({
          id: featureId,
          actions: [
            {
              id: 'start',
              enabled: true,
              disabledReasons: [],
            },
          ],
        }),
      ),
    );

    render(
      <>
        <div id={`sidebar-row-${activeFeatureId}`} role="option" aria-selected="true">
          Active row
        </div>
        <CommandPalette
          ready
          routeRequest={{ id: 1, event: { target: 'palette' } }}
          onRoute={() => undefined}
        />
      </>,
    );

    const palette = await screen.findByRole('dialog', { name: 'Command palette' });
    await userEvent.type(screen.getByLabelText('Search commands'), 'start feature');
    await userEvent.keyboard('{Enter}');

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: activeFeatureId,
        action: 'start',
      }),
    );
    expect(palette).not.toBeInTheDocument();
  });
});
