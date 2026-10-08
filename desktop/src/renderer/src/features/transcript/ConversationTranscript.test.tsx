/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { installTranscriptLayout, viewportOffset } from '../../test/transcriptLayout';
import { ConversationTranscript, FileChangeCard } from './ConversationTranscript';
import type { ConversationItem } from './conversation';

afterEach(cleanup);

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

function rows(from: number, to: number): ConversationItem[] {
  return Array.from({ length: to - from + 1 }, (_, offset) => ({
    kind: 'message' as const,
    key: `message-${String(from + offset)}:0`,
    role: 'assistant' as const,
    text: `Row ${String(from + offset)}`,
  }));
}

function article(text: string): HTMLElement {
  return screen.getByText(text).closest('article')!;
}

describe('ConversationTranscript', () => {
  const isTranscript = (element: Element): boolean =>
    element.getAttribute('aria-label') === 'Transcript';

  it('renders the top slot above the first row', () => {
    render(
      <ConversationTranscript
        ariaLabel="Transcript"
        idleLabel="Idle"
        waiting={false}
        items={rows(1, 2)}
        top={<p>Loading earlier messages…</p>}
      />,
    );
    const region = screen.getByRole('region', { name: 'Transcript' });
    const texts = [...region.querySelectorAll('p')].map((node) => node.textContent);
    expect(texts).toEqual(['Loading earlier messages…', 'Row 1', 'Row 2']);
  });

  it('holds the first visible row at its viewport offset when rows are prepended', () => {
    const restore = installTranscriptLayout(isTranscript);
    try {
      const props = { ariaLabel: 'Transcript', idleLabel: 'Idle', waiting: false } as const;
      const { rerender } = render(
        <ConversationTranscript {...props} items={rows(10, 30)} anchorPrepend />,
      );
      const region = screen.getByRole('region', { name: 'Transcript' });
      region.scrollTop = 120;
      fireEvent.scroll(region);
      const before = viewportOffset(article('Row 12'));
      expect(before).toBeLessThanOrEqual(0);

      rerender(<ConversationTranscript {...props} items={rows(1, 30)} anchorPrepend />);

      expect(viewportOffset(article('Row 12'))).toBe(before);
      expect(region.scrollTop).toBe(120 + 9 * 50);
    } finally {
      restore();
    }
  });

  it('leaves the scroll position alone without prepend anchoring', () => {
    const restore = installTranscriptLayout(isTranscript);
    try {
      const props = { ariaLabel: 'Transcript', idleLabel: 'Idle', waiting: false } as const;
      const { rerender } = render(<ConversationTranscript {...props} items={rows(10, 30)} />);
      const region = screen.getByRole('region', { name: 'Transcript' });
      region.scrollTop = 120;
      fireEvent.scroll(region);

      rerender(<ConversationTranscript {...props} items={rows(1, 30)} />);

      expect(region.scrollTop).toBe(120);
      expect(region.querySelector('[hidden]')).toBeNull();
    } finally {
      restore();
    }
  });

  it('asks for earlier rows when scrolled near the top, and not further down', () => {
    const restore = installTranscriptLayout(isTranscript);
    try {
      const onNearTop = vi.fn();
      render(
        <ConversationTranscript
          ariaLabel="Transcript"
          idleLabel="Idle"
          waiting={false}
          items={rows(1, 40)}
          onNearTop={onNearTop}
        />,
      );
      const region = screen.getByRole('region', { name: 'Transcript' });
      onNearTop.mockClear();
      region.scrollTop = 1200;
      fireEvent.scroll(region);
      expect(onNearTop).not.toHaveBeenCalled();
      region.scrollTop = 40;
      fireEvent.scroll(region);
      expect(onNearTop).toHaveBeenCalledTimes(1);
    } finally {
      restore();
    }
  });

  it('asks for earlier rows when the rows do not fill the viewport', () => {
    const onNearTop = vi.fn();
    render(
      <ConversationTranscript
        ariaLabel="Transcript"
        idleLabel="Idle"
        waiting={false}
        items={rows(1, 2)}
        onNearTop={onNearTop}
      />,
    );
    expect(onNearTop).toHaveBeenCalled();
  });
});
