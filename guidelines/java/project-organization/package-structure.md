# Package Structure

## Package by Feature, Not by Layer

Group code by **what it does** (business capability), not by **what it is**
(technical layer):

```
// Good — package by feature
com.example.myapp/
├── order/
│   ├── Order.java
│   ├── OrderService.java
│   ├── OrderRepository.java
│   ├── OrderController.java
│   └── OrderNotFoundException.java
├── payment/
│   ├── Payment.java
│   ├── PaymentService.java
│   └── PaymentGateway.java
└── user/
    ├── User.java
    ├── UserService.java
    └── UserRepository.java

// Bad — package by layer
com.example.myapp/
├── controllers/
│   ├── OrderController.java
│   ├── PaymentController.java
│   └── UserController.java
├── services/
│   ├── OrderService.java
│   ├── PaymentService.java
│   └── UserService.java
└── repositories/
    ├── OrderRepository.java
    └── UserRepository.java
```

**Why package by feature**:
- Related code is **co-located** — one directory for the entire feature
- Package-private visibility enforces **encapsulation** between features
- Changes to a feature touch **one package** instead of scattered files
- Easier to extract into a **separate module or microservice**

## Package Naming

- **Reverse domain prefix**: `com.company.product.module`
- **All lowercase**, no underscores or camelCase
- **Describe contents**, not actions: `order` (not `ordering`)
- **Singular nouns**: `com.example.user` (not `com.example.users`)

## Package Visibility

Use package-private (default) access to hide implementation details:

```java
// Public — part of the feature's API
public class OrderService { ... }
public record Order(String id, ...) { ... }

// Package-private — implementation detail
class OrderValidator { ... }          // only visible within com.example.order
class OrderMapper { ... }             // only visible within com.example.order
```

This is Java's built-in encapsulation mechanism — use it. If a class is only
used within its own feature package, don't make it public.

## The Common/Shared Package Problem

Avoid `common`, `shared`, `utils`, `helpers` packages. They become
catch-all dumping grounds:

```java
// Wrong — vague grab-bag
com.example.common.StringUtils
com.example.common.DateUtils
com.example.common.Constants

// Better — put utilities where they're used
com.example.order.OrderIdGenerator     // specific to orders
com.example.time.BusinessDayCalculator // specific domain concept
```

If you genuinely have cross-cutting utilities (rare), name the package for
what it contains: `com.example.time`, `com.example.validation`,
`com.example.security`.

## Multi-Module Projects

For larger applications, split into Maven/Gradle modules:

```
my-app/
├── my-app-api/        # Public interfaces and DTOs
├── my-app-core/       # Domain logic, service implementations
├── my-app-persistence/ # Repository implementations, DB migrations
├── my-app-web/        # REST controllers, web configuration
└── my-app-app/        # Main class, wiring, Spring Boot starter
```

**Rules for multi-module**:
- Each module has a clear, single responsibility
- Dependencies flow in one direction (web -> core -> api)
- The `api` module contains only interfaces and value types
- The `app` module wires everything together

## Standard Directory Layout

Maven/Gradle standard layout — never deviate:

```
src/
├── main/
│   ├── java/           # Production code
│   └── resources/      # Config files, templates
├── test/
│   ├── java/           # Unit tests
│   └── resources/      # Test config, fixtures
└── integrationTest/    # Integration tests (Gradle custom source set)
    ├── java/
    └── resources/
```
