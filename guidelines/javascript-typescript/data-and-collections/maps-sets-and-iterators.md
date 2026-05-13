# Maps, Sets, and Iterators

## Map

`Map` is the correct choice for dynamic key-value collections — not plain objects:

```typescript
const cache = new Map<string, User>();
cache.set("user:1", alice);
cache.get("user:1");     // User | undefined
cache.has("user:1");     // true
cache.delete("user:1");
cache.size;              // number of entries
```

### Map vs Plain Object

| Feature | `Map` | Plain Object |
|---------|-------|-------------|
| Key types | Any value (objects, functions, etc.) | Strings and symbols only |
| Key order | Insertion order guaranteed | Mostly insertion order, but numeric keys sort first |
| Size | `map.size` (O(1)) | `Object.keys(obj).length` (O(n)) |
| Iteration | Native (for...of, forEach) | `Object.entries()` |
| Prototype pollution | Immune | Vulnerable (inherits from Object.prototype) |
| Serialization | Manual (no `JSON.stringify`) | Native |
| Performance | Better for frequent add/delete | Better for static shape, engine-optimized |

**Rule**: use `Map` for dynamic collections with frequent changes. Use plain
objects for static shape (configs, records with known keys).

### Common Map Patterns

```typescript
// Initialize from entries:
const map = new Map([
  ["key1", "value1"],
  ["key2", "value2"],
]);

// Convert to/from object:
const obj = Object.fromEntries(map);
const map2 = new Map(Object.entries(obj));

// Iterate:
for (const [key, value] of map) { /* ... */ }
```

## Set

`Set` stores unique values with O(1) membership testing:

```typescript
const tags = new Set<string>();
tags.add("javascript");
tags.add("typescript");
tags.add("javascript"); // no-op, already exists
tags.has("javascript"); // true
tags.size;              // 2
```

### Set Operations (ES2025)

```typescript
const a = new Set([1, 2, 3, 4]);
const b = new Set([3, 4, 5, 6]);

a.union(b);              // Set {1, 2, 3, 4, 5, 6}
a.intersection(b);       // Set {3, 4}
a.difference(b);         // Set {1, 2}
a.symmetricDifference(b); // Set {1, 2, 5, 6}
a.isSubsetOf(b);         // false
a.isSupersetOf(b);       // false
a.isDisjointFrom(b);     // false
```

### Deduplicate Arrays

```typescript
const unique = [...new Set(items)];
```

## WeakMap and WeakRef

### WeakMap

Keys are held weakly — garbage-collected when no other references exist:

```typescript
const metadata = new WeakMap<object, Metadata>();

function attachMeta(obj: object, meta: Metadata) {
  metadata.set(obj, meta);
}

// When obj is garbage-collected, the metadata entry is automatically removed
```

Use cases: private data storage, caching associated data without preventing GC.

**Limitations**: not iterable, no `.size`, keys must be objects.

### WeakRef

Holds a weak reference to an object:

```typescript
const ref = new WeakRef(heavyObject);

// Later:
const obj = ref.deref(); // object or undefined (if GC'd)
if (obj) {
  // still alive, use it
}
```

Use sparingly — primarily for caches where re-creation is cheap.

## Iteration Protocols

### Symbol.iterator

Make any object iterable:

```typescript
class Range {
  constructor(
    private start: number,
    private end: number,
  ) {}

  [Symbol.iterator](): Iterator<number> {
    let current = this.start;
    const end = this.end;
    return {
      next(): IteratorResult<number> {
        if (current <= end) {
          return { value: current++, done: false };
        }
        return { value: undefined, done: true };
      },
    };
  }
}

for (const n of new Range(1, 5)) {
  console.log(n); // 1, 2, 3, 4, 5
}
```

### Generators

Generators are the easiest way to create iterables:

```typescript
function* range(start: number, end: number): Generator<number> {
  for (let i = start; i <= end; i++) {
    yield i;
  }
}

function* fibonacci(): Generator<number> {
  let [a, b] = [0, 1];
  while (true) {
    yield a;
    [a, b] = [b, a + b];
  }
}

// Take first 10 Fibonacci numbers:
function* take<T>(iterable: Iterable<T>, n: number): Generator<T> {
  let count = 0;
  for (const item of iterable) {
    if (count++ >= n) return;
    yield item;
  }
}

const first10 = [...take(fibonacci(), 10)];
```

### Iterator Helpers (ES2025)

```typescript
// Chain operations on iterators without intermediate arrays:
const result = Iterator.from(fibonacci())
  .filter((n) => n % 2 === 0)
  .map((n) => n * 2)
  .take(5)
  .toArray();
```

## Best Practices

- **Use `Map` for dynamic key-value data** — not plain objects
- **Use `Set` for unique collections** — O(1) membership testing
- **Use `WeakMap` for associated metadata** — prevents memory leaks
- **Use generators for lazy sequences** — avoid materializing large arrays
- **Use Set operations** (`.union`, `.intersection`) instead of manual array logic
- **Prefer `for...of`** for iterating Maps and Sets — destructure in the loop
