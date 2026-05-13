# API Design and Pythonic Idioms

## Public vs Private API

```python
# Public: no underscore — part of stable contract
def calculate_discount(price: float, rate: float) -> float:
    """Return price reduced by the given rate (0.0–1.0)."""
    return price * (1 - _clamp(rate, 0.0, 1.0))

# Internal: single underscore — may change, not for external callers
def _clamp(value: float, lo: float, hi: float) -> float:
    return max(lo, min(hi, value))
```

Prefer `_single_underscore` over `__double_underscore` for private methods —
name mangling complicates testing and subclassing.

## `__all__` for Explicit Exports

```python
# mypackage/__init__.py
from .processor import DataProcessor, ProcessingError
from .client import HTTPClient

__all__ = [
    "DataProcessor",
    "ProcessingError",
    "HTTPClient",
]
```

Without `__all__`, `from mypackage import *` pulls in everything — making the
public API implicit and fragile.

## Deprecation

```python
import warnings

def fetch_data(
    url: str,
    *,
    retry_count: int | None = None,   # new name
    retries: int | None = None,       # deprecated alias
) -> Any:
    if retries is not None:
        warnings.warn(
            "The 'retries' argument is deprecated. Use 'retry_count' instead.",
            DeprecationWarning,
            stacklevel=2,             # points warning at the caller
        )
        retry_count = retries
    ...
```

Always use `stacklevel=2` so the warning points at the caller, not the function.

## EAFP vs LBYL

Prefer EAFP (Easier to Ask Forgiveness than Permission) when success is the
common case, and mandatory for race-condition-sensitive paths:

```python
# EAFP — preferred (no TOCTOU race)
try:
    with open(path) as f:
        content = f.read()
except FileNotFoundError:
    content = ""

# LBYL — has a race: file can be deleted between check and open
if os.path.exists(path):
    with open(path) as f:
        content = f.read()

# For dicts, use .get() instead of either pattern
timeout = config.get("timeout", 60)
```

## Truthiness

```python
# Pythonic: rely on truth table
items = []
if not items:          # empty list is falsy
    print("nothing")

# Use `is None` for explicit None checks
if value is None: ...

# Anti-patterns
if len(items) == 0: ...   # verbose
if items == []: ...        # fragile type comparison
if value == None: ...      # can misbehave with custom __eq__
```

## `any()` and `all()`

```python
# any() — True if at least one element is truthy (short-circuits)
if any(v > 3 for v in values):
    print("found a large value")

# all() — True if every element is truthy (short-circuits)
if all(x > 0 for x in data):
    print("all positive")
```

## Unpacking

```python
first, *rest = [1, 2, 3, 4, 5]      # first=1, rest=[2,3,4,5]
*init, last = [1, 2, 3, 4, 5]       # init=[1,2,3,4], last=5
a, b = b, a                          # swap without temp

for number, letter in pairs:         # tuple unpacking in loops
    print(f"{number}: {letter}")
```

## Walrus Operator (`:=`)

Assign and test in one expression — useful when you need the matched value:

```python
import re
if m := re.search(r"Error: (.+)", line):
    print(f"Found error: {m.group(1)}")

# Classic use: read loops
while chunk := sys.stdin.read(8192):
    process(chunk)
```

Avoid for trivial assignments where it hurts readability.

## String Formatting

```python
# f-strings — preferred (Python 3.6+)
message = f"Player {name} scored {score:.1f} points"

# str.format() — acceptable when template is a variable
template = "Player {} scored {} points"
message = template.format(name, score)

# Never use % formatting in new code
```
