import fs from 'node:fs';
import path from 'node:path';
import type { JourneyWorld } from './world';
import { upsertYamlMapScalar, upsertYamlScalar } from './yaml';

export const verificationDecisionPrompt =
  'Enter WAIVE to authorize waiving these blocked checks, or RETRY_AFTER_AUTH after making the required login/permission available.';
export const retryAfterAuth = 'RETRY_AFTER_AUTH';

/** Builds a seeded testing-contract.yaml with one manual, harness-owned, waivable blocker per id. */
export function seededVerificationContractYaml(...itemIds: string[]): string {
  return [
    'version: 2',
    'revision: 1',
    'scope: seeded-e2e-capability',
    'generated_from: {}',
    'items:',
    ...itemIds.flatMap((itemId) => [
      `  - id: ${itemId}`,
      '    source: manual',
      '    owner: harness',
      `    name: ${itemId}`,
      '    command: seeded capability probe',
      '    expected_evidence:',
      '      kind: manual_observation',
      '      matcher: non_empty_summary',
      '    policy:',
      '      required: true',
      '      allow_substitution: false',
      '      allow_blocked: false',
      '      allow_waiver: true',
    ]),
    '',
  ].join('\n');
}

/**
 * Seeds a NEED_USER_INPUT verification gate blocked on a missing "deployment
 * credentials" capability, durable across app restarts: a plan, a matching
 * testing-contract.yaml, the need-user-input.yaml gate itself, and the
 * run.yaml/feature.yaml pointers a real relaunch reads to reopen it.
 */
export function seedVerificationNeedUserInputGate(
  world: JourneyWorld,
  featureId: string,
  repoName = 'gate-lab',
): string {
  const runDir = path.join(world.stateDir, featureId, 'runs', 'run-001');
  const planPath = path.join(runDir, 'phase-03', 'plan', 'phase-plan.md');
  const contractPath = path.join(world.stateDir, featureId, 'testing-contract.yaml');
  const gatePath = path.join(
    runDir,
    'phase-03',
    'implement',
    'iteration-03',
    'need-user-input.yaml',
  );
  fs.mkdirSync(path.dirname(planPath), { recursive: true });
  fs.mkdirSync(path.dirname(gatePath), { recursive: true });
  const trustedContractPath = path.join(
    fs.realpathSync(path.dirname(contractPath)),
    path.basename(contractPath),
  );
  const trustedGatePath = path.join(
    fs.realpathSync(path.dirname(gatePath)),
    path.basename(gatePath),
  );
  fs.writeFileSync(
    planPath,
    [
      '# Seeded Gate Resume Plan',
      '',
      '## Overview',
      'Resume the packaged gate fixture into a real implementation session.',
      '',
      '## Tasks',
      '### Task 1: Resume seeded implementation',
      '',
      `**Repo:** ${repoName}`,
      '',
      '#### What to build',
      'Record that the packaged gate resume path can relaunch implementation.',
      '',
      '#### Acceptance criteria',
      '- [ ] The workflow provider session starts after Resume.',
      '',
    ].join('\n'),
  );
  fs.writeFileSync(contractPath, seededVerificationContractYaml('deployment-capability'));
  fs.writeFileSync(
    gatePath,
    [
      'summary: Required verification is blocked by missing deployment credentials.',
      'iteration: 3',
      'questions:',
      '  - index: 1',
      `    prompt: ${verificationDecisionPrompt}`,
      '    answer: ""',
      'verification:',
      '  blockers:',
      '    - item_id: deployment-capability',
      '      name: Deployment smoke test',
      `      repo_name: ${repoName}`,
      '      command: make deploy-smoke',
      '      reason: missing declared capability "deployment credentials"',
      '      capabilities:',
      '        - deployment credentials',
      '      remediation: Make deployment credentials available, then retry verification.',
      'verification_decision:',
      `  contract_path: ${trustedContractPath}`,
      '  contract_revision: 1',
      '  item_ids:',
      '    - deployment-capability',
      '  allowed_actions:',
      '    - WAIVE',
      `    - ${retryAfterAuth}`,
      '',
    ].join('\n'),
  );

  const runPath = path.join(runDir, 'run.yaml');
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = upsertYamlScalar(runYaml, 'current_iteration', '3');
  runYaml = upsertYamlScalar(runYaml, 'pending_need_user_input_path', trustedGatePath);
  runYaml = upsertYamlMapScalar(runYaml, 'artifacts', 'plan', planPath);
  fs.writeFileSync(runPath, runYaml);

  const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'NeedUserInput');
  // Phase does not implement string YAML marshaling; PhaseImplement persists as 2.
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
  fs.writeFileSync(featurePath, featureYaml);
  return trustedGatePath;
}
