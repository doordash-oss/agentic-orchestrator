# CompletableFuture

## Core Concepts

`CompletableFuture<T>` represents an asynchronous computation. It implements
`Future<T>` and `CompletionStage<T>`, enabling composition of async operations
without blocking.

```java
CompletableFuture<String> future = CompletableFuture
    .supplyAsync(() -> fetchData())           // runs in ForkJoinPool
    .thenApply(data -> transform(data))       // synchronous transform
    .thenCompose(t -> saveAsync(t))           // chain another async op
    .exceptionally(ex -> fallbackValue());    // handle errors
```

## thenApply vs thenCompose

This is the `map` vs `flatMap` distinction:

- **`thenApply(Function<T, R>)`** — synchronous transform, like `Stream.map()`.
  Returns `CompletableFuture<R>`.
- **`thenCompose(Function<T, CompletableFuture<R>>)`** — chains an async
  operation, like `Stream.flatMap()`. Flattens the result.

```java
// thenApply — synchronous transformation
CompletableFuture<Integer> future = CompletableFuture
    .supplyAsync(() -> "42")
    .thenApply(Integer::parseInt);  // String -> Integer

// thenCompose — async chaining (avoids CompletableFuture<CompletableFuture<R>>)
CompletableFuture<Order> future = CompletableFuture
    .supplyAsync(() -> findUserId())
    .thenCompose(id -> fetchOrderAsync(id));  // returns CompletableFuture<Order>

// Wrong — thenApply with async function nests futures
CompletableFuture<CompletableFuture<Order>> nested = CompletableFuture
    .supplyAsync(() -> findUserId())
    .thenApply(id -> fetchOrderAsync(id));  // CompletableFuture<CompletableFuture<Order>>!
```

**Rule of thumb**: if your function returns a `CompletableFuture`, use
`thenCompose`. If it returns a plain value, use `thenApply`.

## Combining Multiple Futures

### allOf — Wait for All

```java
CompletableFuture<String> user = fetchUserAsync(id);
CompletableFuture<List<Order>> orders = fetchOrdersAsync(id);
CompletableFuture<BigDecimal> balance = fetchBalanceAsync(id);

CompletableFuture<UserProfile> profile = CompletableFuture
    .allOf(user, orders, balance)
    .thenApply(v -> new UserProfile(
        user.join(),        // safe — already complete
        orders.join(),
        balance.join()
    ));
```

**Caveat**: `allOf` returns `CompletableFuture<Void>` — it signals completion
but doesn't carry results. Use `thenApply` with `join()` to collect results.

**Caveat**: `allOf` does **not** short-circuit on failure. If one future fails,
others continue running. Handle errors on individual futures if needed.

### anyOf — First to Complete

```java
CompletableFuture<Object> fastest = CompletableFuture.anyOf(
    fetchFromCacheAsync(key),
    fetchFromDbAsync(key)
);
```

Returns the result of whichever future completes first. Note the return type
is `CompletableFuture<Object>` — you'll need to cast.

## Error Handling

Three mechanisms, each for different purposes:

```java
// exceptionally — provide fallback on error
CompletableFuture<String> result = fetchAsync()
    .exceptionally(ex -> "default");

// handle — access both result and exception
CompletableFuture<String> result = fetchAsync()
    .handle((value, ex) -> {
        if (ex != null) return "error: " + ex.getMessage();
        return value.toUpperCase();
    });

// whenComplete — side effects (logging), does not transform result
fetchAsync()
    .whenComplete((value, ex) -> {
        if (ex != null) log.error("fetch failed", ex);
    });
```

**Important**: exceptions in `CompletableFuture` are wrapped in
`CompletionException`. Use `ex.getCause()` to access the original:

```java
.exceptionally(ex -> {
    Throwable cause = (ex instanceof CompletionException) ? ex.getCause() : ex;
    log.error("original error", cause);
    return fallback;
});
```

## Timeout Handling (Java 9+)

```java
CompletableFuture<String> result = fetchAsync()
    .orTimeout(5, TimeUnit.SECONDS)             // throws TimeoutException
    .exceptionally(ex -> "timed out");

CompletableFuture<String> result = fetchAsync()
    .completeOnTimeout("default", 5, TimeUnit.SECONDS);  // returns default
```

## Async Variants

Most methods have `*Async` variants that run the callback in a different thread:

- `thenApply` — runs in the completing thread
- `thenApplyAsync` — runs in ForkJoinPool.commonPool()
- `thenApplyAsync(fn, executor)` — runs in specified executor

Use async variants when the callback does significant work to avoid blocking
the completing thread.

## Best Practices Summary

- **Avoid `get()` and `join()` except at the end of a pipeline** — they block
- **Use `thenCompose` for async chains**, `thenApply` for sync transforms
- **Always handle errors** — uncaught exceptions in a CompletableFuture are
  silently swallowed unless you call `get()`/`join()` or attach a handler
- **Specify timeouts** — unresolved futures are silent resource leaks
- **Use a custom executor for IO** — don't saturate the common ForkJoinPool
