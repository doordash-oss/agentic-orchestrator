# Dataclasses and attrs

## dataclasses (stdlib)

```python
from dataclasses import dataclass, field

@dataclass
class User:
    name: str
    age: int
    tags: list[str] = field(default_factory=list)  # mutable default
```

Automatically generates `__init__`, `__repr__`, and `__eq__`.

### Common Options

```python
@dataclass(frozen=True)       # immutable — generates __hash__
class Point:
    x: float
    y: float

@dataclass(slots=True)        # uses __slots__ — less memory, faster access
class Event:
    name: str
    timestamp: float

@dataclass(kw_only=True)      # all fields are keyword-only (3.10+)
class Config:
    host: str
    port: int = 8080
```

### `__post_init__`

Run validation or derived field computation after `__init__`:

```python
@dataclass
class DateRange:
    start: date
    end: date

    def __post_init__(self):
        if self.end < self.start:
            raise ValueError(f"end {self.end} is before start {self.start}")
```

### `field()` Options

```python
from dataclasses import dataclass, field

@dataclass
class Request:
    url: str
    headers: dict[str, str] = field(default_factory=dict)
    _internal: str = field(default="", repr=False, compare=False)
    id: str = field(default_factory=lambda: str(uuid4()))
```

### Serialization

```python
from dataclasses import asdict, astuple

user = User("Alice", 30, ["admin"])
asdict(user)    # {"name": "Alice", "age": 30, "tags": ["admin"]}
astuple(user)   # ("Alice", 30, ["admin"])
```

Note: `asdict` recursively converts nested dataclasses — this can be slow
for deep structures.

## attrs

Use attrs when you need validators, converters, or features beyond stdlib
dataclasses:

```python
import attrs

@attrs.define                # modern API (replaces @attr.s)
class User:
    name: str
    age: int = attrs.field(validator=attrs.validators.gt(0))
    email: str = attrs.field(converter=str.lower)

user = User("Alice", 30, "Alice@Example.COM")
user.email   # "alice@example.com"
user.age     # 30

User("Bob", -1, "bob@x.com")  # raises ValueError
```

### Validators and Converters

```python
@attrs.define
class Config:
    port: int = attrs.field(
        validator=[
            attrs.validators.instance_of(int),
            attrs.validators.ge(1),
            attrs.validators.le(65535),
        ]
    )
    host: str = attrs.field(
        converter=str.strip,
        validator=attrs.validators.min_len(1),
    )
```

### When to Use attrs vs dataclasses

| Feature | dataclass | attrs |
|---------|-----------|-------|
| Stdlib | Yes | No (pip install) |
| Validators | Manual (`__post_init__`) | Built-in |
| Converters | Manual | Built-in |
| Slots | `slots=True` (3.10+) | Default |
| Frozen | `frozen=True` | `@attrs.define(frozen=True)` |

**Rule**: Start with `dataclass`. Switch to `attrs` when you need validators,
converters, or its other advanced features.

## Anti-Patterns

```python
# Don't use mutable defaults directly
@dataclass
class Bad:
    items: list[str] = []        # BUG: shared between instances!

# Correct
@dataclass
class Good:
    items: list[str] = field(default_factory=list)

# Don't use a regular class when dataclass works
class ManualUser:                # verbose, error-prone
    def __init__(self, name, age):
        self.name = name
        self.age = age
    def __eq__(self, other): ...
    def __repr__(self): ...
```
