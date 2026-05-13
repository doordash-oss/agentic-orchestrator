# Map Patterns

## Modern Map API (Java 8+)

The `Map` interface gained powerful methods in Java 8 that eliminate most
manual get-check-put patterns:

### getOrDefault

```java
// Before
String value = map.get(key);
if (value == null) value = "default";

// After
String value = map.getOrDefault(key, "default");
```

### computeIfAbsent — Lazy Initialize

Creates the value only if the key is missing:

```java
// Cache pattern
Map<String, ExpensiveResult> cache = new HashMap<>();
ExpensiveResult result = cache.computeIfAbsent(key, k -> compute(k));

// Multi-map pattern (map of lists)
Map<String, List<Order>> ordersByCustomer = new HashMap<>();
ordersByCustomer.computeIfAbsent(customerId, k -> new ArrayList<>()).add(order);
```

**Thread-safe with ConcurrentHashMap** — `computeIfAbsent` is atomic:

```java
ConcurrentHashMap<String, AtomicLong> counters = new ConcurrentHashMap<>();
counters.computeIfAbsent(key, k -> new AtomicLong()).incrementAndGet();
```

### computeIfPresent — Update Existing

Updates the value only if the key exists:

```java
map.computeIfPresent(key, (k, v) -> v + 1);  // increment if exists
map.computeIfPresent(key, (k, v) -> null);    // remove if exists (returns null removes entry)
```

### compute — Create or Update

```java
// Increment counter, initialize to 1 if missing
map.compute(key, (k, v) -> v == null ? 1 : v + 1);
```

### merge — Combine Values

The most concise pattern for aggregation:

```java
// Count occurrences
Map<String, Integer> wordCounts = new HashMap<>();
for (String word : words) {
    wordCounts.merge(word, 1, Integer::sum);
}

// Concatenate values
Map<String, String> combined = new HashMap<>();
combined.merge(key, newValue, (old, new_) -> old + ", " + new_);
```

`merge` handles both the "key missing" case (uses the provided value) and the
"key present" case (applies the remapping function).

### putIfAbsent

```java
// Only inserts if key is not already present
map.putIfAbsent(key, value);

// Equivalent to:
if (!map.containsKey(key)) map.put(key, value);
// But atomic for ConcurrentHashMap
```

### replaceAll

```java
// Transform all values in place
map.replaceAll((key, value) -> value.toUpperCase());
```

## Map.of and Map.ofEntries

```java
// Up to 10 entries
Map<String, Integer> small = Map.of("a", 1, "b", 2, "c", 3);

// More than 10 entries
Map<String, Integer> large = Map.ofEntries(
    Map.entry("a", 1),
    Map.entry("b", 2),
    Map.entry("c", 3)
    // ... any number of entries
);

// Copying a map to immutable
Map<String, Integer> immutable = Map.copyOf(mutableMap);
```

## Collecting into Maps

```java
// Simple key-value mapping
Map<String, User> byId = users.stream()
    .collect(Collectors.toMap(User::id, Function.identity()));

// Handle duplicate keys
Map<String, User> byEmail = users.stream()
    .collect(Collectors.toMap(
        User::email,
        Function.identity(),
        (existing, replacement) -> existing));  // keep first on collision

// Collect into specific map type
Map<String, User> ordered = users.stream()
    .collect(Collectors.toMap(
        User::id,
        Function.identity(),
        (a, b) -> a,
        LinkedHashMap::new));
```

## Anti-Patterns

```java
// Wrong — manual get-check-put
if (map.containsKey(key)) {
    map.put(key, map.get(key) + 1);
} else {
    map.put(key, 1);
}
// Correct: map.merge(key, 1, Integer::sum);

// Wrong — iterating to find a value
for (var entry : map.entrySet()) {
    if (entry.getValue().equals(target)) return entry.getKey();
}
// Consider: BiMap (Guava) or a reverse-lookup map if done frequently
```
