# Cancellation and Shutdown

## Cancellation Safety

In async Rust, a future can be **cancelled** (dropped) at any `.await` point.
This happens when:
- `tokio::select!` completes one branch and drops the others
- A `JoinHandle` is dropped (with `abort()`)
- A timeout expires
- The runtime shuts down

### What "Cancellation-Safe" Means

A future is cancellation-safe if dropping it at any `.await` point doesn't
lose data or leave resources in an inconsistent state.

**Cancel-safe**:
```rust
// recv() is cancel-safe — no message is lost if cancelled
loop {
    select! {
        Some(msg) = rx.recv() => process(msg),
        _ = shutdown.cancelled() => break,
    }
}
```

**NOT cancel-safe**:
```rust
// read_exact fills a buffer across multiple .awaits
// If cancelled mid-read, partial data is lost
select! {
    result = reader.read_exact(&mut buf) => { ... }
    _ = shutdown.cancelled() => break,  // buf may be partially filled
}
```

### Making Code Cancel-Safe

1. **Move cancel-unsafe code into a spawned task** — tasks run to completion
   even if the handle is dropped:
```rust
let handle = tokio::spawn(async move {
    reader.read_exact(&mut buf).await  // runs to completion
});

select! {
    result = handle => { ... }
    _ = shutdown.cancelled() => {
        handle.abort();  // explicitly cancel if needed
    }
}
```

2. **Check cancellation at specific points** instead of using `select!`:
```rust
async fn process_batch(items: Vec<Item>, token: &CancellationToken) -> Result<()> {
    for item in items {
        if token.is_cancelled() {
            return Ok(());  // clean exit point
        }
        process_item(item).await?;
    }
    Ok(())
}
```

## CancellationToken

The `tokio_util::sync::CancellationToken` provides structured cancellation:

```rust
use tokio_util::sync::CancellationToken;

let token = CancellationToken::new();

// Spawn a task that respects cancellation
let task_token = token.clone();
let handle = tokio::spawn(async move {
    loop {
        select! {
            _ = task_token.cancelled() => {
                tracing::info!("task cancelled, cleaning up");
                break;
            }
            result = do_work() => {
                handle_result(result);
            }
        }
    }
});

// Later: cancel the task
token.cancel();
handle.await?;
```

### Child Tokens

Create hierarchical cancellation:

```rust
let parent = CancellationToken::new();
let child = parent.child_token();

// Cancelling parent also cancels child
parent.cancel();
assert!(child.is_cancelled());

// But cancelling child does NOT cancel parent
```

## Graceful Shutdown Pattern

The canonical pattern for graceful server shutdown:

```rust
use tokio::signal;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let token = CancellationToken::new();

    // Spawn the server
    let server_token = token.clone();
    let server = tokio::spawn(async move {
        run_server(server_token).await
    });

    // Wait for shutdown signal
    signal::ctrl_c().await?;
    tracing::info!("shutdown signal received");

    // Cancel all tasks
    token.cancel();

    // Wait for graceful shutdown with timeout
    let shutdown_timeout = Duration::from_secs(30);
    match tokio::time::timeout(shutdown_timeout, server).await {
        Ok(result) => result?,
        Err(_) => tracing::warn!("shutdown timed out after {shutdown_timeout:?}"),
    }

    Ok(())
}

async fn run_server(token: CancellationToken) -> anyhow::Result<()> {
    let listener = TcpListener::bind("0.0.0.0:8080").await?;

    loop {
        select! {
            Ok((stream, addr)) = listener.accept() => {
                let conn_token = token.child_token();
                tokio::spawn(handle_connection(stream, addr, conn_token));
            }
            _ = token.cancelled() => {
                tracing::info!("server shutting down");
                break;
            }
        }
    }

    Ok(())
}
```

## spawn_blocking — Blocking in Async

Offload blocking operations to a dedicated thread pool:

```rust
// CPU-heavy computation
let hash = tokio::task::spawn_blocking(move || {
    argon2::hash_password(password)
}).await?;

// Synchronous I/O
let data = tokio::task::spawn_blocking(move || {
    std::fs::read_to_string(path)
}).await??;
```

**When to use**:
- CPU-intensive operations (hashing, compression, parsing large files)
- Synchronous libraries without async alternatives
- File I/O when `tokio::fs` isn't suitable

**Alternative**: `block_in_place` for the current thread (avoids spawn overhead):
```rust
let result = tokio::task::block_in_place(|| {
    synchronous_work()
});
```

## Timeout Pattern

```rust
use tokio::time::timeout;

match timeout(Duration::from_secs(10), fetch_data()).await {
    Ok(Ok(data)) => process(data),
    Ok(Err(err)) => tracing::error!("fetch failed: {err}"),
    Err(_) => tracing::warn!("fetch timed out"),
}
```

## Anti-Patterns

```rust
// Bad: no shutdown path — task runs forever
tokio::spawn(async {
    loop {
        do_work().await;
    }
});

// Bad: blocking the async runtime
async fn handler() {
    std::thread::sleep(Duration::from_secs(5));  // blocks!
}

// Bad: dropping JoinHandle without awaiting
let _ = tokio::spawn(important_work());  // fire and forget!

// Good: track tasks and await them
let mut tasks = JoinSet::new();
tasks.spawn(important_work());
while let Some(result) = tasks.join_next().await { ... }
```
