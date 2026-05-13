# Cancellation and Concurrency

## AbortController Basics

`AbortController` is the standard mechanism for cancelling async operations.
Available in all modern browsers and Node.js:

```typescript
const controller = new AbortController();

// Pass the signal to an async operation:
const response = await fetch("/api/data", { signal: controller.signal });

// Cancel from anywhere:
controller.abort();
```

When aborted, the operation rejects with an `AbortError`.

## Handling Cancellation

Always distinguish cancellation from real errors:

```typescript
try {
  const data = await fetchData({ signal });
} catch (error) {
  if (error instanceof Error && error.name === "AbortError") {
    // Cancellation — not an error, just stop processing
    return;
  }
  // Actual error — handle or propagate
  throw error;
}
```

## AbortSignal.timeout()

Automatically cancel after a duration — no manual `setTimeout` needed:

```typescript
// Abort after 5 seconds:
const response = await fetch("/api/data", {
  signal: AbortSignal.timeout(5000),
});
```

## AbortSignal.any()

Combine multiple signals — cancels when any signal fires:

```typescript
// Cancel on user action OR timeout:
const controller = new AbortController();
const signal = AbortSignal.any([
  controller.signal,
  AbortSignal.timeout(10000),
]);

const response = await fetch("/api/data", { signal });

// User clicks cancel:
cancelButton.addEventListener("click", () => controller.abort());
```

## Passing Signals Through Call Chains

Create the controller at the top level; pass the signal down:

```typescript
async function loadDashboard(signal: AbortSignal) {
  const [user, feed] = await Promise.all([
    fetchUser(signal),
    fetchFeed(signal),
  ]);
  return { user, feed };
}

async function fetchUser(signal: AbortSignal): Promise<User> {
  const response = await fetch("/api/user", { signal });
  return response.json();
}

// Top level:
const controller = new AbortController();
loadDashboard(controller.signal);

// On component unmount or route change:
controller.abort();
```

**Rule**: only create `AbortController` at the top level. Regular functions
accept `AbortSignal` and pass it through.

## Building Custom Abortable APIs

When wrapping non-fetch async operations, listen for the abort event:

```typescript
function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason);
      return;
    }

    const timer = setTimeout(resolve, ms);

    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      reject(signal.reason);
    }, { once: true });
  });
}
```

### Cleanup Pattern

Always remove event listeners when the operation completes to prevent
memory leaks with long-lived controllers:

```typescript
function cancellableOperation(signal?: AbortSignal): Promise<Result> {
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      cleanup();
      reject(signal!.reason);
    };

    signal?.addEventListener("abort", onAbort, { once: true });

    function cleanup() {
      signal?.removeEventListener("abort", onAbort);
    }

    doWork()
      .then((result) => { cleanup(); resolve(result); })
      .catch((error) => { cleanup(); reject(error); });
  });
}
```

## Node.js Built-in Signal Support

Many Node.js APIs accept `signal` natively:

```typescript
import { readFile } from "node:fs/promises";
import { setTimeout } from "node:timers/promises";

// File I/O:
const data = await readFile("large-file.txt", { signal });

// Timers:
await setTimeout(1000, undefined, { signal });

// Child processes:
const child = spawn("cmd", args, { signal });

// Event listeners (with AbortSignal):
element.addEventListener("click", handler, { signal });
// Automatically removed when signal aborts
```

## Best Practices

- **Always provide cancellation** for user-facing async operations
- **Use `AbortSignal.timeout()`** instead of manual `setTimeout` + `clearTimeout`
- **Use `AbortSignal.any()`** to combine user cancellation with timeouts
- **Pass `signal`, not `controller`** — consumers should cancel, not the callee
- **Check `signal.aborted`** at the start of long-running operations
- **Clean up listeners** on completion to avoid memory leaks with long-lived controllers
- **Don't treat cancellation as an error** in UI code — it's an expected flow
