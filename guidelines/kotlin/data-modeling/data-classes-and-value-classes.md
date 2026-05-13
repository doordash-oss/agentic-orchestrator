# Data Classes and Value Classes

Data classes and value classes are foundational Kotlin features for modeling values. Data classes auto-generate boilerplate for value semantics, while value classes provide type-safe wrappers with zero runtime overhead.

## Data Classes

### Auto-Generated Functions

Declaring a class as `data class` generates `equals()`, `hashCode()`, `toString()`, `copy()`, and `componentN()` functions based on primary constructor properties only.

```kotlin
data class User(val name: String, val email: String)

val user = User("Alice", "alice@example.com")
println(user)           // User(name=Alice, email=alice@example.com)
println(user == User("Alice", "alice@example.com"))  // true
```

### Requirements

- At least one primary constructor parameter.
- All primary constructor parameters must be `val` or `var`.
- Cannot be `abstract`, `open`, `sealed`, or `inner`.

### Body Properties Are Excluded

Only primary constructor properties participate in generated functions. Properties declared in the class body are excluded from `equals()`, `hashCode()`, `toString()`, and `copy()`.

```kotlin
data class Person(val name: String) {
    var age: Int = 0  // excluded from equals, hashCode, toString
}

val a = Person("Alice")
a.age = 30
val b = Person("Alice")
b.age = 25
println(a == b)  // true -- age is not compared
```

This is a common source of bugs. If a property matters for identity, put it in the primary constructor.

### copy() Is Shallow

The `copy()` function creates a new instance with the same property values. Mutable reference types are shared, not cloned.

```kotlin
data class Employee(val name: String, val roles: MutableList<String>)

val a = Employee("Alice", mutableListOf("dev"))
val b = a.copy()
b.roles.add("lead")
println(a.roles)  // [dev, lead] -- a.roles is also modified!
```

Best practice: use immutable types (`List<T>`, not `MutableList<T>`) in data class constructors to avoid unintended sharing.

```kotlin
// Prefer this
data class Employee(val name: String, val roles: List<String>)
```

### Prefer Named Classes Over Pair/Triple

`Pair` and `Triple` are convenient but obscure meaning. Named data classes are more readable and maintainable.

```kotlin
// Avoid
fun findUser(): Pair<String, Int>? = ...
val (name, age) = findUser()!!

// Prefer
data class UserResult(val name: String, val age: Int)
fun findUser(): UserResult? = ...
```

### JVM No-Arg Constructor

Some JVM frameworks (JPA, Jackson, serialization) require a no-arg constructor. Provide default values for all parameters.

```kotlin
data class Entity(
    val id: Long = 0L,
    val name: String = "",
)
```

### Destructuring Declarations

Destructuring uses `componentN()` functions in positional order.

```kotlin
val (name, email) = User("Alice", "alice@example.com")
```

Beware reordering constructor parameters -- destructuring binds by position, not name. Use `_` for unused components.

```kotlin
val (_, email) = user  // skip name
```

## Value Classes

### Overview

Value classes wrap a single value with a distinct type at compile time, eliminating boxing overhead where possible.

```kotlin
@JvmInline
value class UserId(private val id: String)

@JvmInline
value class OrderId(private val id: String)

fun findUser(id: UserId): User = ...
fun findOrder(id: OrderId): Order = ...
```

Without value classes, both IDs would be `String`, and accidentally passing an order ID where a user ID is expected compiles silently.

### Rules and Restrictions

- Exactly one property in the primary constructor (must be `val`).
- No backing fields, no `lateinit`, no delegated properties.
- Can implement interfaces but cannot extend classes.
- Can contain methods, computed properties, and init blocks.

```kotlin
@JvmInline
value class Percentage(private val value: Double) {
    init {
        require(value in 0.0..100.0) { "Percentage must be 0..100, got $value" }
    }

    fun asDecimal(): Double = value / 100.0
}
```

### Boxing Rules

Value classes are unboxed when used as the declared wrapper type. They are boxed when used in these contexts:

- As a generic type argument (`List<UserId>` boxes the UserId).
- As a nullable type (`UserId?` boxes the UserId).
- As an interface type (when assigned to an interface the value class implements).

```kotlin
val id: UserId = UserId("abc")        // unboxed -- just a String at runtime
val ids: List<UserId> = listOf(id)    // boxed -- List<UserId> wraps the value
val nullable: UserId? = id            // boxed
```

### Java Interop and Name Mangling

To prevent accidental calls from Java (where the wrapper type is erased), Kotlin mangles function names that accept value classes. Use `@JvmName` for clean Java interop.

```kotlin
@JvmInline
value class UserId(val id: String)

@JvmName("findUserById")
fun findUser(id: UserId): User = ...
```

### Value Class vs Type Alias

- **Value class**: creates a new distinct type. `UserId` and `String` are incompatible.
- **Type alias**: creates an alternative name for an existing type. `typealias UserId = String` means `UserId` and `String` are freely interchangeable.

Use value classes when you want the compiler to enforce type safety. Use type aliases for readability shortcuts where safety is not a concern.

```kotlin
// Type alias -- no safety, just readability
typealias Email = String

// Value class -- compiler-enforced distinct type
@JvmInline
value class Email(val address: String)
```
