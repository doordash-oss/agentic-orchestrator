# Try-with-Resources

## The Rule

**Always** use try-with-resources for any `AutoCloseable` resource. This is
non-negotiable — manual `try-finally` is error-prone and verbose:

```java
// Correct — resources are closed automatically, even on exception
try (var conn = dataSource.getConnection();
     var stmt = conn.prepareStatement(sql);
     var rs = stmt.executeQuery()) {
    while (rs.next()) {
        results.add(mapRow(rs));
    }
}

// Wrong — manual close is error-prone
Connection conn = null;
try {
    conn = dataSource.getConnection();
    // ... what if an exception occurs before close?
} finally {
    if (conn != null) conn.close();  // if this throws, original exception is lost
}
```

## Multiple Resources

Declare multiple resources separated by semicolons. They are closed in
**reverse order** of declaration:

```java
try (var input = new FileInputStream(source);
     var output = new FileOutputStream(target)) {
    input.transferTo(output);
}
// output closed first, then input
```

## Suppressed Exceptions

When the try block throws and `close()` also throws, the close exception is
**suppressed** (attached to the primary exception):

```java
try (var resource = new MyResource()) {
    throw new ServiceException("primary error");
}
// If resource.close() throws IOException:
// - ServiceException is the primary exception
// - IOException is suppressed (accessible via getSuppressed())
```

With manual `try-finally`, the close exception **replaces** the primary
exception — a critical bug that try-with-resources fixes.

## Implementing AutoCloseable

For your own resource-holding classes:

```java
public class ConnectionPool implements AutoCloseable {
    private final List<Connection> connections;
    private volatile boolean closed;

    @Override
    public void close() {
        if (closed) return;  // idempotent
        closed = true;
        var errors = new ArrayList<Exception>();
        for (var conn : connections) {
            try {
                conn.close();
            } catch (Exception e) {
                errors.add(e);
            }
        }
        if (!errors.isEmpty()) {
            var first = errors.removeFirst();
            errors.forEach(first::addSuppressed);
            throw new RuntimeException("failed to close pool", first);
        }
    }
}
```

**Rules for AutoCloseable**:
- `close()` must be **idempotent** — calling it multiple times is safe
- Close sub-resources, collecting exceptions
- Throw after all sub-resources have been attempted

## Effectively-Final Resources (Java 9+)

If the resource variable is effectively final, you can use it in
try-with-resources without re-declaring:

```java
Connection conn = dataSource.getConnection();
try (conn) {  // Java 9+ — no need for: try (Connection c = conn)
    process(conn);
}
```

## When to Use finally Instead

Rarely. The main cases:
- **Non-closeable cleanup** — resetting a flag, releasing a non-AutoCloseable resource
- **Guaranteed logging** — logging that must happen regardless of outcome

```java
lock.lock();
try {
    // critical section
} finally {
    lock.unlock();  // ReentrantLock is not AutoCloseable
}
```
