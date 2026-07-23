import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { PublishModalBody } from './PublishModal';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const preflight: CompletionPreflightResult = {
  featureId: 'f',
  sourceRevision: 'rev-1',
  canMarkDone: false,
  repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
};
function props(over?: Partial<Parameters<typeof PublishModalBody>[0]>) {
  return {
    featureId: 'f',
    preflight,
    dispatchAction: vi.fn(() => Promise.resolve({ result: 'published repo-a' })),
    generatePublishDescription: vi.fn(() =>
      Promise.resolve({ featureId: 'f', title: 'T', body: 'B' }),
    ),
    openExternal: vi.fn(() => Promise.resolve({ ok: true })),
    onDispatched: vi.fn(),
    ...over,
  };
}

describe('PublishModalBody', () => {
  it('disables Publish until a title is entered', () => {
    render(<PublishModalBody {...props()} />);
    expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('PR title'), { target: { value: 'My PR' } });
    expect(screen.getByRole('button', { name: 'Publish' })).toBeEnabled();
  });

  it('dispatches publish with source_revision, repos, and title', async () => {
    const dispatchAction = vi.fn(() => Promise.resolve({ result: 'ok' }));
    const onDispatched = vi.fn();
    render(<PublishModalBody {...props({ dispatchAction, onDispatched })} />);
    fireEvent.change(screen.getByLabelText('PR title'), { target: { value: 'My PR' } });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenCalledWith('f', 'publish', {
        source_revision: 'rev-1',
        repos: ['repo-a'],
        title: 'My PR',
      }),
    );
    await waitFor(() => expect(onDispatched).toHaveBeenCalled());
  });

  it('fills title/body from generate', async () => {
    render(<PublishModalBody {...props()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Generate PR narrative' }));
    await waitFor(() =>
      expect((screen.getByLabelText('PR title') as HTMLInputElement).value).toBe('T'),
    );
  });
});
