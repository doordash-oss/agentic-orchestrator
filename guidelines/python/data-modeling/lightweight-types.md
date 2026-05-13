# Lightweight Types

## NamedTuple

Immutable records with tuple unpacking:

```python
from typing import NamedTuple

class Point(NamedTuple):
    x: float
    y: float

p = Point(1.0, 2.0)
p.x              # 1.0
x, y = p         # tuple unpacking
p[0]             # 1.0 (indexable)
hash(p)          # hashable — usable as dict key
```

Use when:
- You need immutability
- You need tuple unpacking or indexing
- You need hashability without extra work

Don't use when you need methods, validation, or mutability.

## TypedDict

Type annotations for dict-shaped data — useful for JSON responses:

```python
from typing import TypedDict, NotRequired

class UserResponse(TypedDict):
    id: int
    name: str
    email: str
    avatar_url: NotRequired[str]   # optional key

def process_user(data: UserResponse) -> str:
    return data["name"]            # type checker knows this is str
```

### `total=False`

```python
class Filters(TypedDict, total=False):
    # All keys are optional
    name: str
    min_age: int
    max_age: int

filters: Filters = {"name": "Alice"}   # ok — other keys omitted
```

TypedDict is purely a type-checking construct — at runtime it's just a regular
dict with no validation.

## Enum

```python
from enum import Enum, auto

class Status(Enum):
    PENDING = auto()
    ACTIVE = auto()
    SUSPENDED = auto()

status = Status.ACTIVE
status.name      # "ACTIVE"
status.value     # 2

# Use in match statements (3.10+)
match status:
    case Status.ACTIVE:
        allow_access()
    case Status.SUSPENDED:
        deny_access()
```

### StrEnum (Python 3.11+)

```python
from enum import StrEnum

class Color(StrEnum):
    RED = "red"
    GREEN = "green"
    BLUE = "blue"

Color.RED == "red"    # True — StrEnum values are strings
f"color: {Color.RED}" # "color: red"
```

### IntEnum and Flag

```python
from enum import IntEnum, Flag, auto

class Priority(IntEnum):
    LOW = 1
    MEDIUM = 2
    HIGH = 3

Priority.HIGH > Priority.LOW   # True — comparable

class Permission(Flag):
    READ = auto()
    WRITE = auto()
    EXECUTE = auto()

perms = Permission.READ | Permission.WRITE
Permission.READ in perms       # True
```

### Anti-Patterns

```python
# Don't use magic strings — use Enum
if status == "active":       # typo-prone, no autocomplete
    ...

# Don't use magic numbers
if priority > 2:             # what does 2 mean?
    ...

# Correct
if status == Status.ACTIVE:  # type-safe, discoverable
    ...
```

## Choosing the Right Tool

| Need | Use |
|------|-----|
| Mutable data container | `@dataclass` |
| Immutable data container | `@dataclass(frozen=True)` |
| Validation at boundaries | `pydantic.BaseModel` |
| Immutable with tuple unpacking | `NamedTuple` |
| Type dicts (JSON, kwargs) | `TypedDict` |
| Fixed set of values | `Enum` / `StrEnum` |
| Validators and converters | `attrs` |

Start with the simplest option that meets your needs:
1. `NamedTuple` or `TypedDict` for lightweight, typed data
2. `@dataclass` for most data containers
3. `pydantic` for external data validation
4. `attrs` for advanced features
