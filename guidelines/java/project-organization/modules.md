# Java Platform Module System (JPMS)

## When to Use Modules

JPMS (Java 9+) provides compile-time encapsulation at the package level.
Consider modules when:

- **Building a library** that others depend on — modules prevent access to internals
- **Large applications** where you want enforced boundaries between components
- **You need to control which packages are public** — `exports` is explicit

For small applications and microservices, JPMS is often **overkill** — package
visibility and build-module boundaries are sufficient.

## module-info.java

```java
// src/main/java/module-info.java
module com.example.order {
    // Packages this module makes visible to other modules
    exports com.example.order;
    exports com.example.order.api;

    // Dependencies this module needs
    requires com.example.common;
    requires java.sql;
    requires transitive com.example.domain;  // re-exported to consumers

    // Allow frameworks (Jackson, Spring) to access internals via reflection
    opens com.example.order.internal to com.fasterxml.jackson.databind;

    // Service provider interface
    provides com.example.spi.OrderProcessor
        with com.example.order.internal.DefaultOrderProcessor;

    uses com.example.spi.PaymentGateway;
}
```

## Key Concepts

| Directive | Purpose |
|-----------|---------|
| `exports` | Makes a package accessible to other modules |
| `requires` | Declares a dependency on another module |
| `requires transitive` | Dependency is re-exported to consumers |
| `opens` | Allows runtime reflection (for frameworks) |
| `provides...with` | Registers a service implementation |
| `uses` | Declares consumption of a service |

## Encapsulation Benefits

Without JPMS, any public class is accessible from anywhere. With JPMS:

```java
// Module A exports only the API package
module com.example.order {
    exports com.example.order.api;
    // com.example.order.internal is NOT exported
}

// Module B
import com.example.order.api.OrderService;     // OK
import com.example.order.internal.OrderMapper;  // Compile error!
```

## Migration Strategy

For existing projects, adopt JPMS incrementally:

1. **Start on the classpath** — everything works as before (unnamed module)
2. **Add module-info.java to leaf modules first** — libraries with few dependencies
3. **Use `--add-opens` and `--add-reads` as escape hatches** during transition
4. **Test with `--illegal-access=deny`** to find reflection-based access issues

## Common Pitfalls

- **Reflection-heavy frameworks** (Spring, Hibernate) need `opens` directives
- **Split packages** (same package in multiple modules) are not allowed
- **Automatic modules** (JARs without module-info) get module names from the JAR filename — fragile
- **Test code** needs access to module internals — use `opens` for test packages or `--add-opens` in test JVM args

## Recommendation

- **Libraries**: use JPMS — it prevents consumers from depending on your internals
- **Applications**: consider JPMS for large monoliths; skip it for microservices
- **New projects**: start with JPMS if targeting Java 17+ and the team is comfortable
