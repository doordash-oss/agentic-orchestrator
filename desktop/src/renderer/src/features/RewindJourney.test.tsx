import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { RewindJourney } from './RewindJourney';

afterEach(cleanup);

const FEATURE_ID = 'abcd1234ef567890';

function journeyProps(overrides: Partial<Parameters<typeof RewindJourney>[0]> = {}) {
  return {
    featureId: FEATURE_ID,
    featureName: 'Search revamp',
    validPhaseOptions: ['implement'],
    onClose: vi.fn(),
    onRewindComplete: vi.fn(),
    ...overrides,
  };
}

describe('RewindJourney error surfaces', () => {
  it('renders a rejected preview as a compact ErrorSurface', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockRejectedValue(
      ipcError('E_INTERNAL', 'The rewind preview could not be computed.'),
    );
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('E_INTERNAL')).toHaveClass('error-surface__code');
    expect(within(alert).getByText('The rewind preview could not be computed.')).toBeVisible();
    // The legacy step-error markup is gone.
    expect(document.querySelector('.rewind-journey__error')).toBeNull();
  });

  it('renders validation findings as FieldError elements described by the target radiogroup', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue({
      eligible: false,
      sourceRevision: 'rev-1',
      targetPhase: 'implement',
      effectivePhase: 'implement',
      validationFindings: ['The worktree for repository "repo-a" has uncommitted changes.'],
    });
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));

    const findings = await screen.findByLabelText('Validation findings');
    const fieldError = within(findings).getByText(
      'The worktree for repository "repo-a" has uncommitted changes.',
    );
    expect(fieldError).toHaveClass('field-error');
    expect(fieldError).toHaveAttribute('id', 'rewind-finding-0');
    // The findings are wired to the step's input.
    const radiogroup = screen.getByRole('radiogroup');
    expect(radiogroup).toHaveAttribute('aria-describedby', 'rewind-finding-0');
    expect(screen.getByRole('button', { name: 'Continue' })).toBeDisabled();
    expect(document.querySelector('.rewind-journey__findings')).toBeNull();
  });

  it('renders the unavailable-targets notice as a compact ErrorSurface', () => {
    installAgenticoMock();
    render(<RewindJourney {...journeyProps({ validPhaseOptions: [] })} />);

    const alert = screen.getByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('E_REWIND_TARGETS_UNAVAILABLE')).toHaveClass(
      'error-surface__code',
    );
    expect(within(alert).getByText('Rewind targets are no longer available.')).toBeVisible();
    expect(within(alert).getByText('Refresh the feature and try again.')).toHaveClass(
      'error-surface__remediation-hint',
    );
  });

  it('renders a failed rewind as a compact ErrorSurface whose Retry returns to the target step', async () => {
    const mock = installAgenticoMock();
    mock.api.getRewindPreview.mockResolvedValue({
      eligible: true,
      sourceRevision: 'rev-1',
      sourceRunNumber: 3,
      targetPhase: 'implement',
      effectivePhase: 'implement',
    });
    mock.api.executeRewind.mockRejectedValue(ipcError('E_INTERNAL', 'The rewind was refused.'));
    const user = userEvent.setup();
    render(<RewindJourney {...journeyProps()} />);

    await user.click(await screen.findByRole('radio', { name: 'Implement' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue' })).toBeEnabled());
    await user.click(screen.getByRole('button', { name: 'Continue' }));
    await user.type(screen.getByLabelText(/Type REWIND to confirm/), 'REWIND');
    await user.click(screen.getByRole('button', { name: /^Rewind$/ }));

    // The fork did not advance (getFeature reports the same run), so the
    // terminal failure result renders as one compact ErrorSurface.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(alert).toHaveTextContent('Rewind could not be completed');
    expect(within(alert).getByText('The rewind was refused.')).toBeVisible();
    expect(within(alert).getByRole('button', { name: 'Retry' })).toHaveClass(
      'error-surface__action',
    );
    expect(document.querySelector('.rewind-journey__error-result')).toBeNull();

    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    // Retry returns to the target step with the failure cleared.
    expect(await screen.findByRole('radio', { name: 'Implement' })).toBeVisible();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    // The legacy error-result markup and its "Try again" label are gone.
    expect(document.querySelector('.rewind-journey__retry')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });
});
