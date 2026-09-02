import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRef } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalError } from '../../../shared/api/parse';
import { ErrorSurface, type ErrorSurfaceAction } from './ErrorSurface';

afterEach(cleanup);

const REMEDIATION_HINT = 'Commit or stash the listed files, then retry the rebase.';

const BASE_ERROR: CanonicalError = {
  code: 'parent_worktrees_dirty',
  class: 'needs_action',
  title: 'Parent worktrees have uncommitted changes',
  summary: 'Rebase needs a clean parent worktree in every repository.',
};

const FULL_ERROR: CanonicalError = {
  ...BASE_ERROR,
  remediation: { hint: REMEDIATION_HINT, actions: ['rebase_feature'] },
  context: {
    repositories: [
      {
        name: 'repo-a',
        branch: 'main',
        dirty_files: ['src/one.ts', 'src/two.ts'],
        conflict_files: ['src/three.ts'],
        parent_anchor_sha: 'a1b2c3',
      },
    ],
    phase: { name: 'implement', iteration: 2 },
    command: { exit_code: 128, log_paths: ['/logs/rebase.log'] },
  },
  diagnostics: 'exit status 128\nfatal: needs a clean worktree',
};

const ENABLED_ACTION: ErrorSurfaceAction = { enabled: true, label: 'Retry rebase' };

/** Asserts `earlier` precedes `later` in document order. */
function expectBefore(earlier: Element, later: Element): void {
  expect(earlier.compareDocumentPosition(later) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
}

const CLASS_CASES: Array<{
  errorClass: CanonicalError['class'];
  label: string;
  modifier: string;
  role: 'alert' | 'status';
}> = [
  { errorClass: 'blocking', label: 'Failed', modifier: 'error-surface--blocking', role: 'alert' },
  {
    errorClass: 'needs_action',
    label: 'Needs your action',
    modifier: 'error-surface--needs-action',
    role: 'alert',
  },
  { errorClass: 'warning', label: 'Warning', modifier: 'error-surface--warning', role: 'status' },
];

describe('ErrorSurface class treatment', () => {
  for (const { errorClass, label, modifier, role } of CLASS_CASES) {
    it(`renders ${errorClass} with its icon, label, modifier, and role`, () => {
      render(<ErrorSurface error={{ ...BASE_ERROR, class: errorClass }} />);
      const root = screen.getByRole(role);
      expect(root).toHaveClass('error-surface', modifier);
      expect(screen.getByText(label)).toBeVisible();
      // The icon wrapper marks which glyph it carries, so classes stay
      // distinguishable without poking into SVG geometry.
      const icon = root.querySelector<HTMLElement>('.error-surface__icon');
      expect(icon).not.toBeNull();
      expect(icon).toHaveAttribute('data-icon', errorClass);
      expect(icon?.querySelector('svg')).not.toBeNull();
      // The stable code tag rides the header in every variant.
      expect(screen.getByText(BASE_ERROR.code)).toHaveClass('error-surface__code');
    });
  }

  it('carries the code tag and class label in the compact variant too', () => {
    render(<ErrorSurface error={BASE_ERROR} variant="compact" />);
    expect(screen.getByText(BASE_ERROR.code)).toHaveClass('error-surface__code');
    expect(screen.getByText('Needs your action')).toBeVisible();
  });

  it('lands the optional ref and tabIndex on its root div', () => {
    const rootRef = createRef<HTMLDivElement>();
    render(<ErrorSurface error={BASE_ERROR} rootRef={rootRef} rootTabIndex={-1} />);
    expect(rootRef.current).not.toBeNull();
    expect(rootRef.current).toHaveClass('error-surface');
    expect(rootRef.current).toHaveAttribute('tabindex', '-1');
    expect(rootRef.current).toHaveAttribute('role', 'alert');
  });
});

describe('ErrorSurface disclosure order', () => {
  it('lays caption, title, summary, remediation, details, diagnostics out in order', () => {
    const { container } = render(<ErrorSurface error={FULL_ERROR} caption="Rebase was rejected" />);
    const caption = screen.getByText('Rebase was rejected');
    const title = screen.getByText(FULL_ERROR.title);
    const summary = screen.getByText(FULL_ERROR.summary);
    const remediation = screen.getByText(REMEDIATION_HINT);
    const details = screen.getByText('Details').closest('details');
    const diagnostics = container.querySelector('pre');
    expect(details).not.toBeNull();
    expect(diagnostics).not.toBeNull();
    expectBefore(caption, title);
    expectBefore(title, summary);
    expectBefore(summary, remediation);
    expectBefore(remediation, details!);
    expectBefore(details!, diagnostics!);
  });

  it('uses two disclosures in the full variant and one folded disclosure in compact', () => {
    const full = render(<ErrorSurface error={FULL_ERROR} />);
    expect(full.container.querySelectorAll('details')).toHaveLength(2);
    expect(screen.getByText('Details')).toBeInTheDocument();
    expect(screen.getByText('Diagnostics')).toBeInTheDocument();
    cleanup();
    const compact = render(<ErrorSurface error={FULL_ERROR} variant="compact" />);
    expect(compact.container.querySelectorAll('details')).toHaveLength(1);
    expect(screen.getByText('More detail')).toBeInTheDocument();
    // Both detail kinds live inside the single folded disclosure. The
    // disclosure is collapsed by default, so assert presence, not visibility.
    expect(screen.getByText('repo-a')).toBeInTheDocument();
    expect(compact.container.querySelector('pre')).not.toBeNull();
  });

  it('renders no empty elements when remediation, context, and diagnostics are absent', () => {
    const { container } = render(<ErrorSurface error={BASE_ERROR} />);
    expect(container.querySelector('details')).toBeNull();
    expect(container.querySelector('summary')).toBeNull();
    expect(container.querySelector('pre')).toBeNull();
    expect(container.querySelector('.error-surface__caption')).toBeNull();
    expect(container.querySelector('.error-surface__remediation')).toBeNull();
    // Title and summary are the only paragraphs — nothing rendered empty.
    expect(container.querySelectorAll('p')).toHaveLength(2);
  });
});

describe('ErrorSurface structured details', () => {
  it('renders repositories, phase, command, and raw diagnostics', () => {
    // The Details disclosure is collapsed by default, so these assert
    // presence in the document rather than visibility.
    const { container } = render(<ErrorSurface error={FULL_ERROR} />);
    expect(screen.getByText('repo-a')).toBeInTheDocument();
    expect(screen.getByText('main')).toBeInTheDocument();
    expect(screen.getByText('Dirty files')).toBeInTheDocument();
    expect(screen.getByText('src/one.ts').closest('li')).not.toBeNull();
    expect(screen.getByText('src/two.ts')).toBeInTheDocument();
    expect(screen.getByText('Conflict files')).toBeInTheDocument();
    expect(screen.getByText('src/three.ts').closest('li')).not.toBeNull();
    expect(screen.getByText('Parent anchor')).toBeInTheDocument();
    expect(screen.getByText('a1b2c3')).toBeInTheDocument();
    expect(screen.getByText('Phase: implement')).toBeInTheDocument();
    expect(screen.getByText('Iteration 2')).toBeInTheDocument();
    expect(screen.getByText('Exit code: 128')).toBeInTheDocument();
    expect(screen.getByText('Logs')).toBeInTheDocument();
    expect(screen.getByText('/logs/rebase.log').closest('li')).not.toBeNull();
    expect(container.querySelector('pre')).toHaveTextContent('exit status 128');
  });

  it('renders the setup_task block as a task line under Details', () => {
    // A run-level setup-failure record carries only the setup_task block;
    // the full variant must still show visible details for it.
    const error: CanonicalError = {
      code: 'worktree_setup_failed',
      class: 'blocking',
      title: 'Worktree setup failed',
      summary: 'Setup task "Worktree: repo-a" failed.',
      remediation: { hint: 'Resolve the reported problem, then retry setup.', actions: ['setup'] },
      context: { setup_task: { key: 'worktree:repo-a', kind: 'worktree', label: 'Worktree: repo-a' } },
    };
    const { container } = render(<ErrorSurface error={error} variant="full" />);
    const details = container.querySelector('details.error-surface__details');
    expect(details).not.toBeNull();
    expect(screen.getByText('Task: Worktree: repo-a')).toBeInTheDocument();
    expect(screen.getByText('worktree')).toBeInTheDocument();
  });
});

describe('ErrorSurface primary-action slot', () => {
  it('renders the enabled action as the only button and forwards the click', async () => {
    const onAction = vi.fn();
    const resolveAction = vi.fn(() => ENABLED_ACTION);
    render(<ErrorSurface error={FULL_ERROR} resolveAction={resolveAction} onAction={onAction} />);
    expect(resolveAction).toHaveBeenCalledWith('rebase_feature');
    expect(screen.getAllByRole('button')).toHaveLength(1);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Retry rebase' }));
    expect(onAction).toHaveBeenCalledTimes(1);
    expect(onAction).toHaveBeenCalledWith('rebase_feature');
  });

  it('shows the disabled reason as text instead of a button', () => {
    render(
      <ErrorSurface
        error={FULL_ERROR}
        resolveAction={vi.fn(() => ({
          enabled: false,
          label: 'Retry rebase',
          disabledReason: 'Another child is running.',
        }))}
      />,
    );
    expect(screen.getByText('Another child is running.')).toBeVisible();
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('renders nothing in the slot when the error references no action', () => {
    render(
      <ErrorSurface
        error={{ ...FULL_ERROR, remediation: { hint: REMEDIATION_HINT } }}
        resolveAction={vi.fn(() => ENABLED_ACTION)}
      />,
    );
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('renders nothing in the slot without a resolver or when resolution misses', () => {
    render(<ErrorSurface error={FULL_ERROR} />);
    expect(screen.queryByRole('button')).toBeNull();
    cleanup();
    render(<ErrorSurface error={FULL_ERROR} resolveAction={vi.fn(() => undefined)} />);
    expect(screen.queryByRole('button')).toBeNull();
  });
});

describe('ErrorSurface explain-in-chat slot', () => {
  it('renders no chat control in any variant — only the primary action, if any', () => {
    for (const variant of ['full', 'compact'] as const) {
      render(
        <ErrorSurface
          error={FULL_ERROR}
          variant={variant}
          resolveAction={vi.fn(() => ENABLED_ACTION)}
        />,
      );
      expect(screen.queryByRole('button', { name: /chat|explain/i })).toBeNull();
      expect(screen.getAllByRole('button')).toHaveLength(1);
      expect(screen.getByRole('button', { name: 'Retry rebase' })).toBeVisible();
      cleanup();
      render(<ErrorSurface error={FULL_ERROR} variant={variant} />);
      expect(screen.queryByRole('button')).toBeNull();
      cleanup();
    }
  });
});
