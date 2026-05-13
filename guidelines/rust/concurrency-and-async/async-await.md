# Async / Await

## Tokio Runtime

Tokio is the standard async runtime for Rust. Configure it in `main`:

```rust
#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // Multi-threaded runtime (default)
    run_server().await
}

// Or configure explicitly:
#[tokio::main(flavor = "current_thread")]  // single-threaded
async fn main() { ... }
```

**Multi-thread** (default): work-stealing scheduler, best for servers.
**Current-thread**: single-threaded, lower overhead, good for CLI tools or
tests.

## Spawning Tasks

`tokio::spawn` creates a new concurrent task on the runtime:

```rust
let handle = tokio::spawn(async {
    fetch_data().await
});

// Later: await the result
let result = handle.await?;  // JoinError if task panicked
```

**Tasks must be `'static + Send`** — they can be moved between threads:

```rust
// Bad: borrows local data
let data = vec![1, 2, 3];
tokio::spawn(async {
    println!("{data:?}");  // error: data borrowed, not 'static
});

// Good: move ownership
let data = vec![1, 2, 3];
tokio::spawn(async move {
    println!("{data:?}");  // ok: data is owned
});
```

## JoinSet — Managing Task Groups

Track multiple spawned tasks:

```rust
use tokio::task::JoinSet;

let mut set = JoinSet::new();

for url in urls {
    set.spawn(async move {
        fetch(url).await
    });
}

// Collect results as they complete
while let Some(result) = set.join_next().await {
    match result {
        Ok(data) => process(data),
        Err(err) => eprintln!("task failed: {err}"),
    }
}
```

## select! — Racing Futures

Runs multiple futures concurrently, completes when the first one finishes.
**All other branches are cancelled**:

```rust
use tokio::select;

select! {
    result = fetch_from_primary() => {
        handle_primary(result);
    }
    result = fetch_from_fallback() => {
        handle_fallback(result);
    }
    _ = tokio::time::sleep(Duration::from_secs(5)) => {
        eprintln!("timeout");
    }
}
```

### select! in Loops

```rust
loop {
    select! {
        Some(msg) = rx.recv() => {
            process(msg);
        }
        _ = shutdown.cancelled() => {
            break;
        }
    }
}
```

**Warning**: understand [cancellation safety](cancellation-and-shutdown.md)
before using `select!` — not all futures are safe to cancel mid-execution.

## async Trait Methods

### Rust 1.75+: Native async fn in traits (RPITIT)

```rust
trait DataStore {
    async fn get(&self, key: &str) -> Result<Vec<u8>>;
    async fn set(&self, key: &str, value: &[u8]) -> Result<()>;
}

impl DataStore for RedisStore {
    async fn get(&self, key: &str) -> Result<Vec<u8>> {
        self.client.get(key).await
    }
    // ...
}
```

**Limitation**: async trait methods are not object-safe by default.
For `dyn Trait`, use the `trait_variant` crate or manual boxing:

```rust
// With trait_variant
#[trait_variant::make(DataStoreSend: Send)]
trait DataStore {
    async fn get(&self, key: &str) -> Result<Vec<u8>>;
}

// Can now use dyn DataStoreSend
fn make_store() -> Box<dyn DataStoreSend> { ... }
```

### Pre-1.75: async-trait crate

```rust
use async_trait::async_trait;

#[async_trait]
trait DataStore {
    async fn get(&self, key: &str) -> Result<Vec<u8>>;
}
```

This desugars to `Pin<Box<dyn Future>>` — slight allocation overhead.

## Pinning

Async futures are state machines that may be self-referential. `Pin`
prevents them from being moved:

```rust
use tokio::pin;

let future = async { do_work().await };
pin!(future);  // pins to the stack

// Now you can poll it or use it in select!
select! {
    result = &mut future => { ... }
    _ = timeout => { ... }
}
```

In most code, you won't interact with `Pin` directly — `tokio::spawn`,
`select!`, and `.await` handle it.

## Don't Block the Runtime

Never run blocking operations on async threads:

```rust
// Bad: blocks the async thread
async fn bad() {
    std::thread::sleep(Duration::from_secs(1));  // blocks!
    std::fs::read_to_string("file.txt");          // blocks!
}

// Good: use async equivalents
async fn good() {
    tokio::time::sleep(Duration::from_secs(1)).await;
    tokio::fs::read_to_string("file.txt").await;
}

// Good: offload to blocking thread pool
async fn also_good() {
    let data = tokio::task::spawn_blocking(|| {
        heavy_computation()
    }).await?;
}
```
