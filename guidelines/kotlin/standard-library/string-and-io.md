# String and IO

Kotlin provides concise utilities for string manipulation, regular expressions, resource management, and IO operations that replace verbose Java patterns.

## String Templates

String templates embed expressions directly into strings, replacing concatenation and `String.format()`.

```kotlin
val name = "Kotlin"
val version = 2

// Simple variable reference
println("Hello, $name!")                          // Hello, Kotlin!

// Expression in braces
println("Length: ${name.length}")                 // Length: 6
println("Next version: ${version + 1}")           // Next version: 3

// Escaped dollar sign
println("Price: \$9.99")                          // Price: $9.99
```

Prefer templates over concatenation — they are more readable and generate equivalent bytecode.

```kotlin
// Bad
val message = "User " + user.name + " (age " + user.age + ") logged in"

// Good
val message = "User ${user.name} (age ${user.age}) logged in"
```

## Multiline Strings

Triple-quoted strings preserve formatting. Use `trimIndent()` or `trimMargin()` to remove leading whitespace.

```kotlin
// trimIndent removes common leading whitespace
val query = """
    SELECT *
    FROM users
    WHERE active = true
    ORDER BY name
""".trimIndent()

// trimMargin removes everything before the margin prefix (default "|")
val paragraph = """
    |Dear $name,
    |
    |Thank you for your purchase.
    |Total: ${"$"}${total}
""".trimMargin()

// Custom margin prefix
val custom = """
    #First line
    #Second line
""".trimMargin("#")
```

Multiline strings are especially useful for SQL queries, JSON templates, and test fixtures where preserving structure matters.

## Regular Expressions

Kotlin wraps Java's regex engine with a cleaner API using the `Regex` class.

```kotlin
// Create from raw string (no double-escaping needed)
val phoneRegex = """\d{3}-\d{4}""".toRegex()

// Matching
phoneRegex.matches("123-4567")                    // true
phoneRegex.containsMatchIn("Call 123-4567 now")   // true

// Find first match
val input = "Order 12345 and order 67890"
val match = """\d+""".toRegex().find(input)
println(match?.value)                              // 12345

// Find all matches
"""\d+""".toRegex().findAll(input)
    .map { it.value }
    .toList()                                      // [12345, 67890]

// Named groups
val dateRegex = """(?<year>\d{4})-(?<month>\d{2})-(?<day>\d{2})""".toRegex()
val dateMatch = dateRegex.find("2024-03-15")
println(dateMatch?.groups?.get("year")?.value)     // 2024

// Replace
val cleaned = "Hello   World".replace("""\s+""".toRegex(), " ")  // "Hello World"
```

Use raw strings (`"""..."""`) for regex patterns to avoid double-escaping backslashes.

## Resource Management with `use { }`

The `use` extension on `Closeable` and `AutoCloseable` ensures resources are closed after the block completes, even if an exception is thrown. This is Kotlin's equivalent of Java's try-with-resources.

```kotlin
// Explicit buffered reader
File("data.txt").bufferedReader().use { reader ->
    reader.lineSequence().forEach { line ->
        println(line)
    }
}

// Nested resources
FileInputStream("input.bin").use { input ->
    FileOutputStream("output.bin").use { output ->
        input.copyTo(output)
    }
}
```

### File Convenience Functions

Kotlin provides shorthand extensions on `File` for common operations.

```kotlin
// Read entire file as string
val text = File("data.txt").readText()

// Read all lines into a list
val lines = File("data.txt").readLines()

// Read line by line (memory-efficient for large files)
File("large.txt").useLines { lines ->
    lines.filter { it.isNotBlank() }
        .map { it.trim() }
        .toList()
}

// Write text
File("output.txt").writeText("Hello, World!")

// Append text
File("log.txt").appendText("New entry\n")
```

### Path Operations (kotlin.io.path, Kotlin 1.5+)

```kotlin
import kotlin.io.path.*

val path = Path("data.txt")
val content = path.readText()
path.writeText("updated content")

// List directory entries with glob
val ktFiles = Path("src").listDirectoryEntries("*.kt")

// Walk directory tree
Path("project").walk().filter { it.extension == "kt" }.forEach { println(it) }

// Create directories
Path("output/reports").createDirectories()
```

## Duration API (kotlin.time)

The `kotlin.time` package provides type-safe duration handling.

```kotlin
import kotlin.time.Duration.Companion.seconds
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.minutes
import kotlin.time.measureTime
import kotlin.time.measureTimedValue

// Type-safe durations
val timeout = 5.seconds
val interval = 500.milliseconds
val longWait = 2.minutes

// Arithmetic
val total = timeout + interval                     // 5.5s
val doubled = timeout * 2                          // 10s

// Measure execution time
val elapsed = measureTime {
    doExpensiveWork()
}
println("Took $elapsed")                           // Took 1.234s

// Measure time and capture result
val (result, duration) = measureTimedValue {
    computeSomething()
}
println("Got $result in $duration")
```

## Efficient String Construction with buildString

Use `buildString` for constructing strings from multiple parts. It wraps a `StringBuilder` and returns the result as a `String`.

```kotlin
val result = buildString {
    append("Hello")
    appendLine(", World!")
    repeat(3) { append("!") }
}
// "Hello, World!\n!!!"

// Useful for generating structured output
fun formatTable(rows: List<List<String>>): String = buildString {
    for (row in rows) {
        appendLine(row.joinToString(" | "))
    }
}
```

Prefer `buildString` over manual `StringBuilder` creation and `.toString()` calls.

## Anti-Patterns

### String Concatenation in Loops

```kotlin
// Bad: creates a new String object each iteration
var result = ""
for (item in items) {
    result += item.toString() + ", "
}

// Good: use joinToString
val result = items.joinToString(", ")

// Good: use buildString for complex formatting
val result = buildString {
    items.forEachIndexed { index, item ->
        if (index > 0) append(", ")
        append(item)
    }
}
```

### Forgetting to Close Resources

```kotlin
// Bad: resource leak if readText() throws
val reader = File("data.txt").bufferedReader()
val text = reader.readText()
reader.close()

// Good: use ensures close even on exception
val text = File("data.txt").bufferedReader().use { it.readText() }

// Best: use convenience function
val text = File("data.txt").readText()
```
