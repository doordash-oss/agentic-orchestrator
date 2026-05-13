# Protocols and Advanced Typing

## Protocol (Structural Subtyping)

Define interfaces by the methods an object must have — no inheritance required:

```python
from typing import Protocol

class Writable(Protocol):
    def write(self, data: bytes) -> int: ...

# Any class with a write(bytes) -> int method satisfies Writable
# No need to inherit from Writable
class FileWriter:
    def write(self, data: bytes) -> int:
        ...

def save(target: Writable, content: bytes) -> None:
    target.write(content)
```

### When to Use Protocol vs ABC

- **Protocol** — when you want structural ("duck") typing. The implementor
  doesn't need to know about your protocol.
- **ABC** — when you need runtime `isinstance` checks, or when the base class
  provides shared implementation.

### `runtime_checkable`

```python
from typing import Protocol, runtime_checkable

@runtime_checkable
class Closeable(Protocol):
    def close(self) -> None: ...

# Now isinstance works (checks method existence, not signatures)
assert isinstance(open("f"), Closeable)
```

Use sparingly — `runtime_checkable` only checks method names exist, not their
signatures.

## `@overload`

Define different return types based on input types:

```python
from typing import overload

@overload
def get(key: str, default: None = None) -> str | None: ...
@overload
def get(key: str, default: str) -> str: ...

def get(key: str, default: str | None = None) -> str | None:
    value = _store.get(key)
    return value if value is not None else default
```

The `@overload` signatures are for the type checker only — the actual
implementation is the non-decorated version.

## `TypeGuard` and `TypeIs`

Narrow types in conditional checks:

```python
from typing import TypeGuard

def is_string_list(val: list[object]) -> TypeGuard[list[str]]:
    return all(isinstance(x, str) for x in val)

def process(items: list[object]) -> None:
    if is_string_list(items):
        # Type checker knows items is list[str] here
        print(", ".join(items))
```

Python 3.13 adds `TypeIs` — narrower and more precise:
```python
from typing import TypeIs

def is_int(val: int | str) -> TypeIs[int]:
    return isinstance(val, int)
```

## `ParamSpec` (Decorator Typing)

Preserve the signature of wrapped functions:

```python
from typing import ParamSpec, TypeVar
from functools import wraps

P = ParamSpec("P")
R = TypeVar("R")

def retry(fn: Callable[P, R]) -> Callable[P, R]:
    @wraps(fn)
    def wrapper(*args: P.args, **kwargs: P.kwargs) -> R:
        for attempt in range(3):
            try:
                return fn(*args, **kwargs)
            except Exception:
                if attempt == 2:
                    raise
    return wrapper

@retry
def fetch(url: str, timeout: int = 30) -> bytes: ...

# Type checker preserves the original signature of fetch
```

## New-Style Generics (Python 3.12+)

PEP 695 introduces cleaner syntax:

```python
# Old style
from typing import TypeVar
T = TypeVar("T")
def first(items: list[T]) -> T: ...

# New style (3.12+)
def first[T](items: list[T]) -> T: ...

# Generic classes
class Stack[T]:
    def __init__(self) -> None:
        self._items: list[T] = []

    def push(self, item: T) -> None:
        self._items.append(item)

    def pop(self) -> T:
        return self._items.pop()

# Bounded generics
def largest[T: (int, float)](items: list[T]) -> T:
    return max(items)
```

## `TypeVarTuple` (Variadic Generics)

For functions generic over a variable number of types:

```python
from typing import TypeVarTuple, Unpack

Ts = TypeVarTuple("Ts")

def first_element(*args: Unpack[Ts]) -> tuple[Unpack[Ts]]:
    return args
```

Use cases include tensor shape typing and generic tuple manipulation.
