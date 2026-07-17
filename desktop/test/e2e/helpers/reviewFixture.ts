import fs from 'node:fs';
import path from 'node:path';
import type { JourneyWorld } from './world';

export const VALID_PLAN = [
  '# Packaged review fixture',
  '',
  '## Overview',
  '',
  'Exercise the conflict-safe planning review in the bundled desktop app.',
  '',
  '## Tasks',
  '',
  '### Task 1: Preserve explicit review decisions',
  '',
  '**Repo:** review-lab',
  '',
  '#### What to build',
  '',
  'Keep locally edited planning drafts recoverable and revision checked.',
  '',
  '#### Acceptance criteria',
  '',
  '- [ ] A reviewer can save an explicit draft revision.',
  '',
].join('\n');

/** Seeds a durable phase-plan review after normal feature creation and app shutdown. */
export function seedPlanReview(world: JourneyWorld, featureId: string, text = VALID_PLAN): string {
  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const activeRun = activeRunNumber(featureYaml);
  const runDir = path.join(
    world.stateDir,
    featureId,
    'runs',
    `run-${String(activeRun).padStart(3, '0')}`,
  );
  const planRelativePath = path.join('plan', 'phase-plan.md');
  const planPath = path.join(runDir, planRelativePath);
  const researchRelativePath = path.join('research', 'research.md');
  const researchPath = path.join(runDir, researchRelativePath);
  fs.mkdirSync(path.dirname(planPath), { recursive: true });
  fs.writeFileSync(planPath, text);
  // Iterating a plan resumes the real planning phase, which requires prior
  // research or design input for a full-pipeline feature. Keep this fixture
  // intentionally minimal while still exercising the production transition.
  fs.mkdirSync(path.dirname(researchPath), { recursive: true });
  fs.writeFileSync(researchPath, '# Fixture research\n\nReview-plan input.\n');

  const runPath = path.join(runDir, 'run.yaml');
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = upsertYamlMapScalar(runYaml, 'artifacts', 'plan', planRelativePath);
  runYaml = upsertYamlMapScalar(runYaml, 'artifacts', 'research', researchRelativePath);
  fs.writeFileSync(runPath, runYaml);

  featureYaml = upsertYamlScalar(featureYaml, 'status', 'PlanNeedsReview');
  // Phase values are serialized enum values; 2 is Implement. A plan review
  // pauses immediately before implementation, not at Publish.
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
  fs.writeFileSync(featurePath, featureYaml);
  return planPath;
}

function activeRunNumber(featureYaml: string): number {
  const match = featureYaml.match(/^active_run:\s*(\d+)\s*$/m);
  const value = match?.[1];
  if (value === undefined) return 1;
  const activeRun = Number.parseInt(value, 10);
  return Number.isSafeInteger(activeRun) && activeRun > 0 ? activeRun : 1;
}

function upsertYamlScalar(yaml: string, key: string, value: string): string {
  const line = `${key}: ${value}`;
  const pattern = new RegExp(`^${key}:.*$`, 'm');
  return pattern.test(yaml)
    ? yaml.replace(pattern, line)
    : yaml.endsWith('\n')
      ? `${yaml}${line}\n`
      : `${yaml}\n${line}\n`;
}

function upsertYamlMapScalar(yaml: string, mapKey: string, key: string, value: string): string {
  const mapPattern = new RegExp(`^${mapKey}:\\n((?:  .*\\n)*)`, 'm');
  const entry = `  ${key}: ${value}`;
  const match = yaml.match(mapPattern);
  if (match === null) return `${yaml.endsWith('\n') ? yaml : `${yaml}\n`}${mapKey}:\n${entry}\n`;
  const existing = match[0];
  const entryPattern = new RegExp(`^  ${key}:.*$`, 'm');
  const updated = entryPattern.test(existing)
    ? existing.replace(entryPattern, entry)
    : `${existing}${entry}\n`;
  return yaml.replace(existing, updated);
}
