# Fetch and Network

## Fetch API Basics

`fetch` is the standard HTTP client for browsers and Node.js (18+):

```typescript
const response = await fetch("https://api.example.com/users");

// IMPORTANT: fetch does NOT throw on HTTP errors (4xx, 5xx)
if (!response.ok) {
  throw new HttpError(response.status, response.statusText);
}

const users = await response.json();
```

### Common Pitfall: No Automatic Error Throwing

```typescript
// Bug — fetch "succeeds" with a 404:
const data = await fetch("/api/missing").then((r) => r.json());
// data could be an error response body, not what you expect

// Correct — always check response.ok:
const response = await fetch("/api/users");
if (!response.ok) {
  throw new HttpError(response.status, await response.text());
}
return response.json();
```

## Request Options

```typescript
const response = await fetch("https://api.example.com/users", {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    Authorization: `Bearer ${token}`,
  },
  body: JSON.stringify({ name: "Alice", email: "alice@example.com" }),
  signal: AbortSignal.timeout(5000), // timeout
});
```

## Typed Fetch Wrapper

Create a type-safe wrapper that handles common concerns:

```typescript
async function api<T>(
  path: string,
  options: RequestInit & { schema?: z.ZodSchema<T> } = {},
): Promise<T> {
  const { schema, ...fetchOptions } = options;
  const url = `${API_BASE_URL}${path}`;

  const response = await fetch(url, {
    ...fetchOptions,
    headers: {
      "Content-Type": "application/json",
      ...fetchOptions.headers,
    },
  });

  if (!response.ok) {
    throw new HttpError(response.status, await response.text());
  }

  const data = await response.json();
  return schema ? schema.parse(data) : data;
}

// Usage:
const user = await api("/users/1", { schema: UserSchema });
```

## Cancellation

Always provide cancellation for user-facing requests:

```typescript
// Timeout:
const response = await fetch(url, {
  signal: AbortSignal.timeout(5000),
});

// User cancellation:
const controller = new AbortController();
const response = await fetch(url, { signal: controller.signal });
// Later: controller.abort();

// Combined:
const controller = new AbortController();
const signal = AbortSignal.any([
  controller.signal,
  AbortSignal.timeout(10000),
]);
```

## Retry Pattern

```typescript
async function fetchWithRetry(
  url: string,
  options: RequestInit & { retries?: number; backoff?: number } = {},
): Promise<Response> {
  const { retries = 3, backoff = 1000, ...fetchOptions } = options;

  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      const response = await fetch(url, fetchOptions);
      if (response.ok || response.status < 500) return response;
      // Retry on 5xx
    } catch (error) {
      if (attempt === retries) throw error;
      if (error instanceof Error && error.name === "AbortError") throw error;
    }
    await new Promise((r) => setTimeout(r, backoff * 2 ** attempt));
  }

  throw new Error(`Failed after ${retries + 1} attempts`);
}
```

## Streaming Responses

Process large responses without loading everything into memory:

```typescript
async function* streamLines(response: Response): AsyncGenerator<string> {
  if (!response.body) throw new Error("No response body");

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    buffer += decoder.decode(value, { stream: true });
    const lines = buffer.split("\n");
    buffer = lines.pop() ?? "";

    for (const line of lines) {
      yield line;
    }
  }

  if (buffer) yield buffer;
}
```

### Server-Sent Events (SSE)

```typescript
async function* streamSSE(url: string, signal?: AbortSignal): AsyncGenerator<SSEEvent> {
  const response = await fetch(url, {
    headers: { Accept: "text/event-stream" },
    signal,
  });

  for await (const line of streamLines(response)) {
    if (line.startsWith("data: ")) {
      yield JSON.parse(line.slice(6));
    }
  }
}
```

## FormData

For file uploads and multipart forms:

```typescript
const formData = new FormData();
formData.append("name", "Alice");
formData.append("avatar", fileBlob, "avatar.png");

const response = await fetch("/api/upload", {
  method: "POST",
  body: formData,
  // Don't set Content-Type — browser sets it with boundary
});
```

## Caching Strategies

### Cache-Control Headers (Server-Side)

```typescript
// Immutable assets (hashed filenames):
res.setHeader("Cache-Control", "public, max-age=31536000, immutable");

// API responses that change:
res.setHeader("Cache-Control", "private, max-age=60, stale-while-revalidate=300");

// No cache:
res.setHeader("Cache-Control", "no-store");
```

### Client-Side Caching

```typescript
// Simple in-memory cache:
const cache = new Map<string, { data: unknown; expires: number }>();

async function cachedFetch<T>(url: string, ttl = 60000): Promise<T> {
  const cached = cache.get(url);
  if (cached && cached.expires > Date.now()) return cached.data as T;

  const response = await fetch(url);
  if (!response.ok) throw new HttpError(response.status);
  const data = await response.json();

  cache.set(url, { data, expires: Date.now() + ttl });
  return data;
}
```

## Best Practices

- **Always check `response.ok`** — fetch doesn't throw on HTTP errors
- **Always provide cancellation** — `AbortSignal.timeout()` at minimum
- **Validate responses** with Zod — don't trust API data shapes
- **Stream large responses** — don't load everything into memory
- **Retry transient failures** — 5xx and network errors, with exponential backoff
- **Don't set `Content-Type` for FormData** — let the browser set it
- **Cache aggressively** — both server-side headers and client-side storage
