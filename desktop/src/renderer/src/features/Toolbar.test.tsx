import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Toolbar } from './Toolbar';

afterEach(cleanup);

function baseProps() {
  return {
    sidebarCollapsed: false,
    onToggleSidebar: vi.fn(),
    title: 'Overview',
  };
}

describe('Toolbar new-feature button', () => {
  it('renders as a real button when Overview is selected and showNewFeature is true', async () => {
    const onNewFeature = vi.fn();
    render(
      <Toolbar {...baseProps()} showTrailing={false} showNewFeature onNewFeature={onNewFeature} />,
    );

    const button = screen.getByRole('button', { name: 'New feature' });
    expect(button.tagName).toBe('BUTTON');
    expect(button).not.toHaveAttribute('tabindex', '-1');
    await userEvent.click(button);
    expect(onNewFeature).toHaveBeenCalledTimes(1);
  });

  it('is absent when a feature is selected', () => {
    render(
      <Toolbar
        {...baseProps()}
        title="Search revamp"
        showTrailing
        showNewFeature={false}
        onNewFeature={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'New feature' })).not.toBeInTheDocument();
  });

  it('is absent for Settings', () => {
    render(
      <Toolbar
        {...baseProps()}
        title="Settings"
        showTrailing={false}
        showNewFeature={false}
        onNewFeature={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'New feature' })).not.toBeInTheDocument();
  });
});
