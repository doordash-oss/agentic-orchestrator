# Pattern Matching

## instanceof Patterns (Java 16+)

Eliminates the cast-after-check boilerplate:

```java
// Before — manual cast
if (obj instanceof String) {
    String s = (String) obj;
    process(s);
}

// After — pattern variable
if (obj instanceof String s) {
    process(s);  // s is already cast and in scope
}

// Also works with negation
if (!(obj instanceof String s)) {
    return;  // early exit
}
process(s);  // s is in scope here (flow scoping)
```

## Switch Pattern Matching (Java 21+)

Pattern matching in switch enables type-based dispatch with data extraction:

```java
return switch (obj) {
    case Integer i -> "int: " + i;
    case String s  -> "string: " + s;
    case null      -> "null";
    default        -> "other: " + obj;
};
```

## Record Patterns (Java 21+)

Destructure records directly in patterns:

```java
record Point(int x, int y) {}

// Simple destructuring
if (point instanceof Point(int x, int y)) {
    return Math.sqrt(x * x + y * y);
}

// Nested destructuring
record Line(Point start, Point end) {}

if (line instanceof Line(Point(var x1, var y1), Point(var x2, var y2))) {
    return Math.sqrt(Math.pow(x2 - x1, 2) + Math.pow(y2 - y1, 2));
}
```

## Guarded Patterns

Add conditions to patterns with `when`:

```java
return switch (shape) {
    case Circle c when c.radius() <= 0 -> throw new IllegalArgumentException("invalid");
    case Circle c -> Math.PI * c.radius() * c.radius();
    case Rectangle r -> r.w() * r.h();
};
```

**Order matters**: guarded patterns must come before unguarded patterns of the
same type. The compiler checks this.

## Null Handling in Switch

Before Java 21, switch threw `NullPointerException` on null input. Now you
can match null explicitly:

```java
return switch (value) {
    case null -> "no value";
    case String s -> s.toUpperCase();
    case Integer i -> i.toString();
    default -> value.toString();
};

// Combined with other labels
return switch (status) {
    case null, "" -> "unknown";
    case String s -> s;
};
```

## Exhaustiveness

When switching over a sealed type, the compiler verifies all cases are covered:

```java
sealed interface Shape permits Circle, Rectangle {}
record Circle(double r) implements Shape {}
record Rectangle(double w, double h) implements Shape {}

// Compiler error — missing Rectangle case
double area(Shape s) {
    return switch (s) {
        case Circle c -> Math.PI * c.r() * c.r();
        // ERROR: switch expression does not cover all possible input values
    };
}
```

Adding a `default` branch disables exhaustiveness checking — avoid it when
matching sealed types, because you lose the compile-time safety when new
subtypes are added.

## Pattern Matching Best Practices

1. **Use pattern variables instead of manual casts** — safer and more concise
2. **Prefer sealed types + switch over if-else instanceof chains**
3. **Avoid default with sealed types** — let the compiler catch missing cases
4. **Use record patterns for data extraction** — cleaner than calling accessors
5. **Order from specific to general** — guarded patterns before unguarded,
   subtypes before supertypes
