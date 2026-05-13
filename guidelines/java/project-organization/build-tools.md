# Build Tools

## Maven Best Practices

### POM Structure

```xml
<project>
    <parent>
        <groupId>com.example</groupId>
        <artifactId>parent</artifactId>
        <version>1.0.0</version>
    </parent>

    <artifactId>my-service</artifactId>

    <!-- Pin versions in dependencyManagement, not in dependencies -->
    <dependencyManagement>
        <dependencies>
            <!-- Import a BOM for version alignment -->
            <dependency>
                <groupId>org.springframework.boot</groupId>
                <artifactId>spring-boot-dependencies</artifactId>
                <version>${spring-boot.version}</version>
                <type>pom</type>
                <scope>import</scope>
            </dependency>
        </dependencies>
    </dependencyManagement>

    <dependencies>
        <!-- No version needed — managed by BOM -->
        <dependency>
            <groupId>org.springframework.boot</groupId>
            <artifactId>spring-boot-starter-web</artifactId>
        </dependency>
    </dependencies>
</project>
```

### Maven Rules

- **Use a parent POM** for multi-module projects — share plugin config and versions
- **Use BOMs** (`<scope>import</scope>`) for version alignment (Spring, Jackson, etc.)
- **Pin plugin versions** — reproducible builds require explicit versions
- **Never use SNAPSHOT dependencies in releases**
- **Use `<dependencyManagement>`** for version declaration, `<dependencies>` for actual usage

## Gradle Best Practices (Kotlin DSL)

### Version Catalogs (Gradle 7.4+)

Centralize dependency versions in `gradle/libs.versions.toml`:

```toml
[versions]
spring-boot = "3.2.0"
junit = "5.10.1"
assertj = "3.25.1"

[libraries]
spring-boot-starter-web = { module = "org.springframework.boot:spring-boot-starter-web", version.ref = "spring-boot" }
junit-jupiter = { module = "org.junit.jupiter:junit-jupiter", version.ref = "junit" }
assertj-core = { module = "org.assertj:assertj-core", version.ref = "assertj" }

[bundles]
testing = ["junit-jupiter", "assertj-core"]
```

Use in `build.gradle.kts`:

```kotlin
dependencies {
    implementation(libs.spring.boot.starter.web)
    testImplementation(libs.bundles.testing)
}
```

### Dependency Scopes

```kotlin
dependencies {
    // implementation — compile + runtime, NOT exposed to consumers
    implementation(libs.jackson.core)

    // api — compile + runtime, exposed to consumers (use sparingly)
    api(libs.domain.api)

    // compileOnly — compile only (annotations, provided deps)
    compileOnly(libs.lombok)

    // testImplementation — test compile + runtime
    testImplementation(libs.bundles.testing)

    // runtimeOnly — runtime only (JDBC drivers, logging implementations)
    runtimeOnly(libs.postgresql.driver)
}
```

**Rule**: default to `implementation`. Use `api` only when the dependency
type appears in your public API signatures.

### Convention Plugins

Share build logic across modules with convention plugins:

```kotlin
// buildSrc/src/main/kotlin/java-conventions.gradle.kts
plugins {
    java
}

java {
    toolchain {
        languageVersion.set(JavaLanguageVersion.of(21))
    }
}

tasks.test {
    useJUnitPlatform()
    jvmArgs("-XX:+EnableDynamicAgentLoading")
}
```

## Common Build Rules

1. **Lock the Java version** — use toolchains (Gradle) or `maven.compiler.release`
2. **Run tests with `-XX:+EnableDynamicAgentLoading`** — required for Mockito on Java 21+
3. **Enable `-Werror` in CI** — treat warnings as errors
4. **Separate test source sets** — unit tests in `test/`, integration tests in `integrationTest/`
5. **Cache aggressively** — enable Gradle build cache, use Maven's `-T` for parallel builds
6. **Reproducible builds** — pin all versions, use lock files if available
