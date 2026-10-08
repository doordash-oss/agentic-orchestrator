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

/**
 * The queue strip: messages submitted during a turn, waiting above the
 * composer in delivery order. Each item can go back to the composer (Edit),
 * jump the line (Send now) or be dropped (Remove); a delivery that failed
 * shows its error inline until one of those resolves it.
 */
import type { CanonicalError } from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { draftItemCount, type SupervisorQueuedMessage } from './supervisorDrafts';

/** The first line of a queued message, for its one-line row. */
function firstLine(text: string): string {
  return text.trim().split('\n', 1)[0] ?? '';
}

export function SupervisorQueueStrip({
  queue,
  failures,
  onEdit,
  onSendNow,
  onRemove,
}: {
  queue: readonly SupervisorQueuedMessage[];
  /** Delivery failures by queued item id. */
  failures: ReadonlyMap<string, CanonicalError>;
  onEdit(id: string): void;
  onSendNow(id: string): void;
  onRemove(id: string): void;
}) {
  if (queue.length === 0) return null;
  return (
    <section className="supervisor-queue" aria-label="Queued messages">
      <ol className="supervisor-queue__list">
        {queue.map((item, index) => {
          const count = draftItemCount(item.items);
          const failure = failures.get(item.id);
          const line = firstLine(item.text);
          return (
            <li
              key={item.id}
              className="supervisor-queue__item"
              data-next={item.next === true ? 'true' : undefined}
              data-failed={failure === undefined ? undefined : 'true'}
            >
              <span className="supervisor-queue__order" aria-hidden="true">
                {item.next === true ? 'Next' : String(index + 1)}
              </span>
              <span className="supervisor-queue__text" title={item.text}>
                {line === '' ? <em>Attachments only</em> : line}
              </span>
              {count > 0 ? (
                <span className="supervisor-queue__count">
                  {count === 1 ? '1 attachment' : `${String(count)} attachments`}
                </span>
              ) : null}
              <span className="supervisor-queue__actions">
                <button type="button" onClick={() => onEdit(item.id)}>
                  Edit
                </button>
                <button type="button" onClick={() => onSendNow(item.id)}>
                  Send now
                </button>
                <button type="button" onClick={() => onRemove(item.id)}>
                  Remove
                </button>
              </span>
              {failure !== undefined ? (
                <div className="supervisor-queue__error">
                  <ErrorSurface error={failure} variant="compact" />
                </div>
              ) : null}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
