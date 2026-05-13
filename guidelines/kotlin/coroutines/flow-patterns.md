# Flow Patterns

Kotlin `Flow` is a cold, asynchronous stream built on coroutines. Each collector triggers a fresh execution of the flow, making flows lazy and safe by default. For hot streams that share state or broadcast events, use `StateFlow` and `SharedFlow`.

## Cold Flows

A cold flow does not produce values until it is collected. Each collector gets its own independent execution.

```kotlin
// Cold flow — each collector triggers a fresh database query
fun recentOrders(): Flow<Order> = flow {
    val orders = database.queryRecentOrders()
    orders.forEach { order ->
        emit(order)
    }
}

// Two independent executions
recentOrders().collect { println("Collector A: $it") }
recentOrders().collect { println("Collector B: $it") }
```

## Flow Operators

Intermediate operators transform flows without triggering collection. They return a new `Flow`.

```kotlin
fun activeUsers(): Flow<User> = userRepository.allUsers()
    .filter { it.isActive }
    .map { it.toDisplayModel() }
    .take(50)

// zip — combines elements pairwise from two flows
val names: Flow<String> = flowOf("Alice", "Bob")
val ages: Flow<Int> = flowOf(30, 25)
val users: Flow<String> = names.zip(ages) { name, age -> "$name ($age)" }

// combine — emits when either flow emits, using latest from both
val searchResults: Flow<List<Item>> = combine(
    searchQuery,
    filterSettings,
) { query, filters ->
    repository.search(query, filters)
}

// transform — flexible operator for custom logic
fun Flow<Event>.toCommands(): Flow<Command> = transform { event ->
    emit(Command.Log(event))
    if (event.isImportant) {
        emit(Command.Alert(event))
    }
}
```

## Terminal Operators

Terminal operators trigger collection and produce a result.

```kotlin
val orders: List<Order> = recentOrders().toList()
val first: Order = recentOrders().first()
val total: Int = orderAmounts().reduce { acc, value -> acc + value }
val sum: Int = orderAmounts().fold(0) { acc, value -> acc + value }

// collect is the most common terminal operator
recentOrders().collect { order ->
    processOrder(order)
}
```

## StateFlow — Observable State

`StateFlow` is a hot flow that always holds a value. It conflates emissions, meaning collectors always see the latest value. It is the modern replacement for `LiveData`.

```kotlin
class CartViewModel : ViewModel() {
    // Backing property pattern — expose read-only StateFlow publicly
    private val _uiState = MutableStateFlow(CartUiState())
    val uiState: StateFlow<CartUiState> = _uiState.asStateFlow()

    fun addItem(item: Item) {
        _uiState.update { state ->
            state.copy(items = state.items + item)
        }
    }

    fun removeItem(itemId: String) {
        _uiState.update { state ->
            state.copy(items = state.items.filter { it.id != itemId })
        }
    }
}

data class CartUiState(
    val items: List<Item> = emptyList(),
    val isLoading: Boolean = false,
    val error: String? = null,
)
```

Key properties of `StateFlow`:
- Always has a value (constructor requires an initial value)
- Conflated: only the latest value is delivered to slow collectors
- `value` property gives synchronous access to current state
- Equality-based: does not re-emit if the new value equals the current one

## SharedFlow — Broadcasting Events

`SharedFlow` is a hot flow for broadcasting events to multiple collectors. Unlike `StateFlow`, it does not conflate and can buffer emissions.

```kotlin
class EventBus {
    // replay = 0 means new subscribers don't get past events
    private val _events = MutableSharedFlow<AppEvent>(
        replay = 0,
        extraBufferCapacity = 64,
        onBufferOverflow = BufferOverflow.DROP_OLDEST,
    )
    val events: SharedFlow<AppEvent> = _events.asSharedFlow()

    suspend fun emit(event: AppEvent) {
        _events.emit(event)
    }
}
```

Buffer overflow policies:
- `SUSPEND` (default) — suspends the emitter until buffer has space
- `DROP_OLDEST` — drops the oldest value in the buffer
- `DROP_LATEST` — drops the value being emitted

## Converting Cold Flows to Hot: stateIn and shareIn

Use `stateIn` to convert a cold flow into a `StateFlow`, and `shareIn` to convert into a `SharedFlow`.

```kotlin
class UserRepository(private val api: UserApi) {
    // Cold flow — each collector triggers a new API call
    private fun userUpdates(): Flow<User> = flow {
        while (true) {
            emit(api.fetchCurrentUser())
            delay(30_000)
        }
    }
}

class UserViewModel(
    private val repository: UserRepository,
) : ViewModel() {
    // Hot StateFlow — shared across all collectors, starts when first subscriber appears
    val currentUser: StateFlow<User?> = repository.userUpdates()
        .stateIn(
            scope = viewModelScope,
            started = SharingStarted.WhileSubscribed(5_000),
            initialValue = null,
        )
}
```

`SharingStarted` strategies:
- `WhileSubscribed(stopTimeoutMillis)` — starts when first subscriber appears, stops after last subscriber disappears (with optional delay). Best for UI state.
- `Eagerly` — starts immediately, never stops. Use for data that should always be fresh.
- `Lazily` — starts on first subscriber, never stops.

## Collecting Flows in Android

Always collect flows in a lifecycle-aware manner to avoid processing updates when the UI is not visible.

```kotlin
// CORRECT: Lifecycle-aware collection — stops when view is not STARTED
class OrderFragment : Fragment() {
    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        viewLifecycleOwner.lifecycleScope.launch {
            viewLifecycleOwner.repeatOnLifecycle(Lifecycle.State.STARTED) {
                viewModel.uiState.collect { state ->
                    renderState(state)
                }
            }
        }
    }
}
```

```kotlin
// WRONG: Plain launch — keeps collecting even when the app is in the background
class OrderFragment : Fragment() {
    override fun onViewCreated(view: View, savedInstanceState: Bundle?) {
        lifecycleScope.launch {
            viewModel.uiState.collect { state ->
                renderState(state)  // Updates UI even when fragment is stopped
            }
        }
    }
}
```

## Collecting Multiple Flows

When collecting multiple flows in one lifecycle scope, launch each in a separate coroutine.

```kotlin
viewLifecycleOwner.lifecycleScope.launch {
    viewLifecycleOwner.repeatOnLifecycle(Lifecycle.State.STARTED) {
        launch { viewModel.uiState.collect { renderState(it) } }
        launch { viewModel.events.collect { handleEvent(it) } }
        launch { viewModel.errors.collect { showError(it) } }
    }
}
```

## Flow vs LiveData

`StateFlow` is the modern replacement for `LiveData`:

| Feature | LiveData | StateFlow |
|---------|----------|-----------|
| Lifecycle-aware | Built-in | Requires `repeatOnLifecycle` |
| Initial value | Optional | Required |
| Operators | Limited (map, switchMap) | Full flow operator set |
| Testing | Needs `InstantTaskExecutorRule` | Pure coroutines, uses `Turbine` |
| Platform | Android only | Kotlin multiplatform |

## Anti-Pattern: Using flowOn Incorrectly

`flowOn` changes the upstream dispatcher, not the downstream collector.

```kotlin
// CORRECT: Database query runs on IO, collection runs on caller's dispatcher
fun recentOrders(): Flow<Order> = flow {
    emit(database.queryRecentOrders())
}.flowOn(Dispatchers.IO)

// WRONG: flowOn after collect has no effect on the flow
recentOrders()
    .collect { processOrder(it) }  // Runs on caller's dispatcher regardless
```
