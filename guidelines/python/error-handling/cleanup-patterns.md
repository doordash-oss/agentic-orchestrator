# Cleanup Patterns

## Context Managers (`with`)

Always use `with` for resource management — never rely on manual cleanup:

```python
# Correct — file is closed even if an exception occurs
with open("data.csv") as f:
    content = f.read()

# Anti-pattern — file stays open if exception occurs before close()
f = open("data.csv")
content = f.read()
f.close()
```

## Writing Custom Context Managers

### Using `contextlib.contextmanager`

For simple cases, a generator-based context manager is most readable:

```python
from contextlib import contextmanager
import time

@contextmanager
def timer(label: str):
    start = time.perf_counter()
    try:
        yield
    finally:
        elapsed = time.perf_counter() - start
        print(f"{label}: {elapsed:.3f}s")

with timer("database query"):
    results = db.execute(query)
```

### Using a Class

For complex lifecycle or reusable resources:

```python
class DatabaseTransaction:
    def __init__(self, conn: Connection):
        self.conn = conn

    def __enter__(self) -> Connection:
        self.conn.begin()
        return self.conn

    def __exit__(self, exc_type, exc_val, exc_tb) -> bool:
        if exc_type is None:
            self.conn.commit()
        else:
            self.conn.rollback()
        return False   # do not suppress the exception
```

Return `True` from `__exit__` only when you intentionally suppress an exception.

## `contextlib.suppress`

Replace empty `except` blocks:

```python
from contextlib import suppress

# Pythonic
with suppress(FileNotFoundError):
    os.remove(temp_file)

# Equivalent but verbose
try:
    os.remove(temp_file)
except FileNotFoundError:
    pass
```

## `contextlib.ExitStack`

Manage a dynamic number of context managers:

```python
from contextlib import ExitStack

def process_files(paths: list[Path]) -> list[str]:
    with ExitStack() as stack:
        files = [stack.enter_context(open(p)) for p in paths]
        return [f.read() for f in files]
    # All files are closed when the with block exits
```

Also useful for registering arbitrary cleanup callbacks:

```python
with ExitStack() as stack:
    conn = create_connection()
    stack.callback(conn.close)     # called on exit, even if exception
    stack.callback(cleanup_temp, temp_dir)
    ...
```

## `try/except/else/finally`

```python
try:
    conn = connect(host)           # code that might raise
except ConnectionError:
    conn = fallback_connect()      # handle the error
else:
    conn.send_handshake()          # only if try succeeded
finally:
    log_attempt(host)              # always runs (cleanup/logging)
```

Guidelines:
- **`try`** — keep it narrow; only the code that might raise the expected exception
- **`except`** — handle the error or convert it
- **`else`** — success-only code; exceptions here propagate normally
- **`finally`** — unconditional cleanup; runs even after `return` or `raise`

## Async Context Managers

```python
from contextlib import asynccontextmanager

@asynccontextmanager
async def managed_session():
    session = await create_session()
    try:
        yield session
    finally:
        await session.close()

async with managed_session() as session:
    await session.execute(query)
```
