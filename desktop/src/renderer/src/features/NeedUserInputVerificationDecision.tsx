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

import type { VerificationGateAction } from '../../../shared/ipc';
import type { AttentionGate } from './NeedUserInputModal';

export interface NeedUserInputVerificationDecisionProps {
  item: AttentionGate;
  selectedAction: VerificationGateAction | '';
  idPrefix: string;
  onSelect(action: VerificationGateAction): void;
}

export function hasStructuredVerificationDecision(item: AttentionGate): boolean {
  const verification = item.verification;
  return (
    verification !== undefined &&
    verification.blockers.length > 0 &&
    item.questions.length === 1 &&
    verification.allowedActions.length === 2 &&
    verification.allowedActions.includes('RETRY_AFTER_AUTH') &&
    verification.allowedActions.includes('WAIVE')
  );
}

export function NeedUserInputVerificationDecision({
  item,
  selectedAction,
  idPrefix,
  onSelect,
}: NeedUserInputVerificationDecisionProps): React.ReactElement | null {
  const verification = item.verification;
  if (verification === undefined) return null;

  return (
    <div className="need-input-verification">
      <section
        className="need-input-verification__blockers"
        aria-labelledby={`${idPrefix}-blocked-checks`}
      >
        <div className="need-input-verification__section-heading">
          <p>Verification stopped</p>
          <h2 id={`${idPrefix}-blocked-checks`}>Blocked checks</h2>
        </div>
        <ul className="need-input-verification__blocker-list">
          {verification.blockers.map((blocker) => (
            <li key={blocker.itemId} className="need-input-verification__blocker-card">
              <header>
                <h3>{blocker.name}</h3>
                {blocker.repoName === undefined ? null : <code>{blocker.repoName}</code>}
              </header>
              <dl>
                <div>
                  <dt>Command</dt>
                  <dd>
                    <code>{blocker.command}</code>
                  </dd>
                </div>
                <div>
                  <dt>Reason</dt>
                  <dd>{blocker.reason}</dd>
                </div>
                <div>
                  <dt>Next step</dt>
                  <dd>{blocker.remediation}</dd>
                </div>
              </dl>
            </li>
          ))}
        </ul>
      </section>

      <fieldset className="need-input-verification__decisions">
        <legend>How should Agentico continue?</legend>
        <div className="need-input-verification__decision-list">
          <label
            className="need-input-verification__decision"
            data-selected={selectedAction === 'RETRY_AFTER_AUTH'}
          >
            <input
              type="radio"
              name={`${idPrefix}-verification-action`}
              value="RETRY_AFTER_AUTH"
              checked={selectedAction === 'RETRY_AFTER_AUTH'}
              onChange={() => onSelect('RETRY_AFTER_AUTH')}
            />
            <span>
              <strong>I've granted access — retry verification</strong>
              <small>Checks rerun from the same iteration.</small>
            </span>
          </label>
          <label
            className="need-input-verification__decision"
            data-selected={selectedAction === 'WAIVE'}
            data-tone="warning"
          >
            <input
              type="radio"
              name={`${idPrefix}-verification-action`}
              value="WAIVE"
              checked={selectedAction === 'WAIVE'}
              onChange={() => onSelect('WAIVE')}
            />
            <span>
              <strong>Waive blocked checks and continue</strong>
              <small>Checks are recorded as user-authorized waivers and will not run.</small>
            </span>
          </label>
        </div>
      </fieldset>
    </div>
  );
}
