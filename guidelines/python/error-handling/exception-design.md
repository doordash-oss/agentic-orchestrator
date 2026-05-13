# Exception Design

## Use Specific Exceptions

```python
# Correct — tells the caller exactly what went wrong
def parse_age(value: str) -> int:
    try:
        age = int(value)
    except ValueError:
        raise ValueError(f"invalid age: {value!r}")
    if age < 0:
        raise ValueError("age must be non-negative")
    return age

# Anti-pattern — broad catch silences bugs
try:
    result = complex_operation()
except Exception:        # catches TypeError, AttributeError, etc.
    result = None        # bugs are now invisible
```

Never use bare `except:` — it catches `KeyboardInterrupt` and `SystemExit`:

```python
# Dangerous — prevents Ctrl+C from working
try:
    process()
except:         # catches KeyboardInterrupt, SystemExit, GeneratorExit
    pass

# Minimum acceptable broad catch
try:
    process()
except Exception:   # skips KeyboardInterrupt, SystemExit
    log.error("unexpected error", exc_info=True)
```

## Custom Exception Hierarchies

For libraries, create a base exception and derive specific ones:

```python
class AppError(Exception):
    """Base exception for this application."""

class ConfigError(AppError):
    """Invalid or missing configuration."""

class AuthError(AppError):
    """Authentication or authorization failure."""

class NotFoundError(AppError):
    """Requested resource does not exist."""
    def __init__(self, resource: str, id: str):
        self.resource = resource
        self.id = id
        super().__init__(f"{resource} not found: {id}")
```

This lets callers catch broadly (`except AppError`) or narrowly
(`except NotFoundError`).

## Raising with Informative Messages

```python
# Include the failing value in the message
def connect(host: str, port: int) -> Connection:
    if not 1 <= port <= 65535:
        raise ValueError(f"port must be 1-65535, got {port}")
    ...

# Include relevant context for debugging
def load_config(path: Path) -> Config:
    if not path.exists():
        raise FileNotFoundError(f"config file not found: {path}")
    ...
```

## Handle or Propagate, Never Both

```python
# Anti-pattern — logs AND propagates (duplicates the error in logs)
try:
    result = fetch_data(url)
except HTTPError as e:
    logger.error(f"fetch failed: {e}")
    raise                              # caller may also log it

# Correct — either handle it fully...
try:
    result = fetch_data(url)
except HTTPError:
    result = cached_fallback()         # handled, no re-raise

# ...or propagate it (optionally with wrapping)
try:
    result = fetch_data(url)
except HTTPError as e:
    raise DataFetchError(url) from e   # wrap and propagate
```

## Catching Multiple Exception Types

```python
# Tuple of exceptions in one except clause
try:
    value = mapping[key]
except (KeyError, IndexError) as e:
    raise LookupError(f"key not found: {key}") from e

# Separate handlers when recovery differs
try:
    data = parse(raw)
except json.JSONDecodeError:
    data = parse_legacy_format(raw)
except UnicodeDecodeError:
    data = parse_binary_format(raw)
```

## The `else` Clause

Code in `else` runs only when no exception was raised — keep success-only logic
out of the `try` block to avoid accidentally catching its exceptions:

```python
try:
    conn = connect(host)
except ConnectionError:
    conn = fallback_connect()
else:
    # Only runs if connect() succeeded — exceptions here propagate normally
    conn.send_handshake()
```
