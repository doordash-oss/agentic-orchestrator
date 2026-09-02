import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRef, type ReactElement } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { CanonicalError } from '../../../shared/api/parse';
import { ErrorSurface, type ErrorSurfaceAction } from './ErrorSurface';
import { ExplainChatProvider } from '../explainChat';

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
      context: {
        setup_task: { key: 'worktree:repo-a', kind: 'worktree', label: 'Worktree: repo-a' },
      },
    };
    const { container } = render(<ErrorSurface error={error} variant="full" />);
    const details = container.querySelector('details.error-surface__details');
    expect(details).not.toBeNull();
    expect(screen.getByText('Task: Worktree: repo-a')).toBeInTheDocument();
    expect(screen.getByText('worktree')).toBeInTheDocument();
  });

  it('renders a repository entry with a rebase target and remote-only commit count under Details', () => {
    // Publish failure records name the repository with its branch, the
    // rebase target of a conflicted pull-rebase, and the remote-only commit
    // count of a diverged branch; all three render under Details.
    const error: CanonicalError = {
      code: 'publish_rebase_conflict',
      class: 'needs_action',
      title: 'Pull-rebase conflict',
      summary: 'The pull rebase for repository "web" (branch "agentico/f") onto "main" conflicted.',
      context: {
        repositories: [
          {
            name: 'web',
            branch: 'agentico/f',
            rebase_target: 'main',
            remote_only_commits: 3,
          },
        ],
      },
    };
    render(<ErrorSurface error={error} variant="full" />);
    expect(screen.getByText('web')).toBeInTheDocument();
    expect(screen.getByText('agentico/f')).toBeInTheDocument();
    expect(screen.getByText('onto main')).toBeInTheDocument();
    expect(screen.getByText('Remote-only commits')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
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
  const FEATURE_NAME = 'Search revamp';
  const RUN_REFERENCE = { scope: 'run' as const, code: BASE_ERROR.code, featureId: 'abcd1234' };

  /** Renders inside a mounted provider whose requester is a spy. */
  function renderWithChat(ui: ReactElement) {
    const requestRoute = vi.fn();
    render(<ExplainChatProvider requestRoute={requestRoute}>{ui}</ExplainChatProvider>);
    return requestRoute;
  }

  it('renders no chat control in any variant without a provider', () => {
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

  it('renders the button in both variants for every error class', () => {
    for (const { errorClass } of CLASS_CASES) {
      for (const variant of ['full', 'compact'] as const) {
        renderWithChat(
          <ErrorSurface
            error={{ ...BASE_ERROR, class: errorClass }}
            variant={variant}
            explain={{ reference: RUN_REFERENCE, featureName: FEATURE_NAME }}
          />,
        );
        expect(screen.getByRole('button', { name: 'Explain in chat' })).toBeVisible();
        cleanup();
      }
    }
  });

  it('issues the routed request with autoSubmit, the reference, and the templated draft', async () => {
    const requestRoute = renderWithChat(
      <ErrorSurface
        error={FULL_ERROR}
        explain={{ reference: RUN_REFERENCE, featureName: FEATURE_NAME }}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledTimes(1);
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft: `Explain the "${FULL_ERROR.title}" error (${FULL_ERROR.code}) on ${FEATURE_NAME} and what I should do next.`,
      autoSubmit: true,
      chatContext: RUN_REFERENCE,
    });
  });

  it('omits the feature clause when no feature name is known', async () => {
    const requestRoute = renderWithChat(
      <ErrorSurface error={FULL_ERROR} explain={{ reference: RUN_REFERENCE }} />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft: `Explain the "${FULL_ERROR.title}" error (${FULL_ERROR.code}) and what I should do next.`,
      autoSubmit: true,
      chatContext: RUN_REFERENCE,
    });
  });

  it('carries no chatContext when the explain prop passes no reference', async () => {
    const requestRoute = renderWithChat(
      <ErrorSurface error={FULL_ERROR} explain={{ featureName: FEATURE_NAME }} />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft: `Explain the "${FULL_ERROR.title}" error (${FULL_ERROR.code}) on ${FEATURE_NAME} and what I should do next.`,
      autoSubmit: true,
    });
  });

  it('keeps the primary action first in the DOM when one renders', () => {
    renderWithChat(
      <ErrorSurface
        error={FULL_ERROR}
        resolveAction={vi.fn(() => ENABLED_ACTION)}
        explain={{ reference: RUN_REFERENCE, featureName: FEATURE_NAME }}
      />,
    );
    const buttons = screen.getAllByRole('button');
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).toHaveAccessibleName('Retry rebase');
    expect(buttons[1]).toHaveAccessibleName('Explain in chat');
    expectBefore(buttons[0]!, buttons[1]!);
  });
});
