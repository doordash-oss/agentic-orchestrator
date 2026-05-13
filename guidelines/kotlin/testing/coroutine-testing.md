# Coroutine Testing

Testing coroutines requires `kotlinx-coroutines-test`, which provides
virtual time control, test dispatchers, and utilities for deterministic
testing of asynchronous code.

## runTest -- The Standard Entry Point

`runTest` is the primary way to test suspend functions. It creates a test
coroutine scope, automatically skips `delay()` calls, and fails on
uncaught exceptions:

```kotlin
@Test
fun `should fetch user`() = runTest {
    val api = FakeApi()
    val service = UserService(api)

    val user = service.fetchUser("alice")

    assertEquals("Alice", user.name)
}
```

`runTest` automatically advances virtual time past any `delay()` calls,
so tests complete instantly even if the production code has long delays.

## TestDispatcher Types

### StandardTestDispatcher

Tasks don't execute automatically. You must explicitly advance the
dispatcher. This gives precise control over execution order:

```kotlin
@Test
fun `should process items sequentially`() = runTest {
    val dispatcher = StandardTestDispatcher(testScheduler)
    val processor = BatchProcessor(dispatcher)

    processor.start(listOf("a", "b", "c"))

    // Nothing has run yet
    assertEquals(0, processor.processedCount)

    // Advance past all pending coroutines
    advanceUntilIdle()

    assertEquals(3, processor.processedCount)
}
```

### UnconfinedTestDispatcher

Tasks execute eagerly, similar to `Dispatchers.Unconfined`. Useful when
you don't need to control timing:

```kotlin
@Test
fun `should emit values immediately`() = runTest(UnconfinedTestDispatcher()) {
    val flow = flowOf(1, 2, 3)
    val results = mutableListOf<Int>()

    flow.collect { results.add(it) }

    assertEquals(listOf(1, 2, 3), results)
}
```

Use `StandardTestDispatcher` by default. Switch to `UnconfinedTestDispatcher`
only when eager execution simplifies the test without hiding timing bugs.

## Injecting Test Dispatchers

Design production code to accept a dispatcher parameter so tests can
substitute a test dispatcher:

```kotlin
class UserViewModel(
    private val repository: UserRepository,
    private val dispatcher: CoroutineDispatcher = Dispatchers.IO
) : ViewModel() {

    private val _state = MutableStateFlow<UiState>(UiState.Loading)
    val state: StateFlow<UiState> = _state

    fun loadUser(id: String) {
        viewModelScope.launch(dispatcher) {
            val user = repository.fetchUser(id)
            _state.value = UiState.Success(user)
        }
    }
}

// In tests:
@Test
fun `should load user into state`() = runTest {
    val dispatcher = StandardTestDispatcher(testScheduler)
    val repository = FakeUserRepository()
    val viewModel = UserViewModel(repository, dispatcher)

    viewModel.loadUser("alice")
    advanceUntilIdle()

    assertEquals(UiState.Success(User("Alice")), viewModel.state.value)
}
```

## Testing Flow with Turbine

Turbine is the standard library for testing Kotlin Flow emissions. It
provides a clean DSL for asserting emitted values, errors, and completion:

```kotlin
// Add dependency: app.cash.turbine:turbine
@Test
fun `should emit loading then success`() = runTest {
    val viewModel = UserViewModel(FakeRepository())

    viewModel.uiState.test {
        assertEquals(UiState.Loading, awaitItem())
        assertEquals(UiState.Success(data), awaitItem())
        cancelAndConsumeRemainingEvents()
    }
}
```

### Turbine API Reference

```kotlin
flow.test {
    // Wait for the next emitted item
    val item = awaitItem()

    // Assert the flow completes
    awaitComplete()

    // Assert the flow throws
    val error = awaitError()

    // Assert nothing was emitted (within a timeout)
    expectNoEvents()

    // Cancel collection and ignore any remaining events
    cancelAndIgnoreRemainingEvents()

    // Cancel collection and consume remaining (fails if unexpected events)
    cancelAndConsumeRemainingEvents()
}
```

### Testing StateFlow with Turbine

StateFlow always has a current value and replays the latest to new
collectors. Turbine handles this naturally:

```kotlin
@Test
fun `should transition through states`() = runTest {
    val viewModel = OrderViewModel()

    viewModel.state.test {
        // StateFlow replays the initial value
        assertEquals(OrderState.Idle, awaitItem())

        viewModel.placeOrder(item)
        assertEquals(OrderState.Processing, awaitItem())
        assertEquals(OrderState.Confirmed, awaitItem())

        cancelAndIgnoreRemainingEvents()
    }
}
```

### Testing SharedFlow with Turbine

SharedFlow does not replay by default, so you must start collection
before triggering emissions:

```kotlin
@Test
fun `should emit navigation events`() = runTest {
    val viewModel = LoginViewModel()

    viewModel.navigationEvents.test {
        viewModel.onLoginSuccess()
        assertEquals(NavEvent.GoToHome, awaitItem())
    }
}
```

## Testing Delay and Timeout Logic

Use `advanceTimeBy()` to test time-dependent behavior without waiting:

```kotlin
@Test
fun `should retry after delay`() = runTest {
    val api = mockk<Api>()
    coEvery { api.fetch() } throws IOException() andThen Result.success(data)
    val service = RetryingService(api, retryDelayMs = 5000)

    service.start()

    // First attempt fails immediately
    advanceTimeBy(100)
    assertEquals(ServiceState.Retrying, service.state.value)

    // Advance past the retry delay
    advanceTimeBy(5000)
    advanceUntilIdle()
    assertEquals(ServiceState.Success, service.state.value)

    coVerify(exactly = 2) { api.fetch() }
}
```

`advanceUntilIdle()` runs all pending coroutines to completion.
`advanceTimeBy(millis)` advances virtual time by the specified amount,
executing any coroutines whose delays have elapsed.

## Testing Exception Handling in Coroutines

```kotlin
@Test
fun `should handle api failure gracefully`() = runTest {
    val api = mockk<Api>()
    coEvery { api.fetchUser(any()) } throws NetworkException("timeout")
    val viewModel = UserViewModel(api, StandardTestDispatcher(testScheduler))

    viewModel.loadUser("alice")
    advanceUntilIdle()

    val state = viewModel.state.value
    assertIs<UiState.Error>(state)
    assertThat(state.message).contains("timeout")
}
```

## Complete Test Example

```kotlin
class SearchViewModelTest {
    private val api = mockk<SearchApi>()
    private lateinit var viewModel: SearchViewModel

    @BeforeEach
    fun setUp() {
        coEvery { api.search(any()) } returns listOf("result1", "result2")
    }

    @Test
    fun `should debounce search queries`() = runTest {
        viewModel = SearchViewModel(api, StandardTestDispatcher(testScheduler))

        viewModel.results.test {
            assertEquals(emptyList<String>(), awaitItem()) // initial

            viewModel.onQueryChanged("k")
            viewModel.onQueryChanged("ko")
            viewModel.onQueryChanged("kot")

            // Advance past debounce window (300ms)
            advanceTimeBy(300)
            advanceUntilIdle()

            assertEquals(listOf("result1", "result2"), awaitItem())

            // Only the final query should trigger a search
            coVerify(exactly = 1) { api.search("kot") }

            cancelAndIgnoreRemainingEvents()
        }
    }
}
```

## Anti-Patterns

**Using `Thread.sleep()` in coroutine tests.** This blocks the real thread
and defeats the purpose of virtual time. Use `advanceTimeBy()` instead.

**Using `runBlocking` instead of `runTest`.** `runBlocking` does not skip
delays and does not provide virtual time control. Always use `runTest` for
coroutine tests.

**Not injecting dispatchers.** Hardcoding `Dispatchers.IO` or
`Dispatchers.Default` makes code untestable. Accept a `CoroutineDispatcher`
parameter with a default value so tests can substitute a test dispatcher.

**Forgetting `advanceUntilIdle()`.** With `StandardTestDispatcher`, launched
coroutines won't run until you advance time. Missing this call leads to
tests that pass vacuously because the code under test never executes.
