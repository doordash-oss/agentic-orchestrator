# Scope Functions

Kotlin's five scope functions — `let`, `apply`, `run`, `with`, and `also` — are
powerful but overused. Each has a specific purpose defined by two axes: how the
context object is referenced (`this` vs `it`) and what is returned (the context
object vs the lambda result). Picking the wrong one or nesting them makes code
harder to read than the equivalent without scope functions.

## Decision Table

Use this table to pick the right scope function:

| Intent | Function | Context | Returns |
|--------|----------|---------|---------|
| Configure object, get it back | `apply` | `this` | Object |
| Side effects, get object back | `also` | `it` | Object |
| Transform/compute result | `run` | `this` | Result |
| Null-safe transform | `let` | `it` | Result |
| Group calls, no result needed | `with` | `this` | Result |

## `let` — Null-Safe Transformations

Use `let` when you need to perform an operation on a non-null value or transform
a value within a limited scope.

Context object: `it` (lambda argument). Returns: lambda result.

```kotlin
// GOOD — null-safe operation
user?.let { sendEmail(it) }

// GOOD — null-safe transformation with default
val mapped = value?.let { transform(it) } ?: defaultValue

// GOOD — scoping a variable to avoid leaking it
val hexColor = color.let {
    val r = it.red.toString(16).padStart(2, '0')
    val g = it.green.toString(16).padStart(2, '0')
    val b = it.blue.toString(16).padStart(2, '0')
    "#$r$g$b"
}
```

When NOT to use `let`:

```kotlin
// BAD — let adds nothing over a simple if-check
name?.let {
    println(it)
}

// GOOD — just use if
if (name != null) {
    println(name)
}
```

## `apply` — Object Configuration

Use `apply` to configure an object and get it back. The context object is `this`,
so you can access its members directly.

Context object: `this` (receiver). Returns: the context object.

```kotlin
// GOOD — configure a new object
val person = Person().apply {
    name = "Alice"
    age = 30
    address = Address("123 Main St")
}

// GOOD — configure and return
fun createDefaultConfig() = Config().apply {
    host = "localhost"
    port = 8080
    maxRetries = 3
}

// BAD — using apply when you need the result of a computation
val length = "hello".apply { length }  // returns "hello", not 5
```

## `run` — Compute With a Receiver

Use `run` to compute a result using an object's members. Combines the receiver
access of `with` with the chaining capability of `let`.

Context object: `this` (receiver). Returns: lambda result.

```kotlin
// GOOD — compute a result from an object
val result = service.run {
    port = 8080
    connect()   // returns the connection result
}

// GOOD — non-extension run for scoped computation
val hexString = run {
    val digits = "0123456789ABCDEF"
    val r = digits[color.red / 16]
    val g = digits[color.green / 16]
    "$r$g"
}
```

## `with` — Group Calls on the Same Object

Use `with` as a non-extension function when you need to call multiple methods on
the same object and don't need to chain.

Context object: `this` (receiver). Returns: lambda result.

```kotlin
// GOOD — multiple operations on the same object
with(config) {
    println(host)
    println(port)
    println(maxRetries)
}

// GOOD — building a string from object properties
val description = with(person) {
    "Name: $name, Age: $age, City: ${address.city}"
}

// BAD — using with on a nullable object (use run or let instead)
with(nullableObj) {   // NullPointerException risk
    this?.doSomething()
}

// GOOD — use let or run for nullable receivers
nullableObj?.run {
    doSomething()
}
```

## `also` — Side Effects in a Chain

Use `also` when you want to perform a side effect (logging, validation, debug)
without breaking a call chain. The object passes through unchanged.

Context object: `it` (lambda argument). Returns: the context object.

```kotlin
// GOOD — logging during construction
val numbers = mutableListOf(1, 2, 3).also {
    logger.debug("Created list: $it")
}

// GOOD — validation side effect
fun createUser(name: String) = User(name).also {
    require(it.name.isNotBlank()) { "Name must not be blank" }
    audit.log("Created user: ${it.name}")
}

// BAD — using also for configuration (use apply instead)
val person = Person().also {
    it.name = "Alice"    // awkward — apply gives you 'this'
    it.age = 30
}
```

## Named Parameter for Clarity

When using `let` or `also`, give the parameter a name if `it` would be ambiguous:

```kotlin
// BAD — unclear what 'it' refers to
users.firstOrNull()?.let {
    repository.save(it)
    logger.info("Saved ${it.name}")
}

// GOOD — named parameter
users.firstOrNull()?.let { user ->
    repository.save(user)
    logger.info("Saved ${user.name}")
}
```

## Anti-Pattern: Nesting Scope Functions

Nesting scope functions destroys readability. Each level changes what `it` or
`this` refers to, making it nearly impossible to follow.

```kotlin
// BAD — triple-nested let
user?.let { u ->
    u.address?.let { a ->
        a.city?.let { city ->
            println(city)
        }
    }
}

// GOOD — extract the value, then use a single scope function
val city = user?.address?.city
city?.let { println(it) }

// EVEN BETTER — no scope function needed
if (user?.address?.city != null) {
    println(user.address.city)
}
```

```kotlin
// BAD — mixing apply and also in a chain
val widget = Widget().apply {
    color = Color.RED
    size = 12
}.also {
    it.validate()
}.apply {
    label = "OK"
}

// GOOD — single apply, call validate separately
val widget = Widget().apply {
    color = Color.RED
    size = 12
    label = "OK"
}
widget.validate()
```

## Anti-Pattern: `let` for Simple Null Checks

Don't use `let` when a simple `if` or safe call is clearer:

```kotlin
// BAD — let adds ceremony for no benefit
name?.let { println(it) }

// GOOD
if (name != null) println(name)

// BAD — let wrapping a single method call
list?.let { it.size } ?: 0

// GOOD
list?.size ?: 0
```

## Summary

- Pick the scope function based on context access (`this`/`it`) and return value.
- Never nest scope functions — extract intermediate values instead.
- Don't use `let` for trivial null checks; `if` or safe calls are clearer.
- Name the `it` parameter when its meaning is ambiguous.
- Use `also` strictly for side effects; use `apply` for configuration.
