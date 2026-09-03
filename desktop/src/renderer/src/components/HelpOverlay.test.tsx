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

import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { COMMAND_CATALOGUE } from '../../../shared/commands';
import { HelpOverlay } from './HelpOverlay';

describe('HelpOverlay', () => {
  it('derives shortcuts from the shared catalogue and closes with Escape', async () => {
    render(<HelpOverlay routeRequest={{ id: 1, event: { target: 'help' } }} />);
    const dialog = await screen.findByRole('dialog', { name: 'Keyboard shortcuts' });
    for (const command of COMMAND_CATALOGUE.filter((entry) => entry.accelerator)) {
      expect(dialog).toHaveTextContent(command.label);
    }
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
