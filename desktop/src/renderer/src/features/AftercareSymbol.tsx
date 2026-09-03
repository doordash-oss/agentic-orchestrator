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

import type { AftercareActionId } from './postImplementationModel';

/**
 * The runway's leading symbols: hand-drawn inline SVG in the SF Symbols
 * idiom (16px box, 1.4px stroke, round joins), so the grouped list reads as
 * native without adding an icon dependency. Ordering is not information here —
 * the symbol names the kind of follow-up, which is what the deleted 01/02/03
 * numbering pretended to do.
 */
export function AftercareSymbol({ id }: { id: AftercareActionId }): React.ReactElement {
  return (
    <svg
      className="aftercare-workspace__symbol"
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {paths(id)}
    </svg>
  );
}

function paths(id: AftercareActionId): React.ReactElement {
  switch (id) {
    // Delivery upward and out: publication puts the work into service.
    case 'publish':
    case 'publish-updates':
      return (
        <>
          <path d="M8 11.5V3.5" />
          <path d="M4.9 6.6 8 3.5l3.1 3.1" />
          <path d="M3 13h10" />
        </>
      );
    // Two lines becoming one: a merge into the base branch.
    case 'merge':
    case 'merge-updates':
      return (
        <>
          <path d="M4.5 2.75v3.1c0 2.3 1.85 4.15 4.15 4.15h2.6" />
          <path d="M9.3 7.75 11.9 10l-2.6 2.25" />
          <path d="M4.5 13.25V10.4" />
        </>
      );
    // The u-turn: bring the branch back up to date with its target.
    case 'rebase':
      return (
        <>
          <path d="M5 11.5h5.25a2.75 2.75 0 0 0 0-5.5H4" />
          <path d="M6.4 3.6 4 6l2.4 2.4" />
        </>
      );
    // Additive work on top of what shipped.
    case 'refactor':
      return (
        <>
          <path d="M8 3.75v8.5" />
          <path d="M3.75 8h8.5" />
        </>
      );
    // A comment thread: the review is speaking to you.
    case 'review-feedback':
      return (
        <>
          <path d="M13 9.25A1.75 1.75 0 0 1 11.25 11H7.6L4.6 13.1V11h-.35A1.25 1.25 0 0 1 3 9.75v-5A1.75 1.75 0 0 1 4.75 3h6.5A1.75 1.75 0 0 1 13 4.75Z" />
        </>
      );
  }
}
