import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type { SessionSummary } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { CurrentRunInspection } from './CurrentRunInspection';

afterEach(cleanup);

function validator(
  overrides: Partial<SessionSummary> & Pick<SessionSummary, 'id' | 'label'>,
): SessionSummary {
  return {
    featureId: 'abcd1234ef567890',
    runNumber: 8,
    phase: 'Review',
    kind: 'validator',
    status: 'running',
    startedAt: '2026-07-22T00:00:00Z',
    usage: {},
    ...overrides,
  };
}

const REVIEW_GATE = {
  reviewingGate: false,
  reviewFixing: false,
  validatingPlan: false,
  validatorStatuses: {},
};

function renderCohort() {
  const mock = installAgenticoMock();
  mock.api.getLivePreview.mockResolvedValue({
    featureId: 'abcd1234ef567890',
    activity: 'Running implementation',
    contextPercentage: 42,
    totalSeconds: 73,
    totalUsd: 0.12,
    transcript: [],
  });
  mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
  mock.api.listRunSessions.mockResolvedValue({
    runNumber: 8,
    sessions: [
      validator({ id: 'craft', label: 'Craft' }),
      validator({ id: 'sec', label: 'Security' }),
    ],
  });
  mock.api.getSessionTranscript.mockImplementation(({ sessionId }: { sessionId: string }) =>
    Promise.resolve({
      sessionId,
      cursor: { total: 1, start: 0, end: 1 },
      messages: [
        {
          index: 0,
          role: 'assistant',
          type: 'text',
          text: sessionId === 'sec' ? 'Security review underway.' : 'Craft looks solid.',
        },
      ],
    }),
  );
  render(
    <CurrentRunInspection
      featureId="abcd1234ef567890"
      runNumber={8}
      currentPhase="Implement"
      reviewGate={REVIEW_GATE}
    />,
  );
  return mock;
}

describe('CurrentRunInspection', () => {
  it('shows authoritative live activity and loads bounded artifacts and logs', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({
      artifacts: [
        {
          id: 'phase-10-plan',
          runNumber: 2,
          phase: 'Plan',
          contentAvailable: true,
        },
        {
          id: 'design',
          runNumber: 2,
          phase: 'Design',
          contentAvailable: true,
        },
        {
          id: 'phase-plan',
          runNumber: 2,
          phase: 'Implement',
          contentAvailable: true,
        },
        {
          id: 'inquire',
          runNumber: 2,
          phase: 'Inquire',
          contentAvailable: true,
        },
        {
          id: 'phase-2-plan',
          runNumber: 2,
          phase: 'Plan',
          contentAvailable: true,
        },
        {
          id: 'research',
          runNumber: 2,
          phase: 'Research',
          contentAvailable: true,
        },
      ],
    });
    mock.api.listRunLogs.mockResolvedValue({
      logs: [
        {
          id: 'log-research',
          path: 'research/output.txt',
          size: 128 * 1024,
          modifiedAt: '2026-07-22T12:00:00Z',
        },
      ],
    });
    mock.api.getRunArtifactContent.mockResolvedValue({
      id: 'phase-plan',
      offset: 0,
      limit: 65536,
      size: 18,
      text: '# Current artifact',
      truncated: false,
    });
    mock.api.getRunLogContent.mockResolvedValue({
      id: 'log-research',
      offset: 64 * 1024,
      limit: 65536,
      size: 16,
      text: '\u001b[31mcurrent log\u001b[0m',
      truncated: false,
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={2}
        currentPhase="Implement"
        reviewGate={{
          reviewingGate: true,
          reviewFixing: true,
          validatingPlan: false,
          validatorStatuses: {
            Design: 'running',
            Cleanliness: 'CHANGES_REQUESTED',
            'Functionality/Evidence': 'running',
            Craft: 'APPROVED',
          },
        }}
      />,
    );

    expect(await screen.findByText('Running implementation')).toBeVisible();
    expect(screen.getByText('42%')).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Reviewing implementation' })).toBeVisible();
    expect(screen.getByText('Fix pass active')).toBeVisible();
    expect(screen.getByLabelText('Review axes')).toHaveTextContent('Craft✓Func⟳Clean✕Design⟳');
    expect(
      screen
        .getAllByRole('button', { name: /^Open artifact / })
        .map((button) => button.textContent),
    ).toEqual(['inquire', 'research', 'design', 'phase-plan', 'phase-2-plan', 'phase-10-plan']);
    await user.click(screen.getByRole('button', { name: 'Open artifact phase-plan' }));
    const artifact = await screen.findByLabelText('Current run artifact content');
    expect(artifact).toHaveTextContent('Current artifact');
    expect(artifact.querySelector('h1')).toHaveTextContent('Current artifact');

    await user.click(screen.getByRole('button', { name: 'Enlarge artifact' }));
    expect(screen.getByRole('dialog', { name: 'Expanded artifact phase-plan' })).toBeVisible();
    expect(
      screen.getByLabelText('Expanded artifact content').querySelector('h1'),
    ).toHaveTextContent('Current artifact');
    await user.click(screen.getByRole('button', { name: 'Exit enlarged artifact' }));
    expect(
      screen.queryByRole('dialog', { name: 'Expanded artifact phase-plan' }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Open log research/output.txt' }));
    expect(await screen.findByLabelText('Current run log content')).toHaveTextContent(
      'current log',
    );
    expect(screen.getByLabelText('Current run log content')).not.toHaveTextContent('\u001b');
    expect(mock.api.getRunArtifactContent).toHaveBeenCalledWith(
      expect.objectContaining({ artifactId: 'phase-plan', limit: 64 * 1024 }),
    );
    expect(mock.api.getRunLogContent).toHaveBeenCalledWith(
      expect.objectContaining({ logId: 'log-research', offset: 64 * 1024, limit: 64 * 1024 }),
    );
  });

  it('shows the roadmap gauge with phase, total, and iteration during implementation', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentRoadmapPhase={2}
        totalRoadmapPhases={5}
        currentIteration={3}
        phaseStatus="implementing"
        reviewGate={REVIEW_GATE}
      />,
    );

    const gauge = await screen.findByRole('region', {
      name: 'Roadmap progress: phase 2 of 5 — Implementing · Iteration 3',
    });
    expect(gauge).toHaveTextContent('Phase 2 of 5');
    expect(gauge).toHaveTextContent('Implementing · Iteration 3');
    const segments = gauge.querySelectorAll('.roadmap-gauge__segment');
    expect(segments).toHaveLength(5);
    expect(segments[0]).toHaveAttribute('data-state', 'done');
    expect(segments[1]).toHaveAttribute('data-state', 'active');
    expect(segments[2]).toHaveAttribute('data-state', 'upcoming');
  });

  it('labels the roadmap gauge as reviewing while the implementation gate runs', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentRoadmapPhase={4}
        totalRoadmapPhases={4}
        currentIteration={2}
        phaseStatus="reviewing"
        reviewGate={REVIEW_GATE}
      />,
    );

    const gauge = await screen.findByRole('region', { name: /Roadmap progress/ });
    expect(gauge).toHaveTextContent('Phase 4 of 4');
    expect(gauge).toHaveTextContent('Reviewing · Iteration 2');
  });

  it('hides the roadmap gauge before the roadmap exists', async () => {
    renderCohort();
    await screen.findByText('Security review underway.');
    expect(screen.queryByRole('region', { name: /Roadmap progress/ })).not.toBeInTheDocument();
  });

  it('renders one tab per cohort agent and switches transcripts in isolation', async () => {
    const user = userEvent.setup();
    renderCohort();

    expect(await screen.findByRole('tablist', { name: 'Live agents' })).toBeVisible();
    // Security orders before Craft and is the first active tab, so it selects first.
    expect(await screen.findByText('Security review underway.')).toBeVisible();
    expect(screen.queryByText('Craft looks solid.')).not.toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: /Craft/ }));
    expect(await screen.findByText('Craft looks solid.')).toBeVisible();
    expect(screen.queryByText('Security review underway.')).not.toBeInTheDocument();
  });

  it('shows the working indicator while a running agent is selected', async () => {
    renderCohort();
    expect(await screen.findByText('Security review underway.')).toBeVisible();
    expect(screen.getByText('Working')).toBeVisible();
  });

  it('renders created and updated files as readable inline diffs', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 42,
      totalSeconds: 73,
      totalUsd: 0.12,
      transcript: [
        {
          index: 0,
          role: 'system',
          type: 'tool_progress',
          tool: 'Write',
          redacted: true,
          fileChange: {
            path: 'src/new-panel.tsx',
            operation: 'write',
            detail: '+export function NewPanel() {\n+  return null;\n+}',
            addedLines: 3,
            removedLines: 0,
            hasDiffPatch: true,
          },
        },
        {
          index: 1,
          role: 'system',
          type: 'tool_progress',
          tool: 'Edit',
          redacted: true,
          fileChange: {
            path: 'src/app.tsx',
            operation: 'update',
            detail: '-return <OldPanel />;\n+return <NewPanel />;',
            addedLines: 1,
            removedLines: 1,
            hasDiffPatch: true,
          },
        },
      ],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        reviewGate={REVIEW_GATE}
      />,
    );

    const created = await screen.findByRole('article', { name: 'Created src/new-panel.tsx' });
    expect(created).toHaveTextContent('Createdsrc/new-panel.tsx+3');
    expect(screen.getByRole('region', { name: 'Diff for src/new-panel.tsx' })).toHaveTextContent(
      'export function NewPanel()',
    );

    const updated = screen.getByRole('article', { name: 'Updated src/app.tsx' });
    expect(updated).toHaveTextContent('Updatedsrc/app.tsx+1−1');
    expect(screen.getByRole('region', { name: 'Diff for src/app.tsx' })).toHaveTextContent(
      'return <OldPanel />;',
    );
  });

  it('shows auto-picked inquiry dialogue in the conversation preview', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running inquiry',
      contextPercentage: 10,
      totalSeconds: 12,
      totalUsd: 0.01,
      transcript: [
        {
          index: 0,
          role: 'user',
          type: 'text',
          text: 'Redis (Recommended)',
          locallyAppended: true,
          autoPicked: true,
          autoPickQuestion: 'Which cache should this use?',
          autoPickConfidence: 0.85,
        },
      ],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Inquire"
        reviewGate={REVIEW_GATE}
      />,
    );

    const autoPick = await screen.findByRole('article', { name: 'Auto-picked response' });
    expect(autoPick).toHaveTextContent('Option 1: Redis (Recommended)');
    expect(autoPick).not.toHaveTextContent('Which cache should this use?');
    expect(autoPick).not.toHaveTextContent('85% confidence');
  });

  it('shows the durable wait reason when no session exists for the current run', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Building knowledge base',
      contextPercentage: -1,
      totalSeconds: 0,
      totalUsd: 0,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="KnowledgeBase"
        reviewGate={REVIEW_GATE}
        waitReason='Waiting for KB build on repo "repo-a" by feature "Other feature"'
      />,
    );

    expect(
      await screen.findByText('Waiting for KB build on repo "repo-a" by feature "Other feature"'),
    ).toBeVisible();
    expect(screen.queryByText('Waiting for the agent to respond…')).not.toBeInTheDocument();
  });

  it('merges a streamed record into the correct session without touching the selected view', async () => {
    const user = userEvent.setup();
    const mock = renderCohort();
    await screen.findByText('Security review underway.');
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'record',
        sessionId: 'craft',
        index: 1,
        message: { index: 1, role: 'assistant', type: 'text', text: 'Craft added tests.' },
      });
    });

    // The selected (Security) transcript is unaffected by Craft's record.
    expect(screen.queryByText('Craft added tests.')).not.toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: /Craft/ }));
    expect(await screen.findByText('Craft added tests.')).toBeVisible();
  });

  it('cancels every subscription and listener on unmount', async () => {
    const mock = renderCohort();
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
    cleanup();
    await waitFor(() => expect(mock.api.cancelSessionOutput).toHaveBeenCalled());
    expect(mock.sessionOutputListenerCount()).toBe(0);
  });

  it('opens the full-screen overlay and closes it on Escape', async () => {
    const user = userEvent.setup();
    renderCohort();
    await screen.findByText('Security review underway.');

    await user.click(screen.getByRole('button', { name: 'Expand live preview to full screen' }));
    expect(await screen.findByRole('dialog', { name: 'Live agent preview' })).toBeVisible();

    await user.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Live agent preview' })).not.toBeInTheDocument(),
    );
  });

  it('switches the selected agent to the raw signal-trace view', async () => {
    const user = userEvent.setup();
    renderCohort();
    await screen.findByText('Security review underway.');

    await user.click(screen.getByRole('button', { name: 'Signal trace' }));
    const inspector = screen.getByRole('complementary', { name: 'Raw record inspector' });
    expect(inspector).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Inspect raw record 0' }));
    expect(inspector).toHaveTextContent('"index": 0');
  });
});
