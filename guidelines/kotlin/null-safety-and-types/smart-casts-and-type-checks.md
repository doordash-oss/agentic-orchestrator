# Smart Casts and Type Checks

## Smart Casts After `is` / `!is`

The Kotlin compiler tracks type checks and automatically inserts casts where safe. After an `is` check, you can use the variable as the checked type without an explicit cast.

```kotlin
// Good -- smart cast after is check
fun process(value: Any) {
    if (value is String) {
        // value is automatically cast to String here
        println(value.length)
        println(value.uppercase())
    }
}

// Good -- smart cast after !is with early return
fun processString(value: Any): String {
    if (value !is String) return ""
    // value is smart-cast to String from here onward
    return value.trim()
}

// Bad -- redundant explicit cast after is check
fun process(value: Any) {
    if (value is String) {
        println((value as String).length)  // unnecessary cast
    }
}
```

## When Smart Casts Work

Smart casts apply in specific conditions based on mutability and visibility:

| Variable Kind | Smart Cast? | Notes |
|---------------|-------------|-------|
| `val` local variable | Always | Locals cannot be mutated by other threads |
| `val` property (private/internal, no custom getter) | Yes | Compiler can verify immutability |
| `val` property (open or custom getter) | No | Subclass or getter could return different value |
| `var` local variable | Yes, if not modified between check and use | Compiler tracks local mutations |
| `var` property | Never | Could be modified by another thread at any time |
| Delegated property | Never | Delegate controls access |

## When Smart Casts Do NOT Work

```kotlin
class Example {
    var mutableProp: Any = "hello"
    open val openProp: Any = "hello"
    val delegatedProp: Any by lazy { "hello" }

    fun demo() {
        // Bad -- var property: smart cast is impossible
        if (mutableProp is String) {
            // mutableProp.length  // ERROR: smart cast impossible
        }

        // Bad -- open property: subclass could override
        if (openProp is String) {
            // openProp.length  // ERROR: smart cast impossible
        }

        // Bad -- delegated property: delegate controls access
        if (delegatedProp is String) {
            // delegatedProp.length  // ERROR: smart cast impossible
        }
    }
}
```

### The Fix: Capture to a Local `val`

```kotlin
// Good -- capture var property to local val
fun demo() {
    val captured = mutableProp
    if (captured is String) {
        println(captured.length)  // OK -- local val is safe
    }
}
```

This pattern is essential and idiomatic. Always capture mutable or open properties to a local `val` before type checking.

## Smart Casts in `when` Expressions

`when` with smart casts is Kotlin's version of pattern matching.

```kotlin
// Good -- exhaustive pattern matching with smart casts
fun describe(value: Any): String = when (value) {
    is Int -> "Integer: ${value + 1}"           // smart-cast to Int
    is String -> "String of length ${value.length}"  // smart-cast to String
    is List<*> -> "List with ${value.size} items"    // smart-cast to List<*>
    is Boolean -> if (value) "yes" else "no"         // smart-cast to Boolean
    else -> "Unknown: $value"
}

// Good -- when with sealed class (no else needed)
sealed interface Shape {
    data class Circle(val radius: Double) : Shape
    data class Rect(val width: Double, val height: Double) : Shape
}

fun area(shape: Shape): Double = when (shape) {
    is Shape.Circle -> Math.PI * shape.radius * shape.radius
    is Shape.Rect -> shape.width * shape.height
    // no else needed -- sealed hierarchy is exhaustive
}
```

## Smart Casts with Logical Operators

Smart casts propagate through `&&` and `||`.

```kotlin
// Good -- && chains: each condition narrows the type further
fun process(value: Any) {
    if (value is String && value.length > 5) {
        // value is String here (smart-cast from first condition)
        println(value.uppercase())
    }
}

// Good -- || with !is: smart cast applies after the full condition
fun requireString(value: Any) {
    if (value !is String || value.length == 0) {
        throw IllegalArgumentException("Expected non-empty string")
    }
    // value is String here
    println(value.trim())
}
```

## K2 Compiler Improvements

The K2 compiler (default since Kotlin 2.0) significantly improves smart cast analysis.

### Smart Casts from Boolean Variables

```kotlin
// K2 -- smart cast works through Boolean variable assignment
fun process(value: Any) {
    val isString = value is String
    if (isString) {
        println(value.length)  // K2: OK, smart-cast to String
        // K1: ERROR, smart cast not possible
    }
}
```

### Common Supertype on `||`

```kotlin
// K2 -- infers common supertype after || branches
interface A { fun doA() }
interface B { fun doB() }
interface C : A, B

fun test(value: Any) {
    if (value is A || value is C) {
        // K2: value is smart-cast to A (common supertype)
    }
}
```

### Inline Function Lambdas

```kotlin
// K2 -- smart casts survive through inline function lambdas
fun process(value: Any) {
    run {
        if (value is String) {
            println(value.length)  // K2: OK inside inline lambda
        }
    }
}
```

### Properties with Function Types

```kotlin
// K2 -- smart casts work with callable references
val processor: (Any) -> Unit = { value ->
    if (value is String) {
        println(value.length)  // K2: OK
    }
}
```

### Exception Handling Blocks

```kotlin
// K2 -- smart casts work across try/catch
fun process(value: Any) {
    if (value is String) {
        try {
            println(value.length)  // K2: OK even inside try block
        } catch (e: Exception) {
            println(value.uppercase())  // K2: still smart-cast
        }
    }
}
```

## Type Aliases

Type aliases provide alternative names for existing types. They are **fully transparent** -- the compiler expands them at compile time. They do NOT create new types.

```kotlin
typealias UserId = String
typealias UserMap = Map<UserId, User>
typealias Predicate<T> = (T) -> Boolean

// These are interchangeable -- no type safety
val id: UserId = "abc"
val name: String = id  // compiles fine, they are the same type

fun accepts(s: String) {}
accepts(id)  // compiles fine -- UserId IS String
```

### Nested Type Aliases in Classes

Type aliases can reference types from enclosing or inner classes, which is useful for simplifying complex nested generics.

```kotlin
class EventBus {
    typealias Handler<T> = (T) -> Unit  // ERROR: type aliases cannot be in classes
}

// Correct -- type aliases must be top-level or in an object
typealias EventHandler<T> = (T) -> Unit
typealias StringMap<V> = Map<String, V>
```

Note: type aliases are always top-level declarations. They cannot be declared inside classes, interfaces, or functions.

## Type Alias vs Value Class

This is a critical distinction. Type aliases provide **zero type safety**. Value classes provide **full type safety** with **zero runtime overhead**.

```kotlin
// Bad -- type alias: no type safety, just a rename
typealias UserId = String
typealias OrderId = String

fun processOrder(userId: UserId, orderId: OrderId) { ... }

// This compiles and is WRONG -- arguments are swapped
processOrder(orderId = "order-123", userId = "user-456")
// Actually passes "order-123" as userId -- both are just String


// Good -- value class: compiler enforces distinct types
@JvmInline
value class UserId(val value: String)

@JvmInline
value class OrderId(val value: String)

fun processOrder(userId: UserId, orderId: OrderId) { ... }

// This does NOT compile -- type mismatch
// processOrder(OrderId("order-123"), UserId("user-456"))

// Correct usage
processOrder(UserId("user-456"), OrderId("order-123"))
```

### When to Use Each

| Use Case | Type Alias | Value Class |
|----------|-----------|-------------|
| Shorten long generic types | Yes | No |
| Function type abbreviation | Yes | No |
| Domain identifiers (UserId, Email) | No | Yes |
| Units of measure (Meters, Seconds) | No | Yes |
| Interop with existing APIs expecting the underlying type | Yes | Depends |

```kotlin
// Good -- type alias for complex generic type
typealias Cache<K, V> = ConcurrentHashMap<K, MutableList<V>>

// Good -- type alias for function type
typealias OnClickListener = (View) -> Unit

// Good -- value class for domain type
@JvmInline
value class EmailAddress(val value: String) {
    init {
        require(value.contains("@")) { "Invalid email: $value" }
    }
}
```

The rule of thumb: if swapping two values of the aliased type would be a bug, use a value class instead.
