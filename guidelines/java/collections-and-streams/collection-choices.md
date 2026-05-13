# Collection Choices

## Immutable Collections (Java 9+)

Prefer immutable collections by default. They are thread-safe, prevent
accidental modification, and document intent:

```java
// Immutable factory methods (Java 9+)
List<String> names = List.of("Alice", "Bob", "Carol");
Set<Integer> ids = Set.of(1, 2, 3);
Map<String, Integer> scores = Map.of("Alice", 95, "Bob", 87);

// For more than 10 entries in a Map
Map<String, Integer> large = Map.ofEntries(
    Map.entry("Alice", 95),
    Map.entry("Bob", 87),
    // ...
);

// From a stream — unmodifiable result
List<String> filtered = names.stream()
    .filter(n -> n.startsWith("A"))
    .toList();  // Java 16+ — returns unmodifiable list
```

**Key behavior**: `List.of()` / `Map.of()` / `Set.of()` throw
`UnsupportedOperationException` on any mutation attempt and
`NullPointerException` if any element is null.

## Choosing a List Implementation

| Implementation | When to Use |
|---------------|-------------|
| `ArrayList` | Default choice — O(1) random access, amortized O(1) add |
| `List.of(...)` | Immutable, known elements at construction |
| `List.copyOf(collection)` | Immutable copy of existing collection |
| `LinkedList` | Almost never — ArrayList beats it in practice for most workloads |
| `CopyOnWriteArrayList` | Concurrent reads, rare writes (listener lists) |

**Rule**: use `ArrayList` as the default mutable list. Use `List.of()` when
the contents are known upfront and won't change.

## Choosing a Map Implementation

| Implementation | When to Use |
|---------------|-------------|
| `HashMap` | Default choice — O(1) get/put, unordered |
| `LinkedHashMap` | Insertion-order iteration needed |
| `TreeMap` | Sorted keys (natural or custom comparator) |
| `EnumMap` | Keys are enum constants — fastest possible map for enums |
| `ConcurrentHashMap` | Thread-safe, high concurrency |
| `Map.of(...)` | Immutable, known entries at construction |

```java
// EnumMap — always use for enum keys
Map<DayOfWeek, List<Event>> schedule = new EnumMap<>(DayOfWeek.class);
```

## Choosing a Set Implementation

| Implementation | When to Use |
|---------------|-------------|
| `HashSet` | Default choice — O(1) contains/add |
| `LinkedHashSet` | Insertion-order iteration needed |
| `TreeSet` | Sorted elements |
| `EnumSet` | Elements are enum constants — bit-vector backed, very fast |
| `Set.of(...)` | Immutable, known elements |

```java
// EnumSet — always use for enum flags
Set<Permission> perms = EnumSet.of(Permission.READ, Permission.WRITE);
```

## Sequenced Collections (Java 21+)

New interfaces that add first/last access to ordered collections:

```java
// SequencedCollection — List, LinkedHashSet, etc.
SequencedCollection<String> seq = new LinkedHashSet<>(List.of("a", "b", "c"));
seq.getFirst();     // "a"
seq.getLast();      // "c"
seq.reversed();     // reversed view

// SequencedMap — LinkedHashMap, TreeMap, etc.
SequencedMap<String, Integer> map = new LinkedHashMap<>();
map.firstEntry();   // first inserted
map.lastEntry();    // last inserted
map.reversed();     // reversed view
```

## Defensive Copying

When accepting mutable collections from callers:

```java
// Defensive copy on construction
public Order(List<Item> items) {
    this.items = List.copyOf(items);  // immutable copy
}

// Defensive copy on return
public List<Item> getItems() {
    return Collections.unmodifiableList(items);  // or List.copyOf(items)
}
```

## Collection Size Hints

Pre-size collections when the size is known to avoid resizing:

```java
// Good — avoids resizing
var results = new ArrayList<Result>(items.size());
var lookup = new HashMap<String, Result>(items.size() * 4 / 3 + 1);  // load factor

// Also good — compute capacity for HashMap
var lookup = HashMap.<String, Result>newHashMap(items.size());  // Java 19+
```
