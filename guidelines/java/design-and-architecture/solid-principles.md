# SOLID Principles in Java

## Single Responsibility Principle (SRP)

A class should have one reason to change — one responsibility:

```java
// Wrong — does persistence, validation, and notification
class OrderService {
    void createOrder(Order order) {
        validateOrder(order);           // validation logic
        jdbc.insert("orders", order);   // persistence logic
        emailService.send(order.email(), "Order created"); // notification
    }
}

// Better — each class has one responsibility
class OrderValidator { void validate(Order order) { ... } }
class OrderRepository { void save(Order order) { ... } }
class OrderNotifier { void notifyCreation(Order order) { ... } }

class OrderService {
    OrderService(OrderValidator validator, OrderRepository repo, OrderNotifier notifier) { ... }

    void createOrder(Order order) {
        validator.validate(order);
        repo.save(order);
        notifier.notifyCreation(order);
    }
}
```

**Test**: can you describe the class in one sentence without "and"?

## Open/Closed Principle (OCP)

Open for extension, closed for modification. Add behavior without changing
existing code:

```java
// Wrong — adding a new discount type requires modifying this method
double applyDiscount(Order order) {
    if (order.type() == PREMIUM) return order.total() * 0.9;
    if (order.type() == EMPLOYEE) return order.total() * 0.7;
    // every new type modifies this method
}

// Better — extend via new implementations
sealed interface DiscountStrategy permits PremiumDiscount, EmployeeDiscount, NoDiscount {
    BigDecimal apply(BigDecimal total);
}
record PremiumDiscount() implements DiscountStrategy {
    public BigDecimal apply(BigDecimal total) { return total.multiply(new BigDecimal("0.9")); }
}
record EmployeeDiscount() implements DiscountStrategy {
    public BigDecimal apply(BigDecimal total) { return total.multiply(new BigDecimal("0.7")); }
}
```

## Liskov Substitution Principle (LSP)

Subtypes must be substitutable for their base types without breaking behavior:

```java
// Wrong — Square violates Rectangle's contract
class Rectangle {
    void setWidth(int w) { this.width = w; }
    void setHeight(int h) { this.height = h; }
}
class Square extends Rectangle {
    void setWidth(int w) { this.width = w; this.height = w; }  // surprises callers
}

// Better — use immutable value types
sealed interface Shape permits Rectangle, Square {
    double area();
}
record Rectangle(double width, double height) implements Shape {
    public double area() { return width * height; }
}
record Square(double side) implements Shape {
    public double area() { return side * side; }
}
```

## Interface Segregation Principle (ISP)

Clients should not be forced to depend on methods they don't use:

```java
// Wrong — bloated interface
interface UserService {
    User findById(String id);
    void save(User user);
    void sendWelcomeEmail(User user);
    void resetPassword(User user);
    byte[] exportToCsv(List<User> users);
}

// Better — focused interfaces
interface UserRepository { User findById(String id); void save(User user); }
interface UserNotifier { void sendWelcomeEmail(User user); }
interface PasswordService { void resetPassword(User user); }
interface UserExporter { byte[] exportToCsv(List<User> users); }
```

## Dependency Inversion Principle (DIP)

High-level modules should not depend on low-level modules. Both should depend
on abstractions:

```java
// Wrong — OrderService depends directly on PostgresRepository
class OrderService {
    private final PostgresOrderRepository repo;  // concrete dependency
}

// Better — depend on an interface
class OrderService {
    private final OrderRepository repo;  // interface
    OrderService(OrderRepository repo) { this.repo = repo; }
}

interface OrderRepository { Optional<Order> findById(String id); void save(Order order); }
class PostgresOrderRepository implements OrderRepository { ... }
class InMemoryOrderRepository implements OrderRepository { ... }  // for tests
```
