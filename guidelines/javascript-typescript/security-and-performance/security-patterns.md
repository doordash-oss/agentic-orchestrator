# Security Patterns

## Cross-Site Scripting (XSS)

XSS attacks inject malicious scripts through user-controlled data. Prevention
is about ensuring user data is never interpreted as code.

### DOM Injection

```typescript
// VULNERABLE — user input interpreted as HTML:
element.innerHTML = userInput;
document.write(userInput);

// SAFE — textContent is never parsed as HTML:
element.textContent = userInput;

// SAFE — use framework templating (React, Vue, Svelte auto-escape):
return <div>{userInput}</div>; // React escapes by default
```

### `dangerouslySetInnerHTML` (React)

React's escape hatch for raw HTML. Only use with sanitized content:

```typescript
// DANGEROUS — only use with trusted or sanitized content:
<div dangerouslySetInnerHTML={{ __html: sanitize(html) }} />

// Sanitize with DOMPurify:
import DOMPurify from "dompurify";
const clean = DOMPurify.sanitize(dirtyHtml);
```

### URL Injection

```typescript
// VULNERABLE — javascript: protocol executes code:
<a href={userUrl}>Link</a>  // if userUrl = "javascript:alert(1)"

// SAFE — validate URL protocol:
function isSafeUrl(url: string): boolean {
  try {
    const parsed = new URL(url);
    return ["http:", "https:", "mailto:"].includes(parsed.protocol);
  } catch {
    return false;
  }
}
```

## Prototype Pollution

Prototype pollution modifies `Object.prototype`, affecting all objects in the
runtime. It occurs when merging or parsing untrusted data.

### Prevention

```typescript
// Use Object.create(null) for untrusted data maps:
const lookup = Object.create(null);
lookup[untrustedKey] = value; // safe — no prototype chain

// Filter dangerous keys when processing external data:
const FORBIDDEN_KEYS = new Set(["__proto__", "constructor", "prototype"]);

function safeMerge(target: Record<string, unknown>, source: Record<string, unknown>) {
  for (const key of Object.keys(source)) {
    if (FORBIDDEN_KEYS.has(key)) continue;
    target[key] = source[key];
  }
  return target;
}

// Use Map instead of plain objects for dynamic keys:
const userPrefs = new Map<string, string>();
userPrefs.set(untrustedKey, value); // immune to prototype pollution

// Freeze prototypes in high-security contexts:
Object.freeze(Object.prototype);
```

### Schema Validation Prevents Pollution

Validating with Zod strips unrecognized keys by default:

```typescript
const schema = z.object({ name: z.string(), age: z.number() });
const safe = schema.parse(untrustedInput);
// __proto__, constructor, etc. are stripped
```

## Dependency Security

### npm audit

Run `npm audit` in CI to catch known vulnerabilities:

```json
{
  "scripts": {
    "preinstall": "npx npm-force-resolutions",
    "audit": "npm audit --audit-level=high"
  }
}
```

### Supply Chain Protection

- **Lock dependencies** — always commit `package-lock.json` or `pnpm-lock.yaml`
- **Pin versions** in production — use exact versions, not ranges
- **Review new dependencies** — check download counts, maintenance, and bundle size on npm
- **Use `npm audit signatures`** — verifies package provenance

### Subresource Integrity (SRI)

For CDN-loaded scripts, use integrity hashes:

```html
<script
  src="https://cdn.example.com/lib.js"
  integrity="sha384-abc123..."
  crossorigin="anonymous"
></script>
```

## Content Security Policy (CSP)

CSP prevents inline scripts and restricts resource origins:

```http
Content-Security-Policy:
  default-src 'self';
  script-src 'self' 'nonce-abc123';
  style-src 'self' 'unsafe-inline';
  img-src 'self' data: https:;
  connect-src 'self' https://api.example.com;
```

**Never use `'unsafe-eval'`** — it allows `eval()`, `Function()`, and
template string injection. If a library requires it, find an alternative.

## Secrets Management

```typescript
// NEVER bundle secrets in client-side code:
const apiKey = process.env.API_KEY; // only in server-side code

// Use environment variables for server-side secrets
// Use secure backend endpoints to proxy authenticated requests
```

- Never commit `.env` files with real secrets
- Use `.env.example` with placeholder values
- Rotate secrets that are accidentally exposed

## Safe eval Alternatives

```typescript
// NEVER:
eval(userInput);
new Function(userInput)();

// SAFE alternatives:
// - JSON.parse for data
// - Schema validation for structured input
// - Template literals for string interpolation
// - AST parsers (e.g., acorn) for expression evaluation
```

## ReDoS Prevention

Certain regex patterns cause catastrophic backtracking:

```typescript
// VULNERABLE — exponential backtracking:
const bad = /^(a+)+$/;
bad.test("aaaaaaaaaaaaaaaaaaaaX"); // hangs

// SAFE — avoid nested quantifiers:
const good = /^a+$/;
```

Use `re2` (Google's RE2 engine) for regexes on untrusted input — it guarantees
linear-time matching.

## Best Practices

- **Never use `innerHTML` with user data** — use `textContent` or framework escaping
- **Validate URLs** before using them in `href` or `src`
- **Use `Map` or `Object.create(null)`** for dynamic key-value lookups
- **Validate all external data with Zod** — strips prototype pollution keys
- **Run `npm audit`** in CI — block deploys on high-severity findings
- **Set CSP headers** — prevent inline scripts and restrict resource origins
- **Never bundle secrets** — use environment variables and backend proxies
