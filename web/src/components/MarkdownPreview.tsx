import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

// Read-only markdown previewer for artifact bodies. Built on
// react-markdown + remark-gfm so the AST drives the React tree
// directly — no innerHTML, no rehype-raw, and no inline event
// handlers, satisfying the project's HTML/JS security rules
// (~/.claude/rules/security-codeql.md).
//
// Visual styling lives in a single `.markdown-body` rule block in
// src/styles/index.css and uses only existing design tokens so dark
// mode stays automatic.
//
// Anchors are normalised through a URL allow-list (http / https /
// mailto / relative). Anything else (javascript:, data:, vbscript:)
// is dropped so the rendered tree can't house an XSS sink even if the
// artifact body is later hand-crafted by an agent.

const ALLOWED_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

/** Returns the input URL when safe to render, otherwise an empty
 *  string so react-markdown emits a non-clickable anchor. */
export function normaliseHref(input: string): string {
  if (!input) return "";
  const trimmed = input.trim();
  if (trimmed === "") return "";
  // Relative URLs (anchors, paths) have no scheme — let them through.
  // The leading characters that matter for relative refs are #, /, and
  // ?, plus any plain word followed by a slash (e.g. docs/foo.md).
  if (
    trimmed.startsWith("#") ||
    trimmed.startsWith("/") ||
    trimmed.startsWith("?") ||
    !/^[a-z][a-z0-9+.-]*:/i.test(trimmed)
  ) {
    return trimmed;
  }
  try {
    // The base only matters for relative inputs, which we've already
    // returned above. For absolute URLs the URL constructor parses the
    // scheme; we compare protocol against an allow-list.
    const u = new URL(trimmed);
    return ALLOWED_PROTOCOLS.has(u.protocol) ? trimmed : "";
  } catch {
    // Malformed URL — refuse to render it as a link.
    return "";
  }
}

export function MarkdownPreview({ source }: { source: string }) {
  return (
    <div className="markdown-body">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        urlTransform={normaliseHref}
        components={{
          // Force-safe target on every link. react-markdown handles
          // the href escape; we only override behavioural attrs.
          a: ({ href, children, ...rest }) => {
            const safe = href ?? "";
            const external = /^https?:/i.test(safe);
            return (
              <a
                href={safe}
                target={external ? "_blank" : undefined}
                rel={external ? "noopener noreferrer" : undefined}
                {...rest}
              >
                {children}
              </a>
            );
          },
        }}
      >
        {source}
      </ReactMarkdown>
    </div>
  );
}
