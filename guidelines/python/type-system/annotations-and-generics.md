# Annotations and Generics

## Function Signatures

Type-annotate all public functions. Return type is mandatory:

```python
def greet(name: str) -> str:
    return f"Hello, {name}!"

def process(items: list[str], limit: int = 10) -> dict[str, int]:
    ...

# Functions that return None should annotate it explicitly
def log_event(event: str) -> None:
    print(event)
```

## Variable Annotations

```python
# Simple annotations
name: str = "Alice"
count: int = 0
items: list[str] = []

# Annotation without assignment (for instance attributes in __init__)
class User:
    name: str
    age: int

    def __init__(self, name: str, age: int) -> None:
        self.name = name
        self.age = age
```

## Built-in Generics (Python 3.9+)

Use built-in types directly — no imports needed:

```python
# Correct (3.9+)
def process(items: list[str]) -> dict[str, int]: ...
def get_pair() -> tuple[str, int]: ...
def get_unique(items: list[str]) -> set[str]: ...
def maybe_value() -> str | None: ...           # 3.10+

# Legacy (avoid in new code)
from typing import List, Dict, Tuple, Set, Optional, Union
def process(items: List[str]) -> Dict[str, int]: ...
```

For code that must support Python 3.8, use `from __future__ import annotations`
to enable the new syntax as string annotations.

## Union Types

```python
# Python 3.10+ — use | syntax
def parse(value: str | int) -> float: ...
def find_user(id: int) -> User | None: ...

# Pre-3.10 with __future__
from __future__ import annotations
def find_user(id: int) -> User | None: ...

# Legacy (avoid)
from typing import Optional, Union
def find_user(id: int) -> Optional[User]: ...   # same as Union[User, None]
```

`Optional[X]` is just `Union[X, None]` — prefer `X | None` for clarity.

## TypeVar

For functions generic over a type:

```python
from typing import TypeVar

T = TypeVar("T")

def first(items: list[T]) -> T:
    return items[0]

# Bounded TypeVar — restricts to subclasses
Numeric = TypeVar("Numeric", bound=int | float)

def double(x: Numeric) -> Numeric:
    return x * 2

# Constrained TypeVar — exactly one of the listed types
StrOrBytes = TypeVar("StrOrBytes", str, bytes)

def concat(a: StrOrBytes, b: StrOrBytes) -> StrOrBytes:
    return a + b
```

## TypeAlias

Name complex types for readability:

```python
from typing import TypeAlias

# Simple alias
Coordinates: TypeAlias = tuple[float, float]
Headers: TypeAlias = dict[str, str]
Callback: TypeAlias = Callable[[str, int], bool]

# Use in signatures
def distance(a: Coordinates, b: Coordinates) -> float: ...
```

Python 3.12+ introduces the `type` statement:
```python
type Coordinates = tuple[float, float]
type Headers = dict[str, str]
```

## Callable Types

```python
from collections.abc import Callable

# Function that takes (str, int) and returns bool
def apply(fn: Callable[[str, int], bool], data: str) -> bool:
    return fn(data, 0)

# Callable with no args
def run(fn: Callable[[], None]) -> None:
    fn()

# Callable with *args/**kwargs — use ParamSpec (see advanced guide)
```

## `Final` and Constants

```python
from typing import Final

MAX_RETRIES: Final = 3
API_URL: Final[str] = "https://api.example.com"

# Type checker flags reassignment
MAX_RETRIES = 5   # error: Cannot assign to final name "MAX_RETRIES"
```

## `Literal` Types

```python
from typing import Literal

def set_mode(mode: Literal["read", "write", "append"]) -> None: ...

# Type checker catches invalid values
set_mode("read")     # ok
set_mode("delete")   # error: Argument of type "delete" cannot be assigned
```
