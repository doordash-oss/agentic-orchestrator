# Executors and Thread Pools

## Executor Framework Overview

Never create raw `Thread` objects for production work. Use the `ExecutorService`
framework, which manages thread lifecycle, queuing, and error handling:

```java
// Wrong — manual thread management
new Thread(() -> processRequest(request)).start();

// Correct — executor manages the thread
ExecutorService executor = Executors.newFixedThreadPool(10);
executor.submit(() -> processRequest(request));
```

## Choosing an Executor Type

| Executor | Use Case |
|----------|----------|
| `newVirtualThreadPerTaskExecutor()` | IO-bound work (Java 21+) — preferred default |
| `newFixedThreadPool(n)` | CPU-bound work with known parallelism |
| `newCachedThreadPool()` | Short-lived tasks with variable load (be careful — unbounded) |
| `newSingleThreadExecutor()` | Sequential task execution, event loops |
| `newScheduledThreadPool(n)` | Delayed or periodic tasks |
| `newWorkStealingPool()` | CPU-bound fork/join work |

**Java 21+ recommendation**: for IO-bound work, use virtual threads instead of
fixed thread pools. Reserve platform thread pools for CPU-bound computation.

## Thread Pool Sizing

For **CPU-bound** tasks:
```
threads = number of CPU cores (Runtime.getRuntime().availableProcessors())
```

For **IO-bound** tasks (pre-Java 21):
```
threads = CPU cores * (1 + wait_time / compute_time)
```

For IO-bound tasks on Java 21+, use virtual threads instead of sizing a pool.

```java
// CPU-bound pool
int cpus = Runtime.getRuntime().availableProcessors();
ExecutorService cpuPool = Executors.newFixedThreadPool(cpus);

// IO-bound (Java 21+) — no sizing needed
ExecutorService ioExecutor = Executors.newVirtualThreadPerTaskExecutor();
```

## Proper Shutdown

Always shut down executors — they prevent JVM exit if left running:

```java
ExecutorService executor = Executors.newFixedThreadPool(10);
try {
    // submit tasks...
} finally {
    executor.shutdown();  // stop accepting new tasks
    if (!executor.awaitTermination(30, TimeUnit.SECONDS)) {
        executor.shutdownNow();  // interrupt running tasks
        if (!executor.awaitTermination(10, TimeUnit.SECONDS)) {
            log.error("executor did not terminate");
        }
    }
}
```

**Java 19+**: `ExecutorService` implements `AutoCloseable`, so use
try-with-resources:

```java
try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
    executor.submit(() -> process(item));
    // executor.close() called automatically — waits for all tasks
}
```

## Submitting Tasks

```java
// Fire-and-forget (returns Future but result often ignored)
executor.submit(() -> logEvent(event));

// Get a result
Future<Result> future = executor.submit(() -> compute(data));
Result result = future.get(5, TimeUnit.SECONDS);  // blocks with timeout

// Run all and collect results
List<Callable<Result>> tasks = items.stream()
    .map(item -> (Callable<Result>) () -> process(item))
    .toList();
List<Future<Result>> futures = executor.invokeAll(tasks);

// Race — first to complete wins
Result fastest = executor.invokeAny(tasks);
```

## Common Pitfalls

```java
// Wrong — unbounded queue with fixed pool (can OOM)
// newFixedThreadPool uses LinkedBlockingQueue (unbounded)
// If tasks arrive faster than processing, queue grows forever

// Better — bounded queue with rejection policy
var executor = new ThreadPoolExecutor(
    10, 10,                          // core and max size
    0L, TimeUnit.MILLISECONDS,
    new ArrayBlockingQueue<>(1000),  // bounded queue
    new ThreadPoolExecutor.CallerRunsPolicy()  // backpressure
);

// Wrong — using Executors.newCachedThreadPool() for heavy loads
// Creates unlimited threads — can exhaust OS resources

// Wrong — not handling exceptions from submitted tasks
executor.submit(() -> {
    throw new RuntimeException("oops");  // silently swallowed!
});

// Fix — use try-catch inside the task or check the Future
executor.submit(() -> {
    try {
        riskyWork();
    } catch (Exception e) {
        log.error("task failed", e);
    }
});
```

## Thread Naming

Name threads for debuggability:

```java
var factory = Thread.ofPlatform()
    .name("order-processor-", 0)  // order-processor-0, order-processor-1, ...
    .factory();
var executor = Executors.newFixedThreadPool(10, factory);
```
