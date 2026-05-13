# Delegation and Immutability

Kotlin's `by` keyword enables class delegation and property delegation, promoting composition over inheritance and reducing boilerplate. Combined with immutability-first patterns, these features produce code that is easier to reason about and less prone to bugs.

## Class Delegation

### Basics

Class delegation forwards interface implementation to a composed object.

```kotlin
interface Printer {
    fun print(message: String)
    fun format(message: String): String
}

class ConsolePrinter : Printer {
    override fun print(message: String) = println(message)
    override fun format(message: String) = "[LOG] $message"
}

class EnhancedPrinter(base: Printer) : Printer by base {
    override fun print(message: String) {
        // Override only what differs
        println(">>> ${format(message)}")
    }
}
```

The compiler generates forwarding methods for every interface member not overridden in the delegating class. This avoids manually writing boilerplate wrappers.

### Critical Limitation: Internal Dispatch

Overrides in the delegating class are not called by the delegate's internal methods. The delegate has no knowledge of the wrapper.

```kotlin
interface Base {
    fun doA()
    fun doB()
}

class Impl : Base {
    override fun doA() { doB() }  // calls Impl.doB(), NOT Derived.doB()
    override fun doB() { println("Impl.doB") }
}

class Derived(b: Base) : Base by b {
    override fun doB() { println("Derived.doB") }
}

Derived(Impl()).doA()  // prints "Impl.doB", not "Derived.doB"
```

This is because the delegate object's internal calls resolve to its own methods, not the delegating class's overrides. Keep this in mind when overriding selectively.

## Property Delegates

### lazy

Computed once on first access, then cached. Thread-safe by default.

```kotlin
val heavyResource: Resource by lazy {
    println("Initializing...")
    Resource.load()
}
```

Thread safety modes:
- `LazyThreadSafetyMode.SYNCHRONIZED` (default) -- single-thread initialization, safe for concurrent access.
- `LazyThreadSafetyMode.PUBLICATION` -- multiple threads may initialize simultaneously, but only the first published value is used.
- `LazyThreadSafetyMode.NONE` -- no synchronization, use only when single-threaded access is guaranteed.

```kotlin
// When you know single-threaded access (e.g., Android main thread)
val adapter: Adapter by lazy(LazyThreadSafetyMode.NONE) {
    Adapter(context)
}
```

### Delegates.observable

Fires a callback after a property value changes.

```kotlin
import kotlin.properties.Delegates

var name: String by Delegates.observable("initial") { prop, old, new ->
    println("${prop.name}: $old -> $new")
}

name = "Alice"  // prints "name: initial -> Alice"
```

### Delegates.vetoable

Fires a callback before assignment. Return `false` to reject the new value.

```kotlin
var age: Int by Delegates.vetoable(0) { _, _, new ->
    new >= 0  // reject negative ages
}

age = 25   // accepted
age = -1   // rejected, age remains 25
```

### Delegating to Another Property

Useful for backward-compatible renaming.

```kotlin
class Config {
    var newName: Int = 0

    @Deprecated("Use 'newName'")
    var oldName: Int by this::newName
}
```

Reading or writing `oldName` delegates to `newName`. The deprecation warning guides callers to migrate.

### Map-Backed Properties

Parse maps into typed properties using delegation.

```kotlin
class User(val map: Map<String, Any?>) {
    val name: String by map
    val age: Int by map
}

val user = User(mapOf("name" to "Alice", "age" to 30))
println(user.name)  // Alice
println(user.age)   // 30
```

For mutable properties, use `MutableMap`:

```kotlin
class MutableUser(val map: MutableMap<String, Any?>) {
    var name: String by map
    var age: Int by map
}
```

### Custom Delegates

Implement `operator fun getValue` and optionally `operator fun setValue`.

```kotlin
import kotlin.reflect.KProperty

class TrimmedString {
    private var value: String = ""

    operator fun getValue(thisRef: Any?, property: KProperty<*>): String = value

    operator fun setValue(thisRef: Any?, property: KProperty<*>, newValue: String) {
        value = newValue.trim()
    }
}

var input: String by TrimmedString()
input = "  hello  "
println(input)  // "hello"
```

### provideDelegate

Validate or customize delegate creation at binding time using `provideDelegate`.

```kotlin
class NonEmptyString(private val default: String) {
    operator fun provideDelegate(
        thisRef: Any?,
        prop: KProperty<*>
    ): ReadOnlyProperty<Any?, String> {
        require(default.isNotEmpty()) { "${prop.name} must have a non-empty default" }
        return ReadOnlyProperty { _, _ -> default }
    }
}

val title by NonEmptyString("Hello")   // works
// val bad by NonEmptyString("")        // throws at initialization
```

## Immutability Patterns

### val by Default

Default to `val` for all declarations. Use `var` only when mutation is genuinely required.

```kotlin
// Prefer
val maxRetries = 3
val config = loadConfig()

// Only when mutation is needed
var currentAttempt = 0
```

### Backing Property Pattern

Expose read-only collections publicly while keeping a mutable backing field private.

```kotlin
class Repository {
    private val _items = mutableListOf<Item>()
    val items: List<Item> get() = _items

    fun add(item: Item) {
        _items.add(item)
    }
}
```

Callers see `List<Item>` and cannot mutate. The class controls all mutations through its own methods.

### copy() for Modified Instances

Use `copy()` on data classes instead of mutation.

```kotlin
data class Settings(
    val theme: String = "light",
    val fontSize: Int = 14,
    val notifications: Boolean = true,
)

val defaults = Settings()
val custom = defaults.copy(theme = "dark", fontSize = 16)
```

### Immutable Collection Types in Public API

Use `List`, `Set`, and `Map` in public signatures. Keep `MutableList`, `MutableSet`, and `MutableMap` as implementation details.

```kotlin
// Public API
fun getUsers(): List<User> = _users.toList()

// Internal
private val _users = mutableListOf<User>()
```

### const val for Compile-Time Constants

Use `const val` for primitive and String constants known at compile time. These are inlined by the compiler.

```kotlin
const val MAX_RETRIES = 3
const val API_VERSION = "v2"
```

`const val` must be top-level or in an `object`/`companion object`. It cannot be a computed value.

### lateinit var: Use Sparingly

`lateinit var` defers initialization but sacrifices safety. Use it only when unavoidable -- dependency injection, Android lifecycle, or test setup.

```kotlin
class UserServiceTest {
    private lateinit var service: UserService

    @BeforeEach
    fun setUp() {
        service = UserService(mockRepo)
    }
}
```

Check initialization with `::service.isInitialized` when needed. Prefer constructor injection or `by lazy` over `lateinit` whenever possible.
