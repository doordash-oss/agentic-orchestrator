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

import { useEffect, useState } from 'react';
import { DEFAULT_RUNTIME_ID } from '../../../shared/ipc';
import type {
  CanonicalError,
  ReviewDraftKey,
  ReviewSession,
  ReviewValidation,
} from '../../../shared/ipc';
import { MonacoBuffer, MonacoDiff, useResolvedTheme } from '../components/monaco';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError } from '../components/FieldError';
import { useConnectionState, useMediaQuery } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import { renderSanitizedMarkdown } from './sanitizedMarkdown';

type View = 'edit' | 'preview' | 'split';

/**
 * The single notice slot's two branches: plain progress/success text, or a
 * failure rendered as a compact ErrorSurface fed by a canonical error (with
 * an optional caption naming which operation failed). One slot, one visible
 * message at a time.
 */
interface NoticeFailure {
  error: CanonicalError;
  caption?: string;
}

interface ReconcileState {
  current: ReviewSession;
  localText: string;
  baseText: string | null;
  localKey: ReviewDraftKey;
}

function MarkdownPreview({ text }: { text: string }) {
  const html = renderSanitizedMarkdown(text);
  return (
    <div
      className="review-surface__preview"
      aria-label="Sanitized Markdown preview"
      dangerouslySetInnerHTML={{ __html: html }}
    />
  );
}

function reviewKey(
  runtimeId: string,
  session: ReviewSession,
  baseDraftRevision = session.draftRevision,
): ReviewDraftKey {
  return { runtimeId, featureId: session.featureId, reviewId: session.reviewId, baseDraftRevision };
}

export function ReviewSurface({
  featureId,
  onResolved,
}: {
  featureId: string;
  onResolved(): Promise<void>;
}) {
  const [session, setSession] = useState<ReviewSession | null>(null);
  const [text, setText] = useState('');
  const [baseText, setBaseText] = useState('');
  const [runtimeId, setRuntimeId] = useState(DEFAULT_RUNTIME_ID);
  const [view, setView] = useState<View>('edit');
  const [validation, setValidation] = useState<ReviewValidation | null>(null);
  const [notice, setNotice] = useState('Loading review…');
  const [noticeError, setNoticeError] = useState<NoticeFailure | null>(null);
  const [busy, setBusy] = useState(false);
  const [recovered, setRecovered] = useState(false);
  const [recoveredKey, setRecoveredKey] = useState<ReviewDraftKey | null>(null);
  const [reconcile, setReconcile] = useState<ReconcileState | null>(null);
  const isNarrow = useMediaQuery('(max-width: 900px)');
  const connection = useConnectionState();
  const theme = useResolvedTheme();
  const [editorKey, setEditorKey] = useState(0);

  const setStatus = (text: string): void => {
    setNoticeError(null);
    setNotice(text);
  };
  const setFailure = (error: CanonicalError, caption?: string): void => {
    setNotice('');
    setNoticeError(caption === undefined ? { error } : { error, caption });
  };
  const noticeSlot =
    noticeError !== null ? (
      <ErrorSurface
        error={noticeError.error}
        variant="compact"
        {...(noticeError.caption === undefined ? {} : { caption: noticeError.caption })}
      />
    ) : (
      <p role="status">{notice}</p>
    );

  useEffect(() => {
    if (isNarrow && view === 'split') setView('edit');
  }, [isNarrow, view]);

  useEffect(() => {
    let alive = true;
    void window.agentico
      .openReview({ featureId })
      .then(async (next) => {
        // Local recovery drafts are scoped to the connected server's identity,
        // never the global runtime selection: drafts for server A are
        // unreachable while connected to B. The mount-time key is stable —
        // the connection shell remounts this surface on every switch.
        const nextRuntime = connection.serverKey ?? DEFAULT_RUNTIME_ID;
        const local = await window.agentico.loadLocalReviewDraft({
          runtimeId: nextRuntime,
          featureId,
          reviewId: next.reviewId,
        });
        if (!alive) return;
        setRuntimeId(nextRuntime);
        setSession(next);
        setBaseText(next.text);
        if (local === null) {
          setText(next.text);
          setStatus('Server draft loaded.');
          return;
        }
        const recoveredDraftKey = {
          runtimeId: local.runtimeId,
          featureId: local.featureId,
          reviewId: local.reviewId,
          baseDraftRevision: local.baseDraftRevision,
        };
        if (local.baseDraftRevision !== next.draftRevision) {
          setText(local.text);
          setRecovered(true);
          setRecoveredKey(recoveredDraftKey);
          setStatus('Recovered draft needs reconciliation with a newer server draft.');
          setReconcile({
            current: next,
            localText: local.text,
            baseText: null,
            localKey: recoveredDraftKey,
          });
          return;
        }
        setText(local.text);
        setRecovered(true);
        setRecoveredKey(recoveredDraftKey);
        setStatus('Recovered unsaved draft loaded.');
      })
      .catch((error: unknown) => {
        if (alive) setFailure(parseIpcError(error));
      });
    return () => {
      alive = false;
    };
  }, [featureId, connection.serverKey]);

  useEffect(() => {
    if (session === null) return;
    let active = true;
    const timer = window.setTimeout(() => {
      void window.agentico
        .validateReview({ featureId, reviewId: session.reviewId, text })
        .then((result) => {
          if (active) setValidation(result);
        })
        .catch(() => {
          if (active) setValidation(null);
        });
    }, 250);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [featureId, session, text]);

  const dirty = session !== null && text !== baseText;
  useEffect(() => {
    if (session === null || !dirty || reconcile !== null) return;
    const key = reviewKey(runtimeId, session);
    const timer = window.setTimeout(() => {
      void window.agentico.saveLocalReviewDraft({ ...key, text }).catch((error: unknown) =>
        // A local recovery-copy failure: a desktop-local canonical, with the
        // old lead as the caption naming what was lost.
        setFailure(parseIpcError(error), 'Local recovery copy could not be saved.'),
      );
    }, 350);
    return () => window.clearTimeout(timer);
  }, [dirty, reconcile, runtimeId, session, text]);

  const startReconcile = async (
    localText: string,
    localKey: ReviewDraftKey,
    baseOverride?: string | null,
  ) => {
    if (session === null) return;
    setBusy(true);
    setStatus('Loading the current server draft…');
    try {
      const current = await window.agentico.readReview({ featureId });
      setReconcile({
        current,
        localText,
        baseText: baseOverride !== undefined ? baseOverride : baseText,
        localKey,
      });
      setStatus('Choose how to reconcile the two drafts. Nothing has been written.');
    } catch (error) {
      setFailure(parseIpcError(error), 'Could not load the current draft');
    } finally {
      setBusy(false);
    }
  };

  const discardLocal = async (key: ReviewDraftKey) => {
    await window.agentico.discardLocalReviewDraft(key);
    setRecovered(false);
    setRecoveredKey(null);
  };

  const save = async () => {
    if (session === null || busy) return;
    setBusy(true);
    setStatus('Saving draft…');
    try {
      const result = await window.agentico.saveReview({
        featureId,
        reviewId: session.reviewId,
        baseRevision: session.draftRevision,
        text,
      });
      if (result.type === 'conflict') {
        await startReconcile(text, reviewKey(runtimeId, session));
        return;
      }
      setSession(result.session);
      setBaseText(result.session.text);
      setText(result.session.text);
      setEditorKey((k) => k + 1);
      setStatus('Saved to the server.');
      try {
        await discardLocal(reviewKey(runtimeId, session));
      } catch (error) {
        setFailure(
          parseIpcError(error),
          'Saved, but the local recovery copy could not be removed.',
        );
      }
    } catch (error) {
      setFailure(parseIpcError(error), 'Save failed');
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    const shortcut = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 's' && dirty) {
        event.preventDefault();
        event.stopPropagation();
        void save();
      }
    };
    window.addEventListener('keydown', shortcut, true);
    return () => window.removeEventListener('keydown', shortcut, true);
  });

  if (session === null)
    return (
      <section className="review-surface" aria-label="Review editor">
        {noticeSlot}
      </section>
    );
  const proceedBlocked =
    validation === null ||
    (validation.applicable && (!validation.valid || validation.revision !== session.draftRevision));
  const proceedDisabledReason =
    validation !== null && validation.applicable && !validation.valid
      ? 'Fix validation findings before approving.'
      : dirty
        ? 'Save the draft before approving.'
        : validation === null ||
            (validation.applicable && validation.revision !== session.draftRevision)
          ? 'Validating the draft…'
          : null;
  const iterateDisabledReason = !session.canIterate
    ? 'This review does not accept iterate feedback.'
    : dirty
      ? 'Save the draft before iterating.'
      : null;
  const decide = async (decision: 'proceed' | 'iterate') => {
    if (busy) return;
    setBusy(true);
    setStatus(decision === 'proceed' ? 'Approving review…' : 'Sending back for iteration…');
    try {
      const result = await window.agentico.decideReview({
        featureId,
        reviewId: session.reviewId,
        baseRevision: session.draftRevision,
        decision,
      });
      if (result.type === 'conflict') {
        await startReconcile(text, reviewKey(runtimeId, session));
        return;
      }
      setStatus(result.result);
      try {
        await discardLocal(reviewKey(runtimeId, session));
      } catch {
        // Decision succeeded; a leftover recovery copy self-heals on reopen.
      }
      await onResolved();
    } catch (error) {
      setFailure(parseIpcError(error), 'Decision failed');
    } finally {
      setBusy(false);
    }
  };

  if (reconcile !== null) {
    const takeServer = async () => {
      try {
        await window.agentico.discardLocalReviewDraft(reconcile.localKey);
      } catch (error) {
        setFailure(parseIpcError(error), 'Could not discard the local draft');
        return;
      }
      setRecovered(false);
      setRecoveredKey(null);
      setSession(reconcile.current);
      setText(reconcile.current.text);
      setBaseText(reconcile.current.text);
      setReconcile(null);
      setStatus('Using the current server draft.');
    };
    const continueEditing = async () => {
      try {
        await window.agentico.discardLocalReviewDraft(reconcile.localKey);
      } catch (error) {
        setFailure(parseIpcError(error), 'Could not discard the old draft');
        return;
      }
      setRecovered(true);
      setRecoveredKey(reviewKey(runtimeId, reconcile.current));
      setSession(reconcile.current);
      setBaseText(reconcile.current.text);
      setText(reconcile.localText);
      setReconcile(null);
      setStatus('Your local text is ready to edit against the current server revision.');
    };
    const replaceServer = async () => {
      setBusy(true);
      try {
        const result = await window.agentico.saveReview({
          featureId,
          reviewId: reconcile.current.reviewId,
          baseRevision: reconcile.current.draftRevision,
          text: reconcile.localText,
        });
        if (result.type === 'conflict') {
          await startReconcile(reconcile.localText, reconcile.localKey);
          return;
        }
        setSession(result.session);
        setBaseText(result.session.text);
        setText(result.session.text);
        setReconcile(null);
        setStatus('Your local draft replaced the server draft.');
        try {
          await discardLocal(reconcile.localKey);
        } catch (error) {
          setFailure(
            parseIpcError(error),
            'Replaced the server draft, but the local recovery copy could not be removed.',
          );
        }
      } catch (error) {
        setFailure(parseIpcError(error), 'Could not replace the server draft');
      } finally {
        setBusy(false);
      }
    };
    return (
      <section
        className="review-surface review-surface--reconcile"
        aria-label="Reconcile stale review draft"
      >
        <header className="review-surface__header">
          <div>
            <p className="review-surface__eyebrow">Stale revision · no automatic merge</p>
            <h3>Reconcile drafts</h3>
          </div>
        </header>
        <p className="review-surface__reconcile-copy">
          Left is the current server draft; right is your local draft. Choose explicitly before
          anything is written.
        </p>
        <MonacoDiff
          currentText={reconcile.current.text}
          localText={reconcile.localText}
          language="markdown"
          theme={theme}
          ariaLabel="Local draft compared with current server draft"
          className="review-surface__diff"
        />
        {reconcile.baseText !== null ? (
          <details className="review-surface__base">
            <summary>Show the base draft used when editing began</summary>
            <pre>{reconcile.baseText}</pre>
          </details>
        ) : (
          <p className="review-surface__base-note">
            The original base draft is unavailable for recovered drafts — only the current server
            draft and your local text are shown.
          </p>
        )}
        <footer className="review-surface__footer">
          {noticeSlot}
          <div>
            <button
              type="button"
              onClick={() => {
                if (
                  reconcile.baseText === null &&
                  reconcile.localKey.baseDraftRevision !== reconcile.current.draftRevision
                ) {
                  void window.agentico
                    .discardLocalReviewDraft(reconcile.localKey)
                    .catch((error: unknown) =>
                      setFailure(
                        parseIpcError(error),
                        'Could not remove the orphaned local draft — it may reappear on reopen.',
                      ),
                    );
                  setRecoveredKey(reviewKey(runtimeId, reconcile.current));
                }
                setReconcile(null);
              }}
              disabled={busy}
            >
              Cancel
            </button>
            <button type="button" onClick={() => void takeServer()} disabled={busy}>
              Take server draft
            </button>
            <button type="button" onClick={() => void continueEditing()} disabled={busy}>
              Keep editing mine
            </button>
            <button
              type="button"
              className="review-surface__proceed"
              onClick={() => void replaceServer()}
              disabled={busy}
            >
              Replace server with mine
            </button>
          </div>
        </footer>
      </section>
    );
  }
  return (
    <section className="review-surface" aria-label="Review editor">
      <header className="review-surface__header">
        <div>
          <p className="review-surface__eyebrow">
            {session.reviewMode} · {session.targetPhase}
          </p>
          <h3>{session.artifactId}</h3>
        </div>
        <p className="review-surface__state" data-dirty={dirty}>
          {recovered && dirty
            ? 'Recovered unsaved draft'
            : dirty
              ? 'Unsaved changes'
              : 'Saved draft'}
        </p>
      </header>
      <div className="review-surface__toolbar" role="group" aria-label="Review view">
        <button type="button" onClick={() => setView('edit')} aria-pressed={view === 'edit'}>
          Edit
        </button>
        <button type="button" onClick={() => setView('preview')} aria-pressed={view === 'preview'}>
          Preview
        </button>
        {!isNarrow ? (
          <button type="button" onClick={() => setView('split')} aria-pressed={view === 'split'}>
            Split
          </button>
        ) : null}
        <button type="button" onClick={() => void save()} disabled={!dirty || busy}>
          Save draft
        </button>
        {recovered ? (
          <>
            <button
              type="button"
              onClick={() =>
                void startReconcile(text, recoveredKey ?? reviewKey(runtimeId, session), null)
              }
              disabled={busy}
            >
              Compare to server
            </button>
            <button
              type="button"
              onClick={() =>
                void discardLocal(recoveredKey ?? reviewKey(runtimeId, session)).then(() => {
                  setText(baseText);
                  setEditorKey((k) => k + 1);
                  setStatus('Recovered local draft discarded.');
                })
              }
            >
              Discard recovered draft
            </button>
          </>
        ) : null}
      </div>
      {view === 'edit' ? (
        <MonacoBuffer
          key={editorKey}
          defaultValue={text}
          language="markdown"
          theme={theme}
          ariaLabel="Review draft"
          className="review-surface__editor"
          onChange={setText}
        />
      ) : view === 'preview' ? (
        <MarkdownPreview text={text} />
      ) : (
        <div className="review-surface__split">
          <MonacoBuffer
            key={editorKey}
            defaultValue={text}
            language="markdown"
            theme={theme}
            ariaLabel="Review draft"
            className="review-surface__editor"
            onChange={setText}
          />
          <MarkdownPreview text={text} />
        </div>
      )}
      {validation?.applicable && !validation.valid ? (
        // Per-finding FieldError elements; the editor is the finding's host
        // surface (Monaco owns focus, so no live-region role here).
        <ul aria-label="Validation findings">
          {validation.findings.map((finding) => (
            <li key={finding.code}>
              <FieldError id={`review-finding-${finding.code}`} message={finding.message} />
            </li>
          ))}
        </ul>
      ) : null}
      <footer className="review-surface__footer">
        {noticeSlot}
        <div className="review-surface__decisions">
          <div className="review-surface__decision">
            <button
              type="button"
              onClick={() => void decide('iterate')}
              disabled={busy || !session.canIterate || dirty}
            >
              Iterate
            </button>
            {iterateDisabledReason !== null && (busy || !session.canIterate || dirty) ? (
              <span className="review-surface__decision-reason">{iterateDisabledReason}</span>
            ) : null}
          </div>
          <div className="review-surface__decision">
            <button
              type="button"
              className="review-surface__proceed"
              onClick={() => void decide('proceed')}
              disabled={busy || dirty || proceedBlocked}
            >
              Approve
            </button>
            {proceedDisabledReason !== null ? (
              <span className="review-surface__decision-reason">{proceedDisabledReason}</span>
            ) : null}
          </div>
        </div>
      </footer>
    </section>
  );
}
