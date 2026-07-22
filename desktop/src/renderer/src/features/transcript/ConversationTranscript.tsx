import { useEffect, useRef, type ReactNode } from 'react';
import type { TranscriptMessage } from '../../../../shared/ipc';
import type { ConversationItem } from './conversation';

const NEAR_BOTTOM_PX = 40;

export function ActivityIndicator({
  labels,
  active,
  idleLabel,
}: {
  labels: string[];
  active: boolean;
  idleLabel: string;
}) {
  const shownLabels = labels.slice(-3);
  return (
    <div
      className="conversation__activity"
      data-active={active}
      role={active ? 'status' : undefined}
    >
      <span className="conversation__thinking" aria-hidden="true">
        {Array.from({ length: 8 }, (_, index) => (
          <span key={index} />
        ))}
      </span>
      <div className="conversation__activity-copy">
        <strong>{active ? 'Working' : 'Worked'}</strong>
        {shownLabels.length > 0 ? <span>{shownLabels.join(' · ')}</span> : <span>{idleLabel}</span>}
      </div>
    </div>
  );
}

export interface ConversationTranscriptProps {
  items: ConversationItem[];
  /** True while the active agent is still working (drives the live spinner). */
  waiting: boolean;
  idleLabel: string;
  ariaLabel: string;
  assistantName?: string;
  className?: string;
  emptyState?: ReactNode;
  /** Status/loading/error rows rendered above the conversation. */
  status?: ReactNode;
  /** Bump to force a scroll to the newest row (e.g. after sending a message). */
  pinToBottomToken?: number;
}

type FileChange = NonNullable<TranscriptMessage['fileChange']>;
type DiffLineKind = 'added' | 'removed' | 'context' | 'meta';

function fileChangeLabel(operation: string | undefined): string {
  switch (operation?.trim().toLocaleLowerCase()) {
    case 'add':
    case 'create':
    case 'write':
      return 'Created';
    case 'delete':
    case 'remove':
      return 'Deleted';
    case 'move':
    case 'rename':
      return 'Renamed';
    default:
      return 'Updated';
  }
}

function diffLineKind(line: string): DiffLineKind {
  if (
    line.startsWith('diff --git ') ||
    line.startsWith('index ') ||
    line.startsWith('@@') ||
    line.startsWith('--- ') ||
    line.startsWith('+++ ') ||
    line.startsWith('new file mode ') ||
    line.startsWith('deleted file mode ') ||
    line === '...'
  ) {
    return 'meta';
  }
  if (line.startsWith('+')) return 'added';
  if (line.startsWith('-')) return 'removed';
  return 'context';
}

function visibleDiff(change: FileChange): string[] {
  const detail = change.detail?.trim();
  if (detail === undefined || detail === '') return [];
  if (/^Captured from (?:tool usage|tool activity|provider file change)\.$/.test(detail)) return [];
  const lines = detail.split('\n');
  const looksLikeDiff =
    change.hasDiffPatch === true ||
    lines.some((line) => line.startsWith('+') || line.startsWith('-') || line.startsWith('@@'));
  return looksLikeDiff ? lines : [];
}

export function FileChangeCard({ change }: { change: FileChange }): React.ReactElement {
  const lines = visibleDiff(change);
  const inferredAdded = lines.filter((line) => diffLineKind(line) === 'added').length;
  const inferredRemoved = lines.filter((line) => diffLineKind(line) === 'removed').length;
  const added = change.addedLines ?? inferredAdded;
  const removed = change.removedLines ?? inferredRemoved;
  const label = fileChangeLabel(change.operation);

  return (
    <article className="conversation__file-change" aria-label={`${label} ${change.path}`}>
      <header className="conversation__file-change-header">
        <span className="conversation__file-change-status">{label}</span>
        <span className="conversation__file-change-path" title={change.path}>
          {change.oldPath ? `${change.oldPath} → ` : null}
          {change.path}
        </span>
        {added > 0 || removed > 0 ? (
          <span
            className="conversation__file-change-stats"
            aria-label={`${added} lines added, ${removed} lines removed`}
          >
            {added > 0 ? <span data-kind="added">+{added}</span> : null}
            {removed > 0 ? <span data-kind="removed">−{removed}</span> : null}
          </span>
        ) : null}
      </header>
      {lines.length > 0 ? (
        <div className="conversation__diff" role="region" aria-label={`Diff for ${change.path}`}>
          {lines.map((line, index) => {
            const kind = diffLineKind(line);
            return (
              <div key={`${index}-${line}`} className="conversation__diff-line" data-kind={kind}>
                <span className="conversation__diff-marker" aria-hidden="true">
                  {kind === 'added' ? '+' : kind === 'removed' ? '−' : ' '}
                </span>
                <code>{kind === 'added' || kind === 'removed' ? line.slice(1) : line}</code>
              </div>
            );
          })}
        </div>
      ) : null}
    </article>
  );
}

/** Shared conversational renderer for AMA and the current-run live preview. */
export function ConversationTranscript({
  items,
  waiting,
  idleLabel,
  ariaLabel,
  assistantName = 'Agentico',
  className,
  emptyState,
  status,
  pinToBottomToken,
}: ConversationTranscriptProps) {
  const scrollRef = useRef<HTMLElement>(null);
  const stickToBottom = useRef(true);
  const lastItem = items.at(-1);

  useEffect(() => {
    const element = scrollRef.current;
    if (element !== null && stickToBottom.current) element.scrollTop = element.scrollHeight;
  }, [items, waiting]);

  useEffect(() => {
    if (pinToBottomToken === undefined) return;
    stickToBottom.current = true;
    const element = scrollRef.current;
    if (element !== null) element.scrollTop = element.scrollHeight;
  }, [pinToBottomToken]);

  return (
    <section
      ref={scrollRef}
      className={
        className === undefined ? 'conversation__scroll' : `conversation__scroll ${className}`
      }
      aria-label={ariaLabel}
      aria-live="polite"
      onScroll={(event) => {
        const element = event.currentTarget;
        stickToBottom.current =
          element.scrollHeight - element.scrollTop - element.clientHeight < NEAR_BOTTOM_PX;
      }}
    >
      {status}
      {items.length === 0 && !waiting ? (emptyState ?? null) : null}
      {items.map((item, index) =>
        item.kind === 'message' ? (
          <article key={item.key} className="conversation__message" data-role={item.role}>
            <span className="conversation__message-role">
              {item.role === 'user' ? 'You' : assistantName}
            </span>
            <p>{item.text}</p>
          </article>
        ) : item.kind === 'auto-pick' ? (
          <article
            key={item.key}
            className="conversation__auto-pick"
            aria-label="Auto-picked response"
          >
            <span className="conversation__auto-pick-label">Auto-picked</span>
            <strong>{item.question}</strong>
            <p>{item.answer}</p>
            {item.confidence === undefined ? null : (
              <span className="conversation__auto-pick-confidence">
                {Math.round(item.confidence * 100)}% confidence
              </span>
            )}
          </article>
        ) : item.kind === 'file-change' ? (
          <FileChangeCard key={item.key} change={item.change} />
        ) : (
          <ActivityIndicator
            key={item.key}
            labels={item.labels}
            idleLabel={idleLabel}
            active={waiting && index === items.length - 1}
          />
        ),
      )}
      {waiting && lastItem?.kind !== 'activity' ? (
        <ActivityIndicator labels={[]} idleLabel={idleLabel} active />
      ) : null}
    </section>
  );
}
