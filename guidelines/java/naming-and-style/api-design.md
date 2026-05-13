# API Design

## Static Factory Methods Over Constructors

Prefer static factory methods for flexibility and readability (Effective Java
Item 1):

```java
// Static factories — more readable, can cache, can return subtypes
public static Order of(String id, List<Item> items) { ... }
public static Order fromJson(String json) { ... }
public static Order empty() { return EMPTY; }

// Conventional factory method names
of()         // aggregation: EnumSet.of(a, b, c)
from()       // type conversion: Date.from(instant)
valueOf()    // alternative to of: BigInteger.valueOf(42)
getInstance() // returns shared/cached instance
newInstance() // guarantees a new instance each call
create()     // like newInstance but for parameterized factories
```

## Fluent APIs and Builders

Use the builder pattern for objects with many optional parameters (Effective
Java Item 2):

```java
var order = Order.builder()
    .id(UUID.randomUUID().toString())
    .customer(customer)
    .items(items)
    .shippingAddress(address)     // optional
    .discountCode("SAVE20")      // optional
    .build();
```

**Builder rules**:
- Validate in `build()`, not in individual setters
- Return `this` from each setter for chaining
- Make the builder a static inner class
- The built object should be immutable

## Method Overloading Rules

- **Never overload with same number of parameters** where types could be confused
- **Each overload should do the same thing** — just with different convenience signatures

```java
// Good — clear overloads with different arity
public void log(String message) { ... }
public void log(String message, Throwable cause) { ... }

// Dangerous — which overload is called with null?
public void process(String value) { ... }
public void process(Object value) { ... }
process(null);  // ambiguous!
```

## Defensive Copying

Copy mutable inputs and outputs to prevent callers from corrupting internal
state:

```java
// Defensive copy on input
public Period(Date start, Date end) {
    this.start = new Date(start.getTime());  // copy
    this.end = new Date(end.getTime());      // copy
    if (this.start.after(this.end)) {
        throw new IllegalArgumentException("start after end");
    }
}

// Defensive copy on output
public Date getStart() {
    return new Date(start.getTime());  // copy, not the internal reference
}

// Better — use immutable types (Instant, LocalDate) and avoid the problem
public record Period(Instant start, Instant end) {
    public Period {
        if (start.isAfter(end)) throw new IllegalArgumentException("...");
    }
}
```

## Return Types

- **Return `Optional`** instead of null for methods that may have no result
- **Return empty collections** instead of null
- **Return the most general interface** — `List`, not `ArrayList`
- **Return immutable views** when callers shouldn't modify the result

```java
// Good — Optional for absent values
public Optional<User> findByEmail(String email) { ... }

// Good — empty list, not null
public List<Order> findByCustomer(String id) {
    // return List.of() instead of null if no orders
}

// Good — unmodifiable view
public List<String> getTags() {
    return Collections.unmodifiableList(tags);
}
```

## Parameter Validation

Validate public method parameters at entry (Effective Java Item 49):

```java
public void sendMessage(String to, String body) {
    Objects.requireNonNull(to, "to");
    Objects.requireNonNull(body, "body");
    if (to.isBlank()) {
        throw new IllegalArgumentException("'to' must not be blank");
    }
    // ... proceed with validated inputs
}
```

Use `Objects.requireNonNull()` for null checks — it's concise, standard, and
its message appears in the `NullPointerException`.
