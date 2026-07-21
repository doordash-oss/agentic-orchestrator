/**
 * Completion fixture helpers: YAML manipulation and worktree seeding for
 * completion journey specs. Extracted to eliminate duplicated YAML machinery
 * across completion journey specs.
 */
import fs from 'node:fs';
import path from 'node:path';
import type { JourneyWorld } from './world';
import { escapeRegExp, replaceTopLevelBlock, upsertYamlScalar } from './yaml';

// Re-export the canonical YAML splicing helpers so journey specs that import
// them from ./completionFixture keep a single source of truth in ./yaml.
export { replaceTopLevelBlock, upsertYamlScalar };

export interface CompletionWorktrees {
  worktrees: Record<string, string>;
  sources: Record<string, string>;
}

/** Parses a named field from each repo entry in feature.yaml's repos block into a name→value map. */
export function parseFeatureRepoField(yaml: string, fieldPattern: RegExp): Record<string, string> {
  const result: Record<string, string> = {};
  const lines = yaml.replaceAll('\r\n', '\n').split('\n');
  let inRepos = false;
  let current = '';
  for (const line of lines) {
    if (/^repos:\s*$/.test(line)) {
      inRepos = true;
      continue;
    }
    if (inRepos && /^[A-Za-z_]+:/.test(line)) {
      break;
    }
    const name = line.match(/^\s*-\s+name:\s*(.+?)\s*$/);
    if (name !== null) {
      current = cleanYamlValue(name[1]!);
      continue;
    }
    const field = line.match(fieldPattern);
    if (field !== null && current !== '') {
      result[current] = cleanYamlValue(field[1]!);
    }
  }
  return result;
}

/** Parses the repos block from feature.yaml into a name→worktree_path map. */
export function parseFeatureRepos(yaml: string): Record<string, string> {
  return parseFeatureRepoField(yaml, /^\s+worktree_path:\s*(.+?)\s*$/);
}

/** Parses the repos block from feature.yaml into a name→source repo path map. */
export function parseFeatureRepoSources(yaml: string): Record<string, string> {
  return parseFeatureRepoField(yaml, /^\s+path:\s*(.+?)\s*$/);
}

export function cleanYamlValue(value: string): string {
  return value.trim().replace(/^['"]|['"]$/g, '');
}

/** Writes a file into a worktree, creating parent directories as needed. */
export function writeWorktreeChange(worktree: string, relPath: string, content: string): void {
  if (!path.isAbsolute(worktree) || !fs.statSync(worktree).isDirectory()) {
    throw new Error(`invalid worktree path ${worktree}`);
  }
  const target = path.join(worktree, relPath);
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, content);
}

/** Returns the active run number from feature.yaml. */
export function activeRunNumber(featureYaml: string): number {
  const match = featureYaml.match(/^active_run:\s*(\d+)/m);
  return match === null ? 1 : Number.parseInt(match[1]!, 10);
}

/** Finds the line range of a repo block in feature.yaml. Returns [start, end) or null. */
function repoBlockBounds(lines: string[], repoName: string): [number, number] | null {
  const start = lines.findIndex((line) => {
    const match = line.match(/^(\s*)-\s+name:\s*(.+?)\s*$/);
    return match !== null && cleanYamlValue(match[2]!) === repoName;
  });
  if (start < 0) return null;
  const itemIndent = lines[start]!.match(/^(\s*)-/)?.[1] ?? '    ';
  let end = start + 1;
  while (
    end < lines.length &&
    !new RegExp(`^${escapeRegExp(itemIndent)}-\\s+name:`).test(lines[end]!) &&
    !/^[A-Za-z_]+:/.test(lines[end]!)
  ) {
    end += 1;
  }
  return [start, end];
}

/** Sets the publishable flag on a repo in feature.yaml. */
export function setRepoPublishable(yaml: string, repoName: string, publishable: boolean): string {
  const hadTrailingNewline = yaml.endsWith('\n');
  const lines = yaml.replaceAll('\r\n', '\n').split('\n');
  if (hadTrailingNewline) lines.pop();

  const bounds = repoBlockBounds(lines, repoName);
  if (bounds === null) return yaml;
  const [start, end] = bounds;
  const itemIndent = lines[start]!.match(/^(\s*)-/)?.[1] ?? '    ';
  const fieldIndent = `${itemIndent}  `;

  const block = lines.slice(start + 1, end);
  let insertOffset = block.findIndex((line) => /^\s+publishable:/.test(line));
  if (insertOffset < 0) {
    const baseBranchOffset = block.findIndex((line) => /^\s+base_branch:/.test(line));
    insertOffset = baseBranchOffset >= 0 ? baseBranchOffset + 1 : block.length;
  }
  const cleanedBlock = block.filter((line) => !/^\s+publishable:/.test(line));
  const removedBeforeInsert = block
    .slice(0, insertOffset)
    .filter((line) => /^\s+publishable:/.test(line)).length;
  cleanedBlock.splice(
    Math.max(0, insertOffset - removedBeforeInsert),
    0,
    `${fieldIndent}publishable: ${publishable}`,
  );

  const next = [...lines.slice(0, start + 1), ...cleanedBlock, ...lines.slice(end)].join('\n');
  return hadTrailingNewline ? `${next}\n` : next;
}

/** Sets the base_branch field on a repo in feature.yaml. */
export function setRepoBaseBranch(yaml: string, repoName: string, baseBranch: string): string {
  const hadTrailingNewline = yaml.endsWith('\n');
  const lines = yaml.replaceAll('\r\n', '\n').split('\n');
  if (hadTrailingNewline) lines.pop();

  const bounds = repoBlockBounds(lines, repoName);
  if (bounds === null) return yaml;
  const [start, end] = bounds;
  const itemIndent = lines[start]!.match(/^(\s*)-/)?.[1] ?? '    ';
  const fieldIndent = `${itemIndent}  `;

  const block = lines.slice(start + 1, end).filter((line) => !/^\s+base_branch:/.test(line));
  block.push(`${fieldIndent}base_branch: ${baseBranch}`);

  const next = [...lines.slice(0, start + 1), ...block, ...lines.slice(end)].join('\n');
  return hadTrailingNewline ? `${next}\n` : next;
}

/** Reads and returns the feature.yaml path for a feature. */
export function featureYamlPath(world: JourneyWorld, featureId: string): string {
  return path.join(world.stateDir, featureId, 'feature.yaml');
}

/** Reads and returns the active run.yaml path for a feature. */
export function activeRunYamlPath(world: JourneyWorld, featureId: string): string {
  const featureYaml = fs.readFileSync(featureYamlPath(world, featureId), 'utf8');
  const active = activeRunNumber(featureYaml);
  return path.join(
    world.stateDir,
    featureId,
    'runs',
    `run-${String(active).padStart(3, '0')}`,
    'run.yaml',
  );
}

/** Removes failure_type and last_error lines from run.yaml. */
export function clearRunFailures(runYaml: string): string {
  return runYaml.replace(/^failure_type:.*$\n?/m, '').replace(/^last_error:.*$\n?/m, '');
}
