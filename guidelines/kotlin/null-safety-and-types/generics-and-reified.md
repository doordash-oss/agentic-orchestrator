# Generics and Reified Types

## Declaration-Site Variance: `out` and `in`

Kotlin uses declaration-site variance to express the relationship between generic types and their subtypes. The two keywords are `out` (covariant) and `in` (contravariant).

**Mnemonic: "Consumer in, Producer out"**

### `out` (Covariant / Producer)

A type parameter declared `out` can only appear in output positions (return types). This makes the generic type covariant: `Source<Dog>` is a subtype of `Source<Animal>`.

```kotlin
// Good -- T only appears in output positions
interface Source<out T> {
    fun next(): T
}

fun demo(dogs: Source<Dog>) {
    val animals: Source<Animal> = dogs  // OK -- covariant
}
```

### `in` (Contravariant / Consumer)

A type parameter declared `in` can only appear in input positions (parameter types). This makes the generic type contravariant: `Sink<Animal>` is a subtype of `Sink<Dog>`.

```kotlin
// Good -- T only appears in input positions
interface Sink<in T> {
    fun accept(item: T)
}

fun demo(animalSink: Sink<Animal>) {
    val dogSink: Sink<Dog> = animalSink  // OK -- contravariant
}
```

### Common Standard Library Examples

```kotlin
// List is covariant (read-only, only produces T)
interface List<out E> : Collection<E>

// Comparable is contravariant (only consumes T)
interface Comparable<in T> {
    fun compareTo(other: T): Int
}
```

## Use-Site Variance (Type Projections)

When a class uses `T` in both input and output positions (e.g., `MutableList<T>`), you cannot declare it as `in` or `out` at the declaration site. Instead, use type projections at the use site.

```kotlin
// Good -- out projection: can read but not write
fun copy(from: Array<out Any>, to: Array<Any>) {
    for (i in from.indices) {
        to[i] = from[i]
    }
}

// Good -- in projection: can write but reads return Any?
fun fill(dest: Array<in String>, value: String) {
    for (i in dest.indices) {
        dest[i] = value
    }
}

// Bad -- no projection means you can't pass Array<Int> as Array<Any>
fun broken(arr: Array<Any>) { ... }  // Array<Int> is NOT Array<Any>
```

## Star Projections

Use `*` when you don't know or care about the type argument.

```kotlin
// Good -- star projection for unknown type
fun printAll(list: List<*>) {
    for (item in list) {
        println(item)  // item is Any?
    }
}
```

Star projection rules:
- `Foo<out T>` with `*` becomes `Foo<out Any?>` -- you can safely read `Any?`
- `Foo<in T>` with `*` becomes `Foo<in Nothing>` -- you cannot safely write anything
- `Foo<T>` with `*` becomes `Foo<out Any?>` for reading and `Foo<in Nothing>` for writing

## Upper Bounds and `where` Clauses

```kotlin
// Single upper bound
fun <T : Comparable<T>> sort(list: List<T>) { ... }

// Multiple upper bounds with 'where'
fun <T> process(item: T) where T : Serializable, T : Comparable<T> {
    // T must implement both Serializable and Comparable
}

// Good -- using where for clarity with multiple constraints
class Repository<T>(val dao: Dao<T>) where T : Entity, T : Auditable {
    fun save(entity: T) { ... }
}
```

## Definitely Non-Nullable Types: `T & Any`

When a generic type parameter has a nullable upper bound (the default is `T : Any?`), you sometimes need to express "T but definitely not null". Kotlin provides the `T & Any` syntax, which is especially useful for Java interop.

```kotlin
// Good -- T & Any guarantees non-null even when T is nullable
fun <T> elvisLike(x: T, y: T & Any): T & Any = x ?: y

// Practical example: overriding Java methods with @NotNull parameters
// Java: <T> void process(@NotNull T item)
// Kotlin:
override fun <T> process(item: T & Any) {
    // item is guaranteed non-null
}
```

## Type Erasure at Runtime

JVM generics are erased at runtime. You cannot check generic type arguments with `is`.

```kotlin
// Does NOT compile -- cannot check erased type argument
fun isListOfStrings(value: Any): Boolean {
    return value is List<String>  // ERROR: Cannot check for erased type
}

// Good -- check raw type with star projection
fun isList(value: Any): Boolean {
    return value is List<*>
}

// Unchecked cast -- compiler warns but allows it
@Suppress("UNCHECKED_CAST")
fun <T> Any.asListOf(): List<T> = this as List<T>
```

Use `@Suppress("UNCHECKED_CAST")` sparingly and only when you can guarantee safety through other means (e.g., a preceding type check, sealed class hierarchy, or controlled input).

## Reified Type Parameters

The `reified` keyword preserves generic type information at runtime, but it only works on `inline` functions. The compiler substitutes the actual type at each call site.

```kotlin
// Good -- reified allows runtime type checks and class access
inline fun <reified T> List<*>.filterIsInstanceOf(): List<T> {
    return this.filter { it is T }.map { it as T }
}

val strings = mixedList.filterIsInstanceOf<String>()
```

### Accessing Class Metadata

With reified types, you can access `T::class` and its members at runtime.

```kotlin
inline fun <reified T> TreeNode.findParentOfType(): T? {
    var current = parent
    while (current != null) {
        if (current is T) return current
        current = current.parent
    }
    return null
}

// Usage -- no need to pass Class<T> explicitly
val container = node.findParentOfType<Container>()
```

Compare with the non-reified equivalent that requires passing a `Class` parameter:

```kotlin
// Bad -- without reified, must pass class explicitly
fun <T> TreeNode.findParentOfType(clazz: Class<T>): T? {
    var current = parent
    while (current != null) {
        if (clazz.isInstance(current)) return current as T
        current = current.parent
    }
    return null
}

// Clunky call site
val container = node.findParentOfType(Container::class.java)
```

### Other Reified Use Cases

```kotlin
// Good -- reified for JSON deserialization
inline fun <reified T> String.parseJson(): T {
    return objectMapper.readValue(this, T::class.java)
}

val user = jsonString.parseJson<User>()

// Good -- reified for starting Android activities
inline fun <reified T : Activity> Context.startActivity() {
    startActivity(Intent(this, T::class.java))
}
```

### Limitations of Reified

- Only works in `inline` functions (the type is substituted at each call site)
- Cannot be used on classes or non-inline functions
- Cannot create instances of `T` (no `new T()` equivalent) without reflection
- Adds to call-site code size since the function is inlined

## Practical Guidelines

```kotlin
// Good -- use declaration-site variance when the class is clearly a producer or consumer
class ImmutableStack<out T>(private val items: List<T>) {
    fun peek(): T = items.last()
    fun pop(): ImmutableStack<T> = ImmutableStack(items.dropLast(1))
}

// Good -- use reified instead of passing Class<T>
inline fun <reified T : Any> inject(): T = container.resolve(T::class)

// Bad -- passing Class<T> when reified would work
fun <T : Any> inject(clazz: Class<T>): T = container.resolve(clazz.kotlin)

// Good -- use star projection when you only need the container, not the element type
fun logCollectionSize(collection: Collection<*>) {
    logger.info("Size: ${collection.size}")
}
```
