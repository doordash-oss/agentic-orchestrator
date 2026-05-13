# Dispatchers and Context

Every coroutine runs within a `CoroutineContext` that determines its dispatcher (thread pool), Job, name, and other elements. Choosing the right dispatcher is critical for performance — CPU-bound work, blocking I/O, and UI updates each require different thread pools.

## Dispatchers

### Dispatchers.Default

Shared thread pool sized to the number of CPU cores. Use for CPU-intensive computations.

```kotlin
suspend fun computeHash(data: ByteArray): String = withContext(Dispatchers.Default) {
    MessageDigest.getInstance("SHA-256")
        .digest(data)
        .joinToString("") { "%02x".format(it) }
}
```

### Dispatchers.IO

Shared thread pool designed for blocking I/O operations. Grows up to 64 threads (or the number of cores, whichever is larger). Shares threads with Default when idle.

```kotlin
suspend fun readConfig(path: Path): Config = withContext(Dispatchers.IO) {
    val content = Files.readString(path)
    parseConfig(content)
}

suspend fun queryDatabase(id: Long): User = withContext(Dispatchers.IO) {
    connection.prepareStatement("SELECT * FROM users WHERE id = ?").use { stmt ->
        stmt.setLong(1, id)
        stmt.executeQuery().use { rs ->
            rs.next()
            User(rs.getLong("id"), rs.getString("name"))
        }
    }
}
```

### Dispatchers.Main

The UI thread on Android. Use for updating views and interacting with UI components. Available only when a Main dispatcher implementation is on the classpath (e.g., `kotlinx-coroutines-android`).

```kotlin
// Android — update UI on Main, fetch data on IO
class UserViewModel : ViewModel() {
    fun loadUser(id: Long) {
        viewModelScope.launch {  // Launches on Main by default in viewModelScope
            _uiState.value = UiState.Loading
            try {
                val user = withContext(Dispatchers.IO) {
                    repository.fetchUser(id)
                }
                _uiState.value = UiState.Success(user)
            } catch (e: Exception) {
                _uiState.value = UiState.Error(e.message)
            }
        }
    }
}
```

### Dispatchers.Unconfined

Starts on the caller's thread, then resumes on whatever thread the suspending function used. This is unpredictable and should be avoided in production code.

```kotlin
// Avoid in production — thread behavior is unpredictable
launch(Dispatchers.Unconfined) {
    println("Started on ${Thread.currentThread().name}")  // Caller's thread
    delay(100)
    println("Resumed on ${Thread.currentThread().name}")  // kotlinx.coroutines.DefaultExecutor
}
```

The only valid use for `Unconfined` is in tests where you want coroutines to execute eagerly without a test dispatcher.

## withContext — Switching Dispatchers

`withContext` switches the dispatcher for a block of code without creating a new coroutine. It is the primary tool for ensuring work runs on the correct thread pool.

```kotlin
// CORRECT: Switch to IO for the blocking call, return to caller's dispatcher
suspend fun fetchAndParse(url: String): Document {
    val html = withContext(Dispatchers.IO) {
        httpClient.get(url).body<String>()
    }
    // Back on caller's dispatcher — safe to update UI if called from Main
    return parseHtml(html)
}
```

```kotlin
// WRONG: Blocking IO on Default dispatcher — starves CPU workers
suspend fun fetchAndParse(url: String): Document {
    val html = httpClient.get(url).body<String>()  // Blocking call on Default!
    return parseHtml(html)
}
```

## newSingleThreadContext

Creates a dedicated thread for a coroutine. Useful for thread-confined resources that are not thread-safe. Always close it when no longer needed.

```kotlin
val databaseContext = newSingleThreadContext("DatabaseThread")

suspend fun writeRecord(record: Record) = withContext(databaseContext) {
    // All database writes happen on a single thread — no concurrency issues
    database.insert(record)
}

// Cleanup when done
databaseContext.close()
```

## CoroutineContext Combining

Context elements are combined with the `+` operator. Later elements override earlier ones of the same type.

```kotlin
val context = Dispatchers.Default +
    CoroutineName("data-processor") +
    SupervisorJob()

val scope = CoroutineScope(context)

scope.launch {
    println(coroutineContext[CoroutineName])  // CoroutineName(data-processor)
}
```

When you launch a coroutine, the new context is: parent context + arguments to launch.

```kotlin
val scope = CoroutineScope(Dispatchers.Default + CoroutineName("parent"))

scope.launch(CoroutineName("child")) {
    // Dispatcher: Default (inherited from parent)
    // Name: "child" (overridden by launch argument)
}
```

## CoroutineName for Debugging

Adding a name to coroutines makes thread dumps and logs much more useful.

```kotlin
launch(CoroutineName("order-processor")) {
    // In logs and thread dumps, this coroutine is identified as "order-processor"
    processOrders()
}
```

Enable coroutine debug mode to see names in thread names:

```
-Dkotlinx.coroutines.debug
```

With debug mode, thread names become `DefaultDispatcher-worker-1 @order-processor#42`, making it easy to identify which coroutine is running.

## Thread-Local Data with asContextElement

Bridge thread-local data into coroutine context so it is restored on every resumption.

```kotlin
val transactionId = ThreadLocal<String>()

suspend fun processWithTransaction(id: String) {
    withContext(transactionId.asContextElement(id)) {
        // transactionId.get() returns id, even after suspension
        callServiceA()
        callServiceB()  // transactionId is still set correctly here
    }
}
```

This is useful for MDC (Mapped Diagnostic Context) in logging:

```kotlin
val mdcContext = MDCContext()  // Captures current MDC

launch(mdcContext) {
    // MDC is preserved across suspensions
    logger.info("Processing order")  // MDC values are available
}
```

## limitedParallelism

Create a view of a dispatcher with limited concurrency. Available since kotlinx.coroutines 1.6.

```kotlin
// Limit IO operations to 10 concurrent coroutines
val limitedIO = Dispatchers.IO.limitedParallelism(10)

suspend fun fetchAll(urls: List<String>): List<Response> = coroutineScope {
    urls.map { url ->
        async(limitedIO) {
            httpClient.get(url)
        }
    }.awaitAll()
}

// Single-threaded dispatcher (alternative to newSingleThreadContext)
val singleThread = Dispatchers.Default.limitedParallelism(1)
```

## Anti-Pattern: Blocking the Main Dispatcher

Never perform blocking I/O or heavy computation on the Main dispatcher. It freezes the UI.

```kotlin
// WRONG: Blocks the UI thread
viewModelScope.launch {
    val data = database.queryAll()  // Blocking call on Main!
    _uiState.value = UiState.Success(data)
}

// CORRECT: Move blocking work to IO
viewModelScope.launch {
    val data = withContext(Dispatchers.IO) {
        database.queryAll()
    }
    _uiState.value = UiState.Success(data)
}
```

## Anti-Pattern: Unconfined in Production

Unconfined is unpredictable because resumption happens on whatever thread the last suspending function used.

```kotlin
// WRONG: After the delay, this resumes on the DefaultExecutor thread
launch(Dispatchers.Unconfined) {
    updateUi()  // Runs on caller's thread (maybe Main)
    delay(100)
    updateUi()  // Runs on DefaultExecutor thread — NOT Main! Crash on Android.
}

// CORRECT: Explicitly use Main for UI work
launch(Dispatchers.Main) {
    updateUi()
    delay(100)
    updateUi()  // Still on Main — safe
}
```

## Summary

| Dispatcher | Thread Pool | Use For |
|-----------|-------------|---------|
| `Default` | CPU cores | CPU-intensive computation |
| `IO` | Up to 64+ threads | Blocking I/O, database, file, network |
| `Main` | Single UI thread | UI updates (Android) |
| `Unconfined` | Caller's thread, then varies | Tests only |
| `limitedParallelism(n)` | Subset of parent pool | Rate-limiting concurrent work |
| `newSingleThreadContext` | Dedicated single thread | Thread-confined resources |
