# Exception Anti-Patterns

## Never Use Exceptions for Control Flow

Exceptions are for exceptional conditions — never for ordinary control flow
(Effective Java Item 69). The JVM does not optimize exception paths, and
try-catch blocks inhibit JVM optimizations:

```java
// Wrong — using exceptions as loop control
try {
    int i = 0;
    while (true) {
        process(array[i++]);
    }
} catch (ArrayIndexOutOfBoundsException e) { }

// Correct — use standard iteration
for (int i = 0; i < array.length; i++) {
    process(array[i]);
}
```

Performance impact: exception-based loops run up to **2x slower** than standard
loops, and they mask real bugs (what if `process()` throws
`ArrayIndexOutOfBoundsException`?).

## Never Swallow Exceptions

An empty catch block defeats the purpose of exceptions (Effective Java Item 77):

```java
// Wrong — exception silently swallowed
try {
    connection.close();
} catch (SQLException e) { }

// Correct — log it
try {
    connection.close();
} catch (SQLException e) {
    log.warn("failed to close connection", e);
}

// If truly ignorable — document why, name variable 'ignored'
try {
    connection.close();
} catch (SQLException ignored) {
    // Connection is from a pool; pool handles cleanup
}
```

## Never Catch Exception or Throwable Broadly

Catching `Exception` or `Throwable` catches everything including
`NullPointerException`, `OutOfMemoryError`, and other bugs:

```java
// Wrong — catches programming errors too
try {
    processOrder(order);
} catch (Exception e) {
    log.error("order failed", e);
}

// Correct — catch specific types
try {
    processOrder(order);
} catch (OrderValidationException e) {
    return Response.badRequest(e.getMessage());
} catch (PaymentFailedException e) {
    return Response.serviceUnavailable("payment system down");
}
```

**Exception**: catching `Exception` is acceptable in top-level handlers
(e.g., `main()`, HTTP framework error handlers, thread `UncaughtExceptionHandler`)
where you genuinely want to catch everything and log/report it.

## Never Catch and Rethrow Without Adding Context

Catching only to rethrow the same exception adds a stack frame but no value:

```java
// Wrong — pointless catch-and-rethrow
try {
    loadConfig();
} catch (IOException e) {
    throw e;  // adds nothing
}

// Correct — add context when translating
try {
    loadConfig();
} catch (IOException e) {
    throw new StartupException("failed to load application config", e);
}
```

## Never Use return/break/continue in finally

Code in `finally` blocks that alters control flow silently swallows exceptions:

```java
// Wrong — return in finally silently swallows any exception
try {
    return riskyOperation();
} finally {
    return defaultValue;  // exception from try is lost!
}
```

Use try-with-resources instead of `finally` for resource cleanup. Reserve
`finally` for rare cases where try-with-resources is insufficient.

## Avoid Declaring Generic throws

Don't declare `throws Exception` on methods — it forces callers into the
catch-everything anti-pattern:

```java
// Wrong — too broad
public void processOrder(Order order) throws Exception { ... }

// Correct — specific
public void processOrder(Order order) throws OrderException, PaymentException { ... }
```

## Don't Use Exceptions to Return Data

Exceptions should signal failure, not carry result data:

```java
// Wrong — using exceptions as a return channel
try {
    validate(input);
} catch (ValidationResultException e) {
    return e.getValidationResult();  // misuse of exception
}

// Correct — return a result object
ValidationResult result = validate(input);
if (!result.isValid()) {
    return result;
}
```

## Log OR Throw, Never Both

Logging and then throwing means the same error is logged multiple times
as it propagates up the stack:

```java
// Wrong — logs at every level
catch (IOException e) {
    log.error("read failed", e);  // logged here
    throw new ServiceException("read failed", e);  // and again when caught above
}

// Correct — throw with context; let the top-level handler log
catch (IOException e) {
    throw new ServiceException("reading user profile", e);
}
```

The top-level handler (controller, main loop, framework error handler) is
the right place to log the full exception chain once.
