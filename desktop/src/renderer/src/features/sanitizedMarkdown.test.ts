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
import { renderSanitizedMarkdown } from './sanitizedMarkdown';

describe('renderSanitizedMarkdown', () => {
  it('renders headings as real HTML elements', () => {
    const html = renderSanitizedMarkdown('# Title\n\n## Subtitle\n\n### Section');
    expect(html).toContain('<h1>Title</h1>');
    expect(html).toContain('<h2>Subtitle</h2>');
    expect(html).toContain('<h3>Section</h3>');
  });

  it('renders lists as real HTML elements', () => {
    const html = renderSanitizedMarkdown('- One\n- Two\n- Three');
    expect(html).toContain('<ul>');
    expect(html).toContain('<li>One</li>');
    expect(html).toContain('<li>Two</li>');
    expect(html).toContain('<li>Three</li>');
  });

  it('renders ordered lists', () => {
    const html = renderSanitizedMarkdown('1. First\n2. Second');
    expect(html).toContain('<ol>');
    expect(html).toContain('<li>First</li>');
    expect(html).toContain('<li>Second</li>');
  });

  it('renders fenced code blocks', () => {
    const html = renderSanitizedMarkdown('```js\nconst x = 1;\n```');
    expect(html).toContain('<pre><code>');
    expect(html).toContain('const x = 1;');
  });

  it('renders tables', () => {
    const html = renderSanitizedMarkdown('| A | B |\n|---|---|\n| 1 | 2 |');
    expect(html).toContain('<table>');
    expect(html).toContain('<th>A</th>');
    expect(html).toContain('<td>1</td>');
  });

  it('renders bold and italic', () => {
    const html = renderSanitizedMarkdown('**bold** and *italic*');
    expect(html).toContain('<strong>bold</strong>');
    expect(html).toContain('<em>italic</em>');
  });

  it('escapes raw HTML to prevent script injection', () => {
    const html = renderSanitizedMarkdown('<script>alert(1)</script>');
    expect(html).not.toContain('<script>');
    expect(html).toContain('&lt;script&gt;');
  });

  it('renders links as inert text, never as anchors', () => {
    const html = renderSanitizedMarkdown('[click](javascript:alert(1))');
    expect(html).not.toContain('<a');
    expect(html).not.toContain('href');
    expect(html).toContain('click');
    expect(html).toContain('javascript:alert(1)');
  });

  it('renders inline code', () => {
    const html = renderSanitizedMarkdown('Use `npm test` to run');
    expect(html).toContain('<code>npm test</code>');
  });

  it('renders paragraphs for plain text', () => {
    const html = renderSanitizedMarkdown('Just a paragraph.');
    expect(html).toContain('<p>Just a paragraph.</p>');
  });

  it('terminates a paragraph at a table start with no blank line between them', () => {
    const html = renderSanitizedMarkdown('Some text.\n| A | B |\n|---|---|\n| 1 | 2 |');
    expect(html).toContain('<p>Some text.</p>');
    expect(html).toContain('<table>');
    expect(html).toContain('<th>A</th>');
  });
});
