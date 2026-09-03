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

import { useMemo, useState } from 'react';
import type { TranscriptMessage } from '../../../shared/ipc';
import {
  MAX_RENDERED_ENTRIES,
  semanticTimeline,
  stripUnsafeAnsi,
  type SemanticEntry,
} from './timelineModel';

/**
 * Read-only semantic/raw transcript inspection.
 *
 * Used for sealed-run history and as the "Signal trace" view of the live
 * current-run preview. Live streaming, session discovery, and the cohort tab
 * strip live in CurrentRunInspection + useCohortTranscripts.
 */
export function HistoricalTimeline({ messages }: { messages: readonly TranscriptMessage[] }) {
  const [selected, setSelected] = useState<TranscriptMessage | null>(null);
  const entries = useMemo(() => semanticTimeline(messages), [messages]);
  return (
    <div className="run-timeline__layout" data-history="true">
      <div className="run-timeline__reader">
        <div className="run-timeline__viewport" tabIndex={0} aria-label="Semantic timeline">
          {entries.length === 0 ? (
            <p className="setup-step__empty">This session has no transcript records yet.</p>
          ) : (
            <ol className="signal-trace">
              {entries.slice(-MAX_RENDERED_ENTRIES).map((entry) => (
                <TimelineEntry key={entry.id} entry={entry} onInspect={setSelected} />
              ))}
            </ol>
          )}
        </div>
      </div>
      <aside
        className="raw-inspector"
        aria-label="Raw record inspector"
        data-has-selection={selected !== null}
      >
        <div className="raw-inspector__heading">
          <h4>Validated source</h4>
          {selected !== null ? (
            <button
              type="button"
              onClick={() => setSelected(null)}
              aria-label="Close raw inspector"
            >
              ✕
            </button>
          ) : null}
        </div>
        {selected === null ? (
          <p>Select a trace entry to inspect its validated source record.</p>
        ) : (
          <pre>{JSON.stringify(selected, null, 2)}</pre>
        )}
      </aside>
    </div>
  );
}

function TimelineEntry({
  entry,
  onInspect,
}: {
  entry: SemanticEntry;
  onInspect(record: TranscriptMessage): void;
}) {
  if (entry.kind === 'routine-group') {
    return (
      <li className="signal-trace__entry" data-kind={entry.kind}>
        <details>
          <summary>{entry.text}</summary>
          <ul>
            {entry.records.map((record) => (
              <li key={record.index}>
                <button type="button" onClick={() => onInspect(record)}>
                  {record.tool ??
                    record.task?.description ??
                    record.fileChange?.path ??
                    record.type}
                </button>
              </li>
            ))}
          </ul>
        </details>
      </li>
    );
  }
  const record = entry.records[0]!;
  return (
    <li className="signal-trace__entry" data-kind={entry.kind}>
      <span className="signal-trace__label">{entry.label}</span>
      <p>{stripUnsafeAnsi(entry.text)}</p>
      <button
        type="button"
        onClick={() => onInspect(record)}
        aria-label={`Inspect raw record ${record.index}`}
      >
        raw
      </button>
    </li>
  );
}
