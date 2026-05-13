# Sealed Error Types

Sealed classes let you model a closed set of error variants that the compiler can verify
exhaustively. Unlike exceptions, sealed errors are values -- they appear in function
signatures, compose with standard control flow, and carry structured data without stack
traces.

## Why Sealed Classes for Errors

- **Exhaustive `when`**: the compiler forces you to handle every variant. Adding a new
  error variant breaks all unhandled call sites at compile time.
- **Rich data**: each variant carries exactly the data relevant to that failure.
- **No stack-trace overhead**: constructing a sealed object is cheap compared to
  creating an exception (which fills in a stack trace).
- **Explicit in the signature**: callers see that the function can fail and what the
  failure modes are.

## Basic Pattern

```kotlin
sealed class UserError {
    data class NotFound(val id: UserId) : UserError()
    data class InvalidEmail(val email: String, val reason: String) : UserError()
    data object Unauthorized : UserError()
}

fun findUser(id: UserId): Either<UserError, User> {
    val user = db.find(id) ?: return Either.Left(UserError.NotFound(id))
    return Either.Right(user)
}
```

Callers handle every case:

```kotlin
when (val result = findUser(id)) {
    is Either.Left -> when (result.value) {
        is UserError.NotFound -> respond(404, "User ${result.value.id} not found")
        is UserError.InvalidEmail -> respond(400, result.value.reason)
        is UserError.Unauthorized -> respond(403, "Unauthorized")
    }
    is Either.Right -> respond(200, result.value)
}
```

## data object vs data class

Use `data object` for error variants that carry no data (singletons). Use `data class`
for variants that carry contextual information.

```kotlin
sealed class PaymentError {
    // Singleton -- no additional context needed
    data object InsufficientFunds : PaymentError()
    data object GatewayTimeout : PaymentError()

    // Parameterized -- carries relevant context
    data class CardDeclined(val last4: String, val reason: String) : PaymentError()
    data class AmountExceedsLimit(val amount: Money, val limit: Money) : PaymentError()
}
```

## Custom Result Type

If you do not want to pull in Arrow, a minimal sealed result type works well:

```kotlin
sealed class Result<out T> {
    data class Success<T>(val value: T) : Result<T>()
    data class Failure<out E>(val error: E) : Result<Nothing>()
}
```

This differs from `kotlin.Result` in that the error type is generic -- it can be your
sealed error hierarchy rather than only `Throwable`.

```kotlin
fun validateEmail(input: String): Result<Email, EmailError> {
    if (!input.contains("@")) return Result.Failure(EmailError.MissingAtSign(input))
    return Result.Success(Email(input))
}
```

Note: naming this `Result` shadows `kotlin.Result`. You might call it `Outcome`,
`Response`, or use a qualified import.

## Nested Sealed Hierarchies

For larger systems, group related errors under sub-sealed classes:

```kotlin
sealed class AppError {
    sealed class Validation : AppError() {
        data class InvalidField(val field: String, val reason: String) : Validation()
        data class MissingField(val field: String) : Validation()
    }

    sealed class Infrastructure : AppError() {
        data class DatabaseUnavailable(val cause: Throwable) : Infrastructure()
        data object ServiceTimeout : Infrastructure()
    }

    sealed class Auth : AppError() {
        data object Unauthenticated : Auth()
        data class Forbidden(val requiredRole: String) : Auth()
    }
}
```

Callers can match at the category level or drill into specifics:

```kotlin
when (error) {
    is AppError.Validation -> respond(400, "Validation error")
    is AppError.Infrastructure -> respond(503, "Service unavailable")
    is AppError.Auth -> respond(401, "Authentication required")
}
```

## Railway-Oriented Programming

Chain operations that can each fail, short-circuiting on the first error:

```kotlin
fun processOrder(request: OrderRequest): Either<OrderError, Receipt> {
    val validated = validateOrder(request)         // Either<OrderError, ValidOrder>
        .flatMap { checkInventory(it) }            // Either<OrderError, StockedOrder>
        .flatMap { chargePayment(it) }             // Either<OrderError, PaidOrder>
        .map { generateReceipt(it) }               // Either<OrderError, Receipt>
    return validated
}
```

Each function returns `Either<OrderError, T>`. If any step returns `Left`, the chain
stops and the error propagates. This avoids deeply nested `when` blocks.

With Arrow's either DSL, this reads even more naturally:

```kotlin
fun processOrder(request: OrderRequest): Either<OrderError, Receipt> = either {
    val validated = validateOrder(request).bind()
    val stocked = checkInventory(validated).bind()
    val paid = chargePayment(stocked).bind()
    generateReceipt(paid)
}
```

`bind()` short-circuits on `Left`, similar to the `?` operator in Rust.

## Arrow's Either

Arrow provides `Either<A, B>` with a rich set of combinators:

```kotlin
import arrow.core.Either
import arrow.core.left
import arrow.core.right

fun divide(a: Int, b: Int): Either<MathError, Int> =
    if (b == 0) MathError.DivisionByZero.left()
    else (a / b).right()
```

Key operations: `map`, `flatMap`, `fold`, `getOrElse`, `mapLeft`, `bimap`.

Arrow is a well-maintained library with Kotlin Multiplatform support. If your project
already uses Arrow or needs more than basic error handling, prefer `Either` over a
hand-rolled sealed result type.

## When to Use Sealed Errors vs Exceptions

| Use Sealed Errors | Use Exceptions |
|-------------------|----------------|
| Expected failures: validation, not-found, business rule violations | Truly exceptional: OOM, stack overflow, IO corruption |
| Caller is expected to handle the error | Caller cannot reasonably recover |
| You want exhaustive compile-time checking | The failure is a programming bug (use `require`/`check`) |
| You need structured error data | You need a stack trace for debugging |

Do not model every possible failure as a sealed class. Infrastructure-level failures
(disk full, network partition) are often better expressed as exceptions that propagate
to a top-level handler.

## Exhaustive when Expressions

The key benefit of sealed errors is that `when` expressions without an `else` branch are
checked by the compiler. If you add a new variant and miss a handler, the code does not
compile.

```kotlin
// Compiler error if a new variant is added to UserError and not handled here
fun toHttpStatus(error: UserError): Int = when (error) {
    is UserError.NotFound -> 404
    is UserError.InvalidEmail -> 400
    is UserError.Unauthorized -> 403
}
```

Avoid adding a catch-all `else` branch to sealed `when` expressions -- it defeats the
purpose of exhaustive checking.

```kotlin
// BAD -- hides unhandled variants
fun toHttpStatus(error: UserError): Int = when (error) {
    is UserError.NotFound -> 404
    else -> 500  // new variants silently fall through
}
```

## Summary

- Sealed class hierarchies make error handling explicit, type-safe, and exhaustive.
- Use `data object` for singleton errors and `data class` for errors carrying context.
- Chain fallible operations with `flatMap` or Arrow's `either { }` DSL.
- Reserve exceptions for truly exceptional conditions; use sealed errors for expected
  domain-level failures.
- Never add an `else` branch to a `when` over a sealed error type.
