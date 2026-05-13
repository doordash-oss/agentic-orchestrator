# Streams and Async Iterators

## Async Iterators

`for await...of` iterates over asynchronous data sources:

```typescript
async function* fetchPages(url: string): AsyncGenerator<Page> {
  let nextUrl: string | null = url;
  while (nextUrl) {
    const response = await fetch(nextUrl);
    const data = await response.json();
    yield data.page;
    nextUrl = data.nextPageUrl;
  }
}

for await (const page of fetchPages("/api/pages")) {
  processPage(page);
}
```

### Async Generators

Use `async function*` to create async iterables from any data source:

```typescript
async function* pollForUpdates(
  url: string,
  interval: number,
  signal: AbortSignal,
): AsyncGenerator<Update> {
  while (!signal.aborted) {
    const response = await fetch(url, { signal });
    yield await response.json();
    await new Promise((r) => setTimeout(r, interval));
  }
}
```

## Web Streams API

The Streams API provides a standard interface for processing data in chunks:

### ReadableStream

```typescript
// Create a readable stream from an async source:
const stream = new ReadableStream<string>({
  async start(controller) {
    for await (const chunk of dataSource) {
      controller.enqueue(chunk);
    }
    controller.close();
  },
});

// Consume with a reader:
const reader = stream.getReader();
while (true) {
  const { done, value } = await reader.read();
  if (done) break;
  process(value);
}
```

### TransformStream

Process data as it flows through:

```typescript
const uppercaseTransform = new TransformStream<string, string>({
  transform(chunk, controller) {
    controller.enqueue(chunk.toUpperCase());
  },
});

const output = inputStream.pipeThrough(uppercaseTransform);
```

### Streaming Fetch Responses

Process large responses without loading everything into memory:

```typescript
async function streamJSON(url: string): Promise<void> {
  const response = await fetch(url);
  if (!response.body) throw new Error("No response body");

  const reader = response.body.getReader();
  const decoder = new TextDecoder();

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    const text = decoder.decode(value, { stream: true });
    processChunk(text);
  }
}
```

## Node.js Streams

Node.js has its own stream API that predates web streams. Use the `node:stream`
module, and prefer the promise-based API:

```typescript
import { pipeline } from "node:stream/promises";
import { createReadStream, createWriteStream } from "node:fs";
import { createGzip } from "node:zlib";

// Pipeline with automatic error handling and cleanup:
await pipeline(
  createReadStream("input.txt"),
  createGzip(),
  createWriteStream("output.txt.gz"),
);
```

### Converting Between Node.js and Web Streams

```typescript
import { Readable } from "node:stream";

// Node.js Readable → Web ReadableStream:
const webStream = Readable.toWeb(nodeReadable);

// Web ReadableStream → Node.js Readable:
const nodeStream = Readable.fromWeb(webReadableStream);
```

## Iteration Utilities

### Collecting Async Iterables

```typescript
async function collect<T>(iterable: AsyncIterable<T>): Promise<T[]> {
  const items: T[] = [];
  for await (const item of iterable) {
    items.push(item);
  }
  return items;
}
```

### Mapping Async Iterables

```typescript
async function* map<T, U>(
  iterable: AsyncIterable<T>,
  fn: (item: T) => U | Promise<U>,
): AsyncGenerator<U> {
  for await (const item of iterable) {
    yield await fn(item);
  }
}
```

### Taking N Items

```typescript
async function* take<T>(iterable: AsyncIterable<T>, n: number): AsyncGenerator<T> {
  let count = 0;
  for await (const item of iterable) {
    yield item;
    if (++count >= n) return;
  }
}
```

## Best Practices

- **Use streams for large data** — don't load entire files/responses into memory
- **Use `pipeline()`** in Node.js for proper error handling and cleanup
- **Prefer async generators** over manual iterator protocol implementation
- **Cancel streams with AbortController** — pass `signal` to fetch and stream consumers
- **Avoid mixing Node.js and Web stream APIs** in the same code — pick one
- **Use `for await...of`** for consuming — avoid manual `.read()` loops unless needed
