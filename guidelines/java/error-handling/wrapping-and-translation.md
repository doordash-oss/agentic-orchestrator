# Exception Wrapping and Translation

## Exception Translation

Higher layers should catch lower-level exceptions and throw exceptions
appropriate to the higher-level abstraction (Effective Java Item 73):

```java
// Wrong — leaks implementation details
public User getUser(String id) throws SQLException {
    return jdbc.query("SELECT ...", id);  // SQL leaks to callers
}

// Correct — translate to domain exception
public User getUser(String id) {
    try {
        return jdbc.query("SELECT ...", id);
    } catch (SQLException e) {
        throw new UserRepositoryException("fetching user " + id, e);
    }
}
```

## Exception Chaining

Always pass the original exception as the `cause` — never discard it:

```java
// Wrong — original cause is lost
catch (IOException e) {
    throw new ServiceException("load failed");  // cause lost!
}

// Correct — chain preserves the original for debugging
catch (IOException e) {
    throw new ServiceException("loading user profile", e);
}
```

All standard exception classes support chaining via a constructor that accepts
a `Throwable cause`. Custom exceptions should do the same:

```java
public class ServiceException extends RuntimeException {
    public ServiceException(String message, Throwable cause) {
        super(message, cause);
    }
    public ServiceException(String message) {
        super(message);
    }
}
```

## Failure-Capture Information

Include all relevant values in exception messages (Effective Java Item 75).
The detail message should capture **the values of all parameters and fields
that contributed to the exception**:

```java
// Weak — no diagnostic information
throw new IllegalArgumentException("invalid range");

// Strong — captures the actual values
throw new IllegalArgumentException(
    "lower bound " + lower + " exceeds upper bound " + upper);
```

**Never include** passwords, encryption keys, or other sensitive data in
exception messages — they end up in logs and stack traces.

## Multi-Catch (Java 7+)

Use multi-catch to handle multiple exception types with the same handler:

```java
// Before Java 7 — duplicated handler
try { ... }
catch (IOException e) { log.error("IO failed", e); }
catch (ParseException e) { log.error("IO failed", e); }

// Java 7+ — multi-catch
try { ... }
catch (IOException | ParseException e) {
    log.error("processing failed", e);
}
```

The caught variable in a multi-catch is implicitly `final` — you cannot
reassign it within the catch block.

## Try-with-Resources and Suppressed Exceptions

When a try-with-resources block throws and `close()` also throws, the
close exception is **suppressed** (attached to the primary exception):

```java
try (var conn = dataSource.getConnection()) {
    // throws ServiceException
} // conn.close() throws SQLException — becomes suppressed

// Access suppressed exceptions
catch (ServiceException e) {
    for (Throwable suppressed : e.getSuppressed()) {
        log.warn("suppressed during close", suppressed);
    }
}
```

## When NOT to Translate

Exception translation should not be overused. Where possible, ensure that
lower-level methods succeed (e.g., validate inputs before passing them down).
Only translate when the lower-level exception is **inappropriate for the
higher-level abstraction**.

```java
// Unnecessary translation — just let it propagate
try {
    Files.readAllBytes(path);
} catch (IOException e) {
    throw new IOException("reading file", e);  // adds nothing useful
}

// Useful translation — changes the abstraction level
try {
    Files.readAllBytes(configPath);
} catch (IOException e) {
    throw new ConfigLoadException("cannot read config: " + configPath, e);
}
```

## Failure Atomicity

When a method throws an exception, leave the object in its pre-invocation
state (Effective Java Item 76). Four approaches:

1. **Immutable objects** — no state to corrupt
2. **Validate parameters first** — check before mutating
3. **Temporary copy** — operate on a copy, swap on success
4. **Recovery code** — undo changes on failure (rare, for disk-based structures)

```java
// Validate before mutating
public void transfer(Account from, Account to, BigDecimal amount) {
    if (from.balance().compareTo(amount) < 0) {
        throw new InsufficientFundsException(from.id(), amount);
    }
    // Now safe to mutate — preconditions verified
    from.debit(amount);
    to.credit(amount);
}
```
