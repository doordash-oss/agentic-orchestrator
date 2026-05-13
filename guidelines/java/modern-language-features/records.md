# Records (Java 16+)

## What Records Provide

Records are transparent, immutable data carriers. The compiler generates:
- Private final fields for each component
- Public accessor methods (same name as the component, not `getX()`)
- `equals()`, `hashCode()`, `toString()`
- A canonical constructor

```java
// Replaces 50+ lines of POJO boilerplate
public record Point(int x, int y) {}

var p = new Point(1, 2);
p.x();                    // 1 (not getX())
p.toString();             // Point[x=1, y=2]
p.equals(new Point(1, 2)); // true
```

## When to Use Records

- **DTOs and value objects** — API responses, request bodies, query results
- **Intermediate data** — stream pipeline results, method return tuples
- **Configuration holders** — immutable config bundles
- **Map keys and Set elements** — correct equals/hashCode for free

## When NOT to Use Records

- **Entities with identity** — JPA entities, mutable domain objects
- **Classes that need inheritance** — records are implicitly final
- **Objects with complex construction logic** — builders may be more appropriate
- **When you need mutable state** — record fields are always final

## Custom Constructors

### Compact Constructor (Validation)

```java
public record Range(int low, int high) {
    public Range {  // compact form — no parameter list
        if (low > high) {
            throw new IllegalArgumentException(
                "low (%d) > high (%d)".formatted(low, high));
        }
    }
}
```

The compact constructor runs **before** field assignment. It can validate
and normalize parameters:

```java
public record Email(String value) {
    public Email {
        value = value.strip().toLowerCase();  // normalize
        if (!value.contains("@")) {
            throw new IllegalArgumentException("invalid email: " + value);
        }
    }
}
```

### Custom Constructors

```java
public record Money(BigDecimal amount, Currency currency) {
    // Additional constructor (must delegate to canonical)
    public Money(double amount, String currencyCode) {
        this(BigDecimal.valueOf(amount), Currency.getInstance(currencyCode));
    }
}
```

## Records with Interfaces

Records can implement interfaces but cannot extend classes:

```java
public sealed interface Shape permits Circle, Rectangle {}

public record Circle(double radius) implements Shape {
    public double area() { return Math.PI * radius * radius; }
}

public record Rectangle(double width, double height) implements Shape {
    public double area() { return width * height; }
}
```

## Records and Serialization

Records have built-in serialization support that is safer than traditional
Java serialization — they use the canonical constructor for deserialization,
avoiding the security pitfalls of the `Serializable` mechanism.

For JSON serialization, records work with Jackson and Gson out of the box.

## Records as Local Types

Records can be declared locally inside methods:

```java
public List<String> topUsers(List<Transaction> txns) {
    record UserTotal(String user, long total) {}

    return txns.stream()
        .collect(groupingBy(Transaction::user, summingLong(Transaction::amount)))
        .entrySet().stream()
        .map(e -> new UserTotal(e.getKey(), e.getValue()))
        .sorted(comparing(UserTotal::total).reversed())
        .limit(10)
        .map(UserTotal::user)
        .toList();
}
```

## Records and Pattern Matching (Java 21+)

Records support destructuring in pattern matching:

```java
if (shape instanceof Circle(var radius)) {
    return Math.PI * radius * radius;
}

// In switch
return switch (shape) {
    case Circle(var r) -> Math.PI * r * r;
    case Rectangle(var w, var h) -> w * h;
};
```
