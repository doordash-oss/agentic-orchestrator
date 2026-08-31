import { useEffect, useState } from 'react';
import { DEFAULT_RUNTIME_ID } from '../../../shared/ipc';
import type { ReviewDraftKey, ReviewSession, ReviewValidation } from '../../../shared/ipc';
import { MonacoBuffer, MonacoDiff, useResolvedTheme } from '../components/monaco';
import { useConnectionState, useMediaQuery } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import { renderSanitizedMarkdown } from './sanitizedMarkdown';

type View = 'edit' | 'preview' | 'split';

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
  const [busy, setBusy] = useState(false);
  const [recovered, setRecovered] = useState(false);
  const [recoveredKey, setRecoveredKey] = useState<ReviewDraftKey | null>(null);
  const [reconcile, setReconcile] = useState<ReconcileState | null>(null);
  const isNarrow = useMediaQuery('(max-width: 900px)');
  const connection = useConnectionState();
  const theme = useResolvedTheme();
  const [editorKey, setEditorKey] = useState(0);

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
          setNotice('Server draft loaded.');
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
          setNotice('Recovered draft needs reconciliation with a newer server draft.');
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
        setNotice('Recovered unsaved draft loaded.');
      })
      .catch((error: unknown) => alive && setNotice(parseIpcError(error).message));
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
      void window.agentico
        .saveLocalReviewDraft({ ...key, text })
        .catch(() => setNotice('Local recovery copy could not be saved.'));
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
    setNotice('Loading the current server draft…');
    try {
      const current = await window.agentico.readReview({ featureId });
      setReconcile({
        current,
        localText,
        baseText: baseOverride !== undefined ? baseOverride : baseText,
        localKey,
      });
      setNotice('Choose how to reconcile the two drafts. Nothing has been written.');
    } catch (error) {
      setNotice(`Could not load the current draft — ${parseIpcError(error).message}`);
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
    setNotice('Saving draft…');
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
      setNotice('Saved to the server.');
      try {
        await discardLocal(reviewKey(runtimeId, session));
      } catch {
        setNotice('Saved, but the local recovery copy could not be removed.');
      }
    } catch (error) {
      setNotice(`Save failed — ${parseIpcError(error).message}`);
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
        <p role="status">{notice}</p>
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
    setNotice(decision === 'proceed' ? 'Approving review…' : 'Sending back for iteration…');
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
      setNotice(result.result);
      try {
        await discardLocal(reviewKey(runtimeId, session));
      } catch {
        // Decision succeeded; a leftover recovery copy self-heals on reopen.
      }
      await onResolved();
    } catch (error) {
      setNotice(`Decision failed — ${parseIpcError(error).message}`);
    } finally {
      setBusy(false);
    }
  };

  if (reconcile !== null) {
    const takeServer = async () => {
      try {
        await window.agentico.discardLocalReviewDraft(reconcile.localKey);
      } catch (error) {
        setNotice(`Could not discard the local draft — ${parseIpcError(error).message}`);
        return;
      }
      setRecovered(false);
      setRecoveredKey(null);
      setSession(reconcile.current);
      setText(reconcile.current.text);
      setBaseText(reconcile.current.text);
      setReconcile(null);
      setNotice('Using the current server draft.');
    };
    const continueEditing = async () => {
      try {
        await window.agentico.discardLocalReviewDraft(reconcile.localKey);
      } catch (error) {
        setNotice(`Could not discard the old draft — ${parseIpcError(error).message}`);
        return;
      }
      setRecovered(true);
      setRecoveredKey(reviewKey(runtimeId, reconcile.current));
      setSession(reconcile.current);
      setBaseText(reconcile.current.text);
      setText(reconcile.localText);
      setReconcile(null);
      setNotice('Your local text is ready to edit against the current server revision.');
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
        setNotice('Your local draft replaced the server draft.');
        try {
          await discardLocal(reconcile.localKey);
        } catch {
          setNotice('Replaced the server draft, but the local recovery copy could not be removed.');
        }
      } catch (error) {
        setNotice(`Could not replace the server draft — ${parseIpcError(error).message}`);
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
          <p role="status">{notice}</p>
          <div>
            <button
              type="button"
              onClick={() => {
                if (
                  reconcile.baseText === null &&
                  reconcile.localKey.baseDraftRevision !== reconcile.current.draftRevision
                ) {
                  void window.agentico.discardLocalReviewDraft(reconcile.localKey).catch(() => {
                    setNotice(
                      'Could not remove the orphaned local draft — it may reappear on reopen.',
                    );
                  });
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
                  setNotice('Recovered local draft discarded.');
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
        <ul className="review-surface__findings" aria-label="Validation findings">
          {validation.findings.map((finding) => (
            <li key={finding.code}>{finding.message}</li>
          ))}
        </ul>
      ) : null}
      <footer className="review-surface__footer">
        <p role="status">{notice}</p>
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
