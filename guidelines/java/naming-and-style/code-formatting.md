# Code Formatting

## Use an Automated Formatter

Never argue about formatting. Use **google-java-format** or your IDE's built-in
formatter configured to a team standard. Run it on save or in CI.

```bash
# google-java-format
java -jar google-java-format.jar --replace src/**/*.java

# Maven plugin
mvn com.spotify.fmt:fmt-maven-plugin:format

# Gradle plugin
./gradlew googleJavaFormat
```

## Key Formatting Rules (Google Java Style)

### Braces
- **K&R style** — opening brace on same line, closing brace on its own line
- Braces are used even for single-statement blocks

```java
// Correct
if (condition) {
    doSomething();
}

// Wrong — no braces on single statement
if (condition)
    doSomething();  // easy to introduce bugs when adding lines
```

### Indentation
- **2 spaces** (Google style) or **4 spaces** (traditional Java) — pick one, be consistent
- Never use tabs
- Continuation indent: +4 from the original line (Google) or +8 (traditional)

### Line Length
- **100 characters** (Google) or **120 characters** (common alternative)
- Break long lines at meaningful points

### Imports
- No wildcard imports (`import java.util.*`) — always import specific classes
- Order: static imports first, then regular imports, alphabetically within groups
- Remove unused imports (configure IDE to do this on save)

```java
// Correct — specific imports, ordered
import static org.assertj.core.api.Assertions.assertThat;

import com.example.order.Order;
import com.example.order.OrderService;
import java.time.Instant;
import java.util.List;
```

## Class Organization

Order members within a class consistently:

1. Static fields (constants first)
2. Instance fields
3. Constructors
4. Static factory methods
5. Public methods
6. Package-private/protected methods
7. Private methods
8. Inner classes/interfaces

```java
public class OrderService {
    // 1. Constants and static fields
    private static final Logger log = LoggerFactory.getLogger(OrderService.class);
    private static final int MAX_RETRIES = 3;

    // 2. Instance fields
    private final OrderRepository repository;
    private final PaymentService payments;

    // 3. Constructor
    public OrderService(OrderRepository repository, PaymentService payments) {
        this.repository = repository;
        this.payments = payments;
    }

    // 4. Public methods
    public Order createOrder(CreateOrderRequest request) { ... }
    public Order getOrder(String id) { ... }

    // 5. Private methods
    private void validateRequest(CreateOrderRequest request) { ... }
}
```

## Blank Lines

- One blank line between methods
- One blank line between field groups and constructors
- No blank lines at the start/end of a class or method body
- Use blank lines within methods to separate logical sections (sparingly)

## Annotations

- One annotation per line for type/method declarations
- Multiple annotations on one line for parameters (if short)

```java
@Override
@Transactional
public Order save(Order order) { ... }

public void process(@NonNull String id, @Nullable String label) { ... }
```
