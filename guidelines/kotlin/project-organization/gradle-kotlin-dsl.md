# Gradle Kotlin DSL

Gradle Kotlin DSL (build.gradle.kts) provides type-safe build scripts with IDE
auto-completion and refactoring support. Combined with version catalogs and convention
plugins, it eliminates most boilerplate from multi-module builds.

## Basic build.gradle.kts Structure

A minimal Kotlin/JVM project build file:

```kotlin
plugins {
    kotlin("jvm") version "2.0.0"
    application
}

repositories {
    mavenCentral()
}

dependencies {
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core:1.8.0")
    testImplementation(kotlin("test"))
}

kotlin {
    jvmToolchain(21)
}

application {
    mainClass.set("com.example.MainKt")
}
```

Use `jvmToolchain` to pin the JDK version. Gradle will auto-download the specified
JDK if it is not already installed.

## Version Catalogs

Centralize dependency versions in `gradle/libs.versions.toml` at the project root.
This is the standard Gradle mechanism — no plugins required.

```toml
[versions]
kotlin = "2.0.0"
coroutines = "1.8.0"
ktor = "2.3.9"
koin = "3.5.3"

[libraries]
kotlinx-coroutines-core = { module = "org.jetbrains.kotlinx:kotlinx-coroutines-core", version.ref = "coroutines" }
kotlinx-coroutines-test = { module = "org.jetbrains.kotlinx:kotlinx-coroutines-test", version.ref = "coroutines" }
ktor-server-core = { module = "io.ktor:ktor-server-core", version.ref = "ktor" }
koin-core = { module = "io.insert-koin:koin-core", version.ref = "koin" }

[bundles]
ktor-server = ["ktor-server-core"]

[plugins]
kotlin-jvm = { id = "org.jetbrains.kotlin.jvm", version.ref = "kotlin" }
kotlin-serialization = { id = "org.jetbrains.kotlin.plugin.serialization", version.ref = "kotlin" }
```

Reference catalog entries in build files with type-safe accessors:

```kotlin
plugins {
    alias(libs.plugins.kotlin.jvm)
}

dependencies {
    implementation(libs.kotlinx.coroutines.core)
    implementation(libs.bundles.ktor.server)
    testImplementation(libs.kotlinx.coroutines.test)
}
```

Dots in TOML keys become dot-separated accessors. Dashes and underscores in names
are normalized to dots in the accessor.

## Convention Plugins

Convention plugins extract repeated build logic into reusable scripts. Place them in
`buildSrc/` or a dedicated `build-logic` included build.

```kotlin
// buildSrc/src/main/kotlin/kotlin-conventions.gradle.kts
plugins {
    kotlin("jvm")
}

kotlin {
    jvmToolchain(21)
}

tasks.test {
    useJUnitPlatform()
}

tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile>().configureEach {
    compilerOptions {
        freeCompilerArgs.add("-Xjsr305=strict")
        allWarningsAsErrors = true
    }
}
```

Apply the convention plugin in any module:

```kotlin
// feature/user/build.gradle.kts
plugins {
    id("kotlin-conventions")
}

dependencies {
    implementation(project(":core:domain"))
}
```

For larger projects, use an included build (`build-logic/`) instead of `buildSrc`
to improve build cache effectiveness.

## BOM (Bill of Materials)

Use BOMs to align versions of related libraries without specifying each version:

```kotlin
dependencies {
    implementation(platform("org.jetbrains.kotlinx:kotlinx-coroutines-bom:1.8.0"))
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-core")   // version from BOM
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-reactor") // version from BOM

    implementation(platform("io.ktor:ktor-bom:2.3.9"))
    implementation("io.ktor:ktor-server-core")
}
```

BOMs and version catalogs complement each other — use the catalog to pin the BOM
version, and let the BOM manage individual artifact versions.

## KSP vs KAPT

Prefer KSP (Kotlin Symbol Processing) over KAPT (Kotlin Annotation Processing Tool):

- **KSP** — Kotlin-native, significantly faster, understands Kotlin types directly.
  Used by Room, Moshi, Koin annotations, and others.
- **KAPT** — Legacy bridge to Java annotation processors. Requires generating Java
  stubs first, which slows compilation. Only use when the library has no KSP support.

```kotlin
plugins {
    id("com.google.devtools.ksp") version "2.0.0-1.0.21"
}

dependencies {
    ksp("io.insert-koin:koin-ksp-compiler:1.3.1")
}
```

## Kotlin Compiler Options

Configure compiler options for strictness and interop:

```kotlin
kotlin {
    compilerOptions {
        freeCompilerArgs.addAll(
            "-Xjsr305=strict",            // Strict null checks on Java annotations
            "-opt-in=kotlin.RequiresOptIn" // Enable opt-in annotations
        )
        allWarningsAsErrors = true
    }
}
```

The `-Xjsr305=strict` flag makes `@Nonnull` and `@Nullable` Java annotations
enforce platform type nullability in Kotlin code. Without it, annotated Java
types are treated as platform types (implicitly nullable).

## Common Pitfalls

- Do not mix Groovy DSL (.gradle) and Kotlin DSL (.gradle.kts) in the same project.
  Migrate all build files to Kotlin DSL together.
- Avoid declaring dependency versions directly in module build files when a version
  catalog is available. Inline versions defeat centralized management.
- Do not use `buildscript { }` blocks in Kotlin DSL — use the `plugins { }` block
  with version catalogs instead.
- Avoid `subprojects { }` or `allprojects { }` for applying plugins. Use convention
  plugins for shared configuration.
