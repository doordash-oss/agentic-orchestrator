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

import type { SupervisorBackgroundTask } from '../../../../shared/ipc';

export function backgroundTaskActive(task: SupervisorBackgroundTask): boolean {
  return task.state === 'watching' || task.state === 'running';
}

export function backgroundWorkSummary(tasks: readonly SupervisorBackgroundTask[] = []): string {
  const watching = tasks.filter((task) => task.state === 'watching').length;
  const running = tasks.filter((task) => task.state === 'running').length;
  const attention = tasks.filter(
    (task) => task.state === 'interrupted' || task.state === 'failed',
  ).length;
  return [
    watching === 0 ? '' : `${watching} monitor${watching === 1 ? '' : 's'} watching`,
    running === 0 ? '' : `${running} task${running === 1 ? '' : 's'} running`,
    attention === 0 ? '' : `${attention} task${attention === 1 ? ' needs' : 's need'} attention`,
  ]
    .filter(Boolean)
    .join(' · ');
}

const LABELS: Record<SupervisorBackgroundTask['state'], string> = {
  watching: 'Watching',
  running: 'Running',
  completed: 'Completed',
  failed: 'Failed',
  stopped: 'Stopped',
  interrupted: 'Interrupted',
};

function timestamp(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

/** Lives in the dock, independent of transcript pagination and turn completion. */
export function SupervisorBackgroundWork({
  tasks = [],
  onStop,
  stopDisabled,
}: {
  tasks?: readonly SupervisorBackgroundTask[];
  onStop(task: SupervisorBackgroundTask): void;
  stopDisabled: boolean;
}) {
  if (tasks.length === 0) return null;
  const current = tasks.filter(
    (task) => backgroundTaskActive(task) || task.state === 'interrupted' || task.state === 'failed',
  );
  const recent = tasks.filter((task) => !current.includes(task));
  const row = (task: SupervisorBackgroundTask) => (
    <li key={task.id} data-state={task.state} className="supervisor-background__task">
      <details>
        <summary>
          <span className="supervisor-background__state">{LABELS[task.state]}</span>
          <span className="supervisor-background__title">{task.title}</span>
        </summary>
        <div className="supervisor-background__activity">
          <p>{task.detail}</p>
          {task.schedule === '' ? null : <p>Schedule: {task.schedule}</p>}
          <p>
            Started <time dateTime={task.startedAt}>{timestamp(task.startedAt)}</time>
          </p>
          <p>
            Last confirmed <time dateTime={task.updatedAt}>{timestamp(task.updatedAt)}</time>
          </p>
          {task.expiresAt === '' ? null : (
            <p>
              Expires <time dateTime={task.expiresAt}>{timestamp(task.expiresAt)}</time>
            </p>
          )}
          {(task.activity?.length ?? 0) > 0 ? (
            <ol aria-label="Recent task activity">
              {task.activity?.map((event, index) => (
                <li key={`${event.at}:${index}`}>
                  <time dateTime={event.at}>{timestamp(event.at)}</time> · {event.detail}
                </li>
              ))}
            </ol>
          ) : null}
          {backgroundTaskActive(task) ? (
            <button type="button" disabled={stopDisabled} onClick={() => onStop(task)}>
              Ask to stop
            </button>
          ) : null}
        </div>
      </details>
    </li>
  );
  return (
    <section className="supervisor-background" aria-label="Background work">
      <header>
        <strong>Background work</strong>
        <span role="status">{backgroundWorkSummary(tasks) || 'Recent activity'}</span>
      </header>
      <div className="supervisor-background__body">
        {current.length === 0 ? null : <ul>{current.map(row)}</ul>}
        {recent.length === 0 ? null : (
          <details className="supervisor-background__recent">
            <summary>Recent activity ({recent.length})</summary>
            <ul>{recent.map(row)}</ul>
          </details>
        )}
      </div>
    </section>
  );
}
