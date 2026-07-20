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
