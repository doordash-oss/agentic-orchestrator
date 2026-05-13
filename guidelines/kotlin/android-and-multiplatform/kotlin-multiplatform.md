# Kotlin Multiplatform

Kotlin Multiplatform (KMP) enables sharing business logic across Android, iOS, web, and desktop from a single Kotlin codebase. The key is structuring code so that shared logic lives in `commonMain` while platform-specific implementations are isolated in their respective source sets.

## Project Structure

A typical KMP project follows this layout:

```
shared/
├── src/
│   ├── commonMain/kotlin/    # Shared code (business logic, models, interfaces)
│   ├── commonTest/kotlin/    # Shared tests (run on all platforms)
│   ├── androidMain/kotlin/   # Android-specific implementations
│   ├── iosMain/kotlin/       # iOS-specific implementations
│   └── jvmMain/kotlin/       # JVM-specific implementations
└── build.gradle.kts
```

Keep domain models, business logic, repository interfaces, and use cases in `commonMain`. Only put code that genuinely requires platform APIs into platform source sets.

## The expect/actual Mechanism

Use `expect`/`actual` to declare a common API with platform-specific implementations:

```kotlin
// commonMain
expect fun platformName(): String

expect class PlatformLogger() {
    fun log(message: String)
}

// androidMain
actual fun platformName(): String = "Android ${Build.VERSION.SDK_INT}"

actual class PlatformLogger actual constructor() {
    actual fun log(message: String) = Log.d("App", message)
}

// iosMain
actual fun platformName(): String = UIDevice.currentDevice.systemName()

actual class PlatformLogger actual constructor() {
    actual fun log(message: String) = NSLog(message)
}
```

## When to Use expect/actual vs Interfaces

**Use expect/actual** for platform capabilities that must differ at the declaration level:

- File system access primitives
- Cryptographic operations
- Platform identity (OS name, version)
- Native interop (JNI on Android, cinterop on iOS)

**Use interfaces + dependency injection** for everything else:

```kotlin
// commonMain — define the contract
interface Analytics {
    fun track(event: String, properties: Map<String, String>)
}

// androidMain — provide the implementation via DI
class FirebaseAnalytics : Analytics {
    override fun track(event: String, properties: Map<String, String>) {
        Firebase.analytics.logEvent(event) {
            properties.forEach { (k, v) -> param(k, v) }
        }
    }
}
```

Interfaces give you testability (easy to mock), flexibility (swap implementations), and avoid the rigidity of expect/actual class hierarchies.

## Shared Module Design

Prefer multiplatform libraries for common tasks:

| Task | Library |
|------|---------|
| HTTP networking | Ktor Client |
| JSON serialization | kotlinx.serialization |
| Date/time | kotlinx-datetime |
| Coroutines | kotlinx.coroutines |
| Key-value storage | multiplatform-settings |
| Database | SQLDelight |

```kotlin
// commonMain — networking with Ktor
class UserApi(private val client: HttpClient) {
    suspend fun getUser(id: String): User =
        client.get("https://api.example.com/users/$id").body()
}

// commonMain — serialization
@Serializable
data class User(
    val id: String,
    val name: String,
    @SerialName("created_at") val createdAt: Instant
)
```

## Source Set Hierarchy

Source sets form a hierarchy. Intermediate source sets let you share code between a subset of platforms:

```
commonMain
├── nativeMain          # Shared between all native targets
│   ├── iosMain
│   ├── macosMain
│   └── linuxMain
├── jvmMain             # JVM-only (not Android)
└── androidMain         # Android-only
```

Define intermediate source sets in `build.gradle.kts`:

```kotlin
kotlin {
    sourceSets {
        val nativeMain by creating { dependsOn(commonMain) }
        val iosMain by getting { dependsOn(nativeMain) }
        val macosMain by getting { dependsOn(nativeMain) }
    }
}
```

## Compose Multiplatform

Compose Multiplatform extends Jetpack Compose to iOS, desktop, and web. Shared UI code lives in `commonMain`:

```kotlin
// commonMain — shared UI
@Composable
fun App() {
    MaterialTheme {
        var greeting by remember { mutableStateOf("Hello, World!") }
        Column(modifier = Modifier.padding(16.dp)) {
            Text(text = greeting)
            Button(onClick = { greeting = "Hello, KMP!" }) {
                Text("Click me")
            }
        }
    }
}
```

Platform entry points wire up the shared composable to each platform's window system (Activity on Android, UIViewController on iOS, Window on desktop).

## Testing

Tests in `commonTest` run on all configured platforms:

```kotlin
// commonTest
class UserSerializationTest {
    @Test
    fun `deserializes user from JSON`() {
        val json = """{"id":"1","name":"Alice","created_at":"2024-01-01T00:00:00Z"}"""
        val user = Json.decodeFromString<User>(json)
        assertEquals("Alice", user.name)
    }
}
```

Write platform-specific tests in `androidTest` or `iosTest` only for code that uses platform APIs. Run all tests with `./gradlew allTests` or target a specific platform with `./gradlew iosSimulatorArm64Test`.

## Common Pitfalls

- **Leaking platform types into commonMain.** Keep your shared API surface free of Android or iOS types.
- **Overusing expect/actual.** Every expect declaration is a contract every platform must fulfill. Prefer interfaces for flexibility.
- **Ignoring binary compatibility.** Published KMP libraries need explicit API tracking. Use the `binary-compatibility-validator` plugin.
- **Assuming JVM behavior on native.** Native targets have a different memory model and no reflection. Avoid reflection-based libraries in commonMain.
