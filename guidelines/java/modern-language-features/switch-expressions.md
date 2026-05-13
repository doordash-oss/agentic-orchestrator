# Switch Expressions (Java 14+)

## Arrow Syntax

Switch expressions use arrow syntax (`->`) instead of fall-through colon syntax:

```java
// Before — statement with break, fall-through risk
String label;
switch (day) {
    case MONDAY: case TUESDAY:
        label = "early week";
        break;  // forget this and you have a bug
    case FRIDAY:
        label = "TGIF";
        break;
    default:
        label = "other";
}

// After — expression, no fall-through
String label = switch (day) {
    case MONDAY, TUESDAY -> "early week";
    case FRIDAY -> "TGIF";
    default -> "other";
};
```

Switch expressions:
- Return a value (they're expressions, not statements)
- No fall-through — each arm is independent
- Multiple labels per arm with commas
- Must be exhaustive (every possible input is covered)

## yield for Multi-Statement Arms

When an arm needs multiple statements, use a block with `yield`:

```java
String result = switch (status) {
    case PENDING -> "waiting";
    case ACTIVE -> {
        logActivation();
        notifyUser();
        yield "active";  // return value from block
    }
    case CLOSED -> "done";
};
```

**Rule**: simple arms use arrow expressions; complex arms use blocks with
`yield`. Never use `return` inside a switch expression arm.

## Exhaustiveness

Switch expressions **must** be exhaustive — every possible input value must
be covered:

```java
// Enum — must cover all constants (or use default)
String name = switch (season) {
    case SPRING -> "spring";
    case SUMMER -> "summer";
    case FALL -> "fall";
    case WINTER -> "winter";
    // No default needed — all enum values covered
};

// Sealed type — must cover all permitted subtypes
double area = switch (shape) {
    case Circle c -> Math.PI * c.radius() * c.radius();
    case Rectangle r -> r.width() * r.height();
    // Compiler enforces completeness
};

// Other types — default is required
String desc = switch (obj) {
    case String s -> "string";
    case Integer i -> "int";
    default -> "other";  // required for non-sealed types
};
```

## When to Use Switch Expressions

**Replace if-else chains** that map an input to an output:

```java
// Before — verbose, error-prone
String httpMethod;
if (code == 200) httpMethod = "OK";
else if (code == 404) httpMethod = "Not Found";
else if (code == 500) httpMethod = "Internal Server Error";
else httpMethod = "Unknown";

// After — clean, exhaustive
String httpMethod = switch (code) {
    case 200 -> "OK";
    case 404 -> "Not Found";
    case 500 -> "Internal Server Error";
    default -> "Unknown";
};
```

## Combining with Pattern Matching (Java 21+)

Switch expressions become truly powerful with pattern matching:

```java
String describe(Object obj) {
    return switch (obj) {
        case null -> "null";
        case Integer i when i < 0 -> "negative int";
        case Integer i -> "int: " + i;
        case String s when s.isBlank() -> "blank string";
        case String s -> "string: " + s;
        case int[] arr -> "int array of length " + arr.length;
        default -> obj.getClass().getSimpleName();
    };
}
```

## Anti-Patterns

```java
// Wrong — using switch expression where a map suffices
String label = switch (code) {
    case "US" -> "United States";
    case "UK" -> "United Kingdom";
    case "DE" -> "Germany";
    // ... 200 more countries
    default -> "Unknown";
};
// Better — use a Map
String label = COUNTRY_NAMES.getOrDefault(code, "Unknown");

// Wrong — adding default to a sealed type switch
return switch (shape) {
    case Circle c -> ...;
    case Rectangle r -> ...;
    default -> throw new AssertionError();  // hides missing cases
};
// Better — omit default, let compiler check exhaustiveness
```
