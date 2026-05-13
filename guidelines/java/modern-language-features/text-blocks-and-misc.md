# Text Blocks, var, and Other Modern Features

## Text Blocks (Java 15+)

Multi-line string literals that preserve formatting:

```java
String json = """
        {
            "name": "Alice",
            "age": 30
        }
        """;

String sql = """
        SELECT u.name, u.email
        FROM users u
        WHERE u.active = true
        ORDER BY u.name
        """;
```

### Indentation Rules

The closing `"""` determines the baseline indentation. Content indented further
than the closing delimiter is preserved as indentation:

```java
// 8-space indent in the source, but output has 0 indentation
String s = """
        hello
        world
        """;  // closing delimiter at column 8 strips that prefix

// Trailing whitespace is stripped by default
// Use \s to preserve significant trailing spaces
String row = """
        Name:  Alice\s
        Email: alice@example.com\s
        """;
```

### Escape Sequences

```java
// \n within a text block is a literal newline (redundant)
// Use \ at end of line to suppress the newline (line continuation)
String longLine = """
        This is a very long line that we want \
        to keep as a single line in the output""";

// Use \s for a significant trailing space
// Use \" if you need triple quotes inside
```

## Local Variable Type Inference — var (Java 10+)

`var` infers the type from the initializer. Use it when the type is **obvious
from context**:

```java
// Good — type is obvious from the right side
var users = new ArrayList<User>();
var stream = users.stream();
var response = httpClient.send(request, BodyHandlers.ofString());
var entry = Map.entry("key", "value");

// Bad — type is not obvious
var result = service.process(data);   // what type is result?
var x = calculate();                   // obscure
var item = getNext();                  // unclear what getNext returns
```

### var Guidelines

- **Use with constructor calls** — `var list = new ArrayList<String>()`
- **Use with factory methods** — `var path = Path.of("/tmp")`
- **Use with literals** — `var count = 0` (obviously int)
- **Don't use when the type isn't clear** from the initializer
- **Don't use for fields or method parameters** — only local variables
- **Don't use with diamond** — `var list = new ArrayList<>()` infers `ArrayList<Object>`

```java
// Wrong — diamond + var loses type information
var list = new ArrayList<>();       // ArrayList<Object>, not what you want
var map = new HashMap<>();          // HashMap<Object, Object>

// Correct — explicit type argument with var
var list = new ArrayList<String>(); // ArrayList<String>
```

## Helpful NullPointerExceptions (Java 14+)

The JVM now tells you exactly what was null:

```
// Before: NullPointerException at line 42
// After:  Cannot invoke "String.length()" because "user.getName()" is null
```

Enabled by default since Java 14. No code changes needed — just upgrade.

## String Formatting Methods

```java
// formatted() method on String (Java 15+)
String msg = "Hello, %s! You have %d messages.".formatted(name, count);

// Equivalent to String.format() but more fluent
```

## Enhanced Pseudo-Random Number Generators (Java 17+)

```java
// New RandomGenerator interface with multiple implementations
var rng = RandomGenerator.of("L64X128MixRandom");

// SplittableRandom for parallel streams
var random = new SplittableRandom();
long[] numbers = random.longs(100).toArray();
```

## Unnamed Variables (Java 22+)

Use `_` for variables you don't need:

```java
// In catch blocks
try { ... }
catch (IOException _) { handleGenericError(); }

// In loops
for (var _ : collection) { count++; }

// In lambdas
map.forEach((_, value) -> process(value));

// In pattern matching
if (obj instanceof Point(var x, _)) {
    return x;  // only care about x
}
```
