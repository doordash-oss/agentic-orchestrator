# asyncio Patterns

## Semaphore for Rate Limiting

Limit concurrent operations (e.g., HTTP connections, DB queries):

```python
semaphore = asyncio.Semaphore(10)  # max 10 concurrent

async def fetch_limited(url: str) -> bytes:
    async with semaphore:
        return await fetch(url)

async def fetch_all(urls: list[str]) -> list[bytes]:
    async with asyncio.TaskGroup() as tg:
        tasks = [tg.create_task(fetch_limited(url)) for url in urls]
    return [t.result() for t in tasks]
```

## Processing Results as They Complete

```python
async def process_as_ready(urls: list[str]):
    tasks = [asyncio.create_task(fetch(url)) for url in urls]

    for coro in asyncio.as_completed(tasks):
        result = await coro
        process(result)   # process each result as it arrives
```

## Async Context Managers

```python
from contextlib import asynccontextmanager

@asynccontextmanager
async def managed_connection(dsn: str):
    conn = await asyncpg.connect(dsn)
    try:
        yield conn
    finally:
        await conn.close()

async with managed_connection("postgresql://...") as conn:
    rows = await conn.fetch("SELECT * FROM users")
```

## Async Iterators

```python
class AsyncCounter:
    def __init__(self, limit: int):
        self.limit = limit
        self.current = 0

    def __aiter__(self):
        return self

    async def __anext__(self) -> int:
        if self.current >= self.limit:
            raise StopAsyncIteration
        await asyncio.sleep(0.1)   # simulate async work
        self.current += 1
        return self.current

async for value in AsyncCounter(5):
    print(value)
```

## Queue-Based Producer/Consumer

```python
async def producer(queue: asyncio.Queue[str]):
    for item in items:
        await queue.put(item)
    await queue.put(None)              # sentinel

async def consumer(queue: asyncio.Queue[str]):
    while (item := await queue.get()) is not None:
        await process(item)
        queue.task_done()

async def main():
    queue: asyncio.Queue[str] = asyncio.Queue(maxsize=100)
    async with asyncio.TaskGroup() as tg:
        tg.create_task(producer(queue))
        tg.create_task(consumer(queue))
```

## `asyncio.wait` for Partial Completion

```python
tasks = {asyncio.create_task(fetch(url)) for url in urls}

# Wait for the first task to complete
done, pending = await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)

for task in done:
    result = task.result()

# Cancel remaining tasks if needed
for task in pending:
    task.cancel()
```

## Debouncing and Throttling

```python
class Debouncer:
    def __init__(self, delay: float):
        self.delay = delay
        self._task: asyncio.Task | None = None

    async def __call__(self, coro):
        if self._task:
            self._task.cancel()
        self._task = asyncio.create_task(self._run(coro))

    async def _run(self, coro):
        await asyncio.sleep(self.delay)
        await coro
```

## `nullcontext` for Optional Async Context

```python
from contextlib import asyncnullcontext

async def process(use_lock: bool):
    cm = lock if use_lock else asyncnullcontext()
    async with cm:
        await do_work()
```
