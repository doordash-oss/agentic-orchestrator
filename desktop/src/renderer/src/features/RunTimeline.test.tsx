import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type { TranscriptMessage } from '../../../shared/ipc';
import { HistoricalTimeline } from './RunTimeline';

afterEach(cleanup);

describe('HistoricalTimeline', () => {
  it('renders a semantic timeline and exposes the validated raw record on demand', async () => {
    const user = userEvent.setup();
    const messages: TranscriptMessage[] = [
      { index: 0, role: 'assistant', type: 'text', text: 'Backfilled message' },
      { index: 1, role: 'assistant', type: 'text', text: '<script>replacement</script>' },
    ];
    render(<HistoricalTimeline messages={messages} />);

    expect(screen.getByText('Backfilled message')).toBeVisible();
    // Text is rendered as escaped content, never as live markup.
    expect(screen.getByText('<script>replacement</script>')).toBeVisible();
    expect(document.querySelector('script')).toBeNull();

    await user.click(screen.getByRole('button', { name: 'Inspect raw record 1' }));
    const inspector = screen.getByRole('complementary', { name: 'Raw record inspector' });
    expect(inspector).toHaveTextContent('"index": 1');
  });

  it('shows an empty state when the session has no records', () => {
    render(<HistoricalTimeline messages={[]} />);
    expect(screen.getByText('This session has no transcript records yet.')).toBeVisible();
  });
});
