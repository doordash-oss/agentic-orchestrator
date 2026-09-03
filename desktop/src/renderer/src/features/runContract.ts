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
 * The run contract shared by the creation and refactor wizards: pipeline
 * profiles with their default checkpoints, and the wire keys the server's
 * ModelConfig expects for per-phase model/effort choices.
 */
import type { Checkpoints } from '../../../shared/ipc';
import { GATE_FIELDS, applicableGates, type PhaseKey } from './ConfigEditor';

export type Pipeline = 'medium' | 'large' | 'moonshot';

export type CheckpointState = Checkpoints & { draftPublish: boolean };

export const PIPELINE_PROFILES: Record<
  Pipeline,
  { title: string; note: string; checkpoints: CheckpointState }
> = {
  medium: {
    title: 'Medium',
    note: 'Plan, implement, review',
    checkpoints: {
      inquiryReview: false,
      researchReview: false,
      designReview: false,
      roadmapReview: false,
      phasePlanReview: true,
      manualPublish: false,
      draftPublish: false,
    },
  },
  large: {
    title: 'Large',
    note: 'Full discovery and delivery',
    checkpoints: {
      inquiryReview: true,
      researchReview: false,
      designReview: false,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: false,
      draftPublish: false,
    },
  },
  moonshot: {
    title: 'Moonshot',
    note: 'Full depth, maximum scrutiny',
    checkpoints: {
      inquiryReview: true,
      researchReview: true,
      designReview: true,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: true,
      draftPublish: false,
    },
  },
};

export const PIPELINES = (
  Object.entries(PIPELINE_PROFILES) as Array<[Pipeline, (typeof PIPELINE_PROFILES)[Pipeline]]>
).map(([id, profile]) => ({ id, ...profile }));

export function checkpointsForPipeline(pipeline: Pipeline): CheckpointState {
  return { ...PIPELINE_PROFILES[pipeline].checkpoints };
}

export function checkpointSummary(pipeline: Pipeline, checkpoints: CheckpointState): string {
  const gates = applicableGates(pipeline);
  const active = GATE_FIELDS.filter((gate) => gates.has(gate.key) && checkpoints[gate.key]).map(
    (gate) => gate.label,
  );
  return active.length === 0 ? 'No review checkpoints' : active.join(', ');
}

export function isPipeline(value: string | undefined): value is Pipeline {
  return value === 'medium' || value === 'large' || value === 'moonshot';
}

/** The authoritative server contract's ModelConfig JSON keys. */
export function modelConfigKey(key: PhaseKey): string {
  return key === 'kbBuild' ? 'kb_build' : key;
}
