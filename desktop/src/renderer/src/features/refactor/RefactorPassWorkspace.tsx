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

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import {
  attentionOwnerFeatureId,
  isPendingReviewStatus,
  type AttentionItem,
  type FeatureActionView,
  type FeatureSnapshot,
  type RelationshipChildView,
  type ReviewFeedbackCommentView,
} from '../../../../shared/ipc';
import { parseIpcError } from '../../wizard/ipcError';
import {
  AttentionDetail,
  attentionActionNotice,
  attentionErrorMessage,
  runAttentionSubmit,
  type AttentionDrafts,
} from '../AttentionInbox';
import { useAttentionDraftSaves } from '../useAttentionDraftSaves';
import { NeedUserInputModal, type AttentionGate } from '../NeedUserInputModal';
import {
  QuestionComposer,
  QuestionConversationTurn,
  questionAnswersRequest,
} from '../QuestionTurn';
import { CurrentRunInspection, type RunMetrics } from '../CurrentRunInspection';
import { ReviewSurface } from '../ReviewSurface';
import { ImpactPreviewList } from '../ImpactPreviewList';
import { InspectorContent } from '../CockpitInspector';
import { InspectorDrawer } from '../InspectorDrawer';
import { classifyHold, railSegments, railTrio } from '../phaseRail';
import { PhaseRail } from '../PhaseRailRow';
import { featureBranch, showsRun } from '../featureView';
import {
  custodyStations,
  passActions,
  passKindLabel,
  passState,
  COMMENT_TYPE_LABEL,
  commentKey,
  type PassAction,
} from './refactorPassModel';

type ChildState =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'loaded'; child: FeatureSnapshot };

export interface RefactorPassController {
  view: RelationshipChildView | undefined;
  childState: ChildState;
  child: FeatureSnapshot | null;
  /** Catalogue-enabled verbs only — the action bar renders exactly these. */
  actions: PassAction[];
  discardAction: FeatureActionView | undefined;
  busy: boolean;
  notice: string | null;
  discardOpen: boolean;
  dispatch(action: PassAction['id']): Promise<void>;
  /** Launch-time auto-start: dispatch start once this child becomes startable. */
  armAutoStart(childId: string): void;
  openDiscard(): void;
  closeDiscard(): void;
  discard(): Promise<void>;
  reload(): void;
}

/**
 * Owns the active refactor child: the authoritative child snapshot, the
 * contextual verb set, and the discard flow. Lifted out of the workspace so
 * the cockpit action bar can carry the pass verbs exactly like any feature
 * tab. No-ops when the parent has no active child.
 */
export function useRefactorPass(
  parent: FeatureSnapshot | null,
  onChanged: () => void,
  active = true,
): RefactorPassController {
  const view = parent?.activeChild;
  const parentId = parent?.id;
  const childId = view?.id;
  const [childState, setChildState] = useState<ChildState>({ phase: 'loading' });
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [discardOpen, setDiscardOpen] = useState(false);
  const loadRequestRef = useRef(0);
  const childIdRef = useRef<string | undefined>(undefined);

  const loadChild = useCallback(() => {
    if (childId === undefined) return;
    const request = ++loadRequestRef.current;
    window.agentico
      .getFeature(childId)
      .then((snapshot) => {
        if (request !== loadRequestRef.current) return;
        setChildState({ phase: 'loaded', child: snapshot });
      })
      .catch((err: unknown) => {
        if (request !== loadRequestRef.current) return;
        setChildState({ phase: 'error', message: parseIpcError(err).message });
      });
  }, [childId]);

  useEffect(() => {
    if (childIdRef.current !== childId) {
      childIdRef.current = childId;
      setChildState({ phase: 'loading' });
    }
    if (!active) {
      loadRequestRef.current += 1;
      return;
    }
    loadChild();
    return () => {
      loadRequestRef.current += 1;
    };
  }, [active, childId, loadChild]);

  useEffect(() => {
    if (!active || childId === undefined) return;
    return window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') return;
      if (
        event.kind === 'resync' ||
        event.parentId === parentId ||
        event.childId === childId ||
        event.featureId === childId ||
        event.resourceId === childId
      ) {
        loadChild();
      }
    });
  }, [active, loadChild, childId, parentId]);

  const refreshBoth = useCallback(() => {
    loadChild();
    onChanged();
  }, [loadChild, onChanged]);

  const child = childState.phase === 'loaded' ? childState.child : null;
  const discardAction = child?.actions.find((action) => action.id === 'discard');

  const dispatch = useCallback(
    async (action: PassAction['id']) => {
      if (child === null || busy) return;
      setBusy(true);
      setNotice(null);
      try {
        await window.agentico.dispatchFeatureAction({ featureId: child.id, action });
        refreshBoth();
      } catch (err) {
        setNotice(parseIpcError(err).message);
      } finally {
        setBusy(false);
      }
    },
    [busy, child, refreshBoth],
  );

  // The server refuses start while child setup is queued or running, so a
  // launch-time auto-start arms here and fires on the snapshot that first
  // enables the verb (setup completion arrives through the event stream).
  const [autoStartChildId, setAutoStartChildId] = useState<string | null>(null);
  const armAutoStart = useCallback((id: string) => setAutoStartChildId(id), []);
  useEffect(() => {
    if (
      !active ||
      autoStartChildId === null ||
      busy ||
      child === null ||
      child.id !== autoStartChildId
    )
      return;
    if (child.closeOutcome !== undefined && child.closeOutcome !== '') {
      setAutoStartChildId(null);
      return;
    }
    if (!child.actions.some((action) => action.id === 'start' && action.enabled)) return;
    setAutoStartChildId(null);
    void dispatch('start');
  }, [active, autoStartChildId, busy, child, dispatch]);

  const discard = useCallback(async () => {
    if (child === null || discardAction?.impactPreview === undefined || busy) return;
    setBusy(true);
    setNotice(null);
    try {
      const result = await window.agentico.discardRefactorChild({ childId: child.id });
      setNotice(result.result);
      if (result.status === 'completed' || result.status === 'draining') setDiscardOpen(false);
      refreshBoth();
    } catch (err) {
      setNotice(parseIpcError(err).message);
    } finally {
      setBusy(false);
    }
  }, [busy, child, discardAction, refreshBoth]);

  return {
    view,
    childState,
    child,
    actions: child === null ? [] : passActions(child),
    discardAction,
    busy,
    notice,
    discardOpen,
    dispatch,
    armAutoStart,
    openDiscard: useCallback(() => setDiscardOpen(true), []),
    closeDiscard: useCallback(() => setDiscardOpen(false), []),
    discard,
    reload: refreshBoth,
  };
}

export interface RefactorPassWorkspaceProps {
  parent: FeatureSnapshot;
  pass: RefactorPassController;
  /** Whether retained live pass effects may fetch or subscribe. */
  active?: boolean;
  /**
   * Launch receipt from a review-feedback pass (e.g. "2 changed, 1 omitted,
   * 3 deferred since review"); informational only, never blocks the pass.
   */
  launchReceipt?: string | null;
  /** Inbox jump into an attention item; reopens a dismissed gate. */
  attentionPreviewRequest?: { requestId: number; attentionId?: string } | null;
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  /** Whether the shell is at the narrow (drawer) width; mirrors the cockpit. */
  isNarrow?: boolean;
  /**
   * Whether the inspector is shown: the trailing split-view pane when wide,
   * the slide-over drawer when narrow. Owned by the cockpit so the toolbar's
   * inspector toggle controls this workspace exactly like any feature tab.
   */
  inspectorOpen?: boolean;
  /** Closes the inspector (drawer dismissal); owned by the cockpit. */
  onCloseInspector?(): void;
}

/**
 * The parent tab while a refactor pass (child feature) is active. The pass is
 * the thing that is running, so the tab reads like any feature tab — stage on
 * the left, inspector ladder on the right — with the custody strip above for
 * the relationship, and the locked parent reduced to a quiet inspector card.
 * Children never become top-level tabs.
 */
export function RefactorPassWorkspace({
  parent,
  pass,
  active = true,
  launchReceipt = null,
  attentionPreviewRequest = null,
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
  isNarrow = false,
  inspectorOpen = false,
  onCloseInspector,
}: RefactorPassWorkspaceProps): React.ReactElement | null {
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [dismissedGateId, setDismissedGateId] = useState<string | undefined>();
  const [runMetrics, setRunMetrics] = useState<RunMetrics | null>(null);
  const [attentionNotice, setAttentionNotice] = useState<string | null>(null);
  // Stage-bar slots the live surface's view toggle, refresh, and expand
  // controls portal into, exactly like the cockpit's stage bar.
  const [liveControlsHost, setLiveControlsHost] = useState<HTMLDivElement | null>(null);
  const [liveExpandHost, setLiveExpandHost] = useState<HTMLDivElement | null>(null);

  const saveAttentionDraft = useAttentionDraftSaves({
    notify: (result, options) => setAttentionNotice(attentionActionNotice(result, options)),
    notifyError: (error) => setAttentionNotice(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refreshAttention();
      pass.reload();
    },
  });

  useEffect(() => {
    if (attentionPreviewRequest?.attentionId !== undefined) {
      setDismissedGateId(undefined);
    }
  }, [attentionPreviewRequest]);

  const view = pass.view;
  if (view === undefined) return null;
  const { child, childState } = pass;
  const state = child === null ? null : passState(child);
  const stations = custodyStations(parent, child, view);
  const stopEnabled =
    child?.actions.some((action) => action.id === 'pause-stop' && action.enabled) === true;

  const passAttentionItems = attentionItems.filter(
    (item) => item.kind !== 'recovery' && item.kind !== 'review' && item.featureId === view.id,
  );
  const gate = passAttentionItems.find((item): item is AttentionGate => item.kind === 'gate');
  const activeGate = gate?.id === dismissedGateId ? undefined : gate;
  const inlineAttention = passAttentionItems.find((item) => item.kind !== 'gate');
  const questionsAttention = inlineAttention?.kind === 'questions' ? inlineAttention : undefined;
  const pendingQuestion = questionsAttention?.questions[0];
  const suppressQuestion =
    pendingQuestion === undefined
      ? undefined
      : {
          prompt: pendingQuestion.key,
          optionLabels: pendingQuestion.options.map((option) => option.label),
        };
  const showsLiveSurface =
    child !== null && !isPendingReviewStatus(child.status) && showsRun(child);

  // The pass's own phase rail, computed exactly like the cockpit's: the hold
  // looks at every open item this child owns (including review items — the
  // question/gate list above filters those out), the segments come from the
  // child's own pipeline, and the trio reads the live run metrics.
  const railOpenAttentionItems = attentionItems.filter(
    (item) => attentionOwnerFeatureId(item) === view.id,
  );
  const railHold = child === null ? null : classifyHold(child.status, railOpenAttentionItems);
  const railSegmentsList = child === null ? [] : railSegments(child, railHold);
  const railTrioEntries = railTrio({
    totalSeconds: runMetrics?.totalSeconds,
    totalUsd: runMetrics?.totalUsd,
    contextPercentage: runMetrics?.contextPercentage,
    hold: railHold,
  });

  const submitAttention = async (
    item: AttentionItem,
    action: Parameters<typeof runAttentionSubmit>[0],
    options?: Parameters<typeof runAttentionSubmit>[2],
  ) => {
    if (attentionBusy !== null) return;
    setAttentionBusy(item.id);
    try {
      const { notice: submitted } = await runAttentionSubmit(
        action,
        async () => {
          const latest = await refreshAttention();
          pass.reload();
          return latest;
        },
        options,
      );
      setAttentionNotice(submitted);
    } catch (error) {
      setAttentionNotice(attentionErrorMessage(error));
    } finally {
      setAttentionBusy(null);
    }
  };

  const notice = pass.notice ?? attentionNotice;

  const submitQuestionAnswers = () => {
    if (questionsAttention === undefined) return;
    void submitAttention(questionsAttention, () =>
      window.agentico.answerQuestions(questionAnswersRequest(questionsAttention, attentionDrafts)),
    );
  };

  // One inspector body for both presentations: the trailing split-view pane
  // when wide, the slide-over drawer when narrow — mirroring the cockpit.
  const inspectorContent = (
    <>
      {child !== null ? (
        <InspectorContent
          snapshot={child}
          branch={featureBranch(child)}
          stale={false}
          runMetrics={runMetrics}
          onOpenPullRequest={(url) => {
            void window.agentico.openExternal({ url });
          }}
        />
      ) : (
        <header className="cockpit__header">
          <div className="cockpit__identity">
            <h2 className="cockpit__title">{view.name}</h2>
            <p className="refactor-pass__state">{view.displayState}</p>
          </div>
        </header>
      )}
      <section className="refactor-pass__parent-card" aria-label="Parent feature">
        <p className="feature-facts__eyebrow">Parent</p>
        <strong>{parent.name}</strong>
        <p>
          Locked while the pass runs. Review settings stay editable through Edit configuration… —
          changes apply to both records, and each keeps its own pipeline.
        </p>
      </section>
    </>
  );

  return (
    <section
      className="refactor-pass"
      aria-label={passKindLabel(view.kind)}
      data-state={state?.id ?? 'loading'}
      data-kind={view.kind}
    >
      {child !== null ? (
        <PhaseRail
          segments={railSegmentsList}
          trio={railTrioEntries}
          hold={railHold}
          tone={child.status === 'Failed' ? 'error' : 'progress'}
          label="Pass phases"
        />
      ) : null}

      <ol className="refactor-pass__custody" aria-label="Custody of the work">
        {stations.map((station) => (
          <li
            key={station.id}
            className="refactor-pass__station"
            data-station={station.id}
            data-state={station.state}
            aria-current={station.state === 'live' ? 'step' : undefined}
          >
            <p className="refactor-pass__station-eyebrow">{station.eyebrow}</p>
            <strong className="refactor-pass__station-title">{station.title}</strong>
            <span className="refactor-pass__station-detail">
              <span className="refactor-pass__station-dot" aria-hidden="true" />
              {station.detail}
            </span>
          </li>
        ))}
      </ol>

      {launchReceipt !== null ? (
        <p className="refactor-pass__state" role="status" data-tone="quiet">
          {launchReceipt}
        </p>
      ) : null}

      {child?.reviewFeedback !== undefined && child.reviewFeedback.length > 0 ? (
        <SelectedCommentSummary comments={child.reviewFeedback} />
      ) : null}

      {isNarrow && inspectorOpen ? (
        <InspectorDrawer title="Pass inspector" onClose={() => onCloseInspector?.()}>
          {inspectorContent}
        </InspectorDrawer>
      ) : null}

      <div
        className={
          !isNarrow && inspectorOpen
            ? 'cockpit__content cockpit__content--inspector-open'
            : 'cockpit__content'
        }
      >
        <main className="cockpit__stage">
          {childState.phase === 'loading' ? (
            <p className="refactor-pass__state" role="status">
              Loading the pass from the runtime…
            </p>
          ) : childState.phase === 'error' ? (
            <p className="refactor-pass__state" role="alert">
              The pass could not be loaded — {childState.message}{' '}
              <button type="button" className="refactor-pass__retry-load" onClick={pass.reload}>
                Try again
              </button>
            </p>
          ) : state !== null && state.id !== 'working' ? (
            <>
              <p className="refactor-pass__state" role="status" data-tone={state.tone}>
                {state.sentence}
                {gate !== undefined && activeGate === undefined ? (
                  <>
                    {' '}
                    <button
                      type="button"
                      className="refactor-pass__answer-now"
                      onClick={() => setDismissedGateId(undefined)}
                    >
                      Answer now
                    </button>
                  </>
                ) : null}
              </p>
              {state.problems !== undefined && state.problems.length > 0 ? (
                <ul className="refactor-pass__warnings" aria-label="Integration diagnostics">
                  {state.problems.map((problem) => (
                    <li key={problem}>{problem}</li>
                  ))}
                </ul>
              ) : null}
            </>
          ) : null}

          {notice !== null ? (
            <p className="refactor-pass__notice" role="status">
              {notice}
            </p>
          ) : null}

          {view.attention.map((item) => (
            <p
              key={`${item.code}:${item.repo ?? ''}`}
              className="refactor-pass__alert"
              role="alert"
            >
              {item.repo === undefined ? '' : `${item.repo}: `}
              {item.message}
            </p>
          ))}

          {/* The question joins the live conversation instead of stacking a
           * form above it; standalone only when there is no live surface. */}
          {inlineAttention !== undefined && !showsLiveSurface && childState.phase !== 'loading' ? (
            <section className="live-preview__attention" aria-label="Agent request">
              <AttentionDetail
                key={`${inlineAttention.kind}:${inlineAttention.id}`}
                item={inlineAttention}
                busy={attentionBusy === inlineAttention.id}
                drafts={attentionDrafts}
                setDrafts={setAttentionDrafts}
                saveDraft={(action, options) =>
                  saveAttentionDraft(inlineAttention.id, action, options)
                }
                submit={(action, options) => void submitAttention(inlineAttention, action, options)}
              />
            </section>
          ) : null}

          {/* The live surface's Conversation/Signal-trace toggle, refresh, and
           * expand ride a trailing-only stage bar, exactly like the cockpit's
           * stage-bar hosts (the pass has no surface switcher or run popup,
           * so the leading zone stays empty). */}
          {showsLiveSurface ? (
            <div className="cockpit__stage-bar cockpit__stage-bar--aftercare">
              <div className="cockpit__stage-bar-trailing">
                <div className="cockpit__stage-bar-controls" ref={setLiveControlsHost} />
                <div className="cockpit__stage-bar-expand" ref={setLiveExpandHost} />
              </div>
            </div>
          ) : null}

          {child !== null && isPendingReviewStatus(child.status) ? (
            <div className="cockpit__surface cockpit__surface--document">
              <ReviewSurface
                featureId={child.id}
                onResolved={async () => {
                  pass.reload();
                }}
              />
            </div>
          ) : child !== null && showsRun(child) ? (
            <div className="cockpit__surface cockpit__surface--live">
              <CurrentRunInspection
                featureId={child.id}
                runNumber={child.activeRun}
                active={active}
                currentPhase={child.currentPhase}
                currentRoadmapPhase={child.currentRoadmapPhase}
                currentIteration={child.currentIteration}
                phaseStatus={child.phaseStatus}
                reviewGate={child.reviewGate}
                verificationItems={child.verificationItems}
                waitReason={child.waitReason}
                shouldStream={stopEnabled}
                expandHost={liveExpandHost}
                controlsHost={liveControlsHost}
                onRunMetrics={setRunMetrics}
                suppressQuestion={suppressQuestion}
                attentionTurn={
                  questionsAttention === undefined ? undefined : (
                    <QuestionConversationTurn
                      key={`${questionsAttention.kind}:${questionsAttention.id}`}
                      item={questionsAttention}
                      busy={attentionBusy === questionsAttention.id}
                      drafts={attentionDrafts}
                      setDrafts={setAttentionDrafts}
                      onSubmit={submitQuestionAnswers}
                    />
                  )
                }
                attentionFooter={
                  inlineAttention === undefined ? undefined : questionsAttention !== undefined ? (
                    <QuestionComposer
                      item={questionsAttention}
                      busy={attentionBusy === questionsAttention.id}
                      drafts={attentionDrafts}
                      setDrafts={setAttentionDrafts}
                      onSubmit={submitQuestionAnswers}
                    />
                  ) : (
                    <AttentionDetail
                      key={`${inlineAttention.kind}:${inlineAttention.id}`}
                      item={inlineAttention}
                      busy={attentionBusy === inlineAttention.id}
                      drafts={attentionDrafts}
                      setDrafts={setAttentionDrafts}
                      saveDraft={(action, options) =>
                        saveAttentionDraft(inlineAttention.id, action, options)
                      }
                      submit={(action, options) =>
                        void submitAttention(inlineAttention, action, options)
                      }
                    />
                  )
                }
              />
            </div>
          ) : state?.id === 'ready' ? (
            <div className="cockpit__empty-state" role="status">
              <span aria-hidden="true">●</span> Ready to start
              <p>Start runs the {child?.currentPhase ?? 'first'} phase for this pass.</p>
            </div>
          ) : null}

          {view.cleanupWarnings.length > 0 ? (
            <ul className="refactor-pass__warnings">
              {view.cleanupWarnings.map((item) => (
                <li key={`${item.repo ?? ''}:${item.message}`}>{item.message}</li>
              ))}
            </ul>
          ) : null}
        </main>

        {!isNarrow && inspectorOpen ? (
          <aside className="cockpit__inspector" aria-label="Pass inspector">
            {inspectorContent}
          </aside>
        ) : null}
      </div>

      {activeGate !== undefined ? (
        <NeedUserInputModal
          item={activeGate}
          busy={attentionBusy === activeGate.id}
          drafts={attentionDrafts}
          setDrafts={setAttentionDrafts}
          phase={child?.currentPhase}
          onAnswerLater={() => setDismissedGateId(activeGate.id)}
          onResolved={async () => {
            setDismissedGateId(activeGate.id);
            await refreshAttention();
            pass.reload();
          }}
        />
      ) : null}

      {pass.discardOpen && pass.discardAction !== undefined ? (
        <DiscardPassDialog
          passName={view.name}
          action={pass.discardAction}
          busy={pass.busy}
          onClose={pass.closeDiscard}
          onConfirm={() => void pass.discard()}
        />
      ) : null}
    </section>
  );
}

function DiscardPassDialog({
  passName,
  action,
  busy,
  onClose,
  onConfirm,
}: {
  passName: string;
  action: FeatureActionView;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
}): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialogRef.current?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [busy, onClose]);

  const preview = action.impactPreview;
  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="discard-pass-title"
        className="impact-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="discard-pass-title">Discard {passName}?</h3>
        {preview === undefined ? (
          <p role="alert">Impact projection is unavailable. Refresh before continuing.</p>
        ) : (
          <ImpactPreviewList preview={preview} />
        )}
        <p className="impact-dialog__note">This cannot be undone.</p>
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={busy} autoFocus>
            Keep the pass
          </button>
          <button
            type="button"
            className="cockpit__delete-button"
            disabled={preview === undefined || busy}
            onClick={onConfirm}
          >
            {busy ? 'Discarding…' : 'Discard pass'}
          </button>
        </div>
      </div>
    </div>
  );
}

function SelectedCommentSummary({
  comments,
}: {
  comments: ReviewFeedbackCommentView[];
}): React.ReactElement {
  const repos = new Map<string, ReviewFeedbackCommentView[]>();
  for (const comment of comments) {
    const group = repos.get(comment.repo);
    if (group === undefined) {
      repos.set(comment.repo, [comment]);
    } else {
      group.push(comment);
    }
  }
  const repoCount = repos.size;
  const commentCount = comments.length;

  return (
    <details className="refactor-pass__comments">
      <summary className="refactor-pass__comments-rollup">
        {commentCount} {commentCount === 1 ? 'comment' : 'comments'} across {repoCount}{' '}
        {repoCount === 1 ? 'repo' : 'repos'}
      </summary>
      <div className="refactor-pass__comments-groups">
        {Array.from(repos.entries()).map(([repo, group]) => (
          <section key={repo} className="refactor-pass__comments-group">
            <h4 className="refactor-pass__comments-group-title">{repo}</h4>
            <ul className="refactor-pass__comments-list">
              {group.map((comment) => (
                <li key={commentKey(comment)} className="refactor-pass__comment-row">
                  <span className="refactor-pass__comment-author">
                    {comment.author ?? 'unknown'}
                  </span>
                  <span className="refactor-pass__comment-type">
                    {COMMENT_TYPE_LABEL[comment.type]}
                  </span>
                  {comment.path !== undefined ? (
                    <span className="refactor-pass__comment-location">
                      {comment.path}
                      {comment.line !== undefined ? `:${comment.line}` : ''}
                    </span>
                  ) : null}
                  {comment.body !== undefined ? (
                    <p className="refactor-pass__comment-body">{comment.body}</p>
                  ) : null}
                </li>
              ))}
            </ul>
          </section>
        ))}
      </div>
    </details>
  );
}
