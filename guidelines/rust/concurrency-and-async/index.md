# Concurrency & Async — Index

> **This file is an index.** The actual rules and examples live in the topic files below.
> You MUST read at least the topic files relevant to your task.

## Topics

| File | When to Read |
|------|-------------|
| [threads-and-shared-state.md](threads-and-shared-state.md) | `std::thread`, `Arc<Mutex<T>>`, `RwLock`, atomics, Rayon |
| [async-await.md](async-await.md) | Tokio runtime, `async fn`, `tokio::spawn`, `JoinSet`, `select!` |
| [channels-and-synchronization.md](channels-and-synchronization.md) | `mpsc`, `oneshot`, `broadcast`, `watch`, `Semaphore`, `Notify` |
| [cancellation-and-shutdown.md](cancellation-and-shutdown.md) | Cancellation safety, `CancellationToken`, graceful shutdown, `spawn_blocking` |
