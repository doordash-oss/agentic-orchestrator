/**
 * Focused tests for the safe, feedback-only GFM renderer: approved
 * structures, adversarial content, curated syntax classes, read-only task
 * items, and the link/image external trust boundary.
 */
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ReviewFeedbackMarkdown } from './ReviewFeedbackMarkdown';

const openExternal = vi.fn<(request: { url: string }) => Promise<{ ok: boolean }>>();

beforeEach(() => {
  openExternal.mockReset();
  openExternal.mockResolvedValue({ ok: true });
  Object.assign(window, { agentico: { openExternal } });
});

describe('approved GFM structures', () => {
  it('renders headings, emphasis, lists, blockquotes, tables, and inline code', () => {
    const { container } = render(
      <ReviewFeedbackMarkdown
        text={[
          '# Summary',
          '',
          'This is **bold** and *italic* and `inline()`.',
          '',
          '- first',
          '  - nested',
          '- second',
          '',
          '> quoted context',
          '',
          '| col | val |',
          '| --- | --- |',
          '| a   | b   |',
          '',
          '~~superseded~~',
        ].join('\n')}
      />,
    );
    expect(screen.getByRole('heading', { level: 4, name: 'Summary' })).toBeInTheDocument();
    expect(screen.getByText('quoted context')).toBeTruthy();
    expect(screen.getByRole('table')).toBeInTheDocument();
    expect(screen.getAllByRole('listitem').length).toBeGreaterThanOrEqual(3);
    expect(container.querySelector('strong')).not.toBeNull();
    expect(container.querySelector('em')).not.toBeNull();
    expect(container.querySelector('del')).not.toBeNull();
    expect(container.querySelector('code')).not.toBeNull();
  });

  it('never introduces a page-level heading', () => {
    render(<ReviewFeedbackMarkdown text={'# Big\n\n## Sub'} />);
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { level: 2 })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { level: 3 })).not.toBeInTheDocument();
    expect(screen.getAllByRole('heading', { level: 4 })).toHaveLength(2);
  });

  it('renders read-only task list items distinct from selection semantics', () => {
    render(<ReviewFeedbackMarkdown text={'- [x] done\n- [ ] pending'} />);
    const done = screen.getByRole('checkbox', { name: 'Task completed (read-only)' });
    const pending = screen.getByRole('checkbox', { name: 'Task not completed (read-only)' });
    expect(done).toBeChecked();
    expect(done).toBeDisabled();
    expect(pending).not.toBeChecked();
    expect(pending).toBeDisabled();
  });
});

describe('adversarial content', () => {
  it('keeps raw HTML visible as inert source text', () => {
    render(
      <ReviewFeedbackMarkdown
        text={'Look at this: <script>alert(1)</script> and <b onclick="x()">fragile</b>'}
      />,
    );
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
    expect(screen.getByText(/onclick/)).toBeInTheDocument();
    // Nothing executable or author-styled was interpreted into the tree.
    expect(document.querySelector('script')).toBeNull();
    expect(document.querySelector('b[onclick]')).toBeNull();
  });

  it('blocks javascript:, data:, and protocol-relative links with their label kept', () => {
    render(
      <ReviewFeedbackMarkdown
        text={[
          '[click me](javascript:alert(1))',
          '[blobby](data:text/html;<h1>hi</h1>)',
          '[relative](/docs/x)',
          '[proto](//evil.example.com)',
        ].join('\n\n')}
      />,
    );
    expect(screen.getAllByText('Link blocked')).toHaveLength(4);
    expect(screen.getByText('click me')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Open link externally/ })).not.toBeInTheDocument();
    expect(document.querySelector('a')).toBeNull();
  });

  it('never interprets unknown elements or attributes as authored DOM', () => {
    render(
      <ReviewFeedbackMarkdown
        text={'<iframe src="https://evil.example.com"></iframe>\n\n<marquee>whee</marquee>'}
      />,
    );
    expect(document.querySelector('iframe')).toBeNull();
    expect(document.querySelector('marquee')).toBeNull();
    expect(screen.getByText(/<iframe/)).toBeInTheDocument();
  });
});

describe('fenced code', () => {
  it('highlights curated languages via classes only', () => {
    const { container } = render(
      <ReviewFeedbackMarkdown text={'```go\nfunc main() { return }\n```'} />,
    );
    const code = container.querySelector('pre code');
    expect(code?.classList.contains('hljs')).toBe(true);
    expect(code?.querySelector('.hljs-keyword')).not.toBeNull();
    // No inline styles, no workers.
    expect(code?.getAttribute('style')).toBeNull();
  });

  it.each(['brainfuck', 'cobol', ''])(
    'renders untagged or unsupported fences as plain code: %s',
    (language) => {
      const { container } = render(
        <ReviewFeedbackMarkdown text={`\`\`\`${language}\nlet it be plain\n\`\`\``} />,
      );
      const code = container.querySelector('pre code');
      expect(code?.textContent).toContain('let it be plain');
      expect(code?.querySelector('[class^="hljs-"]')).toBeNull();
    },
  );
});

describe('external link actions', () => {
  it('discloses the hostname and opens through the typed boundary', async () => {
    const user = userEvent.setup();
    render(<ReviewFeedbackMarkdown text="See [the docs](https://docs.example.com/guide)" />);
    const action = screen.getByRole('button', {
      name: 'Open link externally: the docs (docs.example.com)',
    });
    expect(action).toHaveTextContent('docs.example.com');
    await user.click(action);
    expect(openExternal).toHaveBeenCalledWith({ url: 'https://docs.example.com/guide' });
  });

  it('downgrades to inert blocked text when the opener rejects', async () => {
    openExternal.mockRejectedValueOnce(new Error('deny'));
    const user = userEvent.setup();
    render(<ReviewFeedbackMarkdown text="See [the docs](https://docs.example.com/guide)" />);
    await user.click(screen.getByRole('button', { name: /Open link externally/ }));
    expect(await screen.findByText('Link blocked')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Open link externally/ })).not.toBeInTheDocument();
  });

  it('blocks credential-bearing https links without disclosing the target', () => {
    render(<ReviewFeedbackMarkdown text="[safe](https://user:pass@github.com/x)" />);
    expect(screen.getByText('Link blocked')).toBeInTheDocument();
    expect(screen.getByText('safe')).toBeInTheDocument();
    expect(screen.queryByText(/user:pass/)).not.toBeInTheDocument();
  });
});

describe('image placeholders', () => {
  it('never creates an img element or resource request', () => {
    render(<ReviewFeedbackMarkdown text="![diagram](https://images.example.com/diagram.png)" />);
    expect(document.querySelector('img')).toBeNull();
    expect(document.querySelector('object')).toBeNull();
    expect(document.querySelector('embed')).toBeNull();
  });

  it('renders alt text, hostname, and an external action for valid https images', async () => {
    const user = userEvent.setup();
    render(
      <ReviewFeedbackMarkdown text="![failure screenshot](https://images.example.com/shot.png)" />,
    );
    const action = screen.getByRole('button', {
      name: 'Open image externally: failure screenshot (images.example.com)',
    });
    expect(screen.getByText('failure screenshot')).toBeInTheDocument();
    expect(screen.getByText('images.example.com')).toBeInTheDocument();
    await user.click(action);
    expect(openExternal).toHaveBeenCalledWith({ url: 'https://images.example.com/shot.png' });
  });

  it.each([
    ['svg', 'https://images.example.com/icon.svg'],
    ['http', 'http://images.example.com/shot.png'],
    ['data URI', 'data:image/png;base64,xxxx'],
    ['blob', 'blob:https://app.example.com/123'],
    ['file', 'file:///etc/passwd.png'],
    ['credentials', 'https://user:pass@images.example.com/shot.png'],
    ['relative', '/images/shot.png'],
  ])('renders an inert blocked placeholder for %s sources', (_kind, src) => {
    render(<ReviewFeedbackMarkdown text={`![alt text](${src})`} />);
    expect(screen.getByText('Image blocked')).toBeInTheDocument();
    expect(screen.getByText('alt text')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Open image externally/ })).not.toBeInTheDocument();
    expect(document.querySelector('img')).toBeNull();
    // No source disclosure of unsafe targets.
    if (!src.startsWith('relative')) {
      expect(screen.queryByText(new RegExp(src.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeNull();
    }
  });

  it('downgrades a valid image placeholder when the opener reports failure', async () => {
    openExternal.mockResolvedValueOnce({ ok: false });
    const user = userEvent.setup();
    render(<ReviewFeedbackMarkdown text="![shot](https://images.example.com/s.png)" />);
    await user.click(screen.getByRole('button', { name: /Open image externally/ }));
    expect(await screen.findByText('Image blocked')).toBeInTheDocument();
  });
});
