import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ModelCatalogue, SessionSummary } from '../../../shared/ipc';
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
  it('keeps cycle activity focused on the current session instead of historical agents', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Resolving the rebase',
      contextPercentage: 7,
      totalSeconds: 90,
      totalUsd: 0.03,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValueOnce({
      runNumber: 8,
      sessions: [
        validator({ id: 'implement-1', label: 'Implement #1', status: 'running' }),
        validator({ id: 'structural', label: 'Structural', status: 'running' }),
        validator({ id: 'implement-2', label: 'Implement #2', status: 'running' }),
      ],
    });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [
        validator({ id: 'implement-1', label: 'Implement #1', status: 'completed' }),
        validator({ id: 'structural', label: 'Structural', status: 'completed' }),
        validator({ id: 'implement-2', label: 'Implement #2', status: 'running' }),
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
            text:
              sessionId === 'implement-2'
                ? 'Working on the current rebase.'
                : 'Historical session.',
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
        presentation="cycle"
        cycle={{
          type: 'rebase',
          status: 'running',
          phase: 'resolve_conflicts',
          startedAt: '2026-07-21T00:00:00Z',
        }}
      />,
    );

    await waitFor(() => expect(mock.api.listRunSessions).toHaveBeenCalledTimes(1));
    const activityHeading = screen.getByRole('heading', { name: 'Live agent activity' });
    expect(activityHeading).toHaveClass('live-preview__title');
    expect(activityHeading.closest('.live-preview__bar')).not.toBeNull();
    expect(screen.queryByText('Current cycle')).not.toBeInTheDocument();
    expect(screen.queryByRole('tablist', { name: 'Live agents' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Refresh' }));

    expect(await screen.findByText('Working on the current rebase.')).toBeVisible();
    expect(screen.queryByRole('tablist', { name: 'Live agents' })).not.toBeInTheDocument();
    expect(screen.queryByText('Implement #1')).not.toBeInTheDocument();
    expect(screen.queryByText('Structural')).not.toBeInTheDocument();
  });

  it('never presents a pre-cycle terminal session as live activity', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Old activity',
      contextPercentage: 100,
      totalSeconds: 10,
      totalUsd: 0.01,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [
        validator({
          id: 'old-session',
          label: 'Old session',
          status: 'completed',
          startedAt: '2026-07-22T00:00:00Z',
        }),
      ],
    });
    mock.api.getSessionTranscript.mockResolvedValue({
      sessionId: 'old-session',
      cursor: { total: 1, start: 0, end: 1 },
      messages: [{ index: 0, role: 'assistant', type: 'text', text: 'Historical session.' }],
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="FinalReview"
        reviewGate={REVIEW_GATE}
        presentation="cycle"
        cycle={{
          type: 'rebase',
          status: 'reviewing',
          phase: 'final_review',
          startedAt: '2026-07-23T00:00:00Z',
        }}
      />,
    );

    expect(await screen.findByText('Starting the final review agent session…')).toBeVisible();
    expect(screen.queryByText('Historical session.')).not.toBeInTheDocument();
  });

  it('renders harness repository operations when no cycle session exists', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Inspecting repositories',
      contextPercentage: 0,
      totalSeconds: 3,
      totalUsd: 0,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Publish"
        reviewGate={REVIEW_GATE}
        presentation="cycle"
        cycle={{
          type: 'rebase',
          status: 'running',
          phase: 'inspect_rebase',
          startedAt: '2026-07-23T00:00:00Z',
        }}
        repoStatus={[
          {
            name: 'api',
            publishable: true,
            rebaseStatus: 'conflict',
            rebaseTarget: 'origin/main',
            conflictFiles: ['internal/api.go'],
            lastError: 'manual resolution required',
          },
        ]}
      />,
    );

    expect(await screen.findByRole('heading', { name: 'Harness activity' })).toBeVisible();
    expect(screen.getByLabelText('Rebase operations')).toHaveTextContent('api');
    expect(screen.getByText('→ origin/main')).toBeVisible();
    expect(screen.getByText('internal/api.go')).toBeVisible();
    expect(screen.getByRole('alert')).toHaveTextContent('manual resolution required');
    expect(screen.queryByLabelText('Live agent transcript')).not.toBeInTheDocument();
  });

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
        featureStatus="Published"
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
    const artifactsToggle = screen.getByRole('button', { name: 'Run artifacts (6)' });
    const logsToggle = screen.getByRole('button', { name: 'Bounded logs (1)' });
    expect(artifactsToggle).toHaveAttribute('aria-expanded', 'false');
    expect(logsToggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByRole('button', { name: /^Open artifact / })).not.toBeInTheDocument();

    await user.click(artifactsToggle);
    expect(artifactsToggle).toHaveAttribute('aria-expanded', 'true');
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

    await user.click(logsToggle);
    expect(logsToggle).toHaveAttribute('aria-expanded', 'true');
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

    rerender(
      <CurrentRunInspection
        featureId="fedcba0987654321"
        runNumber={2}
        currentPhase="Implement"
        reviewGate={REVIEW_GATE}
      />,
    );
    expect(screen.getByRole('button', { name: 'Run artifacts (6)' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
    expect(screen.getByRole('button', { name: 'Bounded logs (1)' })).toHaveAttribute(
      'aria-expanded',
      'false',
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

  it('shows current-phase elapsed/cost with the model, and reports run totals up', async () => {
    const mock = installAgenticoMock();
    const canonicalModel = 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]';
    mock.api.getModelCatalogue.mockResolvedValue({
      providerOrder: ['opencode'],
      providerModels: {
        opencode: [{ id: canonicalModel, displayName: 'GLM 5.2 (1.04M)' }],
      },
      phaseDefaults: {},
      phaseProviderModels: {},
    });
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 21,
      totalSeconds: 3844,
      totalUsd: 21.62,
      session: {
        id: 'sess-impl',
        featureId: 'abcd1234ef567890',
        runNumber: 8,
        phase: 'Implement',
        kind: 'implement',
        status: 'running',
        startedAt: '2026-07-23T10:00:00Z',
        model: canonicalModel,
        usage: {},
      },
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
        featureStatus="Implementing"
        reviewGate={REVIEW_GATE}
        onRunMetrics={onRunMetrics}
      />,
    );

    // Current phase: 760s and $12.40, not the run totals (3844s / $21.62).
    expect(await screen.findByText('12m 40s')).toBeInTheDocument();
    expect(screen.getByText('$12.40')).toBeInTheDocument();
    expect(screen.getByText('GLM 5.2 (1.04M)')).toHaveAttribute('title', canonicalModel);
    expect(screen.queryByText(canonicalModel)).not.toBeInTheDocument();
    await waitFor(() =>
      expect(onRunMetrics).toHaveBeenCalledWith({ totalSeconds: 3844, totalUsd: 21.62 }),
    );
  });

  it('keeps phase cost unavailable until the server reports persisted or running cost', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running research',
      contextPercentage: 7,
      totalSeconds: 600,
      totalUsd: 9.04,
      transcript: [],
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 0,
      timing: { totalSeconds: 600, byPhase: { Inquire: 585, Research: 15 } },
      cost: { totalUsd: 9.04, byPhase: { Inquire: 9.04 } },
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        featureStatus="Researching"
        reviewGate={REVIEW_GATE}
      />,
    );

    expect(await screen.findByText('15s')).toBeVisible();
    const metrics = screen.getByText('Phase cost').closest('div');
    expect(metrics).toHaveTextContent('—');
    expect(metrics).not.toHaveTextContent('$0.00');
  });

  it('refreshes run metrics when the active phase changes', async () => {
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

    const view = render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        featureStatus="Researching"
        reviewGate={REVIEW_GATE}
      />,
    );
    expect(await screen.findByText('$0.50')).toBeVisible();

    view.rerender(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Design"
        featureStatus="Designing"
        reviewGate={REVIEW_GATE}
      />,
    );

    await waitFor(() => expect(mock.api.getRun).toHaveBeenCalledTimes(2));
    expect(screen.getByText('15s')).toBeVisible();
    expect(screen.getByText('$0.50')).toBeVisible();
  });

  it('polls active run metrics while the session is streaming', async () => {
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

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Research"
        featureStatus="Researching"
        reviewGate={REVIEW_GATE}
      />,
    );
    await act(async () => Promise.resolve());
    expect(screen.getByText('1m 00s')).toBeVisible();
    expect(screen.getByText('$0.50')).toBeVisible();

    await act(() => vi.advanceTimersByTimeAsync(1000));

    expect(mock.api.getRun).toHaveBeenCalledTimes(2);
    expect(screen.getByText('1m 01s')).toBeVisible();
    expect(screen.getByText('$0.60')).toBeVisible();
  });

  it('shows elapsed time and cost from the active cycle accounting key', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Resolving conflicts',
      contextPercentage: 31,
      totalSeconds: 4500,
      totalUsd: 18.75,
      transcript: [],
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 0,
      timing: { totalSeconds: 4500, byPhase: { Publish: 300, 'rebase-4': 732 } },
      cost: { totalUsd: 18.75, byPhase: { Publish: 1.5, 'rebase-4': 9.84 } },
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 8, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Publish"
        reviewGate={REVIEW_GATE}
        presentation="cycle"
        cycle={{
          type: 'rebase',
          status: 'running',
          count: 4,
          phase: 'resolve_conflicts',
          startedAt: '2026-07-23T10:00:00Z',
        }}
      />,
    );

    expect(await screen.findByText('12m 12s')).toBeVisible();
    expect(screen.getByText('$9.84')).toBeVisible();
  });

  it('uses the selected cycle session context when the live preview has no active session', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Interrupted',
      contextPercentage: -1,
      totalSeconds: 4500,
      totalUsd: 18.75,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({
      runNumber: 8,
      sessions: [
        validator({
          id: 'rebase-3-impl-1',
          label: 'Implement',
          phase: 'Rebase',
          kind: 'implement',
          status: 'failed',
          contextPercentage: 91,
          startedAt: '2026-07-22T10:00:00Z',
        }),
        validator({
          id: 'rebase-4-impl-1',
          label: 'Implement',
          phase: 'Rebase',
          kind: 'implement',
          status: 'failed',
          contextPercentage: 63,
          startedAt: '2026-07-23T10:00:00Z',
        }),
      ],
    });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Publish"
        reviewGate={REVIEW_GATE}
        presentation="cycle"
        cycle={{
          type: 'rebase',
          status: 'interrupted',
          count: 4,
          phase: 'resolve_conflicts',
          startedAt: '2026-07-23T10:00:00Z',
        }}
      />,
    );

    expect(await screen.findByText('63%')).toBeVisible();
    expect(screen.queryByText('91%')).not.toBeInTheDocument();
    expect(screen.queryByText('Unavailable')).not.toBeInTheDocument();
  });

  it('renders required inspection data before optional model metadata resolves', async () => {
    const mock = installAgenticoMock();
    const canonicalModel = 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]';
    let resolveCatalogue: ((catalogue: ModelCatalogue) => void) | undefined;
    mock.api.getModelCatalogue.mockReturnValue(
      new Promise<ModelCatalogue>((resolve) => {
        resolveCatalogue = resolve;
      }),
    );
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Running implementation',
      contextPercentage: 21,
      totalSeconds: 3844,
      totalUsd: 21.62,
      session: {
        id: 'sess-impl',
        featureId: 'abcd1234ef567890',
        runNumber: 8,
        phase: 'Implement',
        kind: 'implement',
        status: 'running',
        startedAt: '2026-07-23T10:00:00Z',
        model: canonicalModel,
        usage: {},
      },
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

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={8}
        currentPhase="Implement"
        currentRoadmapPhase={5}
        featureStatus="Implementing"
        reviewGate={REVIEW_GATE}
      />,
    );

    expect(await screen.findByText('Running implementation')).toBeVisible();
    expect(screen.getByText('12m 40s')).toBeVisible();
    expect(screen.getByText('$12.40')).toBeVisible();
    expect(screen.getByText('glm-5p2[1.04M]')).toHaveAttribute('title', canonicalModel);
    expect(screen.getByRole('button', { name: 'Run artifacts (0)' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Bounded logs (0)' })).toBeVisible();

    await act(async () => {
      resolveCatalogue?.({
        providerOrder: ['opencode'],
        providerModels: {
          opencode: [{ id: canonicalModel, displayName: 'GLM 5.2 (1.04M)' }],
        },
        phaseDefaults: {},
        phaseProviderModels: {},
      });
    });

    expect(await screen.findByText('GLM 5.2 (1.04M)')).toHaveAttribute('title', canonicalModel);
    expect(screen.queryByText('glm-5p2[1.04M]')).not.toBeInTheDocument();
  });

  it('sets final review apart from the last implementation phase', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'Final reviewing',
      contextPercentage: 3,
      totalSeconds: 4811,
      totalUsd: 7.87,
      transcript: [],
    });
    mock.api.listRunArtifacts.mockResolvedValue({ artifacts: [] });
    mock.api.listRunSessions.mockResolvedValue({ runNumber: 6, sessions: [] });

    render(
      <CurrentRunInspection
        featureId="abcd1234ef567890"
        runNumber={6}
        currentPhase="Final Review"
        featureStatus="FinalReviewing"
        currentRoadmapPhase={2}
        totalRoadmapPhases={2}
        reviewGate={REVIEW_GATE}
      />,
    );

    const gauge = await screen.findByRole('region', {
      name: /Roadmap progress: final review/,
    });
    expect(gauge).toHaveTextContent('Final review');
    expect(gauge).not.toHaveTextContent('Phase 2 of 2');
    // Both roadmap phases are done; final review is its own separated marker.
    const phaseSegments = gauge.querySelectorAll(
      '.roadmap-gauge__segment:not(.roadmap-gauge__segment--final)',
    );
    expect(phaseSegments).toHaveLength(2);
    for (const segment of phaseSegments) {
      expect(segment).toHaveAttribute('data-state', 'done');
    }
    const finalSegment = gauge.querySelector('.roadmap-gauge__segment--final');
    expect(finalSegment).not.toBeNull();
    expect(finalSegment).toHaveAttribute('data-state', 'active');
  });

  it('settles the roadmap gauge once the run rests at Code ready', async () => {
    const mock = installAgenticoMock();
    mock.api.getLivePreview.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      activity: 'CodeReady',
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
        currentPhase="Final Review"
        featureStatus="CodeReady"
        currentRoadmapPhase={12}
        totalRoadmapPhases={12}
        reviewGate={REVIEW_GATE}
      />,
    );

    const gauge = await screen.findByRole('region', {
      name: 'Roadmap progress: phase 12 of 12 — Code ready',
    });
    expect(gauge).toHaveTextContent('Code ready');
    expect(gauge).not.toHaveTextContent('Final Review');
    const segments = gauge.querySelectorAll('.roadmap-gauge__segment');
    expect(segments).toHaveLength(12);
    for (const segment of segments) {
      expect(segment).toHaveAttribute('data-state', 'done');
    }
    expect(gauge.querySelector('.roadmap-gauge__status')).toHaveAttribute('data-tone', 'rest');
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
        totalRoadmapPhases={5}
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

    // Counts: 2 of 4 have a verdict (passed + failed); 1 failure.
    expect(
      await screen.findByRole('heading', { name: 'Verifying implementation · 2/4' }),
    ).toBeVisible();
    const commands = screen.getByLabelText('Verification commands');
    expect(commands).toHaveTextContent('go test ./...✓');
    expect(commands).toHaveTextContent('npm run build⟳');
    expect(commands).toHaveTextContent('e2e smoke✕');

    // The stale context reading is suppressed while verifying.
    expect(screen.queryByText('42%')).not.toBeInTheDocument();
    // The transcript is replaced by the verification log; the review gate hides.
    expect(
      screen.getByText(
        'Verification in progress — no agent session to watch; see the live preview.',
      ),
    ).toBeVisible();
    expect(screen.queryByLabelText('Review axes')).not.toBeInTheDocument();
    // The roadmap gauge reflects verification too.
    expect(
      screen.getByRole('region', {
        name: 'Roadmap progress: phase 2 of 5 — Verifying implementation',
      }),
    ).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Expand live preview to full screen' }));
    const overlay = await screen.findByRole('dialog', { name: 'Live agent preview' });
    expect(overlay).toHaveTextContent('Verification in progress');
    expect(overlay).toHaveTextContent('go test ./...');
    expect(overlay).toHaveTextContent('npm run build');
    expect(overlay).toHaveTextContent('lint');
    expect(overlay).toHaveTextContent('e2e smoke');
    expect(overlay).not.toHaveTextContent('Stale implementation transcript.');
    expect(overlay).not.toHaveTextContent('Running implementation');
    expect(overlay).not.toHaveTextContent('42%');
  });

  it('keeps the review gate when an active reviewing gate coincides with a stale verifying marker', async () => {
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
        phaseStatus="verifying"
        reviewGate={{
          reviewingGate: true,
          reviewFixing: false,
          validatingPlan: false,
          validatorStatuses: { Craft: 'APPROVED' },
        }}
        verificationItems={[{ name: 'go test', state: 'running' }]}
      />,
    );

    expect(await screen.findByRole('heading', { name: 'Reviewing implementation' })).toBeVisible();
    expect(
      screen.queryByRole('heading', { name: /Verifying implementation/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByLabelText('Review axes')).toBeVisible();
  });
});
