# Sealed Classes and Enums

Sealed classes and enums restrict the set of possible types or values, enabling the compiler to verify exhaustive handling. Use them to model closed domains where every variant is known at compile time.

## Sealed Classes

### Basics

A sealed class restricts which classes can directly extend it. All direct subclasses must be defined in the same package and module, and must be known at compile time.

```kotlin
sealed class UIState {
    data object Loading : UIState()
    data class Success(val data: String) : UIState()
    data class Error(val exception: Exception) : UIState()
}
```

### Exhaustive when

Because all subclasses are known, `when` expressions over sealed types require no `else` branch. The compiler verifies every case is covered.

```kotlin
fun render(state: UIState): String = when (state) {
    is UIState.Loading -> "Loading..."
    is UIState.Success -> "Data: ${state.data}"
    is UIState.Error -> "Error: ${state.exception.message}"
    // no else needed -- compiler knows all cases
}
```

If a new subclass is added, every `when` expression missing it produces a compile error. This is the primary advantage over open class hierarchies.

### Sealed Class vs Sealed Interface

**Sealed class**: subclasses share state or behavior through a common constructor or base implementation.

```kotlin
sealed class NetworkResult(val timestamp: Long) {
    class Success(val body: String, ts: Long) : NetworkResult(ts)
    class Failure(val code: Int, ts: Long) : NetworkResult(ts)
}
```

**Sealed interface**: defines a pure contract. Subclasses can extend other classes, enabling more flexible hierarchies.

```kotlin
sealed interface Drawable {
    fun draw()
}

class Circle(val radius: Double) : Shape(), Drawable {
    override fun draw() { ... }
}
```

Prefer sealed interfaces when you do not need shared state, since they allow subclasses to participate in other class hierarchies.

### Constructor Visibility

Sealed class constructors can be `protected` (default) or `private`. They cannot be `public` or `internal`.

```kotlin
sealed class Event private constructor(val id: String) {
    class Click(id: String, val x: Int, val y: Int) : Event(id)
    class Scroll(id: String, val delta: Int) : Event(id)
}
```

### Direct Subclass Rules

Direct subclasses of a sealed class or interface must be:
- In the same package and module.
- Top-level, or nested inside a named class, interface, or object.
- Not anonymous or local classes.

Indirect subclasses (extending a non-sealed direct subclass) have no restrictions.

### data object for Singleton Variants

Use `data object` (Kotlin 1.9+) for sealed class variants that carry no data. It provides a clean `toString()` and structural equality.

```kotlin
sealed class Permission {
    data object Admin : Permission()
    data object ReadOnly : Permission()
    data class Custom(val actions: Set<String>) : Permission()
}

println(Permission.Admin)  // Permission.Admin (not a hash-based string)
```

### State Machine Pattern

Sealed classes are ideal for modeling state machines with exhaustive transitions.

```kotlin
sealed class OrderState {
    data object Draft : OrderState()
    data class Submitted(val submittedAt: Instant) : OrderState()
    data class Approved(val approvedBy: String) : OrderState()
    data class Shipped(val trackingId: String) : OrderState()
    data class Cancelled(val reason: String) : OrderState()
}

fun transition(state: OrderState, event: OrderEvent): OrderState = when (state) {
    is OrderState.Draft -> when (event) {
        is OrderEvent.Submit -> OrderState.Submitted(Instant.now())
        is OrderEvent.Cancel -> OrderState.Cancelled("Cancelled from draft")
        else -> state
    }
    is OrderState.Submitted -> when (event) {
        is OrderEvent.Approve -> OrderState.Approved(event.approver)
        is OrderEvent.Cancel -> OrderState.Cancelled(event.reason)
        else -> state
    }
    is OrderState.Approved -> when (event) {
        is OrderEvent.Ship -> OrderState.Shipped(event.trackingId)
        else -> state
    }
    is OrderState.Shipped, is OrderState.Cancelled -> state  // terminal states
}
```

## Enum Classes

### Basics

Enum classes define a fixed set of singleton constants. Each constant is an instance of the enum class.

```kotlin
enum class Direction {
    NORTH, SOUTH, EAST, WEST
}
```

### Properties and Constructors

Enum constants can have properties initialized through a constructor.

```kotlin
enum class Color(val rgb: Int) {
    RED(0xFF0000),
    GREEN(0x00FF00),
    BLUE(0x0000FF);

    fun hexString(): String = "#%06X".format(rgb)
}
```

### Anonymous Classes for Per-Constant Behavior

Each enum constant can override methods by declaring an anonymous class body.

```kotlin
enum class Operation {
    ADD {
        override fun apply(a: Int, b: Int) = a + b
    },
    SUBTRACT {
        override fun apply(a: Int, b: Int) = a - b
    };

    abstract fun apply(a: Int, b: Int): Int
}
```

### entries Property

Use `entries` (Kotlin 1.9+) instead of the deprecated `values()`. It returns an immutable list.

```kotlin
for (color in Color.entries) {
    println("${color.name} = ${color.hexString()}")
}
```

For generic access, use `enumEntries<T>()`:

```kotlin
inline fun <reified T : Enum<T>> printAll() {
    for (entry in enumEntries<T>()) {
        println(entry)
    }
}
```

### Interface Implementation

Enums can implement interfaces but cannot extend classes (they implicitly extend `Enum`).

```kotlin
interface Printable {
    fun prettyPrint(): String
}

enum class Status : Printable {
    ACTIVE {
        override fun prettyPrint() = "Currently active"
    },
    INACTIVE {
        override fun prettyPrint() = "Not active"
    }
}
```

## Enum vs Sealed Class

| Criteria | Enum | Sealed Class |
|----------|------|-------------|
| Instance count | Each variant is a single fixed instance | Variants can have multiple instances with different data |
| Data per variant | Same properties across all constants | Each subclass defines its own properties |
| Serialization | Built-in `name` and `ordinal` | Custom serialization needed |
| Iteration | `entries` gives all constants | No built-in iteration over subclasses |
| Use case | Fixed set of simple constants | Variants that carry different data or have different structure |

Choose enum when every variant is a singleton with the same shape. Choose sealed class when variants carry different data or need distinct class structure.
