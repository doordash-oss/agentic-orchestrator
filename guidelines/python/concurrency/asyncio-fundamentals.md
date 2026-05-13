# asyncio Fundamentals

## Coroutines and `async def`

```python
import asyncio

async def fetch_data(url: str) -> bytes:
    async with httpx.AsyncClient() as client:
        resp = await client.get(url)
        return resp.content
```

A coroutine does nothing until awaited or scheduled. Calling `fetch_data(url)`
returns a coroutine object — it doesn't execute the function.

## Running Async Code

```python
# Entry point — runs the event loop
async def main():
    data = await fetch_data("https://example.com")
    print(len(data))

asyncio.run(main())

# Anti-pattern: creating the event loop manually
loop = asyncio.get_event_loop()
loop.run_until_complete(main())   # deprecated in 3.10+
```

`asyncio.run()` creates a new event loop, runs the coroutine, and cleans up.
Always use it as the top-level entry point.

## Awaiting Multiple Coroutines

```python
# Sequential — slow
result_a = await fetch("url_a")
result_b = await fetch("url_b")

# Concurrent — fast
result_a, result_b = await asyncio.gather(
    fetch("url_a"),
    fetch("url_b"),
)
```

## Creating Tasks

```python
async def main():
    # create_task schedules the coroutine to run concurrently
    task = asyncio.create_task(fetch_data(url))

    # Do other work while task runs...
    other_result = await process_something()

    # Await the task to get its result
    data = await task
```

Always keep a reference to tasks — if a task is garbage collected while still
running, it may be silently cancelled.

```python
# Anti-pattern: fire-and-forget (task may be GC'd)
asyncio.create_task(send_notification())  # no reference kept!

# Correct: store the reference
background_tasks: set[asyncio.Task] = set()

task = asyncio.create_task(send_notification())
background_tasks.add(task)
task.add_done_callback(background_tasks.discard)
```

## Async Generators

```python
async def stream_lines(url: str) -> AsyncIterator[str]:
    async with httpx.AsyncClient() as client:
        async with client.stream("GET", url) as resp:
            async for line in resp.aiter_lines():
                yield line

async for line in stream_lines(url):
    process(line)
```

## Common Pitfalls

### Forgetting to `await`

```python
# Bug: result is a coroutine object, not the actual data
result = fetch_data(url)      # missing await!
print(type(result))           # <class 'coroutine'>

# Python 3.12+ warns: "coroutine was never awaited"
```

### Calling Sync Code in Async Context

```python
# Anti-pattern: blocks the entire event loop
async def handler():
    data = requests.get(url)       # blocks! other tasks starved

# Correct: use an async HTTP client
async def handler():
    async with httpx.AsyncClient() as client:
        data = await client.get(url)

# Or offload blocking code to a thread
async def handler():
    data = await asyncio.to_thread(requests.get, url)
```
