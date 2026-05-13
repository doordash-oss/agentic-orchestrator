# Checked vs Unchecked Exceptions

## The Decision Rule

Use **checked exceptions** when the caller can reasonably be expected to recover.
Use **runtime exceptions** (unchecked) for programming errors — typically
precondition violations.

```java
// Checked — caller can recover (retry, use default, prompt user)
public Config loadConfig(Path path) throws IOException { ... }

// Unchecked — programming error, caller should fix the code
public void setAge(int age) {
    if (age < 0) throw new IllegalArgumentException("age must be >= 0: " + age);
}
```

## The Throwable Hierarchy

```
Throwable
├── Error              — JVM/system failures (OutOfMemoryError, StackOverflowError)
│                        Never catch these in application code
├── Exception          — Checked exceptions (must be caught or declared)
│   └── RuntimeException — Unchecked exceptions (no catch requirement)
```

**Rule**: all custom unchecked exceptions must extend `RuntimeException`.
Never extend `Error` or `Throwable` directly.

## When to Use Checked Exceptions

Checked exceptions are appropriate when **both** conditions hold:

1. The exceptional condition **cannot be prevented** by proper use of the API
2. The caller **can take useful action** when confronted with the exception

```java
// Good use of checked exception — caller can recover
public User findUser(String id) throws UserNotFoundException {
    // Caller can show an error page, create the user, etc.
}

// Bad use of checked exception — caller can't do anything useful
public void saveConfig() throws ConfigException {
    // If this wraps an internal bug, caller can only rethrow or log
}
```

## When to Avoid Checked Exceptions

Avoid checked exceptions when the only reasonable responses are:
- `e.printStackTrace()` — indicates the exception should be unchecked
- `throw new AssertionError(e)` — indicates a bug, not a recoverable condition
- Wrapping in a generic unchecked exception just to satisfy the compiler

Checked exceptions also **cannot be used in streams** (lambdas in `Stream.map()`
cannot throw checked exceptions), which is another signal to prefer unchecked
exceptions in functional-style APIs.

## Standard Exception Types

Prefer standard exceptions over custom ones when they fit (Effective Java Item 72):

| Exception | When to Use |
|-----------|-------------|
| `IllegalArgumentException` | Non-null parameter value is inappropriate |
| `IllegalStateException` | Object state is inappropriate for the method call |
| `NullPointerException` | Parameter is null where prohibited |
| `IndexOutOfBoundsException` | Index parameter is out of range |
| `ConcurrentModificationException` | Concurrent modification detected where not allowed |
| `UnsupportedOperationException` | Object does not support the requested operation |

**Convention**: prefer `NullPointerException` over `IllegalArgumentException` for
null parameters, and `IndexOutOfBoundsException` over `IllegalArgumentException`
for out-of-range indices.

**Never directly throw** `Exception`, `RuntimeException`, `Throwable`, or `Error`.
Treat these as abstract.

## The Modern Consensus

The Java community has shifted toward **fewer checked exceptions** over time:

- Spring Framework uses only unchecked exceptions
- Most modern libraries follow this pattern
- Checked exceptions remain valuable at **system boundaries** (file IO, network,
  database) where recovery is genuinely possible
- For internal APIs, unchecked exceptions with good documentation (`@throws`
  Javadoc) are preferred
