# Collections and Sequences

Kotlin's collection framework builds on Java's but adds a clear distinction between read-only and mutable interfaces, along with a rich set of extension functions that replace most manual iteration patterns.

## Immutable vs Mutable Collections

Kotlin distinguishes between read-only views and mutable collections at the type level.

```kotlin
// Read-only — no add/remove/set methods available
val names: List<String> = listOf("Alice", "Bob", "Charlie")
val lookup: Map<String, Int> = mapOf("a" to 1, "b" to 2)
val unique: Set<Int> = setOf(1, 2, 3)

// Mutable — full modification API
val mutableNames: MutableList<String> = mutableListOf("Alice", "Bob")
mutableNames.add("Charlie")

val mutableLookup: MutableMap<String, Int> = mutableMapOf("a" to 1)
mutableLookup["b"] = 2
```

Prefer read-only types in public APIs. Use mutable types only during construction or when mutation is genuinely needed.

```kotlin
// Good: return read-only type
fun activeUsers(): List<User> {
    val result = mutableListOf<User>()
    // ... populate ...
    return result  // Returns as List<User>, not MutableList
}
```

## Transformation Functions

These replace the vast majority of manual loops.

```kotlin
val numbers = listOf(1, 2, 3, 4, 5)

// map — transform each element
val doubled = numbers.map { it * 2 }                     // [2, 4, 6, 8, 10]

// filter — keep matching elements
val evens = numbers.filter { it % 2 == 0 }               // [2, 4]

// flatMap — transform and flatten
val nested = listOf(listOf(1, 2), listOf(3, 4))
val flat = nested.flatMap { it }                          // [1, 2, 3, 4]

// groupBy — group into a map
data class Person(val name: String, val city: String)
val people = listOf(Person("Alice", "NYC"), Person("Bob", "NYC"), Person("Charlie", "LA"))
val byCity = people.groupBy { it.city }                   // {NYC=[Alice, Bob], LA=[Charlie]}

// associate / associateBy — create maps from lists
val nameToLength = names.associateWith { it.length }      // {Alice=5, Bob=3, Charlie=7}
val byName = people.associateBy { it.name }               // {Alice=Person(...), ...}

// partition — split into pair of lists
val (big, small) = numbers.partition { it > 3 }           // ([4, 5], [1, 2, 3])

// zip — combine two lists element-wise
val keys = listOf("a", "b", "c")
val values = listOf(1, 2, 3)
val pairs = keys.zip(values)                              // [(a, 1), (b, 2), (c, 3)]

// windowed / chunked — sliding windows and fixed-size chunks
val windows = numbers.windowed(3)                         // [[1,2,3], [2,3,4], [3,4,5]]
val chunks = numbers.chunked(2)                           // [[1,2], [3,4], [5]]
```

## Aggregation Functions

```kotlin
val numbers = listOf(3, 1, 4, 1, 5, 9, 2, 6)

numbers.sum()                                             // 31
numbers.count { it > 3 }                                  // 4
numbers.maxByOrNull { it }                                // 9
numbers.minByOrNull { it }                                // 1
numbers.sumOf { it.toLong() }                             // 31L

// fold / reduce for custom aggregation
numbers.fold(0) { acc, n -> acc + n }                     // 31
numbers.reduce { acc, n -> acc + n }                      // 31 (no initial value)
```

## Collection Builders

Use `buildList`, `buildMap`, and `buildSet` for imperative collection construction with a clean read-only result.

```kotlin
val list = buildList {
    add("first")
    addAll(otherList)
    if (condition) add("conditional")
}

val map = buildMap {
    put("key1", "value1")
    putAll(existingMap)
    if (featureEnabled) put("feature", "on")
}

val set = buildSet {
    add(1)
    addAll(listOf(2, 3, 4))
}
```

These are preferable to creating a `mutableListOf()` and then returning it, because the result type is already read-only.

## Sequence vs Iterable

The critical distinction: **Iterable** operations are eager (each step creates an intermediate collection), while **Sequence** operations are lazy (chained and executed element-by-element).

```kotlin
// Eager: creates intermediate lists at each step
val eagerResult = listOf(1, 2, 3, 4, 5)
    .filter { it > 2 }    // Creates [3, 4, 5]
    .map { it * 2 }        // Creates [6, 8, 10]

// Lazy: no intermediate collections
val lazyResult = listOf(1, 2, 3, 4, 5)
    .asSequence()
    .filter { it > 2 }    // Deferred
    .map { it * 2 }        // Deferred
    .toList()              // Executes everything, produces [6, 8, 10]
```

### When to Use Sequence

- Large collections (thousands+ elements) with multiple chained operations
- Only the first or first few results are needed (`first { }`, `take(n)`)
- Memory-sensitive contexts where intermediate allocations matter

### When to Use Iterable (Default)

- Small collections (dozens of elements)
- Single operation (no chaining benefit)
- Need indexed access to intermediate results

### Custom Generators with `sequence { }`

```kotlin
val fibonacci = sequence {
    var a = 0
    var b = 1
    while (true) {
        yield(a)
        val next = a + b
        a = b
        b = next
    }
}

fibonacci.take(10).toList()  // [0, 1, 1, 2, 3, 5, 8, 13, 21, 34]
```

Use `yield()` for single values and `yieldAll()` for iterables or other sequences.

## Anti-Patterns

### forEach with Mutable State

```kotlin
// Bad: imperative accumulation
val results = mutableListOf<String>()
users.forEach { results.add(it.name) }

// Good: declarative transformation
val results = users.map { it.name }
```

### Redundant filter + first

```kotlin
// Bad: filters the entire list, then takes first
val found = users.filter { it.age > 30 }.first()

// Good: stops at the first match
val found = users.first { it.age > 30 }

// Safe: returns null instead of throwing
val found = users.firstOrNull { it.age > 30 }
```

### Ignoring Nullability in Collection Operations

```kotlin
// Bad: crashes if any element has no match
val mapped = ids.map { findUser(it)!! }

// Good: filter nulls explicitly
val mapped = ids.mapNotNull { findUser(it) }
```

### Converting When Not Needed

```kotlin
// Bad: unnecessary toList() in the middle of a chain
val result = items.asSequence().filter { it.active }.toList().map { it.name }

// Good: stay in Sequence until the terminal operation
val result = items.asSequence().filter { it.active }.map { it.name }.toList()
```
