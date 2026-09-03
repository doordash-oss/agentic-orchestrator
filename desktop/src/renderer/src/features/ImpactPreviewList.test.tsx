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

import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import type { FeatureActionView } from '../../../shared/ipc';
import { ImpactPreviewList } from './ImpactPreviewList';

afterEach(cleanup);

type ImpactPreview = NonNullable<FeatureActionView['impactPreview']>;

describe('ImpactPreviewList', () => {
  it('renders each non-empty category with its item count and items, plus what is kept', () => {
    const preview: ImpactPreview = {
      kind: 'parent_cascade_delete',
      subject: { id: 'feature-1', name: 'Search revamp' },
      categories: [
        { key: 'children', label: 'Child passes', items: ['Refactor pass', 'Rebase pass'] },
        { key: 'worktrees', label: 'Worktrees', items: ['repo-a'] },
        { key: 'sessions', label: 'Sessions', items: [] },
      ],
      retained: ['Published PR #42'],
    };

    render(<ImpactPreviewList preview={preview} />);

    expect(
      screen.getByText('This deletes the feature and the local work listed below.'),
    ).toBeVisible();

    const childrenSection = screen
      .getByRole('heading', { name: 'Child passes' })
      .closest('section')!;
    expect(within(childrenSection).getByText('Refactor pass')).toBeVisible();
    expect(within(childrenSection).getByText('Rebase pass')).toBeVisible();

    const worktreesSection = screen.getByRole('heading', { name: 'Worktrees' }).closest('section')!;
    expect(within(worktreesSection).getByText('repo-a')).toBeVisible();

    // The empty category collapses into the quiet "none" summary rather than
    // its own empty list.
    expect(screen.queryByRole('heading', { name: 'Sessions' })).not.toBeInTheDocument();
    expect(screen.getByText('Sessions: none')).toBeVisible();

    const keptSection = screen.getByRole('heading', { name: 'Kept' }).closest('section')!;
    expect(within(keptSection).getByText('Published PR #42')).toBeVisible();
  });

  it('shows "None" under Kept when nothing is retained, and uses the pass-discard lede for child_discard', () => {
    const preview: ImpactPreview = {
      kind: 'child_discard',
      subject: { id: 'child-1', name: 'Refactor pass' },
      categories: [{ key: 'worktrees', label: 'Worktrees', items: ['repo-a'] }],
      retained: [],
    };

    render(<ImpactPreviewList preview={preview} />);

    expect(
      screen.getByText(
        'This deletes the pass’s local working copies. Everything under Kept stays with the parent.',
      ),
    ).toBeVisible();
    const keptSection = screen.getByRole('heading', { name: 'Kept' }).closest('section')!;
    expect(within(keptSection).getByText('None')).toBeVisible();
  });

  it('omits the quiet "none" summary entirely when every category has items', () => {
    const preview: ImpactPreview = {
      kind: 'parent_cascade_delete',
      subject: { id: 'feature-1', name: 'Search revamp' },
      categories: [{ key: 'children', label: 'Child passes', items: ['Refactor pass'] }],
      retained: [],
    };

    render(<ImpactPreviewList preview={preview} />);
    expect(screen.queryByText(/: none/)).not.toBeInTheDocument();
  });
});
