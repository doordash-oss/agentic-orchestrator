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

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { isEligibleForPublish, DiffViewer } from './completionShared';

describe('isEligibleForPublish', () => {
  it('is true only for publishable, eligible, touched repos', () => {
    expect(isEligibleForPublish({ publishable: true, status: 'eligible', touched: true })).toBe(
      true,
    );
    expect(isEligibleForPublish({ publishable: false, status: 'eligible', touched: true })).toBe(
      false,
    );
    expect(
      isEligibleForPublish({ publishable: true, status: 'already_published', touched: true }),
    ).toBe(false);
    expect(isEligibleForPublish({ publishable: true, status: 'eligible', touched: false })).toBe(
      false,
    );
  });
});

describe('DiffViewer', () => {
  it('splits into two panes when side-by-side', () => {
    const { container } = render(
      <DiffViewer diffText={'@@ -1 +1 @@\n-old\n+new'} renderSideBySide />,
    );
    expect(container.querySelectorAll('.completion-workspace__diff-pane')).toHaveLength(2);
  });
  it('renders one pre when unified', () => {
    const { container } = render(<DiffViewer diffText={'-old\n+new'} renderSideBySide={false} />);
    expect(container.querySelectorAll('.completion-workspace__diff-pane')).toHaveLength(0);
  });
});
