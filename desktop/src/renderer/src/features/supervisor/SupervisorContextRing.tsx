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

import type { CSSProperties } from 'react';
import type { SupervisorState } from '../../../../shared/ipc';

export function SupervisorContextRing({ usage }: { usage: SupervisorState['contextUsage'] }) {
  const percent = usage?.percent ?? 0;
  const warning = usage !== null && percent >= 80;
  const label =
    usage === null
      ? 'Context usage unknown'
      : `Context usage ${percent}%${warning ? ', near the limit' : ''}`;
  const title =
    usage === null
      ? label
      : `${usage.usedTokens.toLocaleString()} of ${usage.windowTokens.toLocaleString()} context tokens used`;
  return (
    <span
      className="supervisor-context-ring"
      data-testid="supervisor-context-ring"
      data-tone={warning ? 'warning' : 'neutral'}
      data-known={usage !== null}
      role="img"
      aria-label={label}
      title={title}
      style={{ '--context-fill': `${percent}%` } as CSSProperties}
    />
  );
}
