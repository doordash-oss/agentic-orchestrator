# Channels and Select

Channels are hot, synchronization-based primitives for communicating between coroutines. Unlike flows, channels are designed for scenarios where producers and consumers run concurrently and you need buffering, fan-out, or fan-in patterns.

## Channel Types

Kotlin provides four channel types with different buffering behaviors.

```kotlin
// Rendezvous (capacity = 0, default) — sender suspends until receiver is ready
val rendezvous = Channel<Int>()

// Buffered — sender suspends only when buffer is full
val buffered = Channel<Int>(capacity = 10)

// Conflated — keeps only the latest value, never suspends the sender
val conflated = Channel<Int>(Channel.CONFLATED)

// Unlimited — unlimited buffer, sender never suspends (careful with memory)
val unlimited = Channel<Int>(Channel.UNLIMITED)
```

## Core Operations

`send` and `receive` are both suspending functions. The channel synchronizes producers and consumers.

```kotlin
val channel = Channel<String>()

// Producer coroutine
launch {
    channel.send("Hello")
    channel.send("World")
    channel.close()  // Signal that no more elements will be sent
}

// Consumer coroutine
launch {
    for (msg in channel) {  // Iterates until channel is closed
        println(msg)
    }
}
```

Always close channels when the producer is done, or cancel the producing coroutine. An unclosed channel with no active producer causes consumers to suspend forever.

## produce { } Builder

The `produce` builder creates a channel and a coroutine that populates it. The channel is closed automatically when the builder block completes.

```kotlin
// CORRECT: produce handles channel lifecycle automatically
fun CoroutineScope.produceNumbers(): ReceiveChannel<Int> = produce {
    var n = 1
    while (true) {
        send(n++)
        delay(100)
    }
}

// Consumer
val numbers = produceNumbers()
repeat(5) { println(numbers.receive()) }
numbers.cancel()  // Cancel the producing coroutine
```

## Producer-Consumer Pattern

A single producer sends work items to one or more consumers through a channel.

```kotlin
suspend fun processOrders(orders: List<Order>) = coroutineScope {
    val channel = Channel<Order>(capacity = 20)

    // Single producer
    launch {
        orders.forEach { channel.send(it) }
        channel.close()
    }

    // Multiple consumers (fan-out)
    repeat(5) { workerId ->
        launch {
            for (order in channel) {
                println("Worker $workerId processing ${order.id}")
                processOrder(order)
            }
        }
    }
}
```

## Pipeline Pattern

Chain producers together, where each stage reads from an upstream channel and writes to a downstream channel.

```kotlin
fun CoroutineScope.produceIds(): ReceiveChannel<Long> = produce {
    var id = 1L
    while (true) {
        send(id++)
        delay(100)
    }
}

fun CoroutineScope.enrichIds(
    ids: ReceiveChannel<Long>,
): ReceiveChannel<User> = produce {
    for (id in ids) {
        send(userService.fetchUser(id))
    }
}

fun CoroutineScope.filterActive(
    users: ReceiveChannel<User>,
): ReceiveChannel<User> = produce {
    for (user in users) {
        if (user.isActive) send(user)
    }
}

// Usage: pipeline of stages
val ids = produceIds()
val users = enrichIds(ids)
val active = filterActive(users)

repeat(10) { println(active.receive()) }

// Cancel the pipeline from the source
ids.cancel()
```

## Fan-Out: Multiple Consumers

Multiple coroutines can receive from a single channel. Each element is delivered to exactly one consumer (load balancing).

```kotlin
// CORRECT: Use for-in loop for fan-out — safe with multiple consumers
val tasks = Channel<Task>(capacity = 100)

repeat(workerCount) { id ->
    launch {
        for (task in tasks) {
            process(task)
        }
    }
}
```

```kotlin
// WRONG: consumeEach cancels the channel on exception — not safe with multiple consumers
repeat(workerCount) { id ->
    launch {
        tasks.consumeEach { task ->  // If this throws, channel is cancelled for all workers
            process(task)
        }
    }
}
```

Use `consumeEach` only when there is a single consumer.

## Fan-In: Multiple Producers

Multiple coroutines can send to a single channel. All elements are interleaved in the order they arrive.

```kotlin
suspend fun mergeFeeds(feeds: List<Feed>): ReceiveChannel<Article> = coroutineScope {
    val merged = Channel<Article>(capacity = 50)

    feeds.forEach { feed ->
        launch {
            feed.articles().collect { article ->
                merged.send(article)
            }
        }
    }

    // Close merged channel when all producers are done
    // (coroutineScope waits for all children)
    // Note: the invokeOnCompletion approach below is needed because
    // coroutineScope itself suspends
    merged
}

// Alternative: explicit completion tracking
suspend fun mergeFeeds(feeds: List<Feed>): ReceiveChannel<Article> {
    val merged = Channel<Article>(capacity = 50)
    val scope = CoroutineScope(currentCoroutineContext() + SupervisorJob())

    feeds.forEach { feed ->
        scope.launch {
            feed.articles().collect { article ->
                merged.send(article)
            }
        }
    }

    scope.launch {
        // Wait for all feed producers, then close
        scope.coroutineContext[Job]!!.children.toList()
            .filter { it !== coroutineContext[Job] }
            .forEach { it.join() }
        merged.close()
    }

    return merged
}
```

## Ticker Channels

Use ticker channels for time-based periodic operations.

```kotlin
fun CoroutineScope.ticker(delayMillis: Long): ReceiveChannel<Unit> = produce {
    while (true) {
        send(Unit)
        delay(delayMillis)
    }
}

// Usage
val tick = ticker(1000)
repeat(10) {
    tick.receive()
    println("Tick at ${System.currentTimeMillis()}")
}
tick.cancel()
```

## Select Expression

`select` lets you await multiple suspending operations simultaneously, proceeding with the first one that becomes available.

```kotlin
suspend fun fetchFastest(
    primary: ReceiveChannel<Data>,
    fallback: ReceiveChannel<Data>,
): Data = select {
    primary.onReceive { it }
    fallback.onReceive { it }
}
```

## When to Use Channels vs Flows

| Use Case | Channels | Flows |
|----------|----------|-------|
| Stream type | Hot (always active) | Cold (on-demand) |
| Multiple consumers sharing work | Yes (fan-out) | No |
| Multiple producers merging | Yes (fan-in) | Use `merge` operator |
| Operators (map, filter, etc.) | Limited | Rich operator set |
| Backpressure | Buffer + suspend | Built-in |
| Reactive UI state | No | StateFlow/SharedFlow |
| One-shot async | No (overkill) | No (use suspend) |

**Rule of thumb**: Start with `Flow`. Reach for channels when you need fan-out, fan-in, or explicit buffering between concurrent coroutines.

## Common Mistakes

```kotlin
// WRONG: Forgetting to close — consumer hangs forever
val channel = Channel<Int>()
launch {
    repeat(5) { channel.send(it) }
    // Missing channel.close()
}
for (value in channel) { /* hangs after 5th element */ }

// CORRECT: Always close or use produce { }
val channel = Channel<Int>()
launch {
    repeat(5) { channel.send(it) }
    channel.close()
}
for (value in channel) { println(value) }
```

```kotlin
// WRONG: Sending to a closed channel throws ClosedSendChannelException
val channel = Channel<Int>()
channel.close()
channel.send(1)  // Throws!

// CORRECT: Check isClosedForSend or use trySend
val result = channel.trySend(1)
if (result.isFailure) {
    println("Channel is closed or full")
}
```
