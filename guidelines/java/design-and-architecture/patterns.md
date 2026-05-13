# Common Design Patterns

## Static Factory Methods (Effective Java Item 1)

```java
public class Money {
    private final BigDecimal amount;
    private final Currency currency;

    // Private constructor
    private Money(BigDecimal amount, Currency currency) {
        this.amount = amount;
        this.currency = currency;
    }

    // Static factories — readable, flexible, can cache
    public static Money of(BigDecimal amount, Currency currency) {
        return new Money(amount, currency);
    }

    public static Money usd(double amount) {
        return new Money(BigDecimal.valueOf(amount), Currency.getInstance("USD"));
    }

    public static Money zero(Currency currency) {
        return new Money(BigDecimal.ZERO, currency);  // could cache these
    }
}

// Usage — reads naturally
Money price = Money.usd(29.99);
Money empty = Money.zero(USD);
```

## Builder Pattern

For objects with many optional fields:

```java
public class HttpRequest {
    private final String url;
    private final String method;
    private final Map<String, String> headers;
    private final Duration timeout;
    private final String body;

    private HttpRequest(Builder builder) {
        this.url = Objects.requireNonNull(builder.url);
        this.method = builder.method;
        this.headers = Map.copyOf(builder.headers);
        this.timeout = builder.timeout;
        this.body = builder.body;
    }

    public static Builder builder(String url) {
        return new Builder(url);
    }

    public static class Builder {
        private final String url;
        private String method = "GET";
        private final Map<String, String> headers = new LinkedHashMap<>();
        private Duration timeout = Duration.ofSeconds(30);
        private String body;

        private Builder(String url) { this.url = url; }

        public Builder method(String method) { this.method = method; return this; }
        public Builder header(String name, String value) { headers.put(name, value); return this; }
        public Builder timeout(Duration timeout) { this.timeout = timeout; return this; }
        public Builder body(String body) { this.body = body; return this; }

        public HttpRequest build() {
            return new HttpRequest(this);
        }
    }
}

// Usage
var request = HttpRequest.builder("https://api.example.com/orders")
    .method("POST")
    .header("Content-Type", "application/json")
    .header("Authorization", "Bearer " + token)
    .body(json)
    .timeout(Duration.ofSeconds(10))
    .build();
```

## Immutable Classes

Design classes to be immutable by default:

```java
// Using a record (simplest)
public record Money(BigDecimal amount, Currency currency) {
    public Money {
        Objects.requireNonNull(amount);
        Objects.requireNonNull(currency);
    }

    public Money add(Money other) {
        if (!currency.equals(other.currency))
            throw new IllegalArgumentException("currency mismatch");
        return new Money(amount.add(other.amount), currency);
    }
}

// Manual immutable class
public final class User {
    private final String name;
    private final List<String> roles;

    public User(String name, List<String> roles) {
        this.name = Objects.requireNonNull(name);
        this.roles = List.copyOf(roles);  // defensive copy, immutable
    }

    public String name() { return name; }
    public List<String> roles() { return roles; }  // already immutable
}
```

## Enum with Behavior

Enums can carry data and behavior — far more powerful than constants:

```java
public enum HttpStatus {
    OK(200, "OK"),
    NOT_FOUND(404, "Not Found"),
    INTERNAL_ERROR(500, "Internal Server Error");

    private final int code;
    private final String reason;

    HttpStatus(int code, String reason) {
        this.code = code;
        this.reason = reason;
    }

    public int code() { return code; }
    public String reason() { return reason; }
    public boolean isSuccess() { return code >= 200 && code < 300; }
    public boolean isError() { return code >= 400; }
}
```

### Strategy Enum

```java
public enum DiscountStrategy {
    NONE {
        public BigDecimal apply(BigDecimal total) { return total; }
    },
    PREMIUM {
        public BigDecimal apply(BigDecimal total) { return total.multiply(new BigDecimal("0.9")); }
    },
    EMPLOYEE {
        public BigDecimal apply(BigDecimal total) { return total.multiply(new BigDecimal("0.7")); }
    };

    public abstract BigDecimal apply(BigDecimal total);
}
```

### EnumSet and EnumMap

Always use `EnumSet` and `EnumMap` for enum-keyed collections — they are
backed by bit vectors and arrays, making them faster than `HashSet`/`HashMap`:

```java
Set<Permission> perms = EnumSet.of(Permission.READ, Permission.WRITE);
Map<DayOfWeek, List<Meeting>> schedule = new EnumMap<>(DayOfWeek.class);
```
