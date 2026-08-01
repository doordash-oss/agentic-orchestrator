import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { RelationshipWorkspace } from './RelationshipWorkspace';

afterEach(cleanup);

const relationship = {
  id: 'child1234ef567890',
  name: 'Extract search core',
  kind: 'refactor',
  displayToken: 'R1',
  displayState: 'Child review',
  pipeline: 'large',
  status: 'Created',
  setupStatus: 'done',
  relationshipState: 'active',
  startedAt: '2026-07-30T10:00:00Z',
  cost: { totalUsd: 1.25, byPhase: {} },
  integrationState: 'pending',
  attention: [],
  cleanupWarnings: [],
};

describe('RelationshipWorkspace', () => {
  it('defaults to child inspection, exposes explicit Start, and keeps immutable history read-only', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        id: relationship.id,
        setupComplete: true,
        actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
      }),
    });
    const parent = featureSnapshot({
      activeChild: relationship,
      childHistory: [
        {
          ...relationship,
          id: 'child0000ef567890',
          displayState: 'Closed — Completed',
          relationshipState: 'completed',
          outcome: 'completed',
          closedAt: '2026-07-29T10:00:00Z',
          diffSummary: '3 files changed',
        },
      ],
    });
    const user = userEvent.setup();
    render(<RelationshipWorkspace parent={parent} onChanged={() => {}} />);
    expect(await screen.findByText('Child review')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Start' }));
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: relationship.id,
      action: 'start',
    });
    await user.click(screen.getByText('Refactor history'));
    expect(screen.getByText('3 files changed')).toBeVisible();
    expect(screen.queryByRole('button', { name: /Restart.*Completed/ })).toBeNull();
  });

  it('fails closed without an impact preview and renders authoritative empty categories as None', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        id: relationship.id,
        actions: [{ id: 'discard', enabled: true, disabledReasons: [] }],
      }),
    });
    const { rerender } = render(
      <RelationshipWorkspace
        parent={featureSnapshot({ activeChild: relationship })}
        onChanged={() => {}}
      />,
    );
    expect(await screen.findByRole('button', { name: 'Discard' })).toBeVisible();
    await userEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByRole('button', { name: 'Discard child' })).toBeDisabled();
    expect(mock.api.discardRefactorChild).not.toHaveBeenCalled();

    mock.api.getFeature.mockResolvedValue(
      featureSnapshot({
        id: relationship.id,
        actions: [
          {
            id: 'discard',
            enabled: true,
            disabledReasons: [],
            impactPreview: {
              kind: 'child_discard',
              subject: { id: relationship.id, name: relationship.name },
              categories: [{ key: 'branches', label: 'Branches', items: [] }],
              retained: ['Immutable history'],
            },
          },
        ],
      }),
    );
    rerender(
      <RelationshipWorkspace
        parent={featureSnapshot({
          activeChild: { ...relationship, displayState: 'Child review updated' },
        })}
        onChanged={() => {}}
      />,
    );
    await waitFor(() => expect(screen.getByText('Child review updated')).toBeVisible());
  });
});
