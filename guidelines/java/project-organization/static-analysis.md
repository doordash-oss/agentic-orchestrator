# Static Analysis

## Why Static Analysis

Static analysis tools catch real bugs at compile time — null dereferences,
resource leaks, concurrency errors, and common mistakes that tests might miss.
Run them in CI; fail the build on violations.

## Error Prone (Google)

Error Prone hooks into the Java compiler and flags common mistakes as compile
errors:

```xml
<!-- Maven configuration -->
<plugin>
    <groupId>org.apache.maven.plugins</groupId>
    <artifactId>maven-compiler-plugin</artifactId>
    <configuration>
        <compilerArgs>
            <arg>-XDcompilePolicy=simple</arg>
            <arg>-Xplugin:ErrorProne</arg>
        </compilerArgs>
        <annotationProcessorPaths>
            <path>
                <groupId>com.google.errorprone</groupId>
                <artifactId>error_prone_core</artifactId>
                <version>2.24.0</version>
            </path>
        </annotationProcessorPaths>
    </configuration>
</plugin>
```

Error Prone catches:
- `DeadException` — exception created but never thrown
- `MissingOverride` — missing `@Override` annotation
- `FutureReturnValueIgnored` — ignoring a `Future` result
- `StreamResourceLeak` — unclosed stream
- `ImmutableEnumChecker` — mutable enum fields

## NullAway (Uber)

Built on Error Prone, focused on null safety:

```gradle
// Gradle
dependencies {
    errorprone("com.uber.nullaway:nullaway:0.10.18")
}
```

Requires `@NullMarked`/`@Nullable` annotations (JSpecify recommended).
Catches null dereferences at compile time with near-zero runtime cost.

## SpotBugs

Bytecode analysis tool (successor to FindBugs):

```xml
<!-- Maven -->
<plugin>
    <groupId>com.github.spotbugs</groupId>
    <artifactId>spotbugs-maven-plugin</artifactId>
    <version>4.8.2.0</version>
    <executions>
        <execution>
            <goals><goal>check</goal></goals>
        </execution>
    </executions>
</plugin>
```

SpotBugs finds:
- Null pointer dereferences
- Infinite loops
- Synchronization issues
- Resource leaks
- Suspicious equality comparisons

## Checkstyle

Enforces coding style conventions:

```xml
<!-- Maven -->
<plugin>
    <groupId>org.apache.maven.plugins</groupId>
    <artifactId>maven-checkstyle-plugin</artifactId>
    <configuration>
        <configLocation>google_checks.xml</configLocation>
        <violationSeverity>warning</violationSeverity>
    </configuration>
</plugin>
```

Use Google's or Sun's checks as a starting point, then customize.

## Recommended CI Pipeline

```yaml
# Run in order of feedback speed
steps:
  - name: Compile with Error Prone
    run: mvn compile  # Error Prone runs during compilation

  - name: Unit Tests
    run: mvn test

  - name: SpotBugs
    run: mvn spotbugs:check

  - name: Checkstyle
    run: mvn checkstyle:check
```

## Choosing Tools

| Tool | Type | Best For |
|------|------|----------|
| **Error Prone** | Compiler plugin | Bug patterns, correctness |
| **NullAway** | Compiler plugin | Null safety |
| **SpotBugs** | Bytecode analyzer | Deep bug detection |
| **Checkstyle** | Source analyzer | Style enforcement |
| **PMD** | Source analyzer | Code complexity, dead code |
| **ArchUnit** | Test library | Architecture rule enforcement |

For a new project, start with **Error Prone + NullAway** (compile-time,
fast feedback) and add **SpotBugs** for deeper analysis.
