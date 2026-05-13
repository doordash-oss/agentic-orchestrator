# Null Safety Patterns

## Non-Nullable vs Nullable Types

In Kotlin, types are non-nullable by default. Adding `?` explicitly opts into nullability.

```kotlin
// Non-nullable -- guaranteed to never be null
val name: String = "Alice"

// Nullable -- may be null
val nickname: String? = null
```

Only use `?` when null represents a meaningful domain state (e.g., "no value found", "not yet initialized by design"). Do not use nullable types to work around initialization order or lazy patterns -- use `lateinit` or `lazy` instead.

## Safe Call Operator `?.`

The safe call operator short-circuits to null if the receiver is null. It chains naturally.

```kotlin
// Good -- safe call chain
val cityName = user?.address?.city?.name

// Good -- safe call on left side of assignment
user?.address?.city = newCity

// Bad -- nested null checks
val cityName = if (user != null) {
    if (user.address != null) {
        if (user.address.city != null) {
            user.address.city.name
        } else null
    } else null
} else null
```

## Elvis Operator `?:`

Provides a default value when the left side is null. The right side can be an expression, `throw`, or `return`.

```kotlin
// Good -- default value
val displayName = user.nickname ?: user.fullName

// Good -- early return
fun process(input: String?): Result {
    val value = input ?: return Result.empty()
    // 'value' is smart-cast to String here
    return parse(value)
}

// Good -- throw with context
val config = loadConfig() ?: throw IllegalStateException(
    "Configuration file not found at $path"
)

// Bad -- ternary-style abuse (hard to read when nested)
val x = a ?: b ?: c ?: d ?: default
```

## Scoped Non-Null Operations with `?.let`

Use `?.let` to execute a block only when the value is non-null.

```kotlin
// Good -- scoped operation on non-null value
user?.email?.let { email ->
    sendWelcomeEmail(email)
}

// Bad -- unnecessary let when a simple safe call suffices
user?.let { it.name }  // just use: user?.name
```

Avoid deeply nesting `?.let` blocks. If you find yourself nesting, consider extracting a function or using early returns.

## Filtering Nulls from Collections

```kotlin
val names: List<String?> = listOf("Alice", null, "Bob", null, "Carol")

// Good -- filterNotNull returns List<String>
val nonNullNames: List<String> = names.filterNotNull()

// Good -- listOfNotNull for building lists
val items = listOfNotNull(
    maybeFirst(),
    maybeSecond(),
    maybeThird(),
)

// Good -- mapNotNull combines map + filterNotNull
val lengths = names.mapNotNull { it?.length }
```

## The `!!` Anti-Pattern

The not-null assertion operator `!!` throws a `NullPointerException` with no message if the value is null. It defeats the purpose of null safety.

```kotlin
// Bad -- !! provides no context on failure
val length = text!!.length

// Bad -- common temptation after a null check in a different scope
if (cache.containsKey(key)) {
    val value = cache[key]!!  // race condition possible
}
```

**Why it is tempting:** `!!` is the shortest way to assert non-nullity when you "know" a value cannot be null. But assumptions change, code evolves, and the crash gives zero diagnostic context.

## Safe Alternatives: `requireNotNull()` and `checkNotNull()`

```kotlin
// Good -- requireNotNull for preconditions (throws IllegalArgumentException)
val userId = requireNotNull(request.userId) {
    "User ID must be provided in the request"
}

// Good -- checkNotNull for state invariants (throws IllegalStateException)
val connection = checkNotNull(connectionPool.acquire()) {
    "Connection pool exhausted; max=$maxConnections"
}
```

Both provide a clear message, a meaningful exception type, and smart-cast the value to non-nullable after the call.

## Nullable Boolean

A `Boolean?` has three states: `true`, `false`, `null`. Never use it directly in `if` conditions.

```kotlin
val enabled: Boolean? = getFeatureFlag("dark-mode")

// Good -- explicit comparison
if (enabled == true) {
    enableDarkMode()
}

// Bad -- does not compile (Boolean? is not Boolean)
// if (enabled) { ... }

// Good -- Elvis for default
if (enabled ?: false) {
    enableDarkMode()
}
```

## Safe Cast `as?`

Returns `null` instead of throwing `ClassCastException`.

```kotlin
// Good -- safe cast
val text: String? = value as? String

// Bad -- unsafe cast that can crash
val text: String = value as String  // ClassCastException if value is not String
```

Combine with Elvis for fallback behavior:

```kotlin
val length = (input as? String)?.length ?: 0
```

## Java Interop: Platform Types

When Kotlin calls Java code that lacks nullability annotations, the return type is a **platform type** (`T!`). Platform types bypass null checks entirely -- they are neither nullable nor non-nullable.

```kotlin
// Java method: public String getName() { ... }
// Kotlin sees return type as: String!  (platform type)

// Dangerous -- if Java returns null, this crashes at runtime
val name: String = javaObject.getName()

// Safe -- explicitly treat as nullable
val name: String? = javaObject.getName()
```

**Best practice for public APIs:** Always explicitly declare the type when assigning from Java calls. Never let platform types propagate through your public surface.

```kotlin
// Good -- explicit type pins the nullability contract
fun getUserName(): String {
    return requireNotNull(javaService.getName()) {
        "Java service returned null for name"
    }
}

// Bad -- inferred platform type leaks into public API
fun getUserName() = javaService.getName()  // return type is String! (platform type)
```

## Nullability Annotations for Java Interop

Kotlin recognizes several families of nullability annotations on Java code and treats them as actual nullable/non-nullable types (not platform types):

- **JSpecify** (`org.jspecify.annotations`) -- the recommended standard for new code
- **JetBrains** (`org.jetbrains.annotations`) -- widely used in the Kotlin/IntelliJ ecosystem
- **JSR-305** (`javax.annotation`) -- older standard, still common

```java
// Java code with JSpecify annotations
import org.jspecify.annotations.NonNull;
import org.jspecify.annotations.Nullable;

public class UserService {
    public @NonNull String getName() { ... }      // Kotlin sees: String
    public @Nullable String getNickname() { ... }  // Kotlin sees: String?
}
```

### Strict Mode

Use `-Xjsr305=strict` compiler flag to treat JSR-305 `@Nonnull` annotations as errors instead of warnings. In Kotlin 2.1+, JSpecify strict mode is enabled by default -- annotated Java code is treated identically to Kotlin-declared nullability.

```kotlin
// build.gradle.kts
kotlin {
    compilerOptions {
        freeCompilerArgs.addAll("-Xjsr305=strict")
    }
}
```

## The `OrNull` Naming Convention

Kotlin's standard library uses a consistent naming convention: methods that return null on failure are suffixed with `OrNull`, while their counterparts throw exceptions.

```kotlin
// Throws on failure
list.first { it > 10 }           // NoSuchElementException if none match
"abc".toInt()                     // NumberFormatException

// Returns null on failure
list.firstOrNull { it > 10 }     // null if none match
"abc".toIntOrNull()               // null

// Other examples
list.maxOrNull()
list.minOrNull()
list.getOrNull(index)
list.singleOrNull()
map.getOrDefault(key, default)
```

Follow this convention in your own APIs: provide a throwing version and an `OrNull` variant when the "not found" case is common and expected.
