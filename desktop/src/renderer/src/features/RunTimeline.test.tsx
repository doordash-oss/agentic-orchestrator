import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { SessionSummary } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { RunTimeline } from './RunTimeline';

const FEATURE_ID = 'abcd1234ef567890';
const session: SessionSummary = {
  id: 'session-1',
  featureId: FEATURE_ID,
  runNumber: 2,
  phase: 'Implement',
  kind: 'implementer',
  status: 'running',
  startedAt: '2026-07-15T10:00:00Z',
  usage: {},
};

describe('RunTimeline', () => {
  it('backfills the current feature session before opening live output at the REST cursor', async () => {
    const mock = installAgenticoMock({
      sessions: [session, { ...session, id: 'other', featureId: 'other-feature' }],
      transcript: {
        sessionId: session.id,
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'assistant', type: 'text', text: 'Backfilled message' }],
      },
    });
    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />);

    expect(await screen.findByText('Backfilled message')).toBeInTheDocument();
    expect(mock.api.getSessionTranscript).toHaveBeenCalledWith({
      sessionId: session.id,
      limit: 500,
    });
    expect(mock.api.openSessionOutput).toHaveBeenCalledWith({ sessionId: session.id, from: 1 });
    expect(mock.api.getSessionTranscript.mock.invocationCallOrder[0]).toBeLessThan(
      mock.api.openSessionOutput.mock.invocationCallOrder[0]!,
    );
  });

  it('replaces repeated live rows, exposes safe raw detail, and cancels on unmount', async () => {
    const mock = installAgenticoMock({ sessions: [session] });
    const user = userEvent.setup();
    const view = render(
      <RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />,
    );
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());

    mock.emitSessionOutput({
      subscriptionId: 'subscription-1',
      type: 'record',
      sessionId: session.id,
      index: 2,
      message: { index: 2, role: 'assistant', type: 'text', text: '<b>first</b>' },
    });
    mock.emitSessionOutput({
      subscriptionId: 'subscription-1',
      type: 'record',
      sessionId: session.id,
      index: 2,
      message: { index: 2, role: 'assistant', type: 'text', text: '<script>replacement</script>' },
    });

    expect(await screen.findByText('<script>replacement</script>')).toBeInTheDocument();
    expect(screen.queryByText('<b>first</b>')).not.toBeInTheDocument();
    expect(document.querySelector('script')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Inspect raw record 2' }));
    const inspector = screen.getByRole('complementary', { name: 'Raw record inspector' });
    expect(inspector).toHaveTextContent('"index": 2');

    view.unmount();
    expect(mock.api.cancelSessionOutput).toHaveBeenCalledWith('subscription-1');
    expect(mock.sessionOutputListenerCount()).toBe(0);
  });

  it('marks stale/resetting output and preserves a reader away from live', async () => {
    const mock = installAgenticoMock({ sessions: [session] });
    const user = userEvent.setup();
    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />);
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());

    mock.emitAppEvent({ type: 'status', stream: 'stale' });
    expect(await screen.findByRole('status')).toHaveTextContent('stale');
    mock.emitAppEvent({ type: 'status', stream: 'connecting' });
    expect(await screen.findByRole('status')).toHaveTextContent('resetting');

    const viewport = screen.getByLabelText('Semantic timeline');
    Object.defineProperties(viewport, {
      scrollHeight: { configurable: true, value: 1000 },
      clientHeight: { configurable: true, value: 100 },
      scrollTop: { configurable: true, writable: true, value: 0 },
    });
    fireEvent.scroll(viewport);
    mock.emitSessionOutput({
      subscriptionId: 'subscription-1',
      type: 'record',
      sessionId: session.id,
      index: 3,
      message: { index: 3, role: 'assistant', type: 'text', text: 'new live message' },
    });
    const jump = await screen.findByRole('button', { name: 'Jump to live' });
    await user.click(jump);
    expect(screen.queryByRole('button', { name: 'Jump to live' })).not.toBeInTheDocument();
  });

  it('ignores newer recent sessions from a sealed run', async () => {
    const mock = installAgenticoMock({
      sessions: [
        { ...session, id: 'sealed-session', runNumber: 1, startedAt: '2026-07-16T10:00:00Z' },
        session,
      ],
    });
    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />);

    await waitFor(() => expect(mock.api.getSession).toHaveBeenCalledWith(session.id));
    expect(mock.api.getSession).not.toHaveBeenCalledWith('sealed-session');
  });

  it('orders review axes and initially focuses the running axis over a failed axis', async () => {
    const design = {
      ...session,
      id: 'review-design',
      phase: 'Final Review',
      kind: 'validator',
      label: 'Design',
      status: 'failed',
    };
    const functionality = {
      ...design,
      id: 'review-functionality',
      label: 'Functionality/Evidence',
      status: 'running',
    };
    const cleanliness = { ...design, id: 'review-cleanliness', label: 'Cleanliness' };
    const mock = installAgenticoMock({ sessions: [design, cleanliness, functionality] });
    const user = userEvent.setup();

    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Final Review" />);

    await waitFor(() => expect(mock.api.getSession).toHaveBeenCalledWith('review-functionality'));
    const tabs = screen.getAllByRole('tab');
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      'Functionality/Evidencerunning',
      'Cleanlinessfailed',
      'Designfailed',
    ]);
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');

    await user.click(screen.getByRole('tab', { name: /Design/ }));
    await waitFor(() => expect(mock.api.getSession).toHaveBeenCalledWith('review-design'));
    expect(screen.getByRole('tab', { name: /Design/ })).toHaveAttribute('aria-selected', 'true');
  });

  it('retries session discovery when the server announces a newly registered session', async () => {
    const mock = installAgenticoMock({ sessions: [] });
    mock.api.listSessions.mockResolvedValueOnce([]).mockResolvedValue([session]);
    mock.api.getSession.mockResolvedValue({
      ...session,
      transcriptCursor: { total: 0, start: 0, end: 0 },
      pendingControlCount: 0,
      canAttach: false,
      logAvailable: false,
    });
    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />);

    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Waiting for the current run session to register…',
      ),
    );
    expect(mock.api.listSessions).toHaveBeenCalledTimes(1);
    mock.emitAppEvent({
      type: 'invalidated',
      kind: 'session.updated',
      featureId: FEATURE_ID,
      resourceId: session.id,
    });

    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
    expect(mock.api.listSessions).toHaveBeenCalledTimes(2);
  });

  it('attaches without remounting when session registration finishes after its invalidation', async () => {
    const mock = installAgenticoMock({ sessions: [] });
    mock.api.listSessions
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([])
      .mockResolvedValue([session]);
    mock.api.getSession.mockResolvedValue({
      ...session,
      transcriptCursor: { total: 0, start: 0, end: 0 },
      pendingControlCount: 0,
      canAttach: false,
      logAvailable: false,
    });
    render(<RunTimeline featureId={FEATURE_ID} activeRun={2} currentPhase="Implement" />);

    await waitFor(() => expect(mock.api.listSessions).toHaveBeenCalledTimes(1));
    mock.emitAppEvent({
      type: 'invalidated',
      kind: 'session.updated',
      featureId: FEATURE_ID,
      resourceId: session.id,
    });
    await waitFor(() => expect(mock.api.listSessions).toHaveBeenCalledTimes(2));

    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled(), { timeout: 2_000 });
    expect(screen.getByRole('status')).toHaveTextContent('live');
  });
});
