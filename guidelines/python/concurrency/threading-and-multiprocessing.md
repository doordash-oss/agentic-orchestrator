# Threading and Multiprocessing

## The GIL (Global Interpreter Lock)

CPython's GIL allows only one thread to execute Python bytecode at a time.
This means:

- **I/O-bound threads** work fine — the GIL is released during I/O operations
- **CPU-bound threads** don't achieve parallelism — use multiprocessing instead

Python 3.13+ introduces an experimental free-threaded mode (`--disable-gil`).

## When to Use What

| Workload | Tool | Why |
|----------|------|-----|
| HTTP calls, DB queries | `asyncio` | Non-blocking, scales to thousands |
| Blocking I/O (file ops, C libs) | `ThreadPoolExecutor` | Releases GIL during I/O |
| CPU-heavy computation | `ProcessPoolExecutor` | Bypasses GIL with separate processes |
| Simple parallelism | `asyncio.to_thread()` | Bridge sync code into async context |

## ThreadPoolExecutor

```python
from concurrent.futures import ThreadPoolExecutor

def fetch_sync(url: str) -> bytes:
    return requests.get(url).content

with ThreadPoolExecutor(max_workers=10) as executor:
    futures = [executor.submit(fetch_sync, url) for url in urls]
    results = [f.result() for f in futures]
```

## Mixing Sync and Async

### Calling Blocking Code from Async

```python
import asyncio

def cpu_intensive(data: bytes) -> bytes:
    """Blocking function — runs in a thread."""
    return heavy_computation(data)

async def handler(data: bytes) -> bytes:
    # Runs cpu_intensive in a thread, doesn't block the event loop
    return await asyncio.to_thread(cpu_intensive, data)
```

### Calling Async Code from Sync

```python
# At the top level — use asyncio.run()
result = asyncio.run(async_function())

# Inside an already-running loop (e.g., Jupyter) — use nest_asyncio or
# run in a separate thread
import asyncio
from concurrent.futures import Future

def run_async_in_thread(coro) -> Future:
    """Run a coroutine in a background thread with its own event loop."""
    import threading
    result_future: Future = Future()

    def runner():
        try:
            result = asyncio.run(coro)
            result_future.set_result(result)
        except Exception as e:
            result_future.set_exception(e)

    threading.Thread(target=runner, daemon=True).start()
    return result_future
```

## ProcessPoolExecutor

For CPU-bound work that needs real parallelism:

```python
from concurrent.futures import ProcessPoolExecutor
import multiprocessing

def process_chunk(chunk: list[int]) -> int:
    return sum(x * x for x in chunk)   # CPU-bound

with ProcessPoolExecutor(max_workers=multiprocessing.cpu_count()) as executor:
    chunks = [data[i:i+1000] for i in range(0, len(data), 1000)]
    results = list(executor.map(process_chunk, chunks))
    total = sum(results)
```

Caveats:
- Arguments and results must be picklable
- Process startup is slower than thread startup
- Shared state requires `multiprocessing.Queue` or `multiprocessing.Manager`

## Thread Safety

```python
import threading

# Use Lock for simple mutual exclusion
lock = threading.Lock()
counter = 0

def increment():
    global counter
    with lock:               # context manager ensures release
        counter += 1

# Use RLock when the same thread needs to acquire multiple times
rlock = threading.RLock()

# Use Event for signaling between threads
stop_event = threading.Event()

def worker():
    while not stop_event.is_set():
        do_work()
        stop_event.wait(timeout=1.0)   # check every second
```

## Anti-Patterns

```python
# Don't create threads inside async code
async def bad():
    threading.Thread(target=blocking_fn).start()  # unmanaged

# Use asyncio.to_thread instead
async def good():
    await asyncio.to_thread(blocking_fn)

# Don't share mutable state between processes without synchronization
# Use queues or pipes instead of shared variables
```
