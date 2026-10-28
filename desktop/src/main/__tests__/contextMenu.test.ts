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

import { describe, expect, it } from 'vitest';
import { editContextMenuTemplate, type EditContextParams } from '../contextMenu';

const flags = { canCut: false, canCopy: true, canPaste: true, canSelectAll: true };

function params(overrides: Partial<EditContextParams>): EditContextParams {
  return { isEditable: false, selectionText: '', editFlags: flags, ...overrides };
}

describe('editContextMenuTemplate', () => {
  it.each([
    {
      name: 'offers Copy over selected text',
      input: params({ selectionText: 'reply' }),
      roles: ['copy'],
    },
    {
      name: 'shows nothing without a selection',
      input: params({ selectionText: '  ' }),
      roles: [],
    },
    {
      name: 'offers the edit roles in a field',
      input: params({ isEditable: true }),
      roles: ['cut', 'copy', 'paste', undefined, 'selectAll'],
    },
  ])('$name', ({ input, roles }) => {
    expect(editContextMenuTemplate(input).map((item) => item.role)).toEqual(roles);
  });

  it('follows the edit flags in a field', () => {
    const template = editContextMenuTemplate(params({ isEditable: true }));
    expect(template.find((item) => item.role === 'cut')?.enabled).toBe(false);
    expect(template.find((item) => item.role === 'paste')?.enabled).toBe(true);
  });
});
