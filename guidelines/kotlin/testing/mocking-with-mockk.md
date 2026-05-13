# Mocking with MockK

MockK is the idiomatic mocking library for Kotlin. Unlike Mockito, it natively
supports final classes (all Kotlin classes are final by default), coroutines,
extension functions, and object declarations.

## Creating Mocks

```kotlin
// Standard mock -- unstubbed calls throw
val service = mockk<UserService>()

// Relaxed mock -- returns default values for unstubbed calls
// (0 for Int, "" for String, false for Boolean, empty list, etc.)
val relaxedService = mockk<UserService>(relaxed = true)

// Relaxed only for Unit-returning functions
val partialRelaxed = mockk<UserService>(relaxUnitFun = true)
```

Use relaxed mocks when you only care about verifying interactions and don't
need specific return values. Prefer strict mocks when return values matter,
as they surface unstubbed calls immediately.

## Stubbing with every

```kotlin
every { service.findUser(any()) } returns User("Alice")
every { service.findUser("bob") } throws NotFoundException("bob not found")
every { service.countUsers() } returns 42
every { service.deleteUser(any()) } just runs  // For Unit-returning functions

// Multiple return values in sequence
every { service.generateId() } returnsMany listOf("id-1", "id-2", "id-3")

// Answer block for dynamic responses
every { service.findUser(any()) } answers {
    val id = firstArg<String>()
    User(id, "$id@example.com")
}
```

## Coroutine Stubbing with coEvery

Use `coEvery` for suspend functions:

```kotlin
coEvery { service.fetchUser(any()) } returns User("Alice")
coEvery { service.fetchUser("error") } throws NetworkException()

coEvery { service.saveUser(any()) } coAnswers {
    val user = firstArg<User>()
    user.copy(id = "generated-id")
}
```

## Verification

```kotlin
// Was the function called?
verify { service.findUser("alice") }

// Exact call count
verify(exactly = 1) { service.save(any()) }
verify(exactly = 0) { service.delete(any()) }  // Was NOT called

// At least / at most
verify(atLeast = 2) { service.log(any()) }
verify(atMost = 5) { service.retry(any()) }

// Ordered verification
verify(ordering = Ordering.ORDERED) {
    service.validate(any())
    service.save(any())
    service.notify(any())
}

// Coroutine verification
coVerify { service.fetchUser("alice") }
coVerify(exactly = 1) { service.saveUser(any()) }
```

## Argument Matching and Capturing

```kotlin
// Matchers
every { service.findUsers(any()) } returns emptyList()
every { service.findUsers(match { it.isNotBlank() }) } returns listOf(User("Alice"))
every { service.findUser(eq("alice")) } returns User("Alice")

// Capturing arguments for later inspection
val slot = slot<User>()
every { service.save(capture(slot)) } returns true

service.save(User("Alice", "alice@example.com"))

assertEquals("Alice", slot.captured.name)
assertEquals("alice@example.com", slot.captured.email)

// Capturing multiple invocations
val allUsers = mutableListOf<User>()
every { service.save(capture(allUsers)) } returns true

service.save(User("Alice"))
service.save(User("Bob"))

assertEquals(2, allUsers.size)
```

## Spy -- Partial Mocks

`spyk` creates a wrapper around a real object. Calls go to the real
implementation unless explicitly stubbed:

```kotlin
val list = spyk(mutableListOf(1, 2, 3))

// Override size but keep everything else real
every { list.size } returns 100

assertEquals(100, list.size)       // Stubbed
assertEquals(1, list[0])           // Real implementation
assertEquals(3, list.count())      // Real -- iterates the actual list
```

Use spies sparingly. They are useful for verifying calls on real objects but
can mask design issues if overused.

## Mocking Objects and Static Functions

```kotlin
// Mock an object declaration or companion object
mockkObject(UserDefaults)
every { UserDefaults.defaultRole } returns Role.ADMIN

// Mock top-level or extension functions
mockkStatic("com.example.ExtensionsKt")
every { any<String>().toSlug() } returns "mocked-slug"

// Mock a companion object
mockkObject(User.Companion)
every { User.create("alice") } returns User("alice", "default@example.com")
```

## Cleanup

Always clean up mocks to prevent test pollution:

```kotlin
@AfterEach
fun tearDown() {
    clearMocks(service, repository)  // Clear specific mocks
    // or
    unmockkAll()  // Clear everything
}
```

For `mockkStatic` and `mockkObject`, use the corresponding `unmockk` variants:

```kotlin
@AfterEach
fun tearDown() {
    unmockkStatic("com.example.ExtensionsKt")
    unmockkObject(UserDefaults)
}
```

## Complete Test Example

```kotlin
class OrderServiceTest {
    private val repository = mockk<OrderRepository>()
    private val notifier = mockk<NotificationService>(relaxUnitFun = true)
    private val service = OrderService(repository, notifier)

    @AfterEach
    fun tearDown() = clearMocks(repository, notifier)

    @Test
    fun `should place order and notify customer`() {
        val order = Order(id = "1", item = "Book", quantity = 2)
        every { repository.save(any()) } returns order

        val result = service.placeOrder("Book", 2)

        assertEquals("1", result.id)
        verify { repository.save(match { it.item == "Book" }) }
        verify { notifier.sendConfirmation(order) }
    }
}
```

## Anti-Patterns

**Over-mocking.** If a test mocks more than two or three dependencies, the
class under test likely has too many responsibilities. Refactor before adding
more mocks.

**Mocking data classes.** Don't mock value objects or data classes. Construct
real instances instead -- they are simple and predictable.

**Using Mockito with Kotlin.** Mockito cannot mock final classes by default,
and its API is less natural in Kotlin. Use MockK for all Kotlin projects.

**Not cleaning up.** Failing to call `clearMocks` or `unmockkAll` can cause
flaky tests when mocks leak state between test methods.
