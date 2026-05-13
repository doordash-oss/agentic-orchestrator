# Collections, itertools, and functools

## collections

### `defaultdict`

```python
from collections import defaultdict

# Group items by key
groups: defaultdict[str, list[int]] = defaultdict(list)
for item in items:
    groups[item.category].append(item.id)

# Count occurrences
counts: defaultdict[str, int] = defaultdict(int)
for word in words:
    counts[word] += 1

# Anti-pattern: manual key checking
groups = {}
for item in items:
    if item.category not in groups:
        groups[item.category] = []
    groups[item.category].append(item.id)
```

### `Counter`

```python
from collections import Counter

words = ["apple", "banana", "apple", "cherry", "banana", "apple"]
counts = Counter(words)
counts.most_common(2)        # [("apple", 3), ("banana", 2)]
counts["apple"]              # 3
counts["missing"]            # 0 (no KeyError)

# Arithmetic
counter_a = Counter(a=3, b=1)
counter_b = Counter(a=1, b=2)
counter_a + counter_b        # Counter(a=4, b=3)
counter_a - counter_b        # Counter(a=2)  — drops zero/negative
```

### `deque`

Double-ended queue with O(1) append/pop on both ends:

```python
from collections import deque

# Bounded buffer
recent = deque(maxlen=100)
recent.append(event)         # oldest auto-dropped at capacity

# BFS pattern
queue = deque([start_node])
while queue:
    node = queue.popleft()   # O(1) — list.pop(0) is O(n)
    queue.extend(node.children)
```

## itertools

### `chain` — Flatten Iterables

```python
from itertools import chain

all_items = list(chain(list_a, list_b, list_c))
# Equivalent to list_a + list_b + list_c but works with any iterable

# chain.from_iterable for a list of lists
nested = [[1, 2], [3, 4], [5, 6]]
flat = list(chain.from_iterable(nested))  # [1, 2, 3, 4, 5, 6]
```

### `islice` — Slice Any Iterator

```python
from itertools import islice

# Take first 10 items from any iterable (no indexing needed)
first_10 = list(islice(huge_generator(), 10))

# Skip and take
page = list(islice(results, 20, 30))   # items 20-29
```

### `groupby`

```python
from itertools import groupby
from operator import attrgetter

# Data MUST be sorted by the key first
users_sorted = sorted(users, key=attrgetter("role"))
for role, group in groupby(users_sorted, key=attrgetter("role")):
    print(f"{role}: {list(group)}")
```

### `product`, `combinations`, `permutations`

```python
from itertools import product, combinations

# Cartesian product
for x, y in product([1, 2], ["a", "b"]):
    print(x, y)   # (1,"a"), (1,"b"), (2,"a"), (2,"b")

# Combinations
for pair in combinations([1, 2, 3, 4], 2):
    print(pair)    # (1,2), (1,3), (1,4), (2,3), (2,4), (3,4)
```

## functools

### `lru_cache` / `cache`

```python
from functools import lru_cache, cache

@lru_cache(maxsize=256)
def expensive_lookup(key: str) -> dict:
    return database.query(key)

@cache           # unbounded cache (3.9+) — same as lru_cache(maxsize=None)
def fibonacci(n: int) -> int:
    if n < 2:
        return n
    return fibonacci(n - 1) + fibonacci(n - 2)
```

Arguments must be hashable. Don't cache functions with mutable args.

### `partial`

```python
from functools import partial

def power(base, exponent):
    return base ** exponent

square = partial(power, exponent=2)
cube = partial(power, exponent=3)

square(5)   # 25
cube(3)     # 27
```

### `singledispatch`

Function overloading based on the type of the first argument:

```python
from functools import singledispatch

@singledispatch
def serialize(obj) -> str:
    raise TypeError(f"unsupported type: {type(obj)}")

@serialize.register
def _(obj: str) -> str:
    return f'"{obj}"'

@serialize.register
def _(obj: int) -> str:
    return str(obj)

@serialize.register
def _(obj: list) -> str:
    return "[" + ", ".join(serialize(x) for x in obj) + "]"
```

## Comprehensions vs itertools

```python
# Comprehensions for simple transformations
squares = [x**2 for x in range(10)]
evens = {x for x in range(20) if x % 2 == 0}
lookup = {user.id: user for user in users}

# Generator expressions for lazy evaluation
total = sum(x**2 for x in range(1_000_000))  # no list in memory

# itertools for complex iteration patterns
# Don't write comprehensions that exceed one level of nesting
```
