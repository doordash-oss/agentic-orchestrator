# Structured Concurrency (Preview)

Structured concurrency (JEP 453/505/525) ties subtask lifetimes to a
lexical scope, ensuring no threads are left behind when a block exits.
Available as a preview feature since Java 21.

## Core Pattern

```java
try (var scope = StructuredTaskScope.open()) {
    Subtask<User> userTask = scope.fork(() -> fetchUser(id));
    Subtask<List<Order>> ordersTask = scope.fork(() -> fetchOrders(id));

    scope.join();  // wait for all subtasks

    return new UserProfile(userTask.get(), ordersTask.get());
}
// All subtask threads are guaranteed terminated here
```

## Shutdown Policies

### ShutdownOnFailure — All Must Succeed

Cancels all subtasks if any one fails. Use when you need all results:

```java
try (var scope = StructuredTaskScope.open(Joiner.allSuccessfulOrThrow())) {
    Subtask<User> user = scope.fork(() -> fetchUser(id));
    Subtask<List<Order>> orders = scope.fork(() -> fetchOrders(id));

    scope.join();  // throws if any subtask failed

    return new UserProfile(user.get(), orders.get());
}
```

### ShutdownOnSuccess — First to Succeed Wins

Cancels remaining subtasks once one succeeds. Use for racing/fallback:

```java
try (var scope = StructuredTaskScope.open(Joiner.anySuccessfulResultOrThrow())) {
    scope.fork(() -> fetchFromCache(key));
    scope.fork(() -> fetchFromDatabase(key));

    return scope.join();  // returns first successful result
}
```

## Why Not Just ExecutorService?

`ExecutorService` allows unstructured concurrency — subtasks can outlive their
parent, leak threads, and make error propagation complex:

```java
// Unstructured — what if fetchUser fails after fetchOrders is submitted?
ExecutorService executor = ...;
Future<User> user = executor.submit(() -> fetchUser(id));
Future<List<Order>> orders = executor.submit(() -> fetchOrders(id));
// orders keeps running even if user failed and we don't need results
```

Structured concurrency ensures:
- **No thread leaks** — all subtask threads terminate before the scope closes
- **Automatic cancellation** — when the scope shuts down, remaining subtasks
  are interrupted
- **Clear error handling** — exceptions propagate to the join point
- **Observable hierarchy** — thread dumps show parent-child relationships

## Best Practices

1. **Always use try-with-resources** — the scope must be closed
2. **Don't fork from non-owner threads** — only the thread that opened the
   scope can fork subtasks
3. **Check subtask state before `get()`** — avoids exceptions from failed tasks:
   ```java
   if (task.state() == Subtask.State.SUCCESS) {
       return task.get();
   }
   ```
4. **Combine with virtual threads** — structured concurrency uses virtual
   threads by default for forked subtasks
5. **Use Scoped Values (JEP 481)** for context propagation through the
   hierarchy, instead of `ThreadLocal`

## Migrating from ExecutorService

```java
// Before — unstructured
ExecutorService exec = Executors.newFixedThreadPool(10);
Future<A> a = exec.submit(() -> fetchA());
Future<B> b = exec.submit(() -> fetchB());
return combine(a.get(), b.get());

// After — structured
try (var scope = StructuredTaskScope.open(Joiner.allSuccessfulOrThrow())) {
    var a = scope.fork(() -> fetchA());
    var b = scope.fork(() -> fetchB());
    scope.join();
    return combine(a.get(), b.get());
}
```

**Note**: structured concurrency is still a preview feature. The API may
change between Java versions. Check the latest JEP for your Java version.
