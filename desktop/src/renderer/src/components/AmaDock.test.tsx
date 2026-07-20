import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock } from '../test/agenticoMock';
import { AmaDock } from './AmaDock';

afterEach(cleanup);

describe('AmaDock', () => {
  it('marks the expanded drawer for a full-width transcript when no question is pending', async () => {
    installAgenticoMock();
    render(
      <AmaDock
        attentionItems={[]}
        refreshAttention={() => Promise.resolve([])}
        setAttentionDrafts={vi.fn()}
        routeRequest={null}
      />,
    );

    await userEvent.click(screen.getByRole('button', { name: 'AMA' }));

    expect(
      (await screen.findByRole('region', { name: 'AMA transcript' })).parentElement,
    ).toHaveAttribute('data-has-attention', 'false');
  });
});
