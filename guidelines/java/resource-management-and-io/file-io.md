# File IO with NIO.2

## Always Use Path and Files

The `java.nio.file` package (NIO.2, Java 7+) replaces `java.io.File`.
Never use `java.io.File` in new code:

```java
// Modern — NIO.2
Path path = Path.of("/data", "config.yaml");
String content = Files.readString(path);
List<String> lines = Files.readAllLines(path);
byte[] bytes = Files.readAllBytes(path);

// Legacy — avoid
File file = new File("/data/config.yaml");
```

## Reading Files

```java
// Read entire file as string (small files only)
String content = Files.readString(Path.of("config.yaml"));

// Read lines (small files)
List<String> lines = Files.readAllLines(Path.of("data.csv"));

// Stream lines (large files — lazy, memory-efficient)
try (Stream<String> lines = Files.lines(Path.of("large.log"))) {
    lines.filter(line -> line.contains("ERROR"))
         .forEach(System.out::println);
}

// Read with specific charset
String content = Files.readString(path, StandardCharsets.UTF_8);
```

## Writing Files

```java
// Write string
Files.writeString(Path.of("output.txt"), content);

// Write lines
Files.write(Path.of("output.txt"), lines);

// Write with options
Files.writeString(path, content,
    StandardOpenOption.CREATE,
    StandardOpenOption.TRUNCATE_EXISTING);

// Append
Files.writeString(path, content, StandardOpenOption.APPEND);
```

## Path Operations

```java
Path path = Path.of("/data/users/config.yaml");

path.getFileName();      // config.yaml
path.getParent();        // /data/users
path.resolve("backup");  // /data/users/config.yaml/backup
path.resolveSibling("other.yaml");  // /data/users/other.yaml
path.toAbsolutePath();   // absolute path

// Combine paths
Path base = Path.of("/data");
Path full = base.resolve("users").resolve("config.yaml");
```

## Directory Operations

```java
// Create directories
Files.createDirectories(Path.of("/data/output"));  // creates parents too

// List directory contents
try (var entries = Files.list(Path.of("/data"))) {
    entries.filter(Files::isRegularFile)
           .forEach(System.out::println);
}

// Walk directory tree recursively
try (var walk = Files.walk(Path.of("/data"))) {
    walk.filter(p -> p.toString().endsWith(".java"))
        .forEach(System.out::println);
}

// Find files matching a glob
try (var finder = Files.find(Path.of("/data"), 10,
        (path, attrs) -> path.toString().endsWith(".yaml"))) {
    finder.forEach(System.out::println);
}
```

## File Operations

```java
// Copy
Files.copy(source, target, StandardCopyOption.REPLACE_EXISTING);

// Move (atomic on same filesystem)
Files.move(source, target, StandardCopyOption.ATOMIC_MOVE);

// Delete
Files.deleteIfExists(path);

// Check existence
Files.exists(path);
Files.isRegularFile(path);
Files.isDirectory(path);
```

## Temporary Files

```java
// Create temp file (auto-deleted on JVM exit with deleteOnExit)
Path temp = Files.createTempFile("prefix-", ".tmp");

// Create temp directory
Path tempDir = Files.createTempDirectory("myapp-");
```

For tests, prefer `@TempDir` (JUnit 5) which handles cleanup automatically.

## Atomic File Writes

Write to a temp file, then atomically rename — prevents partial reads:

```java
Path target = Path.of("config.yaml");
Path temp = Files.createTempFile(target.getParent(), ".config-", ".tmp");
try {
    Files.writeString(temp, newContent);
    Files.move(temp, target, StandardCopyOption.ATOMIC_MOVE);
} catch (Exception e) {
    Files.deleteIfExists(temp);  // cleanup on failure
    throw e;
}
```
