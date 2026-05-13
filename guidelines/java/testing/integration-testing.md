# Integration Testing

## Testcontainers

Run real databases, message brokers, and services in Docker containers
during tests:

```java
@Testcontainers
class OrderRepositoryIntegrationTest {

    @Container
    static PostgreSQLContainer<?> postgres = new PostgreSQLContainer<>("postgres:16")
        .withDatabaseName("test")
        .withUsername("test")
        .withPassword("test");

    @DynamicPropertySource
    static void configureProperties(DynamicPropertyRegistry registry) {
        registry.add("spring.datasource.url", postgres::getJdbcUrl);
        registry.add("spring.datasource.username", postgres::getUsername);
        registry.add("spring.datasource.password", postgres::getPassword);
    }

    @Test
    void shouldPersistAndRetrieveOrder() {
        repository.save(testOrder);
        var found = repository.findById(testOrder.id());
        assertThat(found).isPresent().hasValueSatisfying(order ->
            assertThat(order.total()).isEqualByComparingTo(testOrder.total()));
    }
}
```

**When to use Testcontainers**:
- Database integration tests (PostgreSQL, MySQL, MongoDB)
- Message broker tests (Kafka, RabbitMQ)
- Cache tests (Redis)
- Any external service with a Docker image

## WireMock — HTTP Service Stubbing

Stub external HTTP APIs:

```java
@WireMockTest(httpPort = 8089)
class PaymentClientTest {

    @Test
    void shouldHandleSuccessResponse() {
        stubFor(post("/api/payments")
            .willReturn(okJson("""
                {"status": "SUCCESS", "transactionId": "txn-123"}
                """)));

        var result = client.charge(paymentRequest);

        assertThat(result.status()).isEqualTo("SUCCESS");
    }

    @Test
    void shouldHandleTimeout() {
        stubFor(post("/api/payments")
            .willReturn(ok().withFixedDelay(5000)));  // simulate timeout

        assertThatThrownBy(() -> client.charge(paymentRequest))
            .isInstanceOf(PaymentTimeoutException.class);
    }
}
```

## Spring Boot Test Slices

Load only the parts of the application context needed for the test:

```java
// Full context — slow, use sparingly
@SpringBootTest
class FullIntegrationTest { }

// Web layer only — controllers, filters, interceptors
@WebMvcTest(OrderController.class)
class OrderControllerTest {
    @Autowired MockMvc mockMvc;
    @MockBean OrderService orderService;

    @Test
    void shouldReturn404ForMissingOrder() throws Exception {
        when(orderService.findById("999")).thenReturn(Optional.empty());

        mockMvc.perform(get("/api/orders/999"))
            .andExpect(status().isNotFound());
    }
}

// JPA layer only — repository, entity manager
@DataJpaTest
class OrderRepositoryTest {
    @Autowired OrderRepository repository;
    @Autowired TestEntityManager entityManager;
}

// JSON serialization only
@JsonTest
class OrderJsonTest {
    @Autowired JacksonTester<Order> json;
}
```

## Test Data Builders

Create readable, maintainable test data:

```java
// Builder pattern for test data
public class TestOrders {
    public static Order.Builder anOrder() {
        return Order.builder()
            .id(UUID.randomUUID().toString())
            .customer(TestCustomers.aCustomer().build())
            .items(List.of(TestItems.anItem().build()))
            .status(OrderStatus.PENDING);
    }
}

// Usage in tests
var order = TestOrders.anOrder()
    .status(OrderStatus.SHIPPED)
    .build();
```

## Integration Test Best Practices

1. **Use `@Transactional` for database tests** — automatically rolls back after each test
2. **Share containers across tests** — use `static` containers with `@Container` to avoid per-test startup
3. **Test slices over full context** — `@WebMvcTest` and `@DataJpaTest` are much faster than `@SpringBootTest`
4. **Separate integration tests from unit tests** — use Maven profiles or Gradle source sets:
   ```
   src/test/java/          — unit tests
   src/integrationTest/java/ — integration tests (separate source set)
   ```
5. **Keep integration tests focused** — test one integration point per test class
6. **Use `@DynamicPropertySource`** — inject container URLs/ports into Spring config
