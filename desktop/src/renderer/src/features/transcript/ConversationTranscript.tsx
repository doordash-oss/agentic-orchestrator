import { useEffect, useRef, type ReactNode } from 'react';
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
