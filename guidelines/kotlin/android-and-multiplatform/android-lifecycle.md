# Android Lifecycle Integration

Android components (Activities, Fragments) have complex lifecycles. Coroutines and flows must be scoped correctly to avoid leaks, wasted work, and crashes from updating destroyed UI. This guide covers the correct patterns for tying coroutines to Android lifecycle.

## Lifecycle-Aware Coroutine Scopes

### viewModelScope

Scoped to the ViewModel lifecycle. Coroutines are automatically cancelled when `onCleared()` is called (when the associated UI is permanently destroyed):

```kotlin
class MyViewModel(private val repository: UserRepository) : ViewModel() {
    private val _users = MutableStateFlow<List<User>>(emptyList())
    val users: StateFlow<List<User>> = _users.asStateFlow()

    init {
        viewModelScope.launch {
            _users.value = repository.fetchUsers()
        }
    }

    fun refreshUsers() {
        viewModelScope.launch {
            _users.value = repository.fetchUsers()
        }
    }
}
```

Use `viewModelScope` for all ViewModel-initiated work: data fetching, transformations, business logic. It survives configuration changes (screen rotation) because the ViewModel survives them.

### lifecycleScope

Scoped to the Activity or Fragment lifecycle. Cancelled when the component is destroyed:

```kotlin
class MyActivity : AppCompatActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        lifecycleScope.launch {
            // One-shot work tied to this Activity
            val config = configRepository.loadConfig()
            applyConfig(config)
        }
    }
}
```

Prefer `viewModelScope` over `lifecycleScope` for data operations. Use `lifecycleScope` for UI-specific tasks that should not survive the Activity/Fragment.

## Collecting Flows in the UI

### The Correct Pattern: repeatOnLifecycle

```kotlin
class MyActivity : AppCompatActivity() {
    private val viewModel: MyViewModel by viewModels()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) {
                viewModel.uiState.collect { state ->
                    updateUi(state)
                }
            }
        }
    }
}
```

### Why repeatOnLifecycle Matters

`repeatOnLifecycle` manages the collection lifecycle:

1. **Starts** collection when lifecycle reaches STARTED (Activity visible)
2. **Cancels** the collection block when lifecycle drops below STARTED (Activity goes to background)
3. **Restarts** collection when lifecycle reaches STARTED again (Activity returns to foreground)

This prevents:
- Wasted CPU/network when the app is in the background
- Crashes from updating views that are not attached
- Battery drain from unnecessary upstream work

### Collecting Multiple Flows

Launch each collection in a separate coroutine inside `repeatOnLifecycle`:

```kotlin
lifecycleScope.launch {
    repeatOnLifecycle(Lifecycle.State.STARTED) {
        launch {
            viewModel.uiState.collect { updateUi(it) }
        }
        launch {
            viewModel.events.collect { handleEvent(it) }
        }
    }
}
```

### Anti-Patterns

**Collecting without lifecycle awareness:**

```kotlin
// WRONG: continues collecting when app is in background
lifecycleScope.launch {
    viewModel.uiState.collect { state ->
        updateUi(state)  // May crash if view is destroyed
    }
}
```

**Wrapping flow in LiveData unnecessarily:**

```kotlin
// WRONG: unnecessary LiveData wrapping for new code
val users: LiveData<List<User>> = repository.getUsersFlow().asLiveData()

// CORRECT: expose StateFlow directly
val users: StateFlow<List<User>> = repository.getUsersFlow()
    .stateIn(viewModelScope, SharingStarted.WhileSubscribed(5000), emptyList())
```

## StateFlow vs LiveData

| Feature | StateFlow | LiveData |
|---------|-----------|----------|
| Initial value | Required | Optional |
| Lifecycle awareness | Manual (`repeatOnLifecycle`) | Automatic |
| Testing | `runTest` + Turbine | `InstantTaskExecutorRule` |
| Nullability | Type-safe (non-null by default) | Always nullable |
| Multiplatform | Yes (kotlinx.coroutines) | Android-only |
| Operators | Full flow operators (`map`, `combine`, etc.) | Limited transformations |

**Recommendation:** Use `StateFlow` for new code. It integrates with the coroutines ecosystem, works across KMP, and provides richer operators.

## ViewModel State Pattern

Model UI state as a sealed class and expose it as a `StateFlow`:

```kotlin
sealed class UserUiState {
    data object Loading : UserUiState()
    data class Success(val user: User) : UserUiState()
    data class Error(val message: String?) : UserUiState()
}

class UserViewModel(
    private val userRepository: UserRepository
) : ViewModel() {
    private val _uiState = MutableStateFlow<UserUiState>(UserUiState.Loading)
    val uiState: StateFlow<UserUiState> = _uiState.asStateFlow()

    init {
        viewModelScope.launch {
            try {
                val user = userRepository.getUser()
                _uiState.value = UserUiState.Success(user)
            } catch (e: Exception) {
                _uiState.value = UserUiState.Error(e.message)
            }
        }
    }

    fun retry() {
        _uiState.value = UserUiState.Loading
        viewModelScope.launch {
            try {
                val user = userRepository.getUser()
                _uiState.value = UserUiState.Success(user)
            } catch (e: Exception) {
                _uiState.value = UserUiState.Error(e.message)
            }
        }
    }
}
```

This pattern gives the UI a single source of truth. The composable or Activity renders based on the current state:

```kotlin
@Composable
fun UserScreen(viewModel: UserViewModel = viewModel()) {
    val uiState by viewModel.uiState.collectAsStateWithLifecycle()

    when (val state = uiState) {
        is UserUiState.Loading -> CircularProgressIndicator()
        is UserUiState.Success -> UserContent(state.user)
        is UserUiState.Error -> ErrorMessage(state.message, onRetry = viewModel::retry)
    }
}
```

## SavedStateHandle

`SavedStateHandle` survives process death (unlike regular ViewModel state). Use it for user input and navigation arguments:

```kotlin
class SearchViewModel(private val savedState: SavedStateHandle) : ViewModel() {
    val query = savedState.getStateFlow("query", "")

    fun setQuery(q: String) {
        savedState["query"] = q
    }

    val results: StateFlow<List<Result>> = query
        .debounce(300)
        .flatMapLatest { repository.search(it) }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(5000), emptyList())
}
```

Use `SavedStateHandle` for:
- Search queries, filter selections, scroll positions
- Any user input that would be frustrating to lose on process death
- Navigation arguments passed to the ViewModel

## WorkManager for Background Work

For work that must complete even if the app is killed, use WorkManager instead of coroutines:

```kotlin
class SyncWorker(
    context: Context,
    params: WorkerParameters,
    private val repository: SyncRepository
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        return try {
            repository.syncAll()
            Result.success()
        } catch (e: Exception) {
            if (runAttemptCount < 3) Result.retry() else Result.failure()
        }
    }
}

// Enqueue the work
val request = OneTimeWorkRequestBuilder<SyncWorker>()
    .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
    .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.SECONDS)
    .build()
WorkManager.getInstance(context).enqueueUniqueWork("sync", ExistingWorkPolicy.KEEP, request)
```

Use WorkManager when work must survive process death, needs retry policies, or requires constraints (network, battery).
