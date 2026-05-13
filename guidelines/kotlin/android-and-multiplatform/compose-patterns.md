# Compose Patterns

Jetpack Compose is Android's modern declarative UI toolkit. It replaces XML layouts with composable functions that describe UI as a function of state. The key to effective Compose code is understanding recomposition, state management, and side effects.

## Composable Function Basics

Composable functions describe UI. They accept a `Modifier` parameter (defaulting to `Modifier`) to allow callers to customize layout and behavior:

```kotlin
@Composable
fun Greeting(name: String, modifier: Modifier = Modifier) {
    Text(text = "Hello, $name!", modifier = modifier)
}
```

Composables should be idempotent and side-effect-free with respect to their parameters. Given the same inputs, they should produce the same UI.

## State Hoisting

Lift state up to the caller to make composables stateless, reusable, and testable:

```kotlin
// Stateless composable (preferred) — receives state, emits events
@Composable
fun Counter(count: Int, onIncrement: () -> Unit) {
    Button(onClick = onIncrement) {
        Text("Count: $count")
    }
}

// State owner — manages state, passes it down
@Composable
fun CounterScreen() {
    var count by remember { mutableStateOf(0) }
    Counter(count = count, onIncrement = { count++ })
}
```

The pattern is: **state flows down, events flow up.** This is unidirectional data flow (UDF).

## remember and rememberSaveable

- `remember` keeps a value across recompositions but loses it on configuration changes (screen rotation):

```kotlin
var text by remember { mutableStateOf("") }
```

- `rememberSaveable` survives configuration changes by saving to the Bundle:

```kotlin
var text by rememberSaveable { mutableStateOf("") }
```

Use `rememberSaveable` for user-entered data that should survive rotation. Use `remember` for derived or transient UI state.

## State Types

```kotlin
// Simple mutable state
var count by remember { mutableStateOf(0) }

// Observable mutable list
val items = remember { mutableStateListOf<String>() }

// Derived state — recomputes only when dependencies change
val isValid by remember { derivedStateOf { username.length >= 3 } }
```

Prefer `derivedStateOf` over computing values inline when the computation is expensive or when you want to avoid unnecessary recompositions downstream.

## Unidirectional Data Flow with ViewModel

The standard architecture for Compose screens pairs a ViewModel with a composable:

```kotlin
// State definition
data class SearchUiState(
    val query: String = "",
    val results: List<Result> = emptyList(),
    val isLoading: Boolean = false,
    val error: String? = null
)

// Events the UI can trigger
sealed class SearchEvent {
    data class QueryChanged(val query: String) : SearchEvent()
    data object Search : SearchEvent()
    data object ClearError : SearchEvent()
}

// ViewModel owns the state
class SearchViewModel(
    private val repository: SearchRepository
) : ViewModel() {
    private val _uiState = MutableStateFlow(SearchUiState())
    val uiState: StateFlow<SearchUiState> = _uiState.asStateFlow()

    fun onEvent(event: SearchEvent) {
        when (event) {
            is SearchEvent.QueryChanged -> _uiState.update { it.copy(query = event.query) }
            is SearchEvent.Search -> search()
            is SearchEvent.ClearError -> _uiState.update { it.copy(error = null) }
        }
    }

    private fun search() {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true) }
            try {
                val results = repository.search(_uiState.value.query)
                _uiState.update { it.copy(results = results, isLoading = false) }
            } catch (e: Exception) {
                _uiState.update { it.copy(error = e.message, isLoading = false) }
            }
        }
    }
}

// Composable collects state in a lifecycle-aware manner
@Composable
fun SearchScreen(viewModel: SearchViewModel = viewModel()) {
    val uiState by viewModel.uiState.collectAsStateWithLifecycle()
    SearchContent(state = uiState, onEvent = viewModel::onEvent)
}
```

Use `collectAsStateWithLifecycle()` from the `lifecycle-runtime-compose` artifact. It stops collection when the lifecycle drops below STARTED, preventing wasted work.

## Minimizing Recomposition

Compose recomposes a function whenever its read state changes. Minimize unnecessary recomposition:

**Use stable types.** Data classes with immutable properties and primitives are stable. Compose skips recomposition when stable parameters have not changed:

```kotlin
// Stable — Compose can skip recomposition
data class UserInfo(val name: String, val avatarUrl: String)

// Unstable — Compose always recomposes
data class UserInfo(val name: String, val tags: MutableList<String>)
```

**Use `key()` for dynamic lists** to help Compose identify items:

```kotlin
LazyColumn {
    items(users, key = { it.id }) { user ->
        UserRow(user)
    }
}
```

**Extract and remember lambdas** to avoid creating new instances on every recomposition:

```kotlin
// Anti-pattern: new lambda every recomposition
items.forEach { item ->
    Button(onClick = { viewModel.onItemClick(item.id) }) { Text(item.name) }
}

// Better: use a method reference or remembered lambda
val onItemClick = remember(viewModel) { viewModel::onItemClick }
```

**Split large composables** into smaller functions so only the parts that read changed state recompose.

## Modifier Ordering

Modifiers apply in sequence. Order matters:

```kotlin
// Padding OUTSIDE the background (padding is transparent)
Box(modifier = Modifier.padding(16.dp).background(Color.Red))

// Padding INSIDE the background (padding is red)
Box(modifier = Modifier.background(Color.Red).padding(16.dp))
```

Think of modifiers as wrapping layers from outside in.

## Side Effects

Side effects are operations that escape the scope of a composable (network calls, navigation, logging). Use the appropriate effect handler:

```kotlin
// LaunchedEffect — runs a suspend block when keys change
LaunchedEffect(userId) {
    viewModel.loadUser(userId)
}

// DisposableEffect — setup + cleanup (e.g., listeners)
DisposableEffect(lifecycleOwner) {
    val observer = LifecycleEventObserver { _, event -> /* ... */ }
    lifecycleOwner.lifecycle.addObserver(observer)
    onDispose { lifecycleOwner.lifecycle.removeObserver(observer) }
}

// SideEffect — runs after every successful composition (non-suspend)
SideEffect {
    analytics.trackScreenView(screenName)
}
```

## Common Anti-Patterns

**Performing side effects during composition:**

```kotlin
// WRONG: network call runs on every recomposition
@Composable
fun UserProfile(userId: String) {
    val user = repository.getUser(userId)  // Side effect in composition!
    Text(user.name)
}

// CORRECT: use LaunchedEffect
@Composable
fun UserProfile(userId: String, viewModel: UserViewModel = viewModel()) {
    LaunchedEffect(userId) { viewModel.loadUser(userId) }
    val user by viewModel.user.collectAsStateWithLifecycle()
    user?.let { Text(it.name) }
}
```

**Creating objects without remember:**

```kotlin
// WRONG: new Formatter every recomposition
@Composable
fun PriceDisplay(amount: Double) {
    val formatter = NumberFormat.getCurrencyInstance()  // Recreated every time
    Text(formatter.format(amount))
}

// CORRECT: remember the formatter
@Composable
fun PriceDisplay(amount: Double) {
    val formatter = remember { NumberFormat.getCurrencyInstance() }
    Text(formatter.format(amount))
}
```
