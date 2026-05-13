# Composition and Interfaces

## Composition Over Inheritance

Prefer delegation (has-a) over subclassing (is-a). Inheritance creates tight
coupling and fragile hierarchies:

```java
// Wrong — inheritance for code reuse
class LoggingOrderService extends OrderService {
    @Override
    public Order createOrder(Request req) {
        log.info("creating order");
        return super.createOrder(req);  // fragile — base class changes break this
    }
}

// Better — composition with delegation
class LoggingOrderService implements OrderOperations {
    private final OrderOperations delegate;

    LoggingOrderService(OrderOperations delegate) {
        this.delegate = delegate;
    }

    public Order createOrder(Request req) {
        log.info("creating order");
        return delegate.createOrder(req);
    }
}
```

## When Inheritance IS Appropriate

- **True is-a relationships** in domain models (e.g., abstract base classes)
- **Framework extension points** designed for subclassing (e.g., `AbstractList`)
- **Sealed class hierarchies** — explicitly designed, compiler-checked

## Interfaces with Default Methods

Default methods enable interface evolution and mix-in behavior:

```java
public interface Auditable {
    Instant createdAt();
    Instant updatedAt();

    default boolean wasModified() {
        return !createdAt().equals(updatedAt());
    }
}

// Any implementing record/class gets wasModified() for free
public record Order(String id, Instant createdAt, Instant updatedAt, ...)
    implements Auditable {}
```

**Rules for default methods**:
- Use for **convenience methods** derived from other interface methods
- Don't use for **complex logic** — move that to a utility class or abstract class
- Don't use to **add state** — interfaces have no fields

## Functional Interfaces

A functional interface has exactly one abstract method and can be used with
lambdas:

```java
@FunctionalInterface
public interface Validator<T> {
    ValidationResult validate(T input);
}

// Usage with lambda
Validator<Order> priceCheck = order ->
    order.total().compareTo(BigDecimal.ZERO) > 0
        ? ValidationResult.ok()
        : ValidationResult.error("total must be positive");
```

Use standard functional interfaces when possible:

| Interface | Signature | Use Case |
|-----------|-----------|----------|
| `Function<T,R>` | `R apply(T t)` | Transform a value |
| `Predicate<T>` | `boolean test(T t)` | Filter/test a value |
| `Consumer<T>` | `void accept(T t)` | Perform a side effect |
| `Supplier<T>` | `T get()` | Lazy value production |
| `UnaryOperator<T>` | `T apply(T t)` | Transform same type |
| `BiFunction<T,U,R>` | `R apply(T t, U u)` | Two inputs, one output |

## The Decorator Pattern

Compose behavior by wrapping implementations:

```java
interface OrderRepository {
    void save(Order order);
    Optional<Order> findById(String id);
}

// Core implementation
class JdbcOrderRepository implements OrderRepository { ... }

// Decorator — adds caching
class CachingOrderRepository implements OrderRepository {
    private final OrderRepository delegate;
    private final Map<String, Order> cache = new ConcurrentHashMap<>();

    CachingOrderRepository(OrderRepository delegate) {
        this.delegate = delegate;
    }

    public Optional<Order> findById(String id) {
        return Optional.ofNullable(
            cache.computeIfAbsent(id, k ->
                delegate.findById(k).orElse(null)));
    }

    public void save(Order order) {
        delegate.save(order);
        cache.put(order.id(), order);
    }
}

// Compose at wiring time
var repo = new CachingOrderRepository(new JdbcOrderRepository(dataSource));
```

## Interface Anti-Patterns

```java
// Wrong — "marker interface" with no methods (use annotations instead)
interface Auditable {}

// Wrong — constant interface (use enum or class constants)
interface AppConstants {
    String VERSION = "1.0";
    int MAX_RETRIES = 3;
}

// Wrong — interface with only one implementation (premature abstraction)
interface UserService { User findById(String id); }
class UserServiceImpl implements UserService { ... }  // the "Impl" suffix is a code smell
// Unless you genuinely need multiple implementations (e.g., for testing), skip the interface
```
