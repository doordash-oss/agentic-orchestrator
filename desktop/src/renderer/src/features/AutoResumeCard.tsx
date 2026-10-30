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
import type { FeatureSnapshot } from '../../../shared/ipc';
import { TriangleAlertIcon } from '../components/icons';

type AutoResume = NonNullable<FeatureSnapshot['autoResume']>;

const PROVIDER_LABELS: Record<AutoResume['provider'], string> = {
  claude: 'Claude',
  codex: 'Codex',
};

/** Formats a remaining duration as "2h 05m 09s", "4m 07s", or "12s". */
export function formatCountdown(remainingMs: number): string {
  const total = Math.max(0, Math.ceil(remainingMs / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  const pad = (value: number): string => String(value).padStart(2, '0');
  if (hours > 0) return `${hours}h ${pad(minutes)}m ${pad(seconds)}s`;
  if (minutes > 0) return `${minutes}m ${pad(seconds)}s`;
  return `${seconds}s`;
}

/** Formats a reset or resume moment, adding the day when it is not today. */
function formatMoment(iso: string, nowMs: number): string {
  const moment = new Date(iso);
  const time = moment.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
  if (moment.toDateString() === new Date(nowMs).toDateString()) return time;
  const day = moment.toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
  });
  return `${day}, ${time}`;
}

export interface AutoResumeCardProps {
  autoResume: AutoResume;
  /** Whether the server catalog currently enables pause-stop, which cancels the resume. */
  cancelEnabled: boolean;
  /** Server reason shown when cancel is disabled. */
  cancelDisabledReason?: string;
  busy: boolean;
  onCancel(): void;
  /** Clock seam for tests; defaults to Date.now. */
  now?: () => number;
}

/**
 * The warning card a feature shows while it waits out a provider usage
 * limit: what happened, when the server resumes the feature, a live
 * countdown, and the cancel that parks the feature as Interrupted instead.
 */
export function AutoResumeCard({
  autoResume,
  cancelEnabled,
  cancelDisabledReason,
  busy,
  onCancel,
  now = Date.now,
}: AutoResumeCardProps) {
  const [nowMs, setNowMs] = useState(now);
  useEffect(() => {
    setNowMs(now());
    const timer = window.setInterval(() => setNowMs(now()), 1000);
    return () => window.clearInterval(timer);
  }, [now, autoResume.resumeAt]);

  const provider = PROVIDER_LABELS[autoResume.provider];
  const remainingMs = Date.parse(autoResume.resumeAt) - nowMs;
  const due = remainingMs <= 0;

  return (
    <section className="auto-resume-card" aria-label="Usage-limit auto-resume">
      <div className="auto-resume-card__header">
        <span className="auto-resume-card__icon" aria-hidden="true">
          <TriangleAlertIcon />
        </span>
        <span className="auto-resume-card__label">Waiting for usage limit</span>
      </div>
      <p className="auto-resume-card__title">{provider} usage limit reached</p>
      <p className="auto-resume-card__summary">
        {provider} stopped this run because your plan&apos;s usage limit ran out. It resets at{' '}
        {formatMoment(autoResume.resetAt, nowMs)}, and Agentico will resume the feature
        automatically at {formatMoment(autoResume.resumeAt, nowMs)}.
      </p>
      <p className="auto-resume-card__countdown" data-due={due ? 'true' : 'false'}>
        {due ? (
          'Resuming now…'
        ) : (
          <>
            Resuming in{' '}
            <time
              className="auto-resume-card__remaining"
              dateTime={autoResume.resumeAt}
              aria-live="off"
            >
              {formatCountdown(remainingMs)}
            </time>
          </>
        )}
      </p>
      <p className="auto-resume-card__hint">
        Cancel to stop the automatic resume. The feature becomes Interrupted and you can resume it
        yourself, with a different model if you like.
      </p>
      <div className="auto-resume-card__actions">
        <button
          type="button"
          className="auto-resume-card__cancel"
          onClick={onCancel}
          disabled={busy || !cancelEnabled}
        >
          Cancel auto-resume
        </button>
        {!cancelEnabled && cancelDisabledReason !== undefined ? (
          <span className="auto-resume-card__reason">{cancelDisabledReason}</span>
        ) : null}
      </div>
    </section>
  );
}
