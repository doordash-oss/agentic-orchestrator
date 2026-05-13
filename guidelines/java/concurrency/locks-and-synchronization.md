# Locks and Synchronization

## synchronized vs ReentrantLock

| Feature | `synchronized` | `ReentrantLock` |
|---------|---------------|-----------------|
| Syntax | Block/method keyword | Explicit lock/unlock |
| Fairness | Not configurable | Optional fair ordering |
| Interruptible | No | `lockInterruptibly()` |
| Try-lock | No | `tryLock(timeout)` |
| Condition variables | Single (wait/notify) | Multiple `Condition` objects |
| Virtual thread friendly | Java 24+ only | Java 21+ |

**Use `synchronized`** for simple, short critical sections where you don't need
advanced features. **Use `ReentrantLock`** when you need try-lock, timeout,
fairness, multiple conditions, or virtual thread support (Java 21-23).

```java
// synchronized — simple and clean
synchronized (lock) {
    counter++;
}

// ReentrantLock — more control
private final ReentrantLock lock = new ReentrantLock();

lock.lock();
try {
    counter++;
} finally {
    lock.unlock();  // ALWAYS in finally
}
```

**Critical rule**: always release `ReentrantLock` in a `finally` block.
Forgetting to unlock causes deadlocks.

## ReadWriteLock

Use when reads vastly outnumber writes:

```java
private final ReadWriteLock rwLock = new ReentrantReadWriteLock();
private final Map<String, String> cache = new HashMap<>();

public String get(String key) {
    rwLock.readLock().lock();
    try {
        return cache.get(key);  // multiple readers allowed concurrently
    } finally {
        rwLock.readLock().unlock();
    }
}

public void put(String key, String value) {
    rwLock.writeLock().lock();
    try {
        cache.put(key, value);  // exclusive access
    } finally {
        rwLock.writeLock().unlock();
    }
}
```

## StampedLock (Java 8+)

Optimistic reads for read-heavy workloads with minimal write contention:

```java
private final StampedLock lock = new StampedLock();
private double x, y;

public double distanceFromOrigin() {
    long stamp = lock.tryOptimisticRead();  // no actual lock acquired
    double currentX = x, currentY = y;
    if (!lock.validate(stamp)) {            // check if a write occurred
        stamp = lock.readLock();            // fall back to pessimistic read
        try {
            currentX = x;
            currentY = y;
        } finally {
            lock.unlockRead(stamp);
        }
    }
    return Math.sqrt(currentX * currentX + currentY * currentY);
}
```

**Caution**: `StampedLock` is not reentrant. Using it from synchronized code or
calling it recursively causes deadlock.

## Atomic Variables

For single variables, atomic operations avoid the overhead of locks:

```java
private final AtomicInteger counter = new AtomicInteger(0);

counter.incrementAndGet();                    // atomic increment
counter.compareAndSet(expected, newValue);    // CAS operation
counter.updateAndGet(v -> v * 2);            // atomic update with function

// AtomicReference for objects
private final AtomicReference<Config> config = new AtomicReference<>(defaultConfig);
config.set(newConfig);
Config current = config.get();
```

**Use atomics** for single counters, flags, or references.
**Use locks** when you need to update multiple related fields atomically.

## Common Concurrency Bugs

### Race Conditions
```java
// Bug — check-then-act is not atomic
if (!map.containsKey(key)) {      // Thread A checks
    map.put(key, computeValue()); // Thread B also puts between check and put
}

// Fix — use atomic operations
map.computeIfAbsent(key, k -> computeValue());
```

### Deadlocks
```java
// Bug — inconsistent lock ordering
// Thread 1: lock(A) -> lock(B)
// Thread 2: lock(B) -> lock(A)

// Fix — always acquire locks in a consistent global order
```

### Memory Visibility
```java
// Bug — running flag may never be seen as false by the thread
private boolean running = true;  // not volatile!

// Fix — use volatile for flags read by other threads
private volatile boolean running = true;
```

## Thread Safety Strategies

In order of preference:

1. **Immutability** — immutable objects are inherently thread-safe
2. **Confinement** — don't share state between threads
3. **Concurrent collections** — use `ConcurrentHashMap`, `CopyOnWriteArrayList`
4. **Atomic variables** — for single values
5. **Locks** — when nothing else works, use the simplest lock that fits
