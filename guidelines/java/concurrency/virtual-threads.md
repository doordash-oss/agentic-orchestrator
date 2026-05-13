# Virtual Threads (Java 21+)

## What Are Virtual Threads

Virtual threads (JEP 444) are lightweight threads managed by the JVM, not the OS.
They store call stacks on the heap and are multiplexed onto a small pool of
platform (carrier) threads. You can create millions of them without exhausting
OS resources.

```java
// Create a virtual thread
Thread.startVirtualThread(() -> {
    var result = blockingHttpCall();  // blocks only the virtual thread
    process(result);
});

// Using the factory
try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
    executor.submit(() -> handleRequest(request));
}
```

## When to Use Virtual Threads

**Use virtual threads for IO-bound workloads** — HTTP servers, database clients,
file processing, message consumers. Each task gets its own thread; blocking is
cheap.

**Don't use virtual threads for CPU-bound work** — they share carrier threads
from a ForkJoinPool. CPU-heavy tasks starve other virtual threads. Use platform
thread pools for computation.

```java
// Good — IO-bound: one virtual thread per request
try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
    for (var request : requests) {
        executor.submit(() -> {
            var data = fetchFromDatabase(request);   // blocks, but cheap
            var response = callExternalApi(data);     // blocks, but cheap
            return buildResult(response);
        });
    }
}

// Bad — CPU-bound: starves other virtual threads
try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
    executor.submit(() -> computePrimes(1_000_000));  // monopolizes carrier
}
```

## The Pinning Problem (Java 21-23)

A virtual thread **pins** its carrier thread when it blocks inside a
`synchronized` block/method or during native/JNI calls. While pinned, the
carrier cannot serve other virtual threads.

```java
// Problematic in Java 21-23 — pins carrier during IO
synchronized (lock) {
    var data = inputStream.read();  // carrier is pinned!
}

// Fix for Java 21-23 — use ReentrantLock instead
private final ReentrantLock lock = new ReentrantLock();

lock.lock();
try {
    var data = inputStream.read();  // virtual thread can unmount
} finally {
    lock.unlock();
}
```

**Java 24+ (JEP 491)**: synchronized no longer pins virtual threads. You can
choose between `synchronized` and `ReentrantLock` based purely on which fits
your design. Pinning still occurs with native/JNI/FFM calls.

## Detecting Pinning

```bash
# Full stack trace on pin (Java 21-23)
java -Djdk.tracePinnedThreads=full MyApp

# Short output (just problematic frames)
java -Djdk.tracePinnedThreads=short MyApp
```

JDK Flight Recorder emits `jdk.VirtualThreadPinned` events when pinning
exceeds 20ms (configurable).

## Migration Guidelines

1. **Replace `Executors.newFixedThreadPool(N)` with `Executors.newVirtualThreadPerTaskExecutor()`** for IO-bound work
2. **Remove thread-local caching** — virtual threads are too numerous for per-thread caches; use scoped values (JEP 481) or shared caches
3. **Don't pool virtual threads** — they are cheap to create; pooling defeats the purpose
4. **Update dependencies** — check that JDBC drivers, HTTP clients, etc. are virtual-thread-friendly (e.g., PostgreSQL driver >= 42.6)
5. **Replace `synchronized` with `ReentrantLock`** around blocking IO (Java 21-23 only)

## Anti-Patterns

```java
// Wrong — pooling virtual threads (they are cheap, no pooling needed)
var pool = Executors.newFixedThreadPool(100,
    Thread.ofVirtual().factory());

// Wrong — thread-local cache with virtual threads (millions of copies)
private static final ThreadLocal<SimpleDateFormat> FORMAT =
    ThreadLocal.withInitial(() -> new SimpleDateFormat("yyyy-MM-dd"));

// Wrong — using virtual threads for CPU-bound work
Executors.newVirtualThreadPerTaskExecutor()
    .submit(() -> fibonacci(1_000_000));
```

## Thread-Per-Task vs Reactive

Virtual threads offer a simpler programming model than reactive frameworks
(Project Reactor, RxJava) for IO-bound work. Instead of chaining callbacks:

```java
// Reactive style (complex, hard to debug)
Mono.fromCallable(() -> fetchUser(id))
    .flatMap(user -> fetchOrders(user.id()))
    .map(this::buildResponse);

// Virtual thread style (simple, sequential, debuggable)
Thread.startVirtualThread(() -> {
    var user = fetchUser(id);          // blocks cheaply
    var orders = fetchOrders(user.id()); // blocks cheaply
    return buildResponse(orders);
});
```

Virtual threads achieve similar throughput with straightforward imperative code.
Consider them as a replacement for reactive patterns in new projects.
