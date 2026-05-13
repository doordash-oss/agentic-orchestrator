# API Design and DSLs

Kotlin provides powerful features for designing expressive APIs: extension functions,
operator overloading, function types with receivers, and DSL builders. These features
make Kotlin excellent for building domain-specific languages, but they come with
responsibility. Misuse leads to APIs that are clever but unreadable.

## Extension Functions

Use extension functions to add behavior to types you don't own. They are resolved
statically (not virtually), so they cannot override member functions.

### When to Use

```kotlin
// GOOD — adding behavior to a type you don't own
fun String.isValidEmail(): Boolean =
    matches(Regex("^[A-Za-z0-9+_.-]+@[A-Za-z0-9.-]+$"))

// GOOD — domain-specific operations on standard types
fun LocalDate.isBusinessDay(): Boolean =
    dayOfWeek != DayOfWeek.SATURDAY && dayOfWeek != DayOfWeek.SUNDAY

// GOOD — extension property for a computed value that feels natural
val String.wordCount: Int
    get() = split("\\s+".toRegex()).filter { it.isNotBlank() }.size
```

### When NOT to Use

```kotlin
// BAD — global utility extension polluting every String
fun String.toWidget(): Widget { /* ... */ }  // too domain-specific for String

// BAD — extension that accesses only its own parameters, not the receiver
fun String.combine(a: Int, b: Int): Int = a + b  // receiver is irrelevant

// BAD — extension that duplicates a member function
fun MutableList<Int>.addElement(e: Int) { add(e) }  // just use add()
```

### Keep Extensions Close to Usage

Don't scatter extensions across the codebase. Place them in the package where they
are used, or in a dedicated `extensions` file within the same module.

```kotlin
// GOOD — extension in the module that uses it
// file: payments/MoneyExtensions.kt
package com.example.payments

fun BigDecimal.toCents(): Long = (this * BigDecimal(100)).toLong()
```

### Member Functions Win

If a class has a member function and an extension function with the same signature,
the member always wins. This is a compile-time resolution rule.

```kotlin
class Example {
    fun greet() = "member"
}

fun Example.greet() = "extension"  // never called

Example().greet()  // returns "member"
```

### Prefer Extensions Over Utility Classes

```kotlin
// BAD — Java-style utility class
object StringUtils {
    fun isBlank(s: String): Boolean = s.trim().isEmpty()
    fun capitalize(s: String): String = s.replaceFirstChar { it.uppercase() }
}
StringUtils.isBlank(name)

// GOOD — idiomatic Kotlin extensions
fun String.isBlank(): Boolean = trim().isEmpty()
fun String.capitalize(): String = replaceFirstChar { it.uppercase() }
name.isBlank()
```

## Operator Overloading

Kotlin allows overloading specific operators via named conventions. Only overload
operators when the semantics are obvious and expected.

### Good Uses

```kotlin
// GOOD — arithmetic on a domain type
data class Vector2(val x: Double, val y: Double) {
    operator fun plus(other: Vector2) = Vector2(x + other.x, y + other.y)
    operator fun minus(other: Vector2) = Vector2(x - other.x, y - other.y)
    operator fun times(scalar: Double) = Vector2(x * scalar, y * scalar)
}

// GOOD — indexed access on a container
class Matrix(private val data: Array<DoubleArray>) {
    operator fun get(row: Int, col: Int): Double = data[row][col]
    operator fun set(row: Int, col: Int, value: Double) { data[row][col] = value }
}

// GOOD — contains for membership testing
class DateRange(val start: LocalDate, val end: LocalDate) {
    operator fun contains(date: LocalDate): Boolean =
        date in start..end
}
```

### Anti-Pattern: Surprising Semantics

```kotlin
// BAD — what does "plus" mean for a user?
data class User(val name: String) {
    operator fun plus(other: User) = User("$name & ${other.name}")
}

// BAD — invoke on a non-function-like type
class Database {
    operator fun invoke(query: String): ResultSet { /* ... */ }
}
// Reads as: database("SELECT ...") — confusing
```

Common overloadable operators and their conventions:

| Operator | Function | Expected Semantics |
|----------|----------|-------------------|
| `a + b` | `plus` | Addition, concatenation |
| `a - b` | `minus` | Subtraction, removal |
| `a * b` | `times` | Multiplication, scaling |
| `a / b` | `div` | Division |
| `a[i]` | `get` | Indexed access |
| `a[i] = b` | `set` | Indexed assignment |
| `a in b` | `contains` | Membership test |
| `a()` | `invoke` | Function-like invocation |
| `a > b` | `compareTo` | Ordering comparison |

## DSL Builders

Kotlin's function types with receivers enable type-safe builders that produce
readable, declarative code.

### Basic Pattern

```kotlin
fun html(init: HTML.() -> Unit): HTML {
    val html = HTML()
    html.init()
    return html
}

class HTML {
    private val children = mutableListOf<Tag>()

    fun head(init: Head.() -> Unit) {
        val head = Head()
        head.init()
        children.add(head)
    }

    fun body(init: Body.() -> Unit) {
        val body = Body()
        body.init()
        children.add(body)
    }
}

// Usage
val page = html {
    head {
        title("My Page")
    }
    body {
        p("Hello, world!")
    }
}
```

### `@DslMarker` — Preventing Scope Leaks

Without `@DslMarker`, inner lambdas can accidentally call methods from outer
receivers. This is a common source of bugs in nested DSLs.

```kotlin
// WITHOUT @DslMarker — outer scope leaks in
html {
    body {
        body { }  // compiles! calls HTML.body() from outer scope — bug
    }
}
```

Fix with `@DslMarker`:

```kotlin
@DslMarker
annotation class HtmlTagMarker

@HtmlTagMarker
abstract class Tag(val name: String) {
    val children = mutableListOf<Tag>()
}

@HtmlTagMarker
class HTML : Tag("html") {
    fun head(init: Head.() -> Unit) { /* ... */ }
    fun body(init: Body.() -> Unit) { /* ... */ }
}

@HtmlTagMarker
class Body : Tag("body") {
    fun p(text: String) { /* ... */ }
}

// Now this fails to compile:
html {
    body {
        body { }  // ERROR: 'fun body()' can't be called in this context
                   // by implicit receiver. Use the explicit receiver if needed.
    }
}
```

Always annotate DSL marker classes with `@DslMarker`. There is no valid reason to
skip it in production DSLs.

## When to Use DSLs vs Constructors

| Situation | Prefer |
|-----------|--------|
| Complex hierarchical structures | DSL builder |
| Many optional parameters | DSL builder or named arguments |
| Declarative configuration | DSL builder |
| Simple data with mandatory fields | Constructor / data class |
| One-off object creation | Constructor |

```kotlin
// DSL — good for complex configuration
val server = server {
    host("localhost")
    port(8080)
    routing {
        get("/health") { ok("healthy") }
        post("/users") { createUser(it) }
    }
}

// Constructor — good for simple data
data class Point(val x: Double, val y: Double)
val origin = Point(0.0, 0.0)
```

## Infix Functions

Use `infix` sparingly. It should read like natural language.

```kotlin
// GOOD — reads naturally in test assertions
infix fun <T> T.shouldEqual(expected: T) {
    assertEquals(expected, this)
}
1 shouldEqual 1

// GOOD — reads naturally as a relationship
infix fun <A, B> A.to(that: B): Pair<A, B> = Pair(this, that)
val pair = "key" to "value"

// BAD — does not read naturally
infix fun Int.multiply(other: Int) = this * other
val result = 3 multiply 4  // just use 3 * 4
```

## Library API Guidelines

When designing public APIs (libraries, shared modules), follow stricter rules:

```kotlin
// GOOD — explicit return types on public API
fun parseConfig(input: String): Config { /* ... */ }
val defaultTimeout: Duration = Duration.ofSeconds(30)

// BAD — inferred types in public API can change unexpectedly
fun parseConfig(input: String) = ConfigParser().parse(input)  // return type is implicit
val defaultTimeout = Duration.ofSeconds(30)                    // type is implicit
```

Always explicitly specify return types and property types in public APIs. Type
inference is fine for local variables and private members, but public signatures
should be explicit to prevent accidental API changes when the implementation changes.
