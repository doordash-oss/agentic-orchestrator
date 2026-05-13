# Preconditions and Assertions

Kotlin provides a set of standard-library functions for defensive programming. These
fail fast with clear diagnostics when invariants are violated, catching bugs close to
their source rather than letting invalid state propagate.

## require -- Argument Validation

`require(condition) { "message" }` throws `IllegalArgumentException` when the condition
is false. Use it at the top of public functions to validate incoming arguments.

```kotlin
fun setAge(age: Int) {
    require(age > 0) { "Age must be positive, got $age" }
    require(age < 150) { "Age must be realistic, got $age" }
    this.age = age
}

fun sendEmail(to: String, body: String) {
    require(to.contains("@")) { "Invalid email address: $to" }
    require(body.isNotBlank()) { "Email body must not be blank" }
    // ...
}
```

Place `require` calls before any side effects. This ensures the function either
validates all arguments successfully or throws before modifying state.

### requireNotNull

`requireNotNull(value) { "message" }` throws `IllegalArgumentException` if the value is
null. It returns the non-null value, making it useful for smart-casting.

```kotlin
fun processUser(user: User?) {
    val safeUser = requireNotNull(user) { "User must not be null" }
    // safeUser is smart-cast to User (non-null)
    println(safeUser.name)
}
```

Prefer `requireNotNull` over the `!!` operator. The `!!` operator throws a
`NullPointerException` with no message, making failures harder to diagnose.

```kotlin
// BAD -- no diagnostic information
val name = user.name!!

// GOOD -- clear message on failure
val name = requireNotNull(user.name) { "User name must not be null for user $user" }
```

## check -- State Validation

`check(condition) { "message" }` throws `IllegalStateException` when the condition is
false. Use it to verify that an object is in a valid state for the requested operation.

```kotlin
class Connection {
    private var isOpen = false

    fun open() {
        check(!isOpen) { "Connection is already open" }
        isOpen = true
        // ...
    }

    fun close() {
        check(isOpen) { "Cannot close: connection is already closed" }
        isOpen = false
        // ...
    }

    fun execute(query: String): ResultSet {
        check(isOpen) { "Cannot execute query: connection is not open" }
        // ...
    }
}
```

### checkNotNull

`checkNotNull(value) { "message" }` throws `IllegalStateException` if the value is
null. Use it when a null value indicates invalid internal state rather than bad input.

```kotlin
class OrderProcessor {
    private var currentOrder: Order? = null

    fun confirm() {
        val order = checkNotNull(currentOrder) {
            "Cannot confirm: no order in progress"
        }
        // order is smart-cast to Order (non-null)
        paymentGateway.charge(order)
    }
}
```

## error -- Unconditional Failure

`error("message")` throws `IllegalStateException` unconditionally. Use it in branches
that should be unreachable.

```kotlin
fun statusToColor(status: Status): Color = when (status) {
    Status.ACTIVE -> Color.GREEN
    Status.PAUSED -> Color.YELLOW
    Status.STOPPED -> Color.RED
    // If Status is not sealed, use error() for safety
    else -> error("Unknown status: $status")
}
```

`error()` is also useful as a placeholder during development:

```kotlin
fun processPayment(payment: Payment): Receipt {
    error("Not implemented yet")  // throws IllegalStateException
}
```

Note: for sealed classes, prefer an exhaustive `when` without `else` so the compiler
catches missing branches. Use `error()` only for non-sealed enums or situations where
an `else` branch is required.

## assert -- JVM Assertions

`assert(condition) { "message" }` maps to the JVM `assert` keyword. Assertions are
**disabled by default** and must be enabled with the `-ea` JVM flag.

```kotlin
fun binarySearch(list: List<Int>, target: Int): Int {
    assert(list == list.sorted()) { "List must be sorted for binary search" }
    // ...
}
```

Because assertions are disabled by default, they are rarely used in Kotlin. Prefer
`require` and `check`, which always execute regardless of JVM flags.

The one valid use case for `assert` is expensive checks in performance-critical code
that you want enabled only during testing:

```kotlin
fun processMatrix(matrix: Matrix) {
    assert(matrix.isSymmetric()) { "Matrix must be symmetric" }  // O(n^2) check
    // ...
}
```

## Summary Table

| Function | Throws | Use For |
|----------|--------|---------|
| `require(condition)` | `IllegalArgumentException` | Validating function arguments |
| `requireNotNull(value)` | `IllegalArgumentException` | Validating non-null arguments (replaces `!!`) |
| `check(condition)` | `IllegalStateException` | Validating object/system state |
| `checkNotNull(value)` | `IllegalStateException` | Validating non-null state |
| `error(message)` | `IllegalStateException` | Unreachable branches, not-yet-implemented |
| `assert(condition)` | `AssertionError` | Expensive invariants (disabled by default) |

## Placement Guidelines

1. **require** calls go at the top of the function, before any logic or side effects.
2. **check** calls go at the start of a method that depends on object state.
3. **error** calls go in `else` or `when` branches that should never be reached.
4. **assert** calls go before expensive operations where the invariant is costly to
   verify.

```kotlin
fun transfer(from: Account, to: Account, amount: Money) {
    // 1. Argument validation
    require(amount.isPositive()) { "Transfer amount must be positive: $amount" }
    require(from.id != to.id) { "Cannot transfer to same account: ${from.id}" }

    // 2. State validation
    check(from.isActive) { "Source account ${from.id} is not active" }
    check(to.isActive) { "Destination account ${to.id} is not active" }
    check(from.balance >= amount) {
        "Insufficient funds: ${from.balance} < $amount"
    }

    // 3. Business logic
    from.debit(amount)
    to.credit(amount)
}
```

## Common Mistakes

### Using require for state validation

```kotlin
// BAD -- require is for arguments, not state
fun close() {
    require(isOpen) { "Not open" }  // throws IllegalArgumentException -- misleading
}

// GOOD
fun close() {
    check(isOpen) { "Not open" }  // throws IllegalStateException -- correct
}
```

### Using !! instead of requireNotNull

```kotlin
// BAD -- no message, NPE on failure
val config = loadConfig()!!

// GOOD -- clear diagnostic message
val config = requireNotNull(loadConfig()) { "Failed to load application config" }
```

### Putting require after side effects

```kotlin
// BAD -- side effect happens before validation
fun updateUser(user: User, name: String) {
    auditLog.record("updating user ${user.id}")  // side effect
    require(name.isNotBlank()) { "Name must not be blank" }  // too late
    user.name = name
}

// GOOD -- validate first
fun updateUser(user: User, name: String) {
    require(name.isNotBlank()) { "Name must not be blank" }
    auditLog.record("updating user ${user.id}")
    user.name = name
}
```

## Summary

- Use `require` for argument preconditions, `check` for state preconditions.
- Prefer `requireNotNull`/`checkNotNull` over `!!` for null checks.
- Use `error()` for branches that should be unreachable.
- Avoid `assert` except for expensive checks that should only run during testing.
- Always place precondition checks before side effects.
