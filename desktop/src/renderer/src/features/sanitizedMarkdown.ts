/**
 * Minimal, safe-by-construction Markdown renderer.
 *
 * Every character is HTML-escaped before any structure is applied, so raw
 * HTML, scripts, event handlers, and unsafe URLs can never reach the DOM.
 * Only a known-safe subset of Markdown is supported: headings, paragraphs,
 * unordered/ordered lists, fenced code blocks, inline code, bold, italic,
 * tables, and links (rendered as inert text, never as navigable anchors).
 */

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

/** Renders inline Markdown: **bold**, *italic*, `code`, and [text](url). */
function renderInline(text: string): string {
  let result = escapeHtml(text);
  // Inline code — process first so Markdown inside code is literal.
  result = result.replace(/`([^`]+)`/g, '<code>$1</code>');
  // Bold.
  result = result.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  // Italic.
  result = result.replace(/(?<!\*)\*([^*]+)\*(?!\*)/g, '<em>$1</em>');
  // Links — rendered as inert text with the URL shown, never navigable.
  result = result.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<span class="md-link">$1 ($2)</span>');
  return result;
}

interface Block {
  tag: string;
  content: string;
}

/** Safe line accessor — returns '' for out-of-bounds indices. */
function at(lines: string[], i: number): string {
  return lines[i] ?? '';
}

/** Parses block-level Markdown into a sequence of safe HTML blocks. */
function parseBlocks(lines: string[]): Block[] {
  const blocks: Block[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = at(lines, i);

    // Skip blank lines between blocks.
    if (line.trim() === '') {
      i++;
      continue;
    }

    // Fenced code block.
    if (line.startsWith('```')) {
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !at(lines, i).startsWith('```')) {
        codeLines.push(escapeHtml(at(lines, i)));
        i++;
      }
      i++; // consume closing fence
      blocks.push({ tag: 'pre', content: `<code>${codeLines.join('\n')}</code>` });
      continue;
    }

    // Heading.
    const heading = line.match(/^(#{1,6})\s+(.*)$/);
    if (heading && heading[1] !== undefined && heading[2] !== undefined) {
      const level = heading[1].length;
      blocks.push({
        tag: `h${level}`,
        content: renderInline(heading[2]),
      });
      i++;
      continue;
    }

    // Table — header row followed by a separator row of dashes.
    if (line.includes('|') && /^\s*\|?[\s:|-]+\|?\s*$/.test(at(lines, i + 1))) {
      const headerCells = splitTableRow(line);
      i += 2; // skip header + separator
      const bodyRows: string[][] = [];
      while (i < lines.length && at(lines, i).includes('|') && at(lines, i).trim() !== '') {
        bodyRows.push(splitTableRow(at(lines, i)));
        i++;
      }
      const headerHtml = headerCells.map((c) => `<th>${renderInline(c)}</th>`).join('');
      const bodyHtml = bodyRows
        .map((row) => `<tr>${row.map((c) => `<td>${renderInline(c)}</td>`).join('')}</tr>`)
        .join('');
      blocks.push({
        tag: 'table',
        content: `<thead><tr>${headerHtml}</tr></thead><tbody>${bodyHtml}</tbody>`,
      });
      continue;
    }

    // Unordered list.
    if (/^\s*[-*]\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*[-*]\s+/.test(at(lines, i))) {
        items.push(`<li>${renderInline(at(lines, i).replace(/^\s*[-*]\s+/, ''))}</li>`);
        i++;
      }
      blocks.push({ tag: 'ul', content: items.join('') });
      continue;
    }

    // Ordered list.
    if (/^\s*\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*\d+\.\s+/.test(at(lines, i))) {
        items.push(`<li>${renderInline(at(lines, i).replace(/^\s*\d+\.\s+/, ''))}</li>`);
        i++;
      }
      blocks.push({ tag: 'ol', content: items.join('') });
      continue;
    }

    // Paragraph — consume consecutive non-blank, non-structural lines.
    const paraLines: string[] = [];
    while (
      i < lines.length &&
      at(lines, i).trim() !== '' &&
      !at(lines, i).startsWith('```') &&
      !/^#{1,6}\s+/.test(at(lines, i)) &&
      !/^\s*[-*]\s+/.test(at(lines, i)) &&
      !/^\s*\d+\.\s+/.test(at(lines, i)) &&
      !(at(lines, i).includes('|') && /^\s*\|?[\s:|-]+\|?\s*$/.test(at(lines, i + 1)))
    ) {
      paraLines.push(at(lines, i));
      i++;
    }
    blocks.push({ tag: 'p', content: renderInline(paraLines.join(' ')) });
  }

  return blocks;
}

function splitTableRow(line: string): string[] {
  return line
    .replace(/^\s*\|?\s*/, '')
    .replace(/\s*\|?\s*$/, '')
    .split(/\s*\|\s*/)
    .map((cell) => cell.trim());
}

/** Renders untrusted Markdown text into a sanitized HTML string. */
export function renderSanitizedMarkdown(text: string): string {
  const blocks = parseBlocks(text.split('\n'));
  return blocks.map((block) => `<${block.tag}>${block.content}</${block.tag}>`).join('\n');
}
