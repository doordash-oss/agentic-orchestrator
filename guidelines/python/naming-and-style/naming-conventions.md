# Naming Conventions

## PEP 8 Naming Rules

```python
# Variables and functions — snake_case
user_name = "Alice"
max_retry_count = 3

def calculate_total_price(items: list[float]) -> float:
    return sum(items)

# Classes — PascalCase (CapWords)
class UserManager: ...
class HTTPClient: ...              # acronyms stay fully capitalized
class DataParseError(Exception): ... # exceptions must end with Error

# Constants — UPPER_CASE (module-level)
MAX_RETRIES = 3
DATABASE_URL = "postgresql://localhost/mydb"
_INTERNAL_TIMEOUT = 30             # private constant
```

Anti-patterns:
```python
userName = "Alice"           # camelCase — not Pythonic
class user_manager: ...      # snake_case for classes
maxRetries = 3               # looks like a mutable variable
```

## Underscore Conventions

```python
# Single leading underscore: internal / non-public
def _validate_token(token: str) -> bool:
    """Not part of the public API."""
    ...

# Double leading underscore: name mangling (use sparingly)
class Config:
    def __init__(self):
        self.__secret_key = "..."  # becomes _Config__secret_key

# Trailing underscore: avoid collision with builtins
class_ = "Physics 101"
type_ = "integer"
```

Never invent custom dunder names — `__custom_magic__` is reserved for Python
internals. Prefer `_do_work` over `__do_work` for private methods (name mangling
complicates testing and subclassing).

## Module and Package Naming

```
# Correct
mypackage/
    __init__.py
    data_utils.py
    http_client.py

# Wrong
MyPackage/          # no uppercase in package names
my-package/         # hyphens break import syntax
dataUtils.py        # camelCase for modules
```

Keep `__init__.py` minimal and explicit:
```python
# mypackage/__init__.py
from .data_utils import DataProcessor, parse_record
from .http_client import HTTPClient

__all__ = ["DataProcessor", "parse_record", "HTTPClient"]
```

## Dunder Methods

### `__repr__` vs `__str__`

Always implement `__repr__`; add `__str__` only when display should differ:

```python
class Point:
    def __init__(self, x: float, y: float):
        self.x = x
        self.y = y

    def __repr__(self) -> str:
        # Unambiguous; ideally eval-able
        return f"Point(x={self.x!r}, y={self.y!r})"

    def __str__(self) -> str:
        # Human-readable display
        return f"({self.x}, {self.y})"
```

Without `__repr__`, the REPL shows `<__main__.Point object at 0x...>` — useless
for debugging.

### `__eq__` and `__hash__`

Defining `__eq__` without `__hash__` makes objects unhashable:

```python
# Correct: use frozen dataclass (handles both automatically)
from dataclasses import dataclass

@dataclass(frozen=True)
class Point:
    x: float
    y: float

# Manual: always define both
class Record:
    def __init__(self, id: int, name: str):
        self.id = id
        self.name = name

    def __eq__(self, other: object) -> bool:
        if not isinstance(other, Record):
            return NotImplemented      # let Python try other.__eq__
        return self.id == other.id

    def __hash__(self) -> int:
        return hash(self.id)           # must be consistent with __eq__
```

## Docstrings (PEP 257)

One-line docstrings — imperative mood, ends with period:
```python
def add(a: float, b: float) -> float:
    """Return the sum of a and b."""
    return a + b
```

Multi-line docstrings — Google style recommended:
```python
def fetch_user(user_id: int, require_active: bool = False) -> dict[str, object]:
    """Fetch a single user record by ID.

    Queries the primary database and optionally filters out
    inactive accounts.

    Args:
        user_id: The integer primary key of the user.
        require_active: When True, raise if the user is inactive.

    Returns:
        A dictionary with keys 'id', 'name', 'email', and 'active'.

    Raises:
        UserNotFoundError: If no user with user_id exists.
    """
```

Skip docstrings when they merely restate the signature — add them when behavior
is non-obvious, exceptions can be raised, or the function is part of the public API.
