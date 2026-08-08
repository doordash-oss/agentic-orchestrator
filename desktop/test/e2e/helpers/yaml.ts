/**
 * Shared YAML splicing helpers for e2e fixture seeding. All journey specs that
 * mutate feature.yaml/run.yaml scalars or top-level blocks import from here so
 * the regex escaping and block-replacement semantics stay in one place. The
 * scalar upsert escapes regex metacharacters in the key so field names
 * containing dots or brackets are matched literally.
 */

/** Escapes regex metacharacters in a literal string for use in a RegExp. */
export function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Upserts a top-level YAML scalar key. */
export function upsertYamlScalar(yaml: string, key: string, value: string): string {
  const line = `${key}: ${value}`;
  const pattern = new RegExp(`^${escapeRegExp(key)}:.*$`, 'm');
  return pattern.test(yaml)
    ? yaml.replace(pattern, line)
    : `${yaml.endsWith('\n') ? yaml : `${yaml}\n`}${line}\n`;
}

/** Upserts a scalar entry nested one level under a top-level YAML map key. */
export function upsertYamlMapScalar(
  yaml: string,
  mapKey: string,
  key: string,
  value: string,
): string {
  const lines = yaml.split('\n');
  const parentIndex = lines.findIndex((line) => line === `${mapKey}:`);
  if (parentIndex === -1) {
    const suffix = yaml.endsWith('\n') ? '' : '\n';
    return `${yaml}${suffix}${mapKey}:\n  ${key}: ${value}\n`;
  }
  let insertIndex = parentIndex + 1;
  while (insertIndex < lines.length) {
    const line = lines[insertIndex] ?? '';
    if (line !== '' && !line.startsWith(' ')) break;
    if (line.startsWith(`  ${key}:`)) {
      lines[insertIndex] = `  ${key}: ${value}`;
      return lines.join('\n');
    }
    insertIndex += 1;
  }
  lines.splice(insertIndex, 0, `  ${key}: ${value}`);
  return lines.join('\n');
}

/** Replaces a top-level YAML block (key + indented children). */
export function replaceTopLevelBlock(yaml: string, key: string, block: string[]): string {
  const lines = yaml.replaceAll('\r\n', '\n').split('\n');
  const start = lines.findIndex((line) => line === `${key}:` || line.startsWith(`${key}: `));
  if (start < 0) {
    return `${yaml.endsWith('\n') ? yaml : `${yaml}\n`}${block.join('\n')}\n`;
  }
  let end = start + 1;
  while (end < lines.length && (lines[end]!.startsWith(' ') || lines[end]!.trim() === '')) {
    end += 1;
  }
  const next = [...lines.slice(0, start), ...block, ...lines.slice(end)].join('\n');
  return next.endsWith('\n') ? next : `${next}\n`;
}
