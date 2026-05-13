# Sealed Classes and Interfaces (Java 17+)

## What Sealed Types Provide

Sealed types restrict which classes can implement/extend them. This creates
**closed type hierarchies** that the compiler can reason about:

```java
public sealed interface Shape permits Circle, Rectangle, Triangle {}

public record Circle(double radius) implements Shape {}
public record Rectangle(double w, double h) implements Shape {}
public record Triangle(double a, double b, double c) implements Shape {}
```

Now `Shape` can **only** be `Circle`, `Rectangle`, or `Triangle`. The compiler
knows this, enabling exhaustive pattern matching.

## Exhaustive Pattern Matching

The killer feature — the compiler verifies you handle all cases:

```java
// No default needed — compiler knows all Shape subtypes
public double area(Shape shape) {
    return switch (shape) {
        case Circle c -> Math.PI * c.radius() * c.radius();
        case Rectangle r -> r.w() * r.h();
        case Triangle t -> heronsFormula(t.a(), t.b(), t.c());
        // Compiler error if you forget a case
    };
}
```

If you add a new subtype to the sealed interface, **every switch that matches
on it gets a compiler error** — the compiler forces you to handle the new case.
This is far safer than a default branch that silently passes.

## When to Use Sealed Types

- **Domain models with a fixed set of variants** — payment types, AST nodes,
  protocol messages, state machines
- **Replacing enum + data pattern** — when variants carry different data
- **API contracts** — restrict implementations to your module

```java
// Before: enum can't carry different data per variant
enum Result { SUCCESS, ERROR }  // no payload

// After: sealed hierarchy with variant-specific data
sealed interface Result<T> permits Success, Failure {}
record Success<T>(T value) implements Result<T> {}
record Failure<T>(Exception error) implements Result<T> {}
```

## The permits Clause

Subtypes must be in the same package (or module) as the sealed type. If all
subtypes are in the same file, `permits` can be omitted:

```java
// Explicit permits
public sealed interface Event permits ClickEvent, KeyEvent {}

// Implicit permits (subtypes in same file)
sealed interface Event {}
record ClickEvent(int x, int y) implements Event {}
record KeyEvent(char key) implements Event {}
```

## Subtype Modifiers

Each subtype must be one of:

- **`final`** — no further extension (records are implicitly final)
- **`sealed`** — further restricted subtypes
- **`non-sealed`** — open for arbitrary extension (breaks exhaustiveness)

```java
sealed interface Animal permits Dog, Cat, Fish {}
final class Dog implements Animal {}             // closed
sealed class Cat implements Animal permits Kitten {} // partially open
non-sealed class Fish implements Animal {}       // fully open
```

**Avoid `non-sealed`** unless you deliberately want to open the hierarchy —
it breaks the exhaustiveness guarantee that makes sealed types valuable.

## Combining with Records

The most powerful pattern — sealed interfaces with record subtypes:

```java
sealed interface Expr permits Literal, Add, Mul, Neg {}
record Literal(double value) implements Expr {}
record Add(Expr left, Expr right) implements Expr {}
record Mul(Expr left, Expr right) implements Expr {}
record Neg(Expr operand) implements Expr {}

double eval(Expr expr) {
    return switch (expr) {
        case Literal(var v) -> v;
        case Add(var l, var r) -> eval(l) + eval(r);
        case Mul(var l, var r) -> eval(l) * eval(r);
        case Neg(var e) -> -eval(e);
    };
}
```

This is algebraic data types in Java — sum types (sealed interface) with
product types (records).
