import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { FileChangeCard } from './ConversationTranscript';

describe('FileChangeCard', () => {
  it('renders diff lines with added and removed markers', () => {
    render(
      <FileChangeCard
        change={{
          path: 'src/app.ts',
          operation: 'update',
          detail: '- const a = 1;\n+ const a = 2;\n+ const b = 3;',
        }}
      />,
    );

    expect(screen.getByLabelText('Updated src/app.ts')).toBeVisible();
    expect(screen.getByText('+2')).toBeVisible();
    expect(screen.getByText('−1')).toBeVisible();
    const diff = screen.getByRole('region', { name: 'Diff for src/app.ts' });
    expect(diff).toHaveTextContent('const a = 2;');
    expect(diff).toHaveTextContent('const b = 3;');
  });

  it('renders every line of a write as added content', () => {
    render(
      <FileChangeCard
        change={{
          path: 'README.md',
          operation: 'write',
          detail: '+ # Title\n+ First paragraph.',
        }}
      />,
    );

    expect(screen.getByLabelText('Created README.md')).toBeVisible();
    expect(screen.getByText('+2')).toBeVisible();
    const diff = screen.getByRole('region', { name: 'Diff for README.md' });
    const added = diff.querySelectorAll('[data-kind="added"]');
    expect(added).toHaveLength(2);
  });

  it('renders raw created markdown bullet content as additions', () => {
    render(
      <FileChangeCard
        change={{
          path: 'phase-06/implement/progress.md',
          operation: 'write',
          detail:
            '## Iteration Handoff\n\n### Completed this iteration\n- Extracted helper\n- Added regression coverage',
        }}
      />,
    );

    expect(screen.getByLabelText('Created phase-06/implement/progress.md')).toBeVisible();
    expect(screen.getByText('+5')).toBeVisible();
    expect(screen.queryByText('−2')).not.toBeInTheDocument();
    const diff = screen.getByRole('region', { name: 'Diff for phase-06/implement/progress.md' });
    expect(diff.querySelectorAll('[data-kind="added"]')).toHaveLength(5);
    expect(diff.querySelectorAll('[data-kind="removed"]')).toHaveLength(0);
  });

  it('caps oversized diffs and reports the hidden remainder', () => {
    const detail = Array.from({ length: 40 }, (_, i) => `+ line ${i + 1}`).join('\n');
    render(<FileChangeCard change={{ path: 'big.txt', operation: 'write', detail }} />);

    expect(screen.getByText('+40')).toBeVisible();
    const diff = screen.getByRole('region', { name: 'Diff for big.txt' });
    expect(diff.querySelectorAll('.conversation__diff-line')).toHaveLength(25);
    expect(screen.getByText('… 16 more lines')).toBeVisible();
  });

  it('keeps the header without a diff body for placeholder details', () => {
    render(
      <FileChangeCard
        change={{ path: 'src/app.ts', operation: 'update', detail: 'Captured from tool usage.' }}
      />,
    );

    expect(screen.getByLabelText('Updated src/app.ts')).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Diff for src/app.ts' })).not.toBeInTheDocument();
  });
});
