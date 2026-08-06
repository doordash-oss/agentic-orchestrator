import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SessionSummary } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { CurrentRunInspection } from './CurrentRunInspection';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

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
    taskActivities: [],
    runningTaskCount: 0,
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
  it('reserves a stable two-pane record desk while the archive loads', () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockReturnValue(new Promise(() => undefined));
    mock.api.listRunArtifacts.mockReturnValue(new Promise(() => undefined));
    mock.api.listRunLogs.mockReturnValue(new Promise(() => undefined));

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Publish"
        reviewGate={REVIEW_GATE}
        presentation="record"
        shouldStream={false}
      />,
    );

    expect(screen.getByText('Loading run record…')).toBeVisible();
    expect(screen.getByLabelText('Loading run record')).toHaveClass(
      'current-inspection__record-skeleton',
    );
  });

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
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 2,
      sessions: [
        validator({ id: 'craft', label: 'Craft', iteration: 2, status: 'completed' }),
        validator({
          id: 'functionality',
          label: 'Functionality/Evidence',
          iteration: 2,
          status: 'running',
        }),
        validator({
          id: 'cleanliness',
          label: 'Cleanliness',
          iteration: 2,
          status: 'failed',
        }),
        validator({ id: 'design', label: 'Design', iteration: 2, status: 'running' }),
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

    const { rerender } = render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={2}
        currentPhase="Implement"
        currentIteration={2}
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
    expect(await screen.findByRole('tablist', { name: 'Live agents' })).toBeVisible();
    expect(screen.queryByLabelText('Review gate')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Review axes')).not.toBeInTheDocument();
    // Files live behind their own preview tab; nothing is openable until it opens.
    expect(screen.queryByRole('button', { name: /^Open artifact / })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Files' }));

    expect(
      screen
        .getAllByRole('button', { name: /^Open artifact / })
        .map((button) => button.getAttribute('aria-label')),
    ).toEqual([
      'Open artifact inquire',
      'Open artifact research',
      'Open artifact design',
      'Open artifact phase-plan',
      'Open artifact phase-2-plan',
      'Open artifact phase-10-plan',
    ]);
    // Opening an artifact floats it in a modal so it stays visible regardless of scroll.
    await user.click(screen.getByRole('button', { name: 'Open artifact phase-plan' }));
    const artifactDialog = await screen.findByRole('dialog', { name: 'Run artifact phase-plan' });
    const artifact = screen.getByLabelText('Current run artifact content');
    expect(artifact).toHaveTextContent('Current artifact');
    expect(artifact.querySelector('h1')).toHaveTextContent('Current artifact');
    await user.click(within(artifactDialog).getByRole('button', { name: 'Close file' }));
    expect(
      screen.queryByRole('dialog', { name: 'Run artifact phase-plan' }),
    ).not.toBeInTheDocument();

    // The lone log channel opens by default; its file opens in the same modal.
    await user.click(screen.getByRole('button', { name: 'Open log research/output.txt' }));
    expect(
      await screen.findByRole('dialog', { name: 'Run log research/output.txt' }),
    ).toBeVisible();
    expect(screen.getByLabelText('Current run log content')).toHaveTextContent('current log');
    expect(screen.getByLabelText('Current run log content')).not.toHaveTextContent('\u001b');
    expect(mock.api.getRunArtifactContent).toHaveBeenCalledWith(
      expect.objectContaining({ artifactId: 'phase-plan', limit: 64 * 1024 }),
    );
    expect(mock.api.getRunLogContent).toHaveBeenCalledWith(
      expect.objectContaining({ logId: 'log-research', offset: 64 * 1024, limit: 64 * 1024 }),
    );

    rerender(
      <CurrentRunInspection
        featureId="fedcba0987654321"
        runNumber={2}
        currentPhase="Implement"
        reviewGate={REVIEW_GATE}
      />,
    );
    // Switching runs clears the opened file so the prior run's content can't leak.
    expect(screen.queryByLabelText('Current run log content')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Current run artifact content')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Files' })).toBeInTheDocument();
  });

  it('groups bounded logs into channels and drills into a channel on demand', async () => {
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
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });
    mock.api.listRunLogs.mockResolvedValue({
      logs: [
        {
          id: 'v1',
          path: 'phase-06/verification-events/iter-1.jsonl',
          size: 2048,
          modifiedAt: 'x',
        },
        {
          id: 'v2',
          path: 'phase-06/verification-events/iter-2.jsonl',
          size: 2048,
          modifiedAt: 'x',
        },
        {
          id: 'v3',
          path: 'phase-06/verification-events/iter-3.jsonl',
          size: 2048,
          modifiedAt: 'x',
        },
        { id: 'p1', path: 'phase-06/plan/attempt-1.md', size: 4096, modifiedAt: 'x' },
      ],
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        reviewGate={REVIEW_GATE}
      />,
    );

    await user.click(await screen.findByRole('button', { name: 'Files' }));
    // The dominant channel sorts first; individual files stay collapsed while
    // more than one channel exists, so nothing is directly openable yet.
    const channelToggle = screen.getByRole('button', {
      name: 'phase-06/verification-events channel — 3 files',
    });
    expect(channelToggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('button', { name: /^Open log / })).not.toBeInTheDocument();

    await user.click(channelToggle);
    expect(channelToggle).toHaveAttribute('aria-expanded', 'true');
    expect(
      screen
        .getAllByRole('button', { name: /^Open log / })
        .map((button) => button.getAttribute('aria-label')),
    ).toEqual([
      'Open log phase-06/verification-events/iter-1.jsonl',
      'Open log phase-06/verification-events/iter-2.jsonl',
      'Open log phase-06/verification-events/iter-3.jsonl',
    ]);
  });

  // Phase/roadmap chrome (the old roadmap gauge) and the per-phase telemetry
  // block (model, phase elapsed/cost) are now owned by the PhaseRail row
  // above the cockpit, not by this component — see PhaseRailRow.test.tsx and
  // phaseRail.test.ts. What remains here is the run-totals lift the rail
  // consumes via `onRunMetrics`.

  it('reports run totals, including the live context percentage, up via onRunMetrics', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 21,
      totalSeconds: 3844,
      totalUsd: 21.62,
      transcript: [],
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 0,
      timing: { totalSeconds: 3844, byPhase: { 'phase-5-impl': 760 } },
      cost: { totalUsd: 21.62, byPhase: { 'phase-5-impl': 12.4 } },
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    const onRunMetrics = vi.fn();
    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentRoadmapPhase={5}
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );

    // Run totals (3844s / $21.62), not the per-phase breakdown — that
    // breakdown no longer surfaces anywhere on the live cockpit.
    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith({
        totalSeconds: 3844,
        totalUsd: 21.62,
        contextPercentage: 21,
      }),
    );
  });

  it('omits the context percentage when neither the preview nor its session reports one', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running research',
      contextPercentage: -1,
      totalSeconds: 600,
      totalUsd: 9.04,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    const onRunMetrics = vi.fn();
    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );

    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith({
        totalSeconds: 600,
        totalUsd: 9.04,
        contextPercentage: undefined,
      }),
    );
  });

  it('refetches run totals when the active phase changes', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running pipeline',
      contextPercentage: 12,
      totalSeconds: 615,
      totalUsd: 9.54,
      transcript: [],
    });
    mock.api.getRun
      .mockResolvedValueOnce({
        runNumber: 8,
        artifactCount: 0,
        timing: { totalSeconds: 600, byPhase: { Research: 15 } },
        cost: { totalUsd: 9.04, byPhase: { Research: 0.5 } },
      })
      .mockResolvedValueOnce({
        runNumber: 8,
        artifactCount: 0,
        timing: { totalSeconds: 615, byPhase: { Research: 15, Design: 15 } },
        cost: { totalUsd: 9.54, byPhase: { Research: 0.5, Design: 0.5 } },
      });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    const onRunMetrics = vi.fn();
    const view = render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );
    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith(
        expect.objectContaining({ totalSeconds: 600, totalUsd: 9.04 }),
      ),
    );

    view.rerender(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Design"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );

    await waitFor(() => expect(mock.api.getRun).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith(
        expect.objectContaining({ totalSeconds: 615, totalUsd: 9.54 }),
      ),
    );
  });

  it('polls run totals while the session is streaming', async () => {
    vi.useFakeTimers();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running research',
      contextPercentage: 12,
      totalSeconds: 600,
      totalUsd: 9.04,
      transcript: [],
    });
    mock.api.getRun
      .mockResolvedValueOnce({
        runNumber: 8,
        artifactCount: 0,
        timing: { totalSeconds: 600, byPhase: { Research: 60 } },
        cost: { totalUsd: 9.04, byPhase: { Research: 0.5 } },
      })
      .mockResolvedValueOnce({
        runNumber: 8,
        artifactCount: 0,
        timing: { totalSeconds: 601, byPhase: { Research: 61 } },
        cost: { totalUsd: 9.14, byPhase: { Research: 0.6 } },
      });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    const onRunMetrics = vi.fn();
    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );
    await act(async () => Promise.resolve());
    expect(onRunMetrics).toHaveBeenCalledWith(
      expect.objectContaining({ totalSeconds: 600, totalUsd: 9.04 }),
    );

    await act(() => vi.advanceTimersByTimeAsync(1000));

    expect(mock.api.getRun).toHaveBeenCalledTimes(2);
    expect(onRunMetrics).toHaveBeenCalledWith(
      expect.objectContaining({ totalSeconds: 601, totalUsd: 9.14 }),
    );
  });

  it('pauses hidden live work and refreshes and resubscribes when activated', async () => {
    vi.useFakeTimers();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running research',
      contextPercentage: 12,
      totalSeconds: 600,
      totalUsd: 9.04,
      transcript: [],
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 0,
      timing: { totalSeconds: 600, byPhase: { Research: 60 } },
      cost: { totalUsd: 9.04, byPhase: { Research: 0.5 } },
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [validator({ id: 'researcher', label: 'Researcher', phase: 'Research' })],
    });
    mock.api.getSessionTranscript.mockResolvedValue({
      sessionId: 'researcher',
      cursor: { total: 0, start: 0, end: 0 },
      messages: [],
    });

    const inspection = (active: boolean) => (
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        reviewGate={REVIEW_GATE}
        shouldStream
        active={active}
      />
    );
    const view = render(inspection(true));

    await vi.waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(1));
    view.rerender(inspection(false));
    await vi.waitFor(() => expect(mock.api.cancelSessionOutput).toHaveBeenCalledTimes(1));
    expect(mock.sessionOutputListenerCount()).toBe(0);

    const pausedCalls = {
      run: mock.api.getRun.mock.calls.length,
      preview: mock.api.getLivePreview.mock.calls.length,
      sessions: mock.api.listRunSessions.mock.calls.length,
      transcript: mock.api.getSessionTranscript.mock.calls.length,
      output: mock.api.openSessionOutput.mock.calls.length,
    };
    act(() => mock.emitAppEvent({ type: 'invalidated', kind: 'session.updated' }));
    await act(() => vi.advanceTimersByTimeAsync(6_000));
    expect(mock.api.getRun).toHaveBeenCalledTimes(pausedCalls.run);
    expect(mock.api.getLivePreview).toHaveBeenCalledTimes(pausedCalls.preview);
    expect(mock.api.listRunSessions).toHaveBeenCalledTimes(pausedCalls.sessions);
    expect(mock.api.getSessionTranscript).toHaveBeenCalledTimes(pausedCalls.transcript);
    expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(pausedCalls.output);

    view.rerender(inspection(true));
    await vi.waitFor(() =>
      expect(mock.api.getLivePreview).toHaveBeenCalledTimes(pausedCalls.preview + 1),
    );
    await vi.waitFor(() =>
      expect(mock.api.listRunSessions).toHaveBeenCalledTimes(pausedCalls.sessions + 1),
    );
    await vi.waitFor(() =>
      expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(pausedCalls.output + 1),
    );
  });

  it('clears the reported run totals when the live surface goes away', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running research',
      contextPercentage: 12,
      totalSeconds: 600,
      totalUsd: 9.04,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    const onRunMetrics = vi.fn();
    const view = render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );
    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith(expect.objectContaining({ totalSeconds: 600 })),
    );

    view.unmount();
    expect(onRunMetrics).toHaveBeenLastCalledWith(null);
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

  it('restores only the current review batch after initial hydration', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Reviewing implementation',
      contextPercentage: 37,
      totalSeconds: 100,
      totalUsd: 1.5,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [
        validator({ id: 'old-craft', label: 'Craft', iteration: 1, status: 'completed' }),
        validator({ id: 'craft', label: 'Craft', iteration: 2, status: 'completed' }),
        validator({
          id: 'functionality',
          label: 'Functionality/Evidence',
          iteration: 2,
          status: 'failed',
        }),
        validator({
          id: 'cleanliness',
          label: 'Cleanliness',
          iteration: 2,
          status: 'running',
        }),
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
            text: `${sessionId} transcript`,
          },
        ],
      }),
    );

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentIteration={2}
        reviewGate={{
          reviewingGate: true,
          reviewFixing: false,
          validatingPlan: false,
          validatorStatuses: {
            Craft: 'APPROVED',
            'Functionality/Evidence': 'CHANGES_REQUESTED',
            Cleanliness: 'running',
          },
        }}
      />,
    );

    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((tab) => tab.getAttribute('aria-label'))).toEqual([
      'Craft — completed',
      'Functionality/Evidence — failed',
      'Cleanliness — running',
    ]);
    expect(screen.queryByText('old-craft transcript')).not.toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Craft — completed' }));
    expect(await screen.findByText('craft transcript')).toBeVisible();
  });

  it('groups the roster by role and walks agents with arrow keys', async () => {
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
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [
        validator({ id: 'craft', label: 'Craft' }),
        validator({ id: 'sec', label: 'Security' }),
        validator({ id: 'impl', label: undefined, kind: 'phase', phase: 'Implement' }),
      ],
    });
    const texts: Record<string, string> = {
      impl: 'Implementer transcript.',
      sec: 'Security review underway.',
      craft: 'Craft looks solid.',
    };
    mock.api.getSessionTranscript.mockImplementation(({ sessionId }: { sessionId: string }) =>
      Promise.resolve({
        sessionId,
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'assistant', type: 'text', text: texts[sessionId] ?? '' }],
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

    const tablist = await screen.findByRole('tablist', { name: 'Live agents' });
    expect(tablist).toHaveAttribute('aria-orientation', 'vertical');
    expect(screen.getByText('Implementer')).toBeInTheDocument();
    expect(screen.getByText('Review panel')).toBeInTheDocument();

    // Implementer sorts ahead of the review panel and selects first as the
    // first active agent in cohort order.
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.getAttribute('aria-label'))).toEqual([
      'Implement — running',
      'Security — running',
      'Craft — running',
    ]);
    expect(await screen.findByText('Implementer transcript.')).toBeVisible();

    tabs[0]!.focus();
    await user.keyboard('{ArrowDown}');
    expect(await screen.findByText('Security review underway.')).toBeVisible();
    expect(screen.getByRole('tab', { name: /Security/ })).toHaveFocus();
    await user.keyboard('{End}');
    expect(await screen.findByText('Craft looks solid.')).toBeVisible();
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

  it('shows a harness verification surface instead of the transcript while verifying', async () => {
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
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [validator({ id: 'craft', label: 'Craft', status: 'done' })],
    });
    mock.api.getSessionTranscript.mockResolvedValue({
      sessionId: 'craft',
      cursor: { total: 1, start: 0, end: 1 },
      messages: [
        {
          index: 0,
          role: 'assistant',
          type: 'text',
          text: 'Stale implementation transcript.',
        },
      ],
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentRoadmapPhase={2}
        phaseStatus="verifying"
        reviewGate={REVIEW_GATE}
        verificationItems={[
          { name: 'go test ./...', state: 'passed' },
          { name: 'npm run build', state: 'running' },
          { name: 'lint', state: 'pending' },
          { name: 'e2e smoke', state: 'failed' },
        ]}
      />,
    );

    const progress = await screen.findByLabelText('Verification progress');
    expect(progress).toHaveTextContent('go test ./...');
    expect(progress).toHaveTextContent('npm run build');
    expect(screen.queryByLabelText('Verification commands')).not.toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: 'Verifying implementation · 2/4' }),
    ).not.toBeInTheDocument();

    // The transcript is replaced by the verification log; the review gate hides.
    expect(
      screen.getByText(
        'Verification in progress — no agent session to watch; see the live preview.',
      ),
    ).toBeVisible();
    expect(screen.queryByLabelText('Review axes')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Expand live preview to full screen' }));
    const overlay = await screen.findByRole('dialog', { name: 'Live agent preview' });
    expect(overlay).toHaveTextContent('Verification in progress');
    expect(overlay).toHaveTextContent('go test ./...');
    expect(overlay).toHaveTextContent('npm run build');
    expect(overlay).toHaveTextContent('lint');
    expect(overlay).toHaveTextContent('e2e smoke');
    expect(overlay).not.toHaveTextContent('Stale implementation transcript.');
    expect(overlay).not.toHaveTextContent('Running implementation');
  });

  it('keeps live reviewer tabs when an active gate coincides with a stale verifying marker', async () => {
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
        validator({
          id: 'craft',
          label: 'Craft',
          iteration: 2,
          status: 'completed',
        }),
        validator({
          id: 'functionality',
          label: 'Functionality/Evidence',
          iteration: 2,
          status: 'completed',
        }),
        validator({
          id: 'cleanliness',
          label: 'Cleanliness',
          iteration: 2,
          status: 'running',
        }),
        validator({
          id: 'design',
          label: 'Design',
          iteration: 2,
          status: 'running',
        }),
      ],
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentIteration={2}
        phaseStatus="verifying"
        reviewGate={{
          reviewingGate: true,
          reviewFixing: false,
          validatingPlan: false,
          validatorStatuses: {
            Craft: 'APPROVED',
            'Functionality/Evidence': 'APPROVED',
            Cleanliness: 'running',
            Design: 'running',
          },
        }}
        verificationItems={[{ name: 'go test', state: 'running' }]}
      />,
    );

    expect(await screen.findByRole('tablist', { name: 'Live agents' })).toBeVisible();
    expect(
      screen.queryByRole('heading', { name: /Verifying implementation/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Review gate')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Review axes')).not.toBeInTheDocument();
  });

  it('keeps the review summary in sealed record presentation', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Run complete',
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
        reviewGate={{
          reviewingGate: true,
          reviewFixing: false,
          validatingPlan: false,
          validatorStatuses: { Craft: 'APPROVED' },
        }}
        presentation="record"
        shouldStream={false}
      />,
    );

    expect(await screen.findByLabelText('Review gate')).toBeVisible();
    expect(screen.getByLabelText('Review axes')).toHaveTextContent('Craft✓');
  });
});
