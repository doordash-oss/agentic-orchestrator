# HTTP Client and Serialization

## java.net.http.HttpClient (Java 11+)

The built-in HTTP client is sufficient for most needs — no third-party
dependency required:

```java
HttpClient client = HttpClient.newBuilder()
    .connectTimeout(Duration.ofSeconds(10))
    .followRedirects(HttpClient.Redirect.NORMAL)
    .build();

// Synchronous GET
HttpRequest request = HttpRequest.newBuilder()
    .uri(URI.create("https://api.example.com/users/123"))
    .header("Accept", "application/json")
    .GET()
    .build();

HttpResponse<String> response = client.send(request, BodyHandlers.ofString());
if (response.statusCode() == 200) {
    User user = objectMapper.readValue(response.body(), User.class);
}
```

### Async Requests

```java
client.sendAsync(request, BodyHandlers.ofString())
    .thenApply(HttpResponse::body)
    .thenApply(body -> objectMapper.readValue(body, User.class))
    .thenAccept(this::processUser)
    .exceptionally(ex -> { log.error("request failed", ex); return null; });
```

### POST with JSON Body

```java
String json = objectMapper.writeValueAsString(createRequest);

HttpRequest request = HttpRequest.newBuilder()
    .uri(URI.create("https://api.example.com/orders"))
    .header("Content-Type", "application/json")
    .POST(BodyPublishers.ofString(json))
    .build();
```

## JSON with Jackson

Jackson is the standard JSON library for Java:

```java
ObjectMapper mapper = new ObjectMapper()
    .registerModule(new JavaTimeModule())          // Java 8 date/time support
    .disable(SerializationFeature.WRITE_DATES_AS_TIMESTAMPS)
    .disable(DeserializationFeature.FAIL_ON_UNKNOWN_PROPERTIES);

// Serialize
String json = mapper.writeValueAsString(order);

// Deserialize
Order order = mapper.readValue(json, Order.class);

// With generics (TypeReference)
List<Order> orders = mapper.readValue(json, new TypeReference<List<Order>>() {});
```

### Jackson with Records

Records work out of the box with Jackson 2.12+:

```java
public record CreateOrderRequest(String customerId, List<Item> items) {}

// Deserializes automatically using the canonical constructor
var request = mapper.readValue(json, CreateOrderRequest.class);
```

### Jackson Annotations

```java
public record User(
    String id,
    @JsonProperty("user_name") String name,     // rename in JSON
    @JsonIgnore String internalField,             // exclude from JSON
    @JsonFormat(pattern = "yyyy-MM-dd") LocalDate joinDate
) {}
```

## Avoid Java Serialization

Java's built-in `Serializable` mechanism has fundamental security and
compatibility problems:

- **Security vulnerabilities** — deserialization can execute arbitrary code
- **Brittle versioning** — changing a class can break serialized data
- **Performance** — slower than JSON or protobuf
- **Complexity** — `serialVersionUID`, custom `readObject`/`writeObject`

```java
// Wrong — Java serialization
public class Order implements Serializable { ... }
ObjectOutputStream oos = new ObjectOutputStream(fos);
oos.writeObject(order);

// Correct — use JSON
String json = objectMapper.writeValueAsString(order);
Files.writeString(path, json);
```

For cross-service communication, use JSON (Jackson), Protocol Buffers, or
Avro instead of Java serialization.

## HTTP Client Best Practices

1. **Reuse HttpClient instances** — they manage connection pools internally
2. **Set timeouts** — both connect and request timeouts
3. **Handle non-2xx status codes** — don't assume success
4. **Use async for multiple concurrent requests** — `sendAsync` with `CompletableFuture`
5. **Log request/response for debugging** — but never log sensitive headers (auth tokens)

```java
// Reuse a single client (thread-safe)
private static final HttpClient HTTP_CLIENT = HttpClient.newBuilder()
    .connectTimeout(Duration.ofSeconds(5))
    .build();

// Check status codes
if (response.statusCode() >= 400) {
    throw new ApiException("API error: " + response.statusCode(), response.body());
}
```
