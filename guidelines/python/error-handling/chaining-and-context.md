# Exception Chaining and Context

## `raise ... from ...` (Explicit Chaining)

Use `from` to show the causal chain when wrapping exceptions:

```python
def load_user(user_id: int) -> User:
    try:
        row = db.query(f"SELECT * FROM users WHERE id = {user_id}")
    except DatabaseError as e:
        raise UserLoadError(f"failed to load user {user_id}") from e
```

The traceback shows both exceptions connected by
"The above exception was the direct cause of the following exception".

## `__cause__` vs `__context__`

- **`__cause__`** — set by `raise X from Y`. Explicit: "Y caused X".
- **`__context__`** — set automatically when an exception is raised inside an
  `except` block. Implicit: "while handling Y, X also happened".

```python
# Explicit cause (__cause__)
try:
    parse(data)
except ValueError as e:
    raise AppError("bad input") from e  # e is __cause__

# Implicit context (__context__)
try:
    parse(data)
except ValueError:
    raise AppError("bad input")         # ValueError is __context__
    # Traceback shows: "During handling of the above exception,
    # another exception occurred"
```

Always prefer explicit `from` — it makes the causal relationship clear.

## Suppressing Context with `from None`

When the original exception is an implementation detail:

```python
def get_setting(name: str) -> str:
    try:
        return _settings[name]
    except KeyError:
        # KeyError is an implementation detail — don't expose it
        raise ConfigError(f"unknown setting: {name}") from None
```

Without `from None`, the traceback shows the `KeyError` as context, which leaks
internal details.

## Re-raising Exceptions

```python
# Bare raise preserves the original traceback
try:
    process(data)
except ValueError:
    log_for_monitoring()
    raise                              # re-raises the exact same exception

# Anti-pattern: raise e re-creates the traceback from this point
try:
    process(data)
except ValueError as e:
    raise e                            # traceback starts here, not at origin
```

## Adding Context Without Changing Type

When you want to keep the exception type but add information:

```python
# Use .add_note() (Python 3.11+)
try:
    process_batch(items)
except ValueError as e:
    e.add_note(f"while processing batch of {len(items)} items")
    raise

# Pre-3.11: chain with the same type
try:
    validate(config)
except ValueError as e:
    raise ValueError(f"in config section 'database': {e}") from e
```

## Pattern: Converting External Exceptions

At API boundaries, convert third-party exceptions to your own:

```python
import httpx

class APIError(Exception):
    """Base for all API client errors."""

class APIConnectionError(APIError): ...
class APITimeoutError(APIError): ...

def fetch(url: str) -> bytes:
    try:
        resp = httpx.get(url, timeout=10)
        resp.raise_for_status()
        return resp.content
    except httpx.ConnectError as e:
        raise APIConnectionError(f"cannot reach {url}") from e
    except httpx.TimeoutException as e:
        raise APITimeoutError(f"timeout fetching {url}") from e
    except httpx.HTTPStatusError as e:
        raise APIError(f"HTTP {e.response.status_code} from {url}") from e
```

This isolates callers from your choice of HTTP library.
