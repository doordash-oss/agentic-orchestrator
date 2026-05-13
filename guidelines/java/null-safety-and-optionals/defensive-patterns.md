# Defensive Null Handling Patterns

## Objects.requireNonNull

The standard fail-fast pattern for null parameters:

```java
public OrderService(OrderRepository repository, PaymentService payments) {
    this.repository = Objects.requireNonNull(repository, "repository");
    this.payments = Objects.requireNonNull(payments, "payments");
}
```

Throws `NullPointerException` with the specified message immediately at the
call site — not later in some unrelated method.

## Helpful NullPointerExceptions (Java 14+)

The JVM now provides precise NPE messages:

```
// Before Java 14
Exception in thread "main" java.lang.NullPointerException

// Java 14+
Exception in thread "main" java.lang.NullPointerException:
    Cannot invoke "String.length()" because the return value of
    "User.getName()" is null
```

This makes many defensive null checks unnecessary — the error message itself
tells you what was null.

## Empty Collections Over Null

Never return null where a collection is expected:

```java
// Wrong — forces null checks on every caller
public List<Order> findOrders(String userId) {
    if (noOrders) return null;  // every caller must check for null
}

// Correct — empty collection
public List<Order> findOrders(String userId) {
    if (noOrders) return List.of();  // callers can iterate safely
}
```

Standard empty collections:
- `List.of()` — empty immutable list
- `Set.of()` — empty immutable set
- `Map.of()` — empty immutable map
- `Collections.emptyList()` — same thing, older API
- `Optional.empty()` — for optional values

## Null Object Pattern

Replace null with a no-op implementation:

```java
public interface Logger {
    void log(String message);

    // Null object — does nothing, but is never null
    Logger NOOP = message -> {};
}

// Usage — no null checks needed
Logger logger = config.isDebug() ? new ConsoleLogger() : Logger.NOOP;
logger.log("starting");  // safe even if NOOP
```

## Precondition Checking with Guava

If using Google Guava:

```java
import static com.google.common.base.Preconditions.*;

public void setAge(int age) {
    checkArgument(age >= 0, "age must be >= 0, was: %s", age);
    checkNotNull(name, "name must not be null");
    checkState(isInitialized(), "service not initialized");
}
```

Without Guava, use `Objects.requireNonNull` for nulls and manual `if` +
`IllegalArgumentException` for other preconditions.

## Map Access Patterns

```java
// Avoid null returns from Map.get
String value = map.getOrDefault(key, "default");

// Compute-if-absent pattern (atomic for ConcurrentHashMap)
var cached = cache.computeIfAbsent(key, k -> expensiveCompute(k));

// Merge pattern
counts.merge(word, 1, Integer::sum);  // increment or initialize
```

## The Hierarchy of Null Defense

From most to least preferred:

1. **Design nulls away** — use records, immutable types, empty collections
2. **Annotate with @NullMarked** — let static analysis find issues at compile time
3. **Return Optional** — for public APIs where absence is expected
4. **Fail fast with requireNonNull** — at API boundaries for required parameters
5. **Null checks in code** — last resort for legacy interop
