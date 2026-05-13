# Logging Best Practices

## Use SLF4J as the Facade

SLF4J decouples your code from the logging implementation (Logback, Log4j2):

```java
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

public class OrderService {
    private static final Logger log = LoggerFactory.getLogger(OrderService.class);

    public void processOrder(Order order) {
        log.info("Processing order {}", order.id());
    }
}
```

## Parameterized Messages — Never Concatenate

```java
// Correct — parameterized (lazy evaluation, no string building if level is disabled)
log.debug("Processing order {} for customer {}", orderId, customerId);

// Wrong — string concatenation (always evaluated, even if debug is disabled)
log.debug("Processing order " + orderId + " for customer " + customerId);
```

Parameterized messages avoid unnecessary string allocation when the log level
is disabled — which is most of the time for DEBUG/TRACE.

## Log Levels

| Level | When to Use |
|-------|-------------|
| `ERROR` | Unrecoverable failure — something is broken, needs attention |
| `WARN` | Recoverable issue — degraded service, fallback used, retry succeeded |
| `INFO` | Business milestones — request processed, job completed, startup/shutdown |
| `DEBUG` | Technical details — SQL queries, HTTP responses, algorithm steps |
| `TRACE` | Very detailed — loop iterations, wire-level data (rarely enabled in production) |

```java
log.error("Payment failed for order {}", orderId, exception);  // include exception
log.warn("Retrying connection to {} (attempt {}/{})", host, attempt, maxRetries);
log.info("Order {} created for customer {}", orderId, customerId);
log.debug("Query returned {} results in {}ms", count, elapsed);
```

## Always Log Exceptions Properly

Pass the exception as the **last argument** — SLF4J prints the full stack trace:

```java
// Correct — exception is last arg, gets full stack trace
log.error("Failed to process order {}", orderId, exception);

// Wrong — exception toString() only, no stack trace
log.error("Failed to process order {}: {}", orderId, exception.getMessage());

// Wrong — exception in the format string position
log.error("Failed: {}", exception);  // logs exception.toString(), no stack trace
```

## MDC (Mapped Diagnostic Context)

Add per-request context to all log lines (e.g., request ID, user ID):

```java
// Set at request entry point
MDC.put("requestId", requestId);
MDC.put("userId", userId);
try {
    processRequest(request);
} finally {
    MDC.clear();  // always clear to prevent leaking to other requests
}

// Appears in every log line via pattern:
// %d [%thread] %-5level %logger - [%X{requestId}] %msg%n
// 2024-01-15 10:30:00 [http-1] INFO  OrderService - [req-abc123] Processing order
```

## Structured Logging

For log aggregation (ELK, Datadog), use structured JSON output:

```xml
<!-- logback.xml with JSON encoder -->
<encoder class="net.logstash.logback.encoder.LogstashEncoder" />
```

Key-value pairs in structured logs:

```java
import static net.logstash.logback.argument.StructuredArguments.*;

log.info("Order processed", kv("orderId", id), kv("total", total), kv("durationMs", elapsed));
// JSON: {"message": "Order processed", "orderId": "123", "total": 99.99, "durationMs": 45}
```

## Logging Anti-Patterns

```java
// Wrong — logging sensitive data
log.info("User {} logged in with password {}", user, password);

// Wrong — using System.out/System.err
System.out.println("Processing order");  // use log.info() instead

// Wrong — excessive logging in loops
for (var item : items) {
    log.info("Processing item {}", item.id());  // thousands of log lines
}
// Better: log summary
log.info("Processing {} items", items.size());

// Wrong — logging and throwing (logs the same error multiple times)
catch (IOException e) {
    log.error("failed", e);
    throw new ServiceException("failed", e);  // caller will also log it
}
```
