# Cancellation and Exception Handling

Coroutine cancellation is cooperative — a coroutine must check for cancellation to actually stop. Exception handling follows the structured concurrency hierarchy: exceptions propagate upward through the Job tree. Understanding these mechanics is essential for writing correct concurrent code.

## Cooperative Cancellation

Cancellation works by throwing a `CancellationException` at suspension points. If your coroutine never suspends (e.g., a tight computation loop), it will never be cancelled unless you check explicitly.

```kotlin
// WRONG: This loop ignores cancellation — it runs forever even after cancel()
val job = launch {
    var sum = 0L
    for (i in 1..Long.MAX_VALUE) {
        sum += i  // No suspension point, no cancellation check
    }
}
job.cancel()  // Does nothing — the loop never checks

// CORRECT: Check isActive in computation loops
val job = launch {
    var sum = 0L
    for (i in 1..Long.MAX_VALUE) {
        if (!isActive) break  // Cooperate with cancellation
        sum += i
    }
}
```

## ensureActive()

`ensureActive()` throws `CancellationException` if the coroutine is no longer active. It is more concise than checking `isActive` manually.

```kotlin
suspend fun processLargeDataset(items: List<Item>) {
    items.forEach { item ->
        ensureActive()  // Throws CancellationException if cancelled
        process(item)
    }
}
```

## yield()

`yield()` suspends the coroutine, gives other coroutines a chance to run, and checks for cancellation. Use it in CPU-bound loops.

```kotlin
suspend fun computeResult(data: List<Int>): Long {
    var result = 0L
    data.forEach { value ->
        yield()  // Suspend, let others run, check cancellation
        result += heavyComputation(value)
    }
    return result
}
```

## CancellationException Is Special

`CancellationException` is treated differently from all other exceptions:

1. It does NOT propagate to the parent — it is a normal signal that the coroutine was cancelled.
2. It does NOT trigger `CoroutineExceptionHandler`.
3. The parent does NOT cancel other children when a child throws `CancellationException`.

```kotlin
val parent = launch {
    val child1 = launch {
        delay(Long.MAX_VALUE)
    }
    val child2 = launch {
        delay(Long.MAX_VALUE)
    }
    child1.cancel()  // Only child1 is cancelled; child2 and parent continue
}
```

## Exception Propagation

Exceptions (other than `CancellationException`) propagate differently depending on the builder:

- **`launch`**: Propagates exception to the parent immediately. Parent cancels all children, then propagates to its parent.
- **`async`**: Stores the exception in the `Deferred`. It is thrown when you call `await()`.

```kotlin
// launch — exception propagates to parent immediately
val scope = CoroutineScope(Job())
scope.launch {
    throw RuntimeException("boom")  // Propagates up, cancels the scope
}

// async — exception is deferred
val scope = CoroutineScope(Job())
val deferred = scope.async {
    throw RuntimeException("boom")  // Stored, not propagated yet
}
// Exception thrown here when we access the result:
deferred.await()  // throws RuntimeException("boom")
```

## CoroutineExceptionHandler

A last-resort handler for uncaught exceptions in `launch` coroutines. It does NOT prevent the coroutine from failing — it only lets you log or report the exception.

```kotlin
val handler = CoroutineExceptionHandler { context, exception ->
    logger.error("Uncaught exception in ${context[CoroutineName]}", exception)
    crashReporter.report(exception)
}

val scope = CoroutineScope(SupervisorJob() + handler + CoroutineName("app"))

scope.launch {
    throw RuntimeException("Something went wrong")
    // handler is invoked — the coroutine still fails
}
```

Important rules:
- Install it on the **scope** or the **root coroutine**, not on child coroutines (children propagate to the parent, which invokes the handler).
- It only works with `launch`, not `async` (async exceptions are caught by `await`).
- It does NOT catch `CancellationException`.

## try/finally and NonCancellable

Use `try/finally` for cleanup. If your cleanup code needs to call suspending functions, wrap them in `withContext(NonCancellable)` — otherwise they will throw `CancellationException` immediately because the coroutine is already cancelled.

```kotlin
val job = launch {
    try {
        repeat(1000) { i ->
            println("Processing $i")
            delay(100)
        }
    } finally {
        // Non-suspending cleanup works fine
        closeResource()

        // Suspending cleanup needs NonCancellable
        withContext(NonCancellable) {
            saveProgress()  // This suspending call completes even though we're cancelled
            flushLogs()
        }
    }
}
delay(500)
job.cancel()
```

## withTimeout and withTimeoutOrNull

Set a deadline for a suspending operation. `withTimeout` throws `TimeoutCancellationException` (a subclass of `CancellationException`). `withTimeoutOrNull` returns `null` instead.

```kotlin
// Throws TimeoutCancellationException if fetchUser takes > 5 seconds
val user = withTimeout(5_000) {
    fetchUser(id)
}

// Returns null on timeout — no exception
val user = withTimeoutOrNull(5_000) {
    fetchUser(id)
} ?: fallbackUser()
```

## The runCatching Trap

`runCatching` catches ALL exceptions, including `CancellationException`. This silently breaks cancellation. Never use `runCatching` in coroutines without re-throwing `CancellationException`.

```kotlin
// WRONG: Swallows CancellationException — coroutine cannot be cancelled
suspend fun fetchUser(id: Long): Result<User> = runCatching {
    api.getUser(id)
}
// If the coroutine is cancelled during getUser(), runCatching catches the
// CancellationException and wraps it in Result.failure — the coroutine continues!

// CORRECT: Re-throw CancellationException
suspend fun fetchUser(id: Long): Result<User> = runCatching {
    api.getUser(id)
}.onFailure {
    if (it is CancellationException) throw it
}

// BETTER: Write a coroutine-safe version
suspend inline fun <T> suspendRunCatching(block: () -> T): Result<T> {
    return try {
        Result.success(block())
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        Result.failure(e)
    }
}
```

## SupervisorJob: Isolating Failures

With a regular `Job`, any child failure cancels the parent and all siblings. `SupervisorJob` prevents this — each child handles its own failures.

```kotlin
// Regular Job — one failure kills everything
val scope = CoroutineScope(Job())
scope.launch { fetchPrices() }    // If this throws...
scope.launch { fetchInventory() } // ...this gets cancelled

// SupervisorJob — failures are isolated
val scope = CoroutineScope(SupervisorJob())
scope.launch { fetchPrices() }    // If this throws...
scope.launch { fetchInventory() } // ...this continues running
```

## supervisorScope for Scoped Isolation

Use `supervisorScope` when you want child isolation within a specific block.

```kotlin
suspend fun syncAllData() = supervisorScope {
    val results = listOf(
        async { syncUsers() },
        async { syncOrders() },
        async { syncProducts() },
    )

    // Each async handles its own exception
    results.forEach { deferred ->
        try {
            deferred.await()
        } catch (e: Exception) {
            logger.warn("Sync failed", e)
        }
    }
}
```

Note: With `supervisorScope`, you MUST catch exceptions from `async` at the `await()` call site — they are not propagated to the scope.

## Exception Handling Decision Tree

```
Is it CancellationException?
  YES -> Let it propagate (do NOT catch it)
  NO  -> Is the coroutine built with launch?
           YES -> Exception propagates to parent
                  -> Use CoroutineExceptionHandler for last-resort logging
                  -> Use SupervisorJob if siblings should survive
           NO (async) -> Exception stored in Deferred
                      -> Catch it at await() call site
```

## Summary

| Mechanism | Purpose |
|-----------|---------|
| `isActive` / `ensureActive()` | Cooperative cancellation check |
| `yield()` | Suspend + cancellation check in CPU loops |
| `CancellationException` | Normal cancellation signal (do not catch) |
| `CoroutineExceptionHandler` | Last-resort logging for uncaught launch exceptions |
| `withContext(NonCancellable)` | Suspending cleanup in finally blocks |
| `withTimeout` / `withTimeoutOrNull` | Deadline enforcement |
| `SupervisorJob` / `supervisorScope` | Isolate child failures |
| `suspendRunCatching` | Coroutine-safe alternative to `runCatching` |
