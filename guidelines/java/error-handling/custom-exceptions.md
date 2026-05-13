# Custom Exception Hierarchies

## When to Create Custom Exceptions

Create a custom exception when:

- Standard exceptions don't convey enough **domain-specific meaning**
- You need to carry **additional structured data** beyond a message string
- You want to enable **programmatic distinction** between error cases
- You're building a library/module with a public API

Don't create custom exceptions for one-off errors that `IllegalArgumentException`
or `IllegalStateException` already express well (Effective Java Item 72).

## Designing a Domain Exception Hierarchy

A well-designed hierarchy has a base exception per module or bounded context:

```java
// Base exception for the order module
public class OrderException extends RuntimeException {
    private final String orderId;

    public OrderException(String orderId, String message, Throwable cause) {
        super(message, cause);
        this.orderId = orderId;
    }

    public OrderException(String orderId, String message) {
        super(message);
        this.orderId = orderId;
    }

    public String getOrderId() { return orderId; }
}

// Specific subtypes
public class OrderNotFoundException extends OrderException {
    public OrderNotFoundException(String orderId) {
        super(orderId, "order not found: " + orderId);
    }
}

public class OrderAlreadyShippedException extends OrderException {
    public OrderAlreadyShippedException(String orderId) {
        super(orderId, "order already shipped: " + orderId);
    }
}
```

## Carrying Structured Failure Data

Add fields that aid programmatic recovery or debugging:

```java
public class ValidationException extends RuntimeException {
    private final List<FieldError> errors;

    public ValidationException(List<FieldError> errors) {
        super("validation failed: " + errors.size() + " error(s)");
        this.errors = List.copyOf(errors);  // defensive copy
    }

    public List<FieldError> getErrors() { return errors; }

    public record FieldError(String field, String message) {}
}
```

## Constructor Patterns

Every custom exception should provide at minimum:

```java
public class AppException extends RuntimeException {
    // Message only
    public AppException(String message) {
        super(message);
    }

    // Message + cause (most important — enables chaining)
    public AppException(String message, Throwable cause) {
        super(message, cause);
    }
}
```

Optionally add:
- A cause-only constructor for pure wrapping
- A no-arg constructor (only if a default message makes sense)

## Checked vs Unchecked for Custom Exceptions

Follow the same rule as standard exceptions:

```java
// Checked — caller must handle, recovery is expected
public class InsufficientFundsException extends Exception { ... }

// Unchecked — programming error or unrecoverable
public class InvalidOrderStateException extends RuntimeException { ... }
```

Modern Java practice leans toward unchecked exceptions for most domain errors,
with checked exceptions reserved for truly recoverable conditions at system
boundaries.

## Exception Hierarchy Anti-Patterns

- **Too flat**: every error is `new RuntimeException("message")` — impossible
  to catch selectively
- **Too deep**: 5+ levels of inheritance with no behavioral difference — adds
  complexity without benefit
- **Too broad base class**: catching the base catches everything including bugs
- **Mutable exceptions**: exception fields should be final; exceptions may be
  logged, serialized, or inspected across threads

## Documentation

Document all exceptions thrown by public methods (Effective Java Item 74):

```java
/**
 * Transfers funds between accounts.
 *
 * @param from source account
 * @param to destination account
 * @param amount transfer amount
 * @throws InsufficientFundsException if {@code from} has insufficient balance
 * @throws AccountClosedException if either account is closed
 * @throws IllegalArgumentException if {@code amount} is not positive
 */
public void transfer(Account from, Account to, BigDecimal amount) { ... }
```

- Use `@throws` for both checked and unchecked exceptions
- Do **not** put unchecked exceptions in the `throws` clause of the method
  signature — only in Javadoc
- Document unchecked exceptions that represent preconditions (helps callers
  understand the contract)
