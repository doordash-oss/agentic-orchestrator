# Mocking with Mockito

## When to Mock

**Mock at system boundaries** — external services, databases, file systems,
clocks, network calls. These are slow, non-deterministic, or unavailable in tests.

**Don't mock** things you own unless they are expensive to construct. Prefer
real objects, fakes, or in-memory implementations:

```java
// Good — mock an external HTTP service
var httpClient = mock(HttpClient.class);

// Good — mock a database repository
var repository = mock(OrderRepository.class);

// Bad — mocking a simple value object
var order = mock(Order.class);  // just create a real Order

// Bad — mocking everything your class depends on
// If you're mocking 5+ dependencies, the class has too many responsibilities
```

## Basic Mockito Patterns

```java
// Create mock
OrderRepository repository = mock(OrderRepository.class);

// Stub behavior
when(repository.findById("123")).thenReturn(Optional.of(testOrder));
when(repository.findById("missing")).thenReturn(Optional.empty());
when(repository.save(any())).thenAnswer(inv -> inv.getArgument(0));

// Stub to throw
when(repository.findById("error")).thenThrow(new DatabaseException("timeout"));

// Verify interactions
verify(repository).save(any(Order.class));
verify(repository, times(1)).findById("123");
verify(repository, never()).delete(any());
```

## Argument Captors

Capture and assert on arguments passed to mocked methods:

```java
@Captor
ArgumentCaptor<Order> orderCaptor;

@Test
void shouldSaveOrderWithDiscount() {
    service.createOrder(request);

    verify(repository).save(orderCaptor.capture());
    assertThat(orderCaptor.getValue().discount()).isEqualTo(0.10);
}
```

## BDD Style (Given/When/Then)

```java
import static org.mockito.BDDMockito.*;

@Test
void shouldReturnUserProfile() {
    // Given
    given(userRepo.findById("123")).willReturn(Optional.of(testUser));
    given(orderRepo.findByUserId("123")).willReturn(List.of(testOrder));

    // When
    UserProfile profile = service.getProfile("123");

    // Then
    then(userRepo).should().findById("123");
    assertThat(profile.name()).isEqualTo("Alice");
}
```

## Annotations Setup

```java
@ExtendWith(MockitoExtension.class)
class OrderServiceTest {

    @Mock OrderRepository repository;
    @Mock PaymentService payments;
    @InjectMocks OrderService service;  // injects mocks into constructor

    @Test
    void shouldProcessOrder() {
        when(payments.charge(any())).thenReturn(PaymentResult.success());
        // ...
    }
}
```

## Mocking Anti-Patterns

```java
// Wrong — verifying every call (brittle, over-specified)
verify(service).validate(order);
verify(service).calculateTax(order);
verify(service).applyDiscount(order);
verify(service).save(order);
// Tests break when you reorder internal steps

// Better — verify the outcome, not the journey
assertThat(result.total()).isEqualTo(expectedTotal);
verify(repository).save(any());  // only verify the important side effect

// Wrong — mocking concrete classes (fragile, tests internals)
var service = mock(OrderService.class);  // don't mock the class under test!

// Wrong — when/thenReturn for void methods
when(service.notify(any()));  // compile error for void methods
// Fix: doNothing().when(service).notify(any());

// Wrong — too many mocks (code smell)
// If you need 5+ mocks, the class under test has too many dependencies
// Refactor the production code, not the test
```

## Strict vs Lenient Stubbing

Mockito 3+ uses strict stubbing by default — it flags unused stubs as errors.
This is good. Don't disable it. If a stub is unused, either:
- Remove it (it's dead test code)
- The test isn't calling the code path you think it is

```java
// If you must be lenient for a specific stub
lenient().when(repo.findById(any())).thenReturn(Optional.empty());
```

## Spies — Partial Mocking

Use sparingly. Spies wrap real objects and let you stub specific methods:

```java
var realService = new OrderService(realRepo, realPayments);
var spy = spy(realService);
doReturn(mockResult).when(spy).expensiveOperation();

// The rest of the methods call the real implementation
```

**Prefer fakes over spies** — a hand-written in-memory implementation is
clearer than partial mocking.
