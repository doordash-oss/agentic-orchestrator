# Concurrent Collections

## Choosing the Right Collection

| Need | Thread-Safe Choice |
|------|-------------------|
| Key-value map, high concurrency | `ConcurrentHashMap` |
| List, mostly reads | `CopyOnWriteArrayList` |
| Queue, producer-consumer | `BlockingQueue` implementations |
| Sorted set, concurrent | `ConcurrentSkipListSet` |
| Sorted map, concurrent | `ConcurrentSkipListMap` |
| Fast reads, occasional writes | `CopyOnWriteArraySet` |

**Never** synchronize a `ConcurrentHashMap` externally — it defeats the purpose
of the lock-striping design.

## ConcurrentHashMap

The workhorse of concurrent Java. Uses lock striping for high throughput:

```java
ConcurrentHashMap<String, Integer> counts = new ConcurrentHashMap<>();

// Atomic compute operations — the right way
counts.computeIfAbsent("key", k -> expensiveComputation(k));
counts.merge("key", 1, Integer::sum);  // atomic increment
counts.compute("key", (k, v) -> v == null ? 1 : v + 1);

// Wrong — not atomic, race condition
Integer count = counts.get("key");
counts.put("key", count == null ? 1 : count + 1);  // lost update!
```

### Bulk Operations (Java 8+)

```java
// Parallel forEach (threshold = parallelism trigger)
counts.forEach(1000, (key, value) ->
    log.info("{} = {}", key, value));

// Parallel search — returns first non-null result
String found = counts.search(1000, (key, value) ->
    value > 100 ? key : null);

// Parallel reduce
int total = counts.reduce(1000, (key, value) -> value, Integer::sum);
```

The `long` parameter is the parallelism threshold — use `1` for maximum
parallelism, `Long.MAX_VALUE` for sequential execution.

## BlockingQueue

The standard producer-consumer pattern:

```java
BlockingQueue<Task> queue = new ArrayBlockingQueue<>(100);  // bounded

// Producer
queue.put(task);  // blocks if full — provides backpressure

// Consumer
Task task = queue.take();  // blocks if empty

// With timeout
Task task = queue.poll(5, TimeUnit.SECONDS);  // null if timeout
```

| Implementation | When to Use |
|---------------|-------------|
| `ArrayBlockingQueue` | Bounded, fixed capacity, fairness option |
| `LinkedBlockingQueue` | Optionally bounded, slightly higher throughput |
| `PriorityBlockingQueue` | Unbounded, priority ordering |
| `SynchronousQueue` | Zero capacity — direct handoff, tight coupling |
| `LinkedTransferQueue` | High-performance, used by ForkJoinPool |

**Rule**: always use bounded queues in production. Unbounded queues can
cause `OutOfMemoryError` under load.

## CopyOnWriteArrayList

Thread-safe by copying the entire array on every write. Reads are lock-free:

```java
CopyOnWriteArrayList<EventListener> listeners = new CopyOnWriteArrayList<>();

// Fast, lock-free iteration — sees a snapshot
for (var listener : listeners) {
    listener.onEvent(event);
}

// Writes copy the array — expensive
listeners.add(newListener);
```

**Use only when** reads vastly outnumber writes (e.g., listener lists, config).
For frequent writes, use `ConcurrentLinkedQueue` or synchronized collections.

## Collections.synchronizedXxx Wrappers

A legacy approach — wraps every method in `synchronized`:

```java
List<String> syncList = Collections.synchronizedList(new ArrayList<>());

// Iteration MUST be manually synchronized
synchronized (syncList) {
    for (var item : syncList) {  // ConcurrentModificationException without sync
        process(item);
    }
}
```

**Prefer concurrent collections** (`ConcurrentHashMap`, `CopyOnWriteArrayList`)
over synchronized wrappers — they offer better concurrency and don't require
manual synchronization during iteration.

## Anti-Patterns

```java
// Wrong — synchronizing ConcurrentHashMap externally
synchronized (concurrentMap) {
    concurrentMap.put(key, value);  // defeats lock striping
}

// Wrong — iterating ConcurrentHashMap to update (race condition)
for (var entry : map.entrySet()) {
    map.put(entry.getKey(), entry.getValue() + 1);  // lost updates possible
}
// Fix — use atomic operations
map.replaceAll((key, value) -> value + 1);

// Wrong — unbounded queue in production
new LinkedBlockingQueue<>();  // default is Integer.MAX_VALUE
// Fix — always specify capacity
new LinkedBlockingQueue<>(10_000);
```
