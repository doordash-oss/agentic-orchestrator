# Exception Groups (Python 3.11+)

## When to Use

Exception groups handle scenarios where multiple errors occur concurrently —
structured concurrency (`TaskGroup`), batch validation, or parallel operations.

## `ExceptionGroup` and `except*`

```python
# Creating an exception group
errors = ExceptionGroup("validation failed", [
    ValueError("name is required"),
    TypeError("age must be int"),
    ValueError("email is invalid"),
])
raise errors
```

Catching with `except*` — matches by type and splits the group:

```python
try:
    validate_all(data)
except* ValueError as eg:
    # eg is an ExceptionGroup containing only the ValueErrors
    for err in eg.exceptions:
        print(f"validation: {err}")
except* TypeError as eg:
    for err in eg.exceptions:
        print(f"type error: {err}")
```

Key difference from `except`: multiple `except*` clauses can match the same
`ExceptionGroup`, each receiving only their matching subset.

## With `asyncio.TaskGroup`

`TaskGroup` raises `ExceptionGroup` when tasks fail:

```python
import asyncio

async def main():
    try:
        async with asyncio.TaskGroup() as tg:
            tg.create_task(fetch_users())
            tg.create_task(fetch_orders())
            tg.create_task(fetch_inventory())
    except* ConnectionError as eg:
        print(f"{len(eg.exceptions)} connection failures")
    except* TimeoutError as eg:
        print(f"{len(eg.exceptions)} timeouts")
```

## `BaseExceptionGroup` vs `ExceptionGroup`

- `ExceptionGroup` — subclass of `Exception`, for normal errors
- `BaseExceptionGroup` — subclass of `BaseException`, for `KeyboardInterrupt`
  and other base exceptions

```python
# ExceptionGroup can only contain Exception subclasses
ExceptionGroup("errs", [ValueError("x")])          # ok
ExceptionGroup("errs", [KeyboardInterrupt()])       # TypeError!

# BaseExceptionGroup can contain anything
BaseExceptionGroup("errs", [KeyboardInterrupt()])   # ok
```

## Subgroup Filtering

```python
eg = ExceptionGroup("errs", [
    ValueError("a"),
    TypeError("b"),
    ValueError("c"),
])

# .subgroup() returns a new group with only matching exceptions
value_errors = eg.subgroup(ValueError)
# ExceptionGroup("errs", [ValueError("a"), ValueError("c")])

# .derive() creates a new group with the same message but different exceptions
```

## Adding Notes to Groups

```python
try:
    process_batch(items)
except* ValueError as eg:
    eg.add_note(f"batch contained {len(items)} items")
    raise
```

## Backwards Compatibility

For code that must support Python < 3.11, use the `exceptiongroup` backport:

```python
# pip install exceptiongroup
from exceptiongroup import ExceptionGroup, catch

# Or use try/except with isinstance checks instead of except*
try:
    validate_all(data)
except ExceptionGroup as eg:
    value_errors = eg.subgroup(ValueError)
    if value_errors:
        handle_validation(value_errors)
```
