import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { RefactorModal } from './RefactorModal';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function props(repos = ['repo-a']) {
  return {
    featureId: 'abcd1234ef567890',
    snapshot: featureSnapshot({
      status: 'Published',
      repos,
      actions: [
        {
          id: 'refactor',
          enabled: true,
          disabledReasons: [],
          inputs: [{ name: 'pipeline', options: ['fast', 'medium'] }],
        },
      ],
    }),
    onCancel: vi.fn(),
    onDispatched: vi.fn(),
  };
}

describe('RefactorModal', () => {
  it('uses static scope for a single repository and debounces valid preflight', async () => {
    vi.useFakeTimers();
    const mock = installAgenticoMock();
    mock.api.preflightRefactor.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      sourceRevision: 'revision-10',
      scope: 'one',
      repos: ['repo-a'],
      prompt: 'Extract the parser.',
    });
    render(<RefactorModal {...props()} />);

    expect(screen.getByText('Runs on repo-a')).toBeVisible();
    expect(screen.queryByRole('group', { name: 'Scope' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: 'Refactor prompt' }), {
      target: { value: 'Extract the parser.' },
    });
    expect(mock.api.preflightRefactor).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(400));
    expect(mock.api.preflightRefactor).toHaveBeenCalledWith({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
      prompt: 'Extract the parser.',
    });
    expect(screen.getByText('Applies to: repo-a')).toBeVisible();
  });

  it('disables start when the preflight reports blockers', async () => {
    const mock = installAgenticoMock();
    mock.api.preflightRefactor.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      sourceRevision: 'revision-11',
      scope: 'one',
      repos: ['repo-a'],
      prompt: 'Extract the parser.',
      blockers: ['A repository cycle is already active.'],
    });
    render(<RefactorModal {...props()} />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Refactor prompt' }), {
      target: { value: 'Extract the parser.' },
    });

    expect(await screen.findByText('A repository cycle is already active.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Start refactor' })).toBeDisabled();
  });

  it('starts with the guarded source revision from the latest preflight', async () => {
    const mock = installAgenticoMock();
    mock.api.preflightRefactor.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      sourceRevision: 'revision-12',
      scope: 'one',
      repos: ['repo-a'],
      prompt: 'Extract the parser.',
      pipeline: 'medium',
    });
    mock.api.startRefactor.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      cycleType: 'refactor',
      result: 'started',
    });
    const modalProps = props();
    render(<RefactorModal {...modalProps} />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Refactor prompt' }), {
      target: { value: 'Extract the parser.' },
    });
    fireEvent.change(screen.getByRole('combobox', { name: 'Pipeline (optional)' }), {
      target: { value: 'medium' },
    });

    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Start refactor' })).toBeEnabled(),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Start refactor' }));
    await waitFor(() =>
      expect(mock.api.startRefactor).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        repo: 'repo-a',
        prompt: 'Extract the parser.',
        pipeline: 'medium',
        sourceRevision: 'revision-12',
      }),
    );
    expect(modalProps.onDispatched).toHaveBeenCalled();
    expect(modalProps.onCancel).toHaveBeenCalled();
  });
});
