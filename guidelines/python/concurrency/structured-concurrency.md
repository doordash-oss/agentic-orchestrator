# Structured Concurrency

## TaskGroup (Python 3.11+)

`TaskGroup` ensures all spawned tasks complete (or are cancelled) before the
`async with` block exits — no orphaned tasks:

```python
async def fetch_all(urls: list[str]) -> list[bytes]:
    results: list[bytes] = []

    async with asyncio.TaskGroup() as tg:
        for url in urls:
            tg.create_task(fetch(url))

    # All tasks are guaranteed complete here
    return results
```

If any task raises, the remaining tasks are cancelled and the group raises
an `ExceptionGroup`:

```python
try:
    async with asyncio.TaskGroup() as tg:
        tg.create_task(fetch_users())
        tg.create_task(fetch_orders())
except* ConnectionError as eg:
    print(f"{len(eg.exceptions)} connection failures")
except* TimeoutError as eg:
    print(f"{len(eg.exceptions)} timeouts")
```

## `TaskGroup` vs `gather`

| | `TaskGroup` | `gather` |
|---|---|---|
| Cancellation | Cancels all tasks on first failure | `return_exceptions=True` collects errors |
| Exception handling | `ExceptionGroup` with `except*` | Single exception or mixed results |
| Task addition | Dynamic (add tasks inside the block) | Static (all tasks upfront) |
| Recommended | Yes (3.11+) | For simple cases or pre-3.11 |

## Cancellation

```python
async def worker(name: str):
    try:
        while True:
            await asyncio.sleep(1)
            print(f"{name} working")
    except asyncio.CancelledError:
        # Perform cleanup
        print(f"{name} cancelled, cleaning up")
        raise                          # always re-raise CancelledError

# Cancel a specific task
task = asyncio.create_task(worker("a"))
await asyncio.sleep(5)
task.cancel()
try:
    await task
except asyncio.CancelledError:
    print("task was cancelled")
```

Always re-raise `CancelledError` after cleanup — swallowing it prevents proper
cancellation propagation.

## Timeouts

```python
# Python 3.11+ — asyncio.timeout()
async def fetch_with_deadline(url: str) -> bytes:
    async with asyncio.timeout(10):    # 10-second deadline
        return await fetch(url)
    # Raises TimeoutError if exceeded

# Pre-3.11 — asyncio.wait_for()
async def fetch_with_deadline(url: str) -> bytes:
    return await asyncio.wait_for(fetch(url), timeout=10)

# Nested timeouts
async def process():
    async with asyncio.timeout(30):        # overall deadline
        data = await fetch(url)
        async with asyncio.timeout(10):    # inner deadline
            result = await transform(data)
    return result
```

## Graceful Shutdown

```python
import signal

async def main():
    # Setup signal handlers
    loop = asyncio.get_running_loop()
    shutdown_event = asyncio.Event()

    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, shutdown_event.set)

    async with asyncio.TaskGroup() as tg:
        tg.create_task(server.serve())
        tg.create_task(shutdown_event.wait())
        # When shutdown_event is set, remaining tasks are cancelled
```

## Shielding from Cancellation

Protect critical operations from being cancelled:

```python
async def save_and_cleanup(data):
    # save_to_db must complete even if the parent task is cancelled
    await asyncio.shield(save_to_db(data))
    # cleanup can be cancelled
    await cleanup()
```

Use `shield` sparingly — it defeats structured concurrency.
