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

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/*
 * A `var(--x)` naming a custom property nobody declares is invalid at
 * computed-value time: the whole declaration falls back to `initial`, so a
 * card silently loses its border and a button silently becomes the
 * user-agent default. Nothing in a unit or DOM test notices, because the
 * markup and the class names are all correct — only a screenshot shows it.
 * This test is the cheap standing check: every bare reference must resolve
 * against a declaration in the stylesheets. References that carry an
 * explicit fallback (`var(--x, …)`) are deliberate optional overrides and
 * are exempt, since they degrade to a real value.
 */

const STYLESHEETS = ['./tokens.css', './app.css'] as const;

function read(relative: string): string {
  return readFileSync(fileURLToPath(new URL(relative, import.meta.url)), 'utf8');
}

const sources = STYLESHEETS.map((path) => ({ path, text: read(path) }));

function declaredProperties(): Set<string> {
  const declared = new Set<string>();
  for (const { text } of sources) {
    for (const [, property] of text.matchAll(/(--[A-Za-z0-9-]+)\s*:/g)) {
      if (property !== undefined) declared.add(property);
    }
  }
  return declared;
}

/** Every `var(--x)` with no fallback, as `path:line --x`. */
function bareReferences(): { property: string; where: string }[] {
  const references: { property: string; where: string }[] = [];
  for (const { path, text } of sources) {
    text.split('\n').forEach((line, index) => {
      for (const [, property] of line.matchAll(/var\(\s*(--[A-Za-z0-9-]+)\s*\)/g)) {
        if (property !== undefined) references.push({ property, where: `${path}:${index + 1}` });
      }
    });
  }
  return references;
}

describe('renderer stylesheets', () => {
  it('declares every custom property they reference without a fallback', () => {
    const declared = declaredProperties();
    const dangling = bareReferences()
      .filter((reference) => !declared.has(reference.property))
      .map((reference) => `${reference.where} ${reference.property}`);
    expect(dangling).toEqual([]);
  });

  it('sees the properties it is checking against', () => {
    // A regex that stopped matching would make the check above vacuous.
    expect(declaredProperties().has('--hairline')).toBe(true);
    expect(bareReferences().length).toBeGreaterThan(100);
  });
});
