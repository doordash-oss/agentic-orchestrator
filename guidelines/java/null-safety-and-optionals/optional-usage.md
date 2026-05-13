# Optional Usage

## The Rule

`Optional<T>` is designed for one thing: **return values that may be absent**.
It signals to the caller that "this method might not have a result" and forces
them to handle that case.

```java
// Good — signals possible absence
public Optional<User> findByEmail(String email) {
    User user = userMap.get(email);
    return Optional.ofNullable(user);
}

// Caller is forced to handle absence
findByEmail(email)
    .map(User::name)
    .orElse("unknown");
```

## Where NOT to Use Optional

- **Method parameters** — use overloaded methods or `@Nullable`
- **Fields** — use null with `@Nullable` annotation
- **Collection elements** — use empty collections or filter nulls out
- **Constructor parameters** — use `@Nullable` or provide defaults
- **Serialization** — Optional is not Serializable

```java
// Wrong — Optional as parameter
public void sendEmail(String to, Optional<String> cc) { ... }

// Correct — overload or @Nullable
public void sendEmail(String to) { ... }
public void sendEmail(String to, String cc) { ... }

// Wrong — Optional as field
private Optional<Address> address;  // not serializable, wastes memory

// Correct — @Nullable field
@Nullable private Address address;
```

## Chaining Operations

Use `map`, `flatMap`, and `filter` instead of `isPresent()`/`get()` pairs:

```java
// Wrong — imperative style defeats the purpose
Optional<User> opt = findUser(id);
if (opt.isPresent()) {
    User user = opt.get();
    return user.getEmail();
}
return "unknown";

// Correct — functional chain
return findUser(id)
    .map(User::email)
    .orElse("unknown");
```

### map vs flatMap

```java
// map — when the function returns a plain value
optional.map(User::name);          // Optional<String>

// flatMap — when the function returns Optional (avoids Optional<Optional<T>>)
optional.flatMap(User::address);   // if address() returns Optional<Address>
```

### or (Java 9+) — Fallback Optional Chain

```java
return findInCache(key)
    .or(() -> findInDatabase(key))
    .or(() -> findInRemoteService(key))
    .orElseThrow(() -> new NotFoundException(key));
```

### ifPresentOrElse (Java 9+)

```java
findUser(id).ifPresentOrElse(
    user -> sendWelcomeEmail(user),
    () -> log.warn("user {} not found", id)
);
```

## Getting Values Out

| Method | Behavior | When to Use |
|--------|----------|-------------|
| `orElse(default)` | Returns default if empty | Default is cheap to compute |
| `orElseGet(supplier)` | Calls supplier if empty | Default is expensive |
| `orElseThrow()` | Throws NoSuchElementException | Value is required (fail fast) |
| `orElseThrow(supplier)` | Throws custom exception | Specific error needed |
| `stream()` | Converts to 0-or-1 element stream | Integration with stream pipelines |

```java
// orElse vs orElseGet
user.orElse(createDefaultUser());         // createDefaultUser() always called!
user.orElseGet(() -> createDefaultUser()); // only called when empty

// orElseThrow with custom exception
user.orElseThrow(() -> new UserNotFoundException(id));
```

## Creating Optionals

```java
Optional.of(value);           // throws NullPointerException if value is null
Optional.ofNullable(value);   // empty Optional if value is null
Optional.empty();             // always empty

// Rule: use of() when null would be a bug
// Rule: use ofNullable() when null is a valid possibility
```

## Anti-Patterns

```java
// Wrong — Optional.get() without isPresent (can throw NoSuchElementException)
return optional.get();

// Wrong — using Optional for control flow
if (optional.isPresent()) { return optional.get(); } else { return default; }
// Correct: return optional.orElse(default);

// Wrong — Optional in collections
List<Optional<String>> list;  // just filter nulls out
// Correct: List<String> list; (filter nulls during collection)

// Wrong — Optional.of(null)
Optional.of(null);  // throws NPE — use ofNullable() if null is possible

// Wrong — nesting Optionals
Optional<Optional<String>> nested;  // use flatMap to flatten
```
