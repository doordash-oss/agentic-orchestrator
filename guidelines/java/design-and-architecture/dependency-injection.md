# Dependency Injection

## Constructor Injection

The preferred DI pattern. Dependencies are explicit, immutable, and required:

```java
public class OrderService {
    private final OrderRepository repository;
    private final PaymentService payments;
    private final Clock clock;

    public OrderService(OrderRepository repository, PaymentService payments, Clock clock) {
        this.repository = Objects.requireNonNull(repository);
        this.payments = Objects.requireNonNull(payments);
        this.clock = Objects.requireNonNull(clock);
    }
}
```

**Benefits**:
- Dependencies are visible in the constructor signature
- Fields can be `final` — immutable after construction
- Objects are always fully initialized — no half-constructed state
- Easy to test — pass mocks or fakes directly

## Manual DI vs Frameworks

**Manual DI** (wiring in `main()` or a composition root):

```java
public static void main(String[] args) {
    var dataSource = createDataSource();
    var repository = new JdbcOrderRepository(dataSource);
    var payments = new StripePaymentService(config.stripeKey());
    var clock = Clock.systemUTC();
    var service = new OrderService(repository, payments, clock);
    var controller = new OrderController(service);
    startServer(controller);
}
```

**Framework DI** (Spring, Guice, Dagger):

```java
// Spring — automatic constructor injection (single constructor = implicit @Autowired)
@Service
public class OrderService {
    private final OrderRepository repository;
    private final PaymentService payments;

    // Spring injects automatically — no @Autowired needed with single constructor
    public OrderService(OrderRepository repository, PaymentService payments) {
        this.repository = repository;
        this.payments = payments;
    }
}
```

**When to use which**:
- **Manual DI** — small services, libraries, CLIs, when you want zero framework magic
- **Framework DI** — large applications with many components, when you benefit from auto-wiring and lifecycle management

## Avoid Field and Setter Injection

```java
// Wrong — field injection (hidden dependencies, untestable without framework)
@Service
class OrderService {
    @Autowired OrderRepository repository;  // can't set in tests without Spring
    @Autowired PaymentService payments;
}

// Wrong — setter injection (mutable, partially constructed objects)
class OrderService {
    private OrderRepository repository;

    @Autowired
    void setRepository(OrderRepository repository) {
        this.repository = repository;  // can be called after construction
    }
}
```

## Inject Abstractions, Not Implementations

```java
// Good — depends on interface
public OrderService(OrderRepository repository) { ... }

// Bad — depends on concrete class
public OrderService(PostgresOrderRepository repository) { ... }
```

## Clock Injection for Time-Dependent Code

Never call `Instant.now()` or `System.currentTimeMillis()` directly. Inject
a `Clock` for testability:

```java
public class OrderService {
    private final Clock clock;

    public OrderService(OrderRepository repo, Clock clock) {
        this.clock = clock;
    }

    public Order createOrder(Request req) {
        return new Order(req.items(), Instant.now(clock));
    }
}

// In production
new OrderService(repo, Clock.systemUTC());

// In tests — fixed clock for deterministic tests
new OrderService(repo, Clock.fixed(Instant.parse("2024-01-15T10:00:00Z"), ZoneOffset.UTC));
```

## Avoid the Service Locator Anti-Pattern

```java
// Wrong — service locator hides dependencies
class OrderService {
    void createOrder(Order order) {
        var repo = ServiceLocator.get(OrderRepository.class);  // hidden dependency
        repo.save(order);
    }
}

// Correct — explicit constructor dependency
class OrderService {
    private final OrderRepository repo;
    OrderService(OrderRepository repo) { this.repo = repo; }
}
```

## Too Many Constructor Parameters

If a constructor has more than ~5 parameters, the class likely has too many
responsibilities. Options:

1. **Split the class** — extract a focused collaborator
2. **Introduce a parameter object** — group related parameters into a record
3. **Use a facade** — one class orchestrates, others do the work
