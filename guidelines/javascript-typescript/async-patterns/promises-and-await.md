# Promises and Async/Await

## async/await over Raw Promises

Prefer `async/await` for readability and better error stack traces:

```typescript
// Avoid — nested .then chains:
function loadDashboard() {
  return fetchUser()
    .then((user) => fetchPosts(user.id)
      .then((posts) => ({ user, posts })));
}

// Prefer — flat async/await:
async function loadDashboard() {
  const user = await fetchUser();
  const posts = await fetchPosts(user.id);
  return { user, posts };
}
```

## Sequential vs Concurrent

### Avoid Sequential Awaits for Independent Operations

```typescript
// Bad — waits 2x longer than necessary:
const users = await fetchUsers();
const posts = await fetchPosts(); // doesn't depend on users

// Good — concurrent execution:
const [users, posts] = await Promise.all([fetchUsers(), fetchPosts()]);
```

### Keep Sequential When There's a Dependency

```typescript
// Correct — posts depend on the user ID:
const user = await fetchUser(id);
const posts = await fetchPosts(user.id);
```

## Promise Combinators

| Combinator | Behavior | Use When |
|-----------|----------|----------|
| `Promise.all` | Resolves when all resolve; rejects on first rejection | All results are needed, fail fast |
| `Promise.allSettled` | Always resolves with status of each | Partial failure is acceptable |
| `Promise.race` | Resolves/rejects with the first settled | Timeout patterns, first response wins |
| `Promise.any` | Resolves with first fulfillment; rejects only if all reject | Fallback/redundancy patterns |

### `Promise.all` — Typed Destructuring

```typescript
const [users, config, permissions] = await Promise.all([
  fetchUsers(),
  loadConfig(),
  checkPermissions(userId),
] as const);
// Each variable has its correct type
```

### `Promise.allSettled` — Partial Failure

```typescript
const results = await Promise.allSettled(urls.map(fetch));

const succeeded = results
  .filter((r): r is PromiseFulfilledResult<Response> => r.status === "fulfilled")
  .map((r) => r.value);

const failed = results
  .filter((r): r is PromiseRejectedResult => r.status === "rejected")
  .map((r) => r.reason);
```

### `Promise.race` — Timeout Pattern

```typescript
async function fetchWithTimeout<T>(promise: Promise<T>, ms: number): Promise<T> {
  const timeout = new Promise<never>((_, reject) =>
    setTimeout(() => reject(new Error(`Timeout after ${ms}ms`)), ms)
  );
  return Promise.race([promise, timeout]);
}
```

Note: prefer `AbortSignal.timeout()` for fetch requests (see
cancellation-and-concurrency.md).

## Avoid the Promise Constructor Anti-Pattern

Don't wrap existing promises in `new Promise`:

```typescript
// Anti-pattern:
function load() {
  return new Promise((resolve, reject) => {
    fetch("/data").then(resolve).catch(reject);
  });
}

// Correct — just return the promise:
function load() {
  return fetch("/data");
}
```

The constructor is only needed when bridging callback-based APIs:

```typescript
function readFileAsync(path: string): Promise<string> {
  return new Promise((resolve, reject) => {
    fs.readFile(path, "utf8", (err, data) => {
      if (err) reject(err);
      else resolve(data);
    });
  });
}
```

## Top-Level Await

ES2022+ and Node.js ESM modules support `await` at the top level:

```typescript
// In an ESM module:
const config = await loadConfig();
export const db = await connectDatabase(config.databaseUrl);
```

Use sparingly — top-level await blocks module loading and can cause deadlocks
in circular dependency chains.

## Async Loops

### `for...of` with await — Sequential

```typescript
for (const url of urls) {
  await fetch(url); // one at a time, in order
}
```

### `Promise.all` with map — Concurrent

```typescript
await Promise.all(urls.map((url) => fetch(url))); // all at once
```

### Controlled Concurrency

When you need concurrency but not unbounded parallelism:

```typescript
async function mapConcurrent<T, R>(
  items: T[],
  fn: (item: T) => Promise<R>,
  concurrency: number,
): Promise<R[]> {
  const results: R[] = [];
  const executing = new Set<Promise<void>>();

  for (const item of items) {
    const p = fn(item).then((result) => { results.push(result); });
    executing.add(p);
    p.finally(() => executing.delete(p));

    if (executing.size >= concurrency) {
      await Promise.race(executing);
    }
  }

  await Promise.all(executing);
  return results;
}
```

## Best Practices

- **Use `async/await`** — cleaner than `.then()` chains
- **Run independent operations concurrently** with `Promise.all`
- **Use `allSettled` for partial failure tolerance**
- **Avoid `new Promise` wrapping existing promises**
- **Control concurrency** for large batch operations to avoid overwhelming resources
- **Return the await** in try/catch blocks (`return await`) to catch errors properly
