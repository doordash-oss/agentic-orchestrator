# Naming Conventions

Consistent naming is the single most impactful readability decision in a codebase.
Kotlin follows the conventions established by the Kotlin Coding Conventions and the
Android Kotlin Style Guide. Deviating from these creates friction for every reader.

## Classes and Interfaces

Use `UpperCamelCase`. Names should be nouns or noun phrases that describe what the
type represents.

```kotlin
// GOOD
class DeclarationProcessor { }
interface Clickable { }
class HttpClient { }
class UserRepository { }

// BAD — lowercase, underscores, verb names for classes
class declaration_processor { }
class processDeclarations { }
interface clickable { }
```

Acronyms in class names: treat as words when longer than two letters.

```kotlin
// GOOD
class HttpClient { }
class JsonParser { }
class IOStream { }       // two-letter acronym — all caps is acceptable

// BAD
class HTTPClient { }
class JSONParser { }
```

## Functions

Use `lowerCamelCase`. Names should be verbs or verb phrases describing the action.

```kotlin
// GOOD
fun processDeclarations() { }
fun calcTaxes(): BigDecimal { }
fun sendEmail(to: String) { }

// BAD — UpperCamelCase, underscores
fun ProcessDeclarations() { }
fun calc_taxes(): BigDecimal { }
```

Factory functions that return instances of a class may use the class name:

```kotlin
fun List(size: Int, init: (Int) -> Int): List<Int> = MutableList(size, init)
```

## Properties and Variables

Use `lowerCamelCase`. No Hungarian notation, no type prefixes, no underscores —
except for the backing property pattern.

```kotlin
// GOOD
val declarationCount = 1
val isValid = true
var currentIndex = 0

// BAD — prefixes, underscores
val iDeclarationCount = 1
val m_valid = true
var current_index = 0
```

### Backing Properties

Use an underscore prefix for the private mutable backing property:

```kotlin
// GOOD — idiomatic backing property
private val _elementList = mutableListOf<Element>()
val elementList: List<Element> get() = _elementList

// BAD — exposing mutable collection directly
val elementList = mutableListOf<Element>()
```

## Constants

Use `SCREAMING_SNAKE_CASE` for `const val`, top-level or object `val` with no
custom getter, and deeply immutable data.

```kotlin
// GOOD
const val MAX_COUNT = 8
val USER_NAME_FIELD = "UserName"
val EMPTY_ARRAY = arrayOf<String>()

object Config {
    const val DEFAULT_TIMEOUT = 30_000L
}

// BAD — lowerCamelCase for true constants
const val maxCount = 8
val userNameField = "UserName"
```

Values that are not truly constant (have custom getters, are mutable, or depend on
runtime state) should use regular `lowerCamelCase`:

```kotlin
// Not a constant — computed at runtime
val currentTimeMillis: Long get() = System.currentTimeMillis()
```

## Packages

All lowercase, no underscores. Use camelCase for multi-word segments only when
absolutely necessary.

```kotlin
// GOOD
org.example.project
org.example.myProject

// BAD — underscores
org.example.my_project
org.example.My_Project
```

## Enum Constants

Both `SCREAMING_SNAKE_CASE` and `UpperCamelCase` are acceptable. Pick one style per
project and stick with it.

```kotlin
// Style A — SCREAMING_SNAKE_CASE
enum class Direction {
    NORTH, SOUTH, EAST, WEST
}

// Style B — UpperCamelCase (common when enums have behavior)
enum class Color(val hex: String) {
    Red("#FF0000"),
    Green("#00FF00"),
    Blue("#0000FF")
}
```

## Singleton Objects

Use `UpperCamelCase`, same as class names:

```kotlin
object PaymentGateway { }
object DatabaseConfig { }
```

## Test Method Names

Use backtick-wrapped descriptive names or camelCase:

```kotlin
// GOOD — backtick names read like specifications
@Test fun `should return empty list for null input`() { }
@Test fun `throws IllegalArgumentException when id is negative`() { }

// GOOD — camelCase also acceptable
@Test fun returnsEmptyListForNullInput() { }
```

## Modifier Ordering

When multiple modifiers are present, follow this order:

```
public / protected / private / internal
expect / actual
final / open / abstract / sealed / const
override
lateinit
suspend
inner
companion
inline / value
infix
operator
data
```

```kotlin
// GOOD
protected open override suspend fun execute() { }
internal inline fun <reified T> parse(): T { }

// BAD — modifiers out of order
override open protected suspend fun execute() { }
inline internal fun <reified T> parse(): T { }
```

## Class Member Ordering

Organize members in this order within a class:

1. Property declarations and initializer blocks
2. Secondary constructors
3. Method declarations (grouped by related functionality, NOT alphabetically)
4. Companion object

```kotlin
class UserService(private val repo: UserRepository) {
    // 1. Properties and init blocks
    private val cache = mutableMapOf<String, User>()
    private var lastSync: Instant = Instant.EPOCH

    init {
        logger.info("UserService initialized")
    }

    // 2. Secondary constructors
    constructor() : this(DefaultUserRepository())

    // 3. Methods — grouped by related functionality
    fun findUser(id: String): User? = cache[id] ?: repo.findById(id)
    fun saveUser(user: User) { repo.save(user); cache[user.id] = user }

    fun syncAll() { /* ... */ }
    fun clearCache() { cache.clear() }

    // 4. Companion object
    companion object {
        private val logger = LoggerFactory.getLogger(UserService::class.java)
    }
}
```

Keep overloads adjacent to each other — never scatter them across the class.

```kotlin
// GOOD — overloads together
fun send(message: String) { }
fun send(message: String, priority: Int) { }
fun send(message: Message) { }

// BAD — overloads separated by unrelated methods
fun send(message: String) { }
fun receive(): String { }
fun send(message: String, priority: Int) { }
```
