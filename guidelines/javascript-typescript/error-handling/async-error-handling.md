# Async Error Handling

## try/catch with async/await

Every `await` can throw. Wrap at the right granularity — not every line, but at
logical operation boundaries:

```typescript
// Too granular — cluttered:
async function load() {
  let response;
  try { response = await fetch(url); } catch (e) { /* ... */ }
  let data;
  try { data = await response.json(); } catch (e) { /* ... */ }
}

// Right level — one try/catch per logical operation:
async function loadUser(id: string): Promise<User> {
  try {
    const response = await fetch(`/api/users/${id}`);
    if (!response.ok) throw new HttpError(response.status, response.statusText);
    return await response.json();
  } catch (error) {
    throw new AppError(`Failed to load user ${id}`, { cause: error });
  }
}
```

## Unhandled Promise Rejections

In Node.js, unhandled rejections terminate the process by default. Every async
call chain must eventually catch:

```typescript
// Bad — unhandled rejection if loadUser throws:
loadUser("123").then(render);

// Good — explicit catch:
loadUser("123").then(render).catch(handleError);

// Or with async/await:
try {
  const user = await loadUser("123");
  render(user);
} catch (error) {
  handleError(error);
}
```

### Global Handlers (Safety Net Only)

Register global handlers to log and gracefully exit, but don't rely on them:

```typescript
// Node.js:
process.on("unhandledRejection", (reason) => {
  logger.error("Unhandled rejection", { reason });
  process.exit(1);
});

// Browser:
window.addEventListener("unhandledrejection", (event) => {
  logger.error("Unhandled rejection", { reason: event.reason });
});
```

## Promise Combinator Error Handling

### `Promise.all` — Fails Fast

Rejects as soon as any promise rejects. The remaining promises continue
executing but their results are lost:

```typescript
try {
  const [users, posts] = await Promise.all([fetchUsers(), fetchPosts()]);
} catch (error) {
  // One failed — but which one? Add context or use allSettled
}
```

### `Promise.allSettled` — Never Rejects

Returns all results, whether fulfilled or rejected:

```typescript
const results = await Promise.allSettled([fetchUsers(), fetchPosts()]);

for (const result of results) {
  if (result.status === "fulfilled") {
    process(result.value);
  } else {
    logger.warn("Partial failure", { reason: result.reason });
  }
}
```

Use `allSettled` when partial failure is acceptable and you want to process
whatever succeeds.

### `Promise.any` — First Success Wins

Resolves with the first fulfilled promise. Rejects only if all promises reject
(with an `AggregateError`):

```typescript
try {
  const result = await Promise.any([fetchFromPrimary(), fetchFromFallback()]);
} catch (error) {
  // AggregateError — all failed
  console.error(error.errors); // array of individual errors
}
```

## Error Boundaries (React)

React error boundaries catch render errors in the component tree:

```typescript
class ErrorBoundary extends React.Component<Props, State> {
  state = { hasError: false, error: null };

  static getDerivedStateFromError(error: Error) {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    logger.error("React error boundary", { error, componentStack: info.componentStack });
  }

  render() {
    if (this.state.hasError) return this.props.fallback;
    return this.props.children;
  }
}
```

**Limitations**: error boundaries do not catch errors in event handlers, async
code, or server-side rendering. Handle those with try/catch.

## Typed Catch Blocks

TypeScript's `catch` clause types the error as `unknown` (with strict mode).
Always narrow before accessing properties:

```typescript
try {
  await riskyOperation();
} catch (error) {
  // error is unknown — must narrow
  const message = error instanceof Error ? error.message : String(error);
  logger.error("Operation failed", { message });
}
```

## Best Practices

- **Catch at operation boundaries** — not at every await
- **Always add context** when re-throwing: `new Error("context", { cause })`
- **Distinguish cancellation from failure** — check `error.name === "AbortError"`
- **Use `allSettled`** when partial failure is acceptable
- **Never ignore catch blocks** — at minimum, log the error
- **Type-narrow in catch** — don't assume `error` is an `Error` instance
