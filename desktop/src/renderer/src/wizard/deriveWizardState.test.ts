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

import { describe, expect, it } from 'vitest';
import type { ReadinessIssue, ReadinessSnapshot } from '../../../shared/ipc';
import { readySnapshot, unreadySnapshot } from '../test/agenticoMock';
import { WIZARD_STEPS, deriveWizardState } from './deriveWizardState';

const canonicalIssue = (
  code: ReadinessIssue['code'],
  title: string,
  summary: string,
): ReadinessIssue => ({
  code,
  class: 'blocking',
  title,
  summary,
});

describe('deriveWizardState', () => {
  it('derives the fresh-install state: providers step active, nothing satisfied', () => {
    const state = deriveWizardState(unreadySnapshot());
    expect(state.steps).toEqual(WIZARD_STEPS);
    expect(state.activeStep).toBe('providers');
    expect(state.activeIndex).toBe(0);
    expect(state.complete).toBe(false);
    expect(state.gates).toEqual({ providers: false, models: false });
  });

  it('is complete only when every mandatory gate is satisfied', () => {
    const state = deriveWizardState(readySnapshot());
    expect(state.complete).toBe(true);
    expect(state.activeStep).toBe('ready');
    expect(state.activeIndex).toBe(WIZARD_STEPS.length - 1);
    expect(state.blockers).toEqual([]);
  });

  it('one usable provider satisfies the provider gate even if others are broken', () => {
    const snapshot = unreadySnapshot({
      providers: [
        { name: 'claude', installed: true, version: '2.1.0', ready: true },
        {
          name: 'codex',
          installed: false,
          ready: false,
          issue: canonicalIssue('missing_executable', 'Missing executable', 'not installed'),
        },
      ],
    });
    const state = deriveWizardState(snapshot);
    expect(state.gates.providers).toBe(true);
    expect(state.activeStep).toBe('models');
  });

  it('an empty provider list leaves the provider gate blocked', () => {
    const state = deriveWizardState(unreadySnapshot({ providers: [] }));
    expect(state.gates.providers).toBe(false);
    expect(state.activeStep).toBe('providers');
  });

  it('an empty workspace is complete: repositories are chosen where work is defined', () => {
    const state = deriveWizardState(readySnapshot({ workspaceRoots: [], repositories: [] }));
    expect(state.complete).toBe(true);
    expect(state.activeStep).toBe('ready');
  });

  it('never blocks on workspace or repository issues', () => {
    const snapshot = readySnapshot({
      workspaceRoots: [
        {
          path: '/gone',
          valid: false,
          issue: canonicalIssue(
            'invalid_workspace_root',
            'Invalid workspace root',
            'missing directory',
          ),
        },
      ],
      repositories: [
        {
          name: 'broken',
          path: '/work/space/broken',
          valid: false,
          issue: canonicalIssue('invalid_repository', 'Invalid repository', 'not a git repository'),
        },
      ],
      issues: [
        canonicalIssue('invalid_workspace_root', 'Invalid workspace root', 'missing directory'),
        canonicalIssue('invalid_repository', 'Invalid repository', 'not a git repository'),
      ],
    });
    const state = deriveWizardState(snapshot);
    expect(state.complete).toBe(true);
    expect(state.blockers).toEqual([]);
  });

  it('never trusts local state: a server-unready snapshot is never complete', () => {
    // Gates look satisfied but the server says not ready (e.g. stale probe).
    const snapshot = readySnapshot({ ready: false });
    const state = deriveWizardState(snapshot);
    expect(state.complete).toBe(false);
  });

  it('surfaces an invalid configuration as a cross-cutting blocker', () => {
    const issue: ReadinessIssue = {
      code: 'invalid_configuration',
      class: 'blocking',
      title: 'Invalid configuration',
      summary: 'config.yaml is unreadable',
      remediation: { hint: 'Fix config.yaml' },
    };
    const snapshot = readySnapshot({ ready: false, configuration: { valid: false, issue } });
    const state = deriveWizardState(snapshot);
    expect(state.configurationIssue).toEqual(issue);
    expect(state.complete).toBe(false);
  });

  it('collects only the active step blockers from the flattened issue list', () => {
    const state = deriveWizardState(unreadySnapshot());
    expect(state.activeStep).toBe('providers');
    expect(state.blockers.map((issue) => issue.code)).toEqual([
      'unauthenticated',
      'missing_executable',
    ]);
  });

  it('maps model issues to the models step blockers', () => {
    const snapshot = unreadySnapshot({
      providers: [{ name: 'claude', installed: true, ready: true }],
      issues: [canonicalIssue('models_unavailable', 'Models unavailable', 'no models')],
    });
    const state = deriveWizardState(snapshot);
    expect(state.activeStep).toBe('models');
    expect(state.blockers.map((issue) => issue.code)).toEqual(['models_unavailable']);
  });

  it('marks earlier steps done and later steps upcoming for the spine', () => {
    const snapshot = unreadySnapshot({
      providers: [{ name: 'claude', installed: true, ready: true }],
    });
    const state = deriveWizardState(snapshot);
    expect(state.activeIndex).toBe(WIZARD_STEPS.indexOf('models'));
  });

  it('is a pure projection: identical snapshots produce identical states', () => {
    const snapshot: ReadinessSnapshot = unreadySnapshot();
    expect(deriveWizardState(snapshot)).toEqual(deriveWizardState(snapshot));
  });
});
