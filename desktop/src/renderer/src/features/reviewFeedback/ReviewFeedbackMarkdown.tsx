/**
 * Safe, feedback-only rich text renderer for review comment bodies.
 *
 * Untrusted reviewer Markdown is parsed with a React-native GFM pipeline and
 * emitted as a bounded React tree: every transformation (GFM, syntax classes,
 * sanitization) runs over syntax trees, and no path uses
 * `dangerouslySetInnerHTML`. Author-supplied raw HTML is flattened into inert
 * source text before parsing continues, so markup like `<script>` stays
 * visible but inert. An explicit sanitizer allowlist runs last and permits
 * only the elements, attributes, and syntax-token classes this component
 * needs.
 *
 * Media and navigation never happen here: links and images are turned into
 * explicit review actions routed through the typed preload/main-process
 * external-browser boundary (`window.agentico.openExternal`), with the
 * destination hostname disclosed in place. Everything the main-process policy
 * cannot safely open renders as an inert, labelled placeholder instead.
 *
 * This renderer is deliberately separate from the shared sanitized Markdown
 * preview used elsewhere in the app; that preview is unchanged.
 */
import { useState, type ReactElement, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import { common, createLowlight } from 'lowlight';

/** Fence languages this repository recognizes; everything else is plain code. */
const CURATED_LANGUAGES: ReadonlySet<string> = new Set([
  'bash',
  'css',
  'diff',
  'go',
  'javascript',
  'json',
  'markdown',
  'python',
  'rust',
  'sql',
  'typescript',
  'xml',
  'yaml',
]);

/** Common aliases authors write on fences, mapped onto curated grammars. */
const LANGUAGE_ALIASES: Readonly<Record<string, string>> = {
  js: 'javascript',
  ts: 'typescript',
  tsx: 'typescript',
  jsx: 'javascript',
  sh: 'bash',
  shell: 'bash',
  zsh: 'bash',
  py: 'python',
  golang: 'go',
  yml: 'yaml',
  md: 'markdown',
};

const lowlight = createLowlight(common);

function resolveFenceLanguage(tag: string | undefined): string | null {
  if (tag === undefined) return null;
  const normalized = tag.trim().toLowerCase();
  const aliased = LANGUAGE_ALIASES[normalized] ?? normalized;
  return CURATED_LANGUAGES.has(aliased) ? aliased : null;
}

// --- Hast helpers (syntax classes + sanitizer run over trees, never strings) ---

interface HastText {
  type: 'text';
  value: string;
}

interface HastElement {
  type: 'element';
  tagName: string;
  properties?: { className?: string[] } & Record<string, unknown>;
  children: HastNode[];
}

interface HastRoot {
  type: 'root';
  children: HastNode[];
}

type HastNode = HastText | HastElement | HastRoot;

function elementChildren(node: HastNode): HastElement[] | null {
  return 'children' in node ? node.children.filter((c) => c.type === 'element') : null;
}

function textOf(node: HastNode): string {
  if (node.type === 'text') return node.value;
  if ('children' in node) return node.children.map(textOf).join('');
  return '';
}

function languageOf(code: HastElement): string | undefined {
  const classes = code.properties?.className ?? [];
  for (const cls of classes) {
    if (cls.startsWith('language-')) return cls.slice('language-'.length);
  }
  return undefined;
}

/**
 * Class-based syntax treatment for explicitly named fence languages only.
 * Unknown or untagged fences are left as plain code: no automatic detection,
 * no inline styles, no evaluation, no workers.
 */
function rehypeFeedbackHighlight(): (tree: HastRoot) => void {
  return (tree) => {
    const walk = (node: HastNode): void => {
      if (node.type !== 'element') {
        if ('children' in node) node.children.forEach(walk);
        return;
      }
      if (node.tagName === 'pre') {
        const code = elementChildren(node)?.find((c) => c.tagName === 'code');
        const language = code === undefined ? null : resolveFenceLanguage(languageOf(code));
        if (code !== undefined && language !== null) {
          try {
            const highlighted = lowlight.highlight(language, textOf(code), { prefix: 'hljs-' });
            code.children = highlighted.children as HastNode[];
            code.properties = {
              ...code.properties,
              className: [...(code.properties?.className ?? []), 'hljs'],
            };
          } catch {
            // Grammar rejected the input; leave the fence as plain code.
          }
        }
        node.children.forEach(walk);
        return;
      }
      node.children.forEach(walk);
    };
    walk(tree);
  };
}

/**
 * The sanitizer allowlist for this renderer only. Runs after GFM and syntax
 * transformations. URL-bearing attributes keep the default protocol hygiene
 * (disallowed schemes drop the attribute, which the components below treat as
 * a blocked destination); all link/image destinations are independently
 * validated in the React layer before any external action exists.
 */
const SANITIZE_SCHEMA = {
  ...defaultSchema,
  tagNames: [...(defaultSchema.tagNames ?? []), 'span'],
  attributes: {
    ...defaultSchema.attributes,
    '*': [...(defaultSchema.attributes?.['*'] ?? [])],
    // hljs token classes only; no arbitrary class or style attributes.
    span: [['className', 'hljs', /^hljs-[\w-]+$/]],
    // Replaces the default code rule: only hljs and language-* fence tags.
    code: [['className', 'hljs', /^language-[\w+-]+$/]],
    input: ['checked', 'disabled', 'type', ['className', 'task-list-item-checkbox']],
  },
  // Raw HTML is flattened to text before this stage; strip any residual
  // element rather than interpreting it.
  strip: [
    'script',
    'style',
    'iframe',
    'object',
    'embed',
    'form',
    'textarea',
    'select',
    'button',
    'video',
    'audio',
    'svg',
    'math',
  ],
};

/** A parsed destination, or null when the URL cannot be safely opened. */
interface SafeDestination {
  href: string;
  hostname: string;
}

/**
 * Mirrors the main-process external-browser policy for link/image actions:
 * well-formed absolute https without credentials. Any mismatch fails closed.
 */
function parseSafeDestination(href: string | undefined): SafeDestination | null {
  if (href === undefined) return null;
  let parsed: URL;
  try {
    parsed = new URL(href);
  } catch {
    return null;
  }
  if (parsed.protocol !== 'https:') return null;
  if (parsed.username !== '' || parsed.password !== '') return null;
  if (parsed.hostname === '') return null;
  return { href, hostname: parsed.hostname };
}

/** SVG never crosses the boundary as an image: active content, blocked in place. */
function isSvgDestination(pathname: string): boolean {
  return pathname
    .replace(/[?#].*$/, '')
    .toLowerCase()
    .endsWith('.svg');
}

/** Recursively collect rendered text so accessible names can quote the label. */
function labelText(children: ReactNode): string {
  if (children === null || children === undefined) return '';
  if (typeof children === 'string' || typeof children === 'number') return String(children);
  if (Array.isArray(children)) return children.map(labelText).join('');
  if (typeof children === 'object' && 'props' in children) {
    return labelText((children as { props: { children?: ReactNode } }).props.children);
  }
  return '';
}

interface ExternalLinkProps {
  href?: string;
  children?: ReactNode;
}

/**
 * A Markdown link becomes an explicit review action instead of navigation.
 * Safe destinations render a button that discloses the hostname and opens
 * only through the privileged external-browser boundary; unsafe ones keep
 * their authored label as inert text with a compact blocked status.
 */
function ExternalLink({ href, children }: ExternalLinkProps): ReactElement {
  const [failed, setFailed] = useState(false);
  const destination = parseSafeDestination(href);
  const label = labelText(children);
  if (destination === null || failed) {
    return (
      <span className="review-feedback-md__link-blocked">
        {children}
        <span className="review-feedback-md__blocked-status">Link blocked</span>
      </span>
    );
  }
  return (
    <button
      type="button"
      className="review-feedback-md__link"
      aria-label={`Open link externally: ${label} (${destination.hostname})`}
      onClick={(event) => {
        event.stopPropagation();
        // Fail-closed: a rejected opener downgrades to the inert blocked state.
        void window.agentico
          .openExternal({ url: destination.href })
          .then((result) => {
            if (result.ok !== true) setFailed(true);
          })
          .catch(() => setFailed(true));
      }}
    >
      <span className="review-feedback-md__link-label">{children}</span>
      <span className="review-feedback-md__external-indicator" aria-hidden="true">
        ↗
      </span>
      <span className="review-feedback-md__hostname">{destination.hostname}</span>
    </button>
  );
}

interface ImagePlaceholderProps {
  src?: string;
  alt?: string;
}

/**
 * A Markdown image never becomes an `img` (or any other resource request)
 * inside Electron. Valid non-SVG remote https images render a semantic
 * placeholder with alt text, hostname, and an external action; everything
 * else renders an inert `Image blocked` placeholder that discloses no source.
 */
function ImagePlaceholder({ src, alt }: ImagePlaceholderProps): ReactElement {
  const [failed, setFailed] = useState(false);
  const destination = parseSafeDestination(src);
  let pathname = '';
  if (destination !== null) {
    try {
      pathname = new URL(destination.href).pathname;
    } catch {
      pathname = '';
    }
  }
  if (destination === null || isSvgDestination(pathname) || failed) {
    return (
      <span className="review-feedback-md__image" role="img" aria-label="Image blocked">
        <span className="review-feedback-md__image-alt">{alt === '' ? 'Image' : alt}</span>
        <span className="review-feedback-md__blocked-status">Image blocked</span>
      </span>
    );
  }
  return (
    <span className="review-feedback-md__image" role="group" aria-label="External image">
      <span className="review-feedback-md__image-alt">{alt === '' ? 'Image' : alt}</span>
      <span className="review-feedback-md__hostname">{destination.hostname}</span>
      <button
        type="button"
        className="review-feedback-md__image-action"
        aria-label={`Open image externally: ${alt === '' ? destination.hostname : alt} (${destination.hostname})`}
        onClick={(event) => {
          event.stopPropagation();
          void window.agentico
            .openExternal({ url: destination.href })
            .then((result) => {
              if (result.ok !== true) setFailed(true);
            })
            .catch(() => setFailed(true));
        }}
      >
        Open image externally
        <span className="review-feedback-md__external-indicator" aria-hidden="true">
          ↗
        </span>
      </button>
    </span>
  );
}

interface TaskCheckboxProps {
  checked?: boolean;
  children?: ReactNode;
}

/** GFM task items: read-only, disabled, and clearly not the selection control. */
function TaskCheckbox({ checked }: TaskCheckboxProps): ReactElement {
  return (
    <input
      type="checkbox"
      className="review-feedback-md__task"
      checked={checked ?? false}
      disabled
      readOnly
      aria-label={checked ? 'Task completed (read-only)' : 'Task not completed (read-only)'}
    />
  );
}

/** Card-owned headings must live below the section h3, competing with nothing. */
function CardHeading({ children }: { children?: ReactNode }): ReactElement {
  return <h4 className="review-feedback-md__heading">{children}</h4>;
}

interface TableProps {
  children?: ReactNode;
}

/** Tables are bounded and scroll horizontally rather than stretching the card. */
function BoundedTable({ children }: TableProps): ReactElement {
  return (
    <div className="review-feedback-md__table-scroll">
      <table>{children}</table>
    </div>
  );
}

const COMPONENTS = {
  h1: CardHeading,
  h2: CardHeading,
  h3: CardHeading,
  h4: CardHeading,
  h5: CardHeading,
  h6: CardHeading,
  a: ExternalLink,
  img: ImagePlaceholder,
  input: TaskCheckbox,
  table: BoundedTable,
};

export interface ReviewFeedbackMarkdownProps {
  text: string;
}

export function ReviewFeedbackMarkdown({ text }: ReviewFeedbackMarkdownProps): ReactElement {
  return (
    <div className="review-feedback-md">
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkFlattenRawHtml]}
        rehypePlugins={[rehypeFeedbackHighlight, [rehypeSanitize, SANITIZE_SCHEMA]]}
        components={COMPONENTS}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}

// --- Raw HTML flattening --------------------------------------------------------

interface MdastLike {
  type: string;
  value?: string;
  children?: MdastLike[];
}

/**
 * Raw Markdown HTML stays visible as inert source text: every `html` node is
 * rewritten to a text node carrying the exact authored source, so scripts,
 * event handlers, style injection, and unknown elements are never interpreted.
 */
function remarkFlattenRawHtml(): (tree: MdastLike) => void {
  return (tree) => {
    const walk = (node: MdastLike): void => {
      if (node.type === 'html') {
        node.type = 'text';
        return;
      }
      node.children?.forEach(walk);
    };
    walk(tree);
  };
}

export const __testing = { parseSafeDestination, resolveFenceLanguage };
