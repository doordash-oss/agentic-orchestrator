# Stream Best Practices

## When to Use Streams vs Loops

**Use streams** for data transformation pipelines — filter, map, reduce, collect:

```java
// Good stream use — transformation pipeline
List<String> activeEmails = users.stream()
    .filter(User::isActive)
    .map(User::email)
    .sorted()
    .toList();
```

**Use loops** when you need:
- Side effects (writing to a file, logging, mutating external state)
- Early termination with complex conditions
- Index-based access
- Exception handling at each step

```java
// Better as a loop — side effects with error handling
for (var user : users) {
    try {
        sendEmail(user);
    } catch (EmailException e) {
        log.warn("failed to email {}", user.id(), e);
    }
}
```

## Stream Pipeline Structure

A well-formed pipeline: **source -> intermediate ops -> terminal op**

```java
orders.stream()                           // source
    .filter(o -> o.status() == ACTIVE)    // intermediate
    .map(Order::total)                    // intermediate
    .reduce(BigDecimal.ZERO, BigDecimal::add);  // terminal
```

## Common Collectors

```java
// toList() — Java 16+, returns unmodifiable list
.toList()

// Collectors.toList() — returns mutable ArrayList
.collect(Collectors.toList())

// toUnmodifiableList/Set/Map — explicit immutability
.collect(Collectors.toUnmodifiableList())

// groupingBy — group into a Map<K, List<V>>
.collect(Collectors.groupingBy(Order::customerId))

// groupingBy with downstream collector
.collect(Collectors.groupingBy(
    Order::customerId,
    Collectors.summingLong(Order::total)))

// partitioningBy — split into two groups (true/false)
.collect(Collectors.partitioningBy(User::isActive))

// toMap — explicit key and value mapping
.collect(Collectors.toMap(User::id, User::name))

// joining — concatenate strings
.collect(Collectors.joining(", ", "[", "]"))
```

## Stream Pitfalls

### Never Reuse a Stream

```java
Stream<String> stream = names.stream();
stream.forEach(System.out::println);
stream.count();  // IllegalStateException — stream already consumed!
```

### Avoid Side Effects in Intermediate Operations

```java
// Wrong — side effect in map
List<String> results = new ArrayList<>();
stream.map(s -> {
    results.add(s);  // side effect — order is undefined for parallel streams
    return s.toUpperCase();
}).toList();

// Correct — collect instead
List<String> results = stream
    .map(String::toUpperCase)
    .toList();
```

### Parallel Streams — Almost Never

Parallel streams add overhead for thread coordination. They're only beneficial
for:
- **Large datasets** (10,000+ elements)
- **CPU-intensive operations** per element
- **No shared mutable state**
- **Splittable source** (ArrayList, array, IntRange — NOT LinkedList, Stream.iterate)

```java
// Rarely beneficial — overhead > work
names.parallelStream().filter(n -> n.startsWith("A")).toList();

// Potentially beneficial — expensive per-element computation
largeDataset.parallelStream()
    .map(item -> expensiveComputation(item))
    .toList();
```

**Rule**: never use parallel streams without benchmarking. Sequential streams
are faster for the vast majority of real-world workloads.

### flatMap for Nested Collections

```java
// Flatten nested collections
List<Item> allItems = orders.stream()
    .flatMap(order -> order.items().stream())
    .toList();

// flatMap with Optional
Optional<Address> address = findUser(id)
    .flatMap(User::address);  // avoids Optional<Optional<Address>>
```

## Stream of Primitives

Use `IntStream`, `LongStream`, `DoubleStream` to avoid boxing:

```java
// Good — no boxing
int sum = IntStream.rangeClosed(1, 100).sum();

// Bad — unnecessary boxing
int sum = Stream.iterate(1, i -> i + 1)
    .limit(100)
    .mapToInt(Integer::intValue)
    .sum();
```
