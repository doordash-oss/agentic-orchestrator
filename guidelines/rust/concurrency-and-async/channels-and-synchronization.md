# Channels and Synchronization

## Tokio Channel Types

| Channel | Pattern | Buffered | Use Case |
|---------|---------|----------|----------|
| `mpsc` | Many producers → one consumer | Yes | Task communication, work queues |
| `oneshot` | One sender → one receiver | No (capacity 1) | Request-response, task results |
| `broadcast` | One sender → many receivers | Yes | Event bus, pub-sub |
| `watch` | One sender → many receivers | No (latest only) | Config changes, state updates |

## mpsc — Multi-Producer, Single-Consumer

The workhorse channel for async Rust:

```rust
use tokio::sync::mpsc;

let (tx, mut rx) = mpsc::channel(100);  // bounded, backpressure at 100

// Producer
let tx_clone = tx.clone();
tokio::spawn(async move {
    tx_clone.send("hello").await.unwrap();
});

// Consumer
while let Some(msg) = rx.recv().await {
    println!("received: {msg}");
}
```

### Bounded vs Unbounded

```rust
// Bounded: backpressure — send blocks when full
let (tx, rx) = mpsc::channel(100);

// Unbounded: no backpressure — can OOM if producer is faster
let (tx, rx) = mpsc::unbounded_channel();
```

**Always prefer bounded channels** — unbounded channels can cause memory
exhaustion if the consumer falls behind. Use unbounded only when you can
prove the producer won't outpace the consumer.

### Dropping Senders Signals Completion

```rust
let (tx, mut rx) = mpsc::channel(10);

tokio::spawn(async move {
    for i in 0..5 {
        tx.send(i).await.unwrap();
    }
    // tx dropped here — signals end of stream
});

// recv() returns None when all senders are dropped
while let Some(val) = rx.recv().await {
    println!("{val}");
}
println!("channel closed");
```

## oneshot — Single-Use Request/Response

```rust
use tokio::sync::oneshot;

let (tx, rx) = oneshot::channel();

tokio::spawn(async move {
    let result = expensive_computation().await;
    let _ = tx.send(result);  // ignore error if receiver dropped
});

let result = rx.await?;
```

Common pattern — embed a oneshot in a command:

```rust
enum Command {
    Get {
        key: String,
        respond_to: oneshot::Sender<Option<Vec<u8>>>,
    },
}

// Sender side
let (tx, rx) = oneshot::channel();
cmd_tx.send(Command::Get { key: "foo".into(), respond_to: tx }).await?;
let value = rx.await?;
```

## broadcast — Fan-Out

Every receiver gets every message:

```rust
use tokio::sync::broadcast;

let (tx, _) = broadcast::channel(100);

let mut rx1 = tx.subscribe();
let mut rx2 = tx.subscribe();

tx.send("event")?;

assert_eq!(rx1.recv().await?, "event");
assert_eq!(rx2.recv().await?, "event");
```

**Slow receivers get `RecvError::Lagged(n)`** — they missed `n` messages.
Handle this explicitly:

```rust
loop {
    match rx.recv().await {
        Ok(msg) => process(msg),
        Err(broadcast::error::RecvError::Lagged(n)) => {
            tracing::warn!("missed {n} messages, catching up");
        }
        Err(broadcast::error::RecvError::Closed) => break,
    }
}
```

## watch — Latest-Value Only

Receivers always see the most recent value — no buffering:

```rust
use tokio::sync::watch;

let (tx, mut rx) = watch::channel(Config::default());

// Update config
tx.send(Config::new())?;

// Receiver waits for changes
loop {
    rx.changed().await?;
    let config = rx.borrow().clone();
    apply_config(config);
}
```

Perfect for configuration reloading, health status, feature flags.

## Semaphore — Bounded Concurrency

Limit the number of concurrent operations:

```rust
use tokio::sync::Semaphore;
use std::sync::Arc;

let semaphore = Arc::new(Semaphore::new(10));  // max 10 concurrent

for url in urls {
    let permit = semaphore.clone().acquire_owned().await?;
    tokio::spawn(async move {
        let _permit = permit;  // held until dropped
        fetch(url).await
    });
}
```

## Notify — Simple Wake-Up Signal

```rust
use tokio::sync::Notify;
use std::sync::Arc;

let notify = Arc::new(Notify::new());
let notify_clone = notify.clone();

tokio::spawn(async move {
    do_work().await;
    notify_clone.notify_one();  // wake one waiter
});

notify.notified().await;  // blocks until notified
```

## Channels vs Shared State

**Prefer channels when**:
- Tasks communicate via messages (actor pattern)
- You want to decouple producers and consumers
- Work needs to be distributed across tasks

**Prefer `Arc<Mutex<T>>` when**:
- Multiple tasks need to read/write the same data structure
- The data structure doesn't map naturally to messages
- You need transactional updates across multiple fields
