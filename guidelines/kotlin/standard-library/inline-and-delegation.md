# Inline Functions and Delegation

Inline functions are a key Kotlin feature that eliminates the overhead of lambda allocations and enables capabilities impossible in Java, such as reified type parameters and non-local returns. Property delegation provides reusable patterns for common property behaviors.

## Inline Functions

When a function is marked `inline`, the compiler copies its body (and the bodies of its lambda parameters) directly into the call site, avoiding the creation of lambda objects.

```kotlin
inline fun <T> measureTime(block: () -> T): Pair<T, Long> {
    val start = System.nanoTime()
    val result = block()
    return result to (System.nanoTime() - start)
}

// At the call site, no lambda object is created:
val (value, elapsed) = measureTime {
    expensiveComputation()
}
```

### Why Inline Matters

Without `inline`, each lambda creates an anonymous class instance at runtime. For functions called frequently (in loops, hot paths, or collection operations), this creates garbage collection pressure. With `inline`, the lambda body is copied directly into the caller — zero allocation overhead.

```kotlin
// Without inline: allocates a Function1 object each call
fun <T> withLogging(block: () -> T): T {
    println("Start")
    val result = block()
    println("End")
    return result
}

// With inline: no allocation, block body is inlined
inline fun <T> withLogging(block: () -> T): T {
    println("Start")
    val result = block()
    println("End")
    return result
}
```

## noinline

When an inline function receives multiple lambda parameters, some may need to be stored or passed to other non-inline functions. Mark those with `noinline`.

```kotlin
inline fun transaction(
    action: () -> Unit,
    noinline onError: (Exception) -> Unit  // Cannot be inlined — stored for later
) {
    try {
        action()
    } catch (e: Exception) {
        errorHandlers.add(onError)  // Stored in a collection
    }
}
```

A `noinline` lambda behaves like a normal lambda — it creates an object and cannot use non-local returns.

## crossinline

Use `crossinline` when an inlined lambda is invoked from a different execution context (like inside another lambda or an anonymous object). This prevents non-local returns, which would be unsafe across execution boundaries.

```kotlin
inline fun createRunnable(crossinline body: () -> Unit) = Runnable {
    body()  // Called inside Runnable's run(), not directly
}

// Without crossinline, a `return` inside body would try to return
// from the enclosing function, which is impossible from Runnable.run()
```

## Non-Local Returns

In inline lambdas, `return` exits the enclosing function, not just the lambda. This is called a non-local return.

```kotlin
fun findFirst(list: List<Int>): Int? {
    list.forEach { item ->       // forEach is inline
        if (item > 5) return item  // Returns from findFirst, not just the lambda
    }
    return null
}
```

This is only possible because `forEach` is an inline function. Non-inline lambdas require `return@label` for local returns.

```kotlin
fun processItems(list: List<Int>) {
    list.forEach { item ->
        if (item < 0) return@forEach  // Local return — skips to next iteration
        println(item)
    }
}
```

## Reified Type Parameters

Normally, generic type information is erased at runtime (JVM type erasure). Inline functions with `reified` type parameters retain type information because the actual type is substituted at each call site.

```kotlin
// Without reified — must pass Class explicitly
fun <T> parseJson(json: String, clazz: Class<T>): T =
    objectMapper.readValue(json, clazz)

val user = parseJson(jsonString, User::class.java)

// With reified — type is available at runtime
inline fun <reified T> parseJson(json: String): T =
    objectMapper.readValue(json, T::class.java)

val user = parseJson<User>(jsonString)
// Or with type inference:
val user: User = parseJson(jsonString)
```

Common uses of reified types:

```kotlin
// Type checking
inline fun <reified T> isInstance(value: Any): Boolean = value is T

// Starting Android activities
inline fun <reified T : Activity> Context.startActivity() {
    startActivity(Intent(this, T::class.java))
}

// Logger creation
inline fun <reified T> logger(): Logger = LoggerFactory.getLogger(T::class.java)
```

## When to Use Inline

Use inline when:
- The function accepts lambda parameters (avoids allocation)
- You need reified type parameters
- Small utility functions in performance-critical paths

Do NOT use inline when:
- The function body is large (causes code bloat at every call site)
- The function has no lambda parameters and no reified types (no benefit)
- The function is recursive (cannot be inlined)

## Contract Declarations

Contracts tell the compiler about function behavior, enabling smarter analysis.

```kotlin
import kotlin.contracts.*

inline fun checkNotEmpty(value: String?): String {
    contract {
        returns() implies (value != null)
    }
    if (value.isNullOrEmpty()) throw IllegalArgumentException("Value must not be empty")
    return value
}

fun process(input: String?) {
    checkNotEmpty(input)
    // Compiler knows input is non-null here thanks to the contract
    println(input.length)
}
```

The stdlib functions `require()`, `check()`, `requireNotNull()`, and `checkNotNull()` all use contracts internally.

## Property Delegation

Property delegation lets you reuse common property patterns via the `by` keyword. The delegate object handles `getValue` and optionally `setValue`.

### lazy — Computed Once on First Access

```kotlin
val expensiveValue: String by lazy {
    println("Computing...")
    loadFromDatabase()
}
// "Computing..." prints only on first access; subsequent accesses return cached value
```

By default, `lazy` is thread-safe (`LazyThreadSafetyMode.SYNCHRONIZED`). Use `LazyThreadSafetyMode.NONE` when thread safety is not needed.

```kotlin
val cachedValue: String by lazy(LazyThreadSafetyMode.NONE) {
    computeValue()
}
```

### observable — React to Changes

```kotlin
import kotlin.properties.Delegates

var name: String by Delegates.observable("initial") { property, oldValue, newValue ->
    println("${property.name} changed from $oldValue to $newValue")
}

name = "updated"  // Prints: name changed from initial to updated
```

### vetoable — Reject Invalid Changes

```kotlin
var age: Int by Delegates.vetoable(0) { _, _, newValue ->
    newValue >= 0  // Reject negative values
}

age = 25   // Accepted
age = -1   // Rejected, age stays 25
```

### Map Delegation — Dynamic Properties

```kotlin
class Config(private val map: Map<String, Any?>) {
    val host: String by map
    val port: Int by map
    val debug: Boolean by map
}

val config = Config(mapOf("host" to "localhost", "port" to 8080, "debug" to true))
println(config.host)  // localhost
println(config.port)  // 8080
```

This is especially useful for configuration objects backed by maps, JSON, or environment variables.

For more on delegation patterns in the context of data modeling, see `data-modeling/delegation-and-immutability.md`.
