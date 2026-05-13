# Result and runCatching

`Result<T>` is Kotlin's standard-library wrapper for a value that is either a success or
a failure. It pairs well with functional transforms but has a critical interaction with
coroutines that you must understand before using it.

## The Result Type

`Result<T>` holds either a successful value of type `T` or a `Throwable` representing
failure. It is an inline class, so in most cases there is zero allocation overhead on
the success path.

### Creating a Result

```kotlin
// From a known value
val ok: Result<Int> = Result.success(42)
val err: Result<Int> = Result.failure(IllegalArgumentException("bad input"))

// From a block that might throw
val parsed: Result<Int> = runCatching { "123".toInt() }
```

### Consuming a Result

```kotlin
val result: Result<Config> = loadConfig()

// Nullable unwrap
val configOrNull: Config? = result.getOrNull()

// Default on failure
val config: Config = result.getOrElse { Config.default() }

// Re-throw on failure
val config: Config = result.getOrThrow()
```

### Transforming

```kotlin
result
    .map { it.databaseUrl }               // transform success value
    .mapCatching { URI(it) }              // transform, catching exceptions in the lambda
    .recover { URI("jdbc:h2:mem:test") }  // replace failure with a success value
    .recoverCatching { fallbackUri() }    // replace failure, catching exceptions
```

`map` and `recover` propagate exceptions thrown in their lambdas. The `*Catching`
variants wrap those exceptions back into a `Result.failure`.

### Side Effects

```kotlin
result
    .onSuccess { logger.info("Loaded config: $it") }
    .onFailure { logger.error("Failed to load config", it) }
```

These return the original `Result` unchanged, so they can be chained.

### Exhaustive Handling with fold

```kotlin
val message: String = result.fold(
    onSuccess = { "Config loaded from ${it.path}" },
    onFailure = { "Failed: ${it.message}" }
)
```

`fold` forces you to handle both cases and returns a single value.

## The Critical Pitfall: runCatching in Coroutines

`runCatching` catches **all** exceptions, including `CancellationException`. In a
coroutine context, `CancellationException` is the mechanism for structured cancellation.
Catching it silently makes the coroutine uncancellable.

### WRONG -- catches CancellationException

```kotlin
// DO NOT do this in a suspend function
suspend fun fetchData(): Result<Data> = runCatching {
    api.getData() // if the coroutine is cancelled here, CancellationException is swallowed
}
```

When the parent scope is cancelled, `api.getData()` throws `CancellationException`.
`runCatching` catches it and wraps it in `Result.failure`. The coroutine continues
running instead of terminating, and the caller sees a generic failure instead of
propagating cancellation.

### CORRECT -- re-throw CancellationException

```kotlin
import kotlinx.coroutines.CancellationException

suspend fun fetchData(): Result<Data> = try {
    Result.success(api.getData())
} catch (e: CancellationException) {
    throw e  // always re-throw to preserve structured concurrency
} catch (e: Exception) {
    Result.failure(e)
}
```

### CORRECT -- helper extension (if you need this pattern often)

```kotlin
import kotlinx.coroutines.CancellationException

inline fun <T> runSuspendCatching(block: () -> T): Result<T> = try {
    Result.success(block())
} catch (e: CancellationException) {
    throw e
} catch (e: Exception) {
    Result.failure(e)
}

suspend fun fetchData(): Result<Data> = runSuspendCatching {
    api.getData()
}
```

This is a common enough need that several libraries (including Arrow) provide it.

## When to Use Result vs Sealed Error Types vs Exceptions

| Mechanism | Use When |
|-----------|----------|
| `Result<T>` | The failure is a `Throwable` and you want lightweight success/failure wrapping (IO, parsing, network calls). |
| Sealed error types | The failure is a domain concept with structured data (validation errors, business rule violations). You want exhaustive `when` matching. |
| Exceptions | The condition is truly exceptional and unrecoverable at the call site (programming bugs, assertion failures, OOM). |

`Result<T>` is limited to `Throwable` as its failure type. If you need typed errors
(e.g., `UserError.NotFound`), use a sealed class or Arrow's `Either`.

## Anti-Patterns

### Wrapping everything in runCatching "just in case"

```kotlin
// BAD -- obscures bugs, catches things you don't expect
fun processOrder(order: Order): Result<Receipt> = runCatching {
    validate(order)
    charge(order)
    generateReceipt(order)
}
```

If `validate` throws a `NullPointerException` due to a bug, this silently wraps it as a
failure instead of crashing early. Only catch exceptions you expect.

### Using Result as a function parameter

```kotlin
// BAD -- forces callers to wrap their values
fun process(input: Result<Data>): Result<Output> { ... }

// GOOD -- accept the value, return a Result
fun process(input: Data): Result<Output> { ... }
```

`Result<T>` is a return-type concept. Accepting it as a parameter complicates the API
and conflates the caller's error handling with the callee's.

### Ignoring the failure

```kotlin
// BAD -- silently discards the error
val data = fetchData().getOrNull() ?: return
```

If you discard the error, at minimum log it. Otherwise failures become invisible.

```kotlin
// BETTER
val data = fetchData().getOrElse { e ->
    logger.warn("fetchData failed, using fallback", e)
    return fallbackData()
}
```

## Summary

- `Result<T>` is a clean way to represent success/failure without exceptions.
- Use `fold`, `getOrElse`, and `recover` for safe consumption.
- **Never** use `runCatching` in `suspend` functions. Re-throw `CancellationException`.
- Use `Result` as a return type only, not as a parameter type.
- For typed domain errors, prefer sealed class hierarchies over `Result<T>`.
