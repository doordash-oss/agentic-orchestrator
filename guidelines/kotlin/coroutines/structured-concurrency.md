# Structured Concurrency

Structured concurrency is the foundation of Kotlin coroutines. Every coroutine belongs to a scope, every scope has a Job, and parent Jobs wait for all child Jobs to complete. This hierarchy guarantees that no coroutine outlives its scope, preventing resource leaks and making cancellation predictable.

## CoroutineScope and Why It Matters

A `CoroutineScope` defines the lifetime of coroutines launched within it. When the scope is cancelled, all coroutines inside it are cancelled too.

```kotlin
// CORRECT: Coroutines tied to a lifecycle
class OrderService(private val scope: CoroutineScope) {
    fun processOrder(order: Order) {
        scope.launch {
            val validated = validateOrder(order)
            val receipt = chargePayment(validated)
            sendConfirmation(receipt)
        }
    }
}
```

```kotlin
// WRONG: GlobalScope leaks coroutines — no structured cancellation
class OrderService {
    fun processOrder(order: Order) {
        GlobalScope.launch {  // Who cancels this? Nobody.
            val validated = validateOrder(order)
            val receipt = chargePayment(validated)
            sendConfirmation(receipt)
        }
    }
}
```

## Parent-Child Job Relationships

When you launch a coroutine inside a scope, its Job becomes a child of the scope's Job. This gives you two guarantees:

1. **Parent cancellation cancels all children** — cancelling the parent Job cancels every child recursively.
2. **Parent waits for all children** — a parent Job does not complete until all children complete.

```kotlin
fun main() = runBlocking {
    val parentJob = launch {
        launch {
            delay(1000)
            println("Child 1 done")
        }
        launch {
            delay(2000)
            println("Child 2 done")
        }
    }
    // parentJob completes only after both children finish
    parentJob.join()
    println("All children done")
}
```

## coroutineScope { } — All-or-Nothing

`coroutineScope` creates a new scope that fails if any child fails. All other children are cancelled when one fails. Use it for parallel decomposition where partial results are useless.

```kotlin
// CORRECT: Both fetches run in parallel; if either fails, both are cancelled
suspend fun loadDashboard(): Dashboard = coroutineScope {
    val user = async { fetchUser() }
    val orders = async { fetchOrders() }
    Dashboard(user.await(), orders.await())
}
```

If `fetchUser()` throws, `fetchOrders()` is cancelled automatically, and `loadDashboard()` propagates the exception.

## supervisorScope { } — Independent Children

`supervisorScope` lets children fail independently. A failure in one child does not cancel siblings or the parent. Use it when tasks are independent and partial success is acceptable.

```kotlin
// CORRECT: Each notification is independent — one failure shouldn't stop others
suspend fun notifyAll(users: List<User>) = supervisorScope {
    users.forEach { user ->
        launch {
            try {
                sendNotification(user)
            } catch (e: NotificationException) {
                logger.warn("Failed to notify ${user.id}", e)
            }
        }
    }
}
```

## SupervisorJob vs Regular Job

A `SupervisorJob` prevents child failures from propagating upward. Use it when constructing a scope that should survive individual child failures.

```kotlin
// CORRECT: Application-level scope that survives individual task failures
class Application : CoroutineScope {
    override val coroutineContext = SupervisorJob() + Dispatchers.Default

    fun startBackgroundTasks() {
        launch { syncInventory() }   // failure here won't kill metrics
        launch { reportMetrics() }   // failure here won't kill inventory
    }

    fun shutdown() {
        coroutineContext.cancel()
    }
}
```

```kotlin
// WRONG: Regular Job — one child failure cancels all siblings
class Application : CoroutineScope {
    override val coroutineContext = Job() + Dispatchers.Default

    fun startBackgroundTasks() {
        launch { syncInventory() }   // if this fails...
        launch { reportMetrics() }   // ...this gets cancelled too
    }
}
```

## Tying Coroutines to Lifecycle (Android)

In Android, tie coroutines to component lifecycles so they are automatically cancelled when the component is destroyed.

```kotlin
// ViewModel — survives configuration changes, cancelled when ViewModel is cleared
class OrderViewModel : ViewModel() {
    val orders = MutableStateFlow<List<Order>>(emptyList())

    init {
        viewModelScope.launch {
            orders.value = repository.fetchOrders()
        }
    }
}

// Activity/Fragment — cancelled when lifecycle reaches DESTROYED
class OrderActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                viewModel.orders.collect { renderOrders(it) }
            }
        }
    }
}
```

## The cancel() Pattern for Cleanup

Always cancel your scope when the owning component is done. If you implement `CoroutineScope` manually, pair it with a cleanup method.

```kotlin
class WebSocketManager {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    fun connect(url: String) {
        scope.launch { /* ... */ }
    }

    fun disconnect() {
        scope.cancel()  // Cancels all coroutines, releases resources
    }
}
```

## Anti-Pattern: Breaking the Parent-Child Hierarchy

Creating a standalone `Job()` and passing it to `launch` breaks the parent-child relationship. The parent scope no longer waits for or cancels that coroutine.

```kotlin
// WRONG: Standalone Job breaks structured concurrency
fun CoroutineScope.doWork() {
    launch(Job()) {  // This coroutine is now an orphan
        delay(5000)
        println("This runs even after the parent scope is cancelled")
    }
}
```

```kotlin
// CORRECT: Let the coroutine inherit the parent's Job
fun CoroutineScope.doWork() {
    launch {
        delay(5000)
        println("Cancelled when parent scope is cancelled")
    }
}
```

## Anti-Pattern: GlobalScope

`GlobalScope` creates top-level coroutines with no parent. They run until completion or process exit. There is no way to cancel them as a group, and they can leak if you lose the reference to their Job.

```kotlin
// WRONG: Leaked coroutine — runs forever, no way to cancel
fun startPolling() {
    GlobalScope.launch {
        while (true) {
            poll()
            delay(5000)
        }
    }
}

// CORRECT: Scoped coroutine — cancelled when the service stops
class PollingService(private val scope: CoroutineScope) {
    fun startPolling() {
        scope.launch {
            while (isActive) {
                poll()
                delay(5000)
            }
        }
    }
}
```

## Summary

| Construct | Behavior on Child Failure | Use When |
|-----------|--------------------------|----------|
| `coroutineScope` | Cancels all children, propagates exception | Parallel tasks that are all-or-nothing |
| `supervisorScope` | Other children continue | Independent tasks, partial success is OK |
| `SupervisorJob()` | Child failure doesn't cancel parent | Long-lived scopes (application, service) |
| `Job()` | Child failure cancels parent and siblings | Default — most task hierarchies |
