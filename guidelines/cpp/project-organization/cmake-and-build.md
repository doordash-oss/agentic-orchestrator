# CMake and Build System

## Modern CMake: Think in Targets

Use exclusively `target_*` commands. Never use directory-level settings:

```cmake
# CORRECT: target-based
target_compile_options(mylib PRIVATE -Wall -Wextra)
target_include_directories(mylib PUBLIC include/ PRIVATE src/)
target_link_libraries(mylib PRIVATE ZLIB::ZLIB PUBLIC fmt::fmt)

# BANNED: global scope
add_compile_options(-Wall)     # Affects all targets
include_directories(include/)  # Same problem
```

## PUBLIC / PRIVATE / INTERFACE

| Keyword | Compiles into target | Propagates to consumers |
|---------|---------------------|------------------------|
| PRIVATE | Yes | No |
| PUBLIC | Yes | Yes |
| INTERFACE | No | Yes |

Warning flags must be **PRIVATE** — never force warnings on consumers.

## Compile Features and C++ Standard

```cmake
target_compile_features(mylib PUBLIC cxx_std_17)
set_target_properties(mylib PROPERTIES CXX_EXTENSIONS OFF)
```

Never set `-std=c++17` manually.

## Warning Flags via Generator Expressions

```cmake
target_compile_options(project_warnings INTERFACE
    $<$<OR:$<CXX_COMPILER_ID:GNU>,$<CXX_COMPILER_ID:Clang>>:
        -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion>
    $<$<CXX_COMPILER_ID:MSVC>: /W4 /permissive->
    $<$<BOOL:${ENABLE_WERROR}>:
        $<$<OR:$<CXX_COMPILER_ID:GNU>,$<CXX_COMPILER_ID:Clang>>:-Werror>
        $<$<CXX_COMPILER_ID:MSVC>:/WX>>
)
```

Gate `-Werror` behind an option — enable only in CI.

## CMakePresets.json

```json
{
  "version": 6,
  "configurePresets": [
    {
      "name": "base", "hidden": true,
      "generator": "Ninja",
      "binaryDir": "${sourceDir}/build/${presetName}",
      "cacheVariables": { "CMAKE_EXPORT_COMPILE_COMMANDS": "ON" }
    },
    {
      "name": "debug", "inherits": "base",
      "cacheVariables": { "CMAKE_BUILD_TYPE": "Debug" }
    },
    {
      "name": "ci-linux", "inherits": "debug",
      "cacheVariables": { "ENABLE_WERROR": "ON" }
    }
  ]
}
```

Check `CMakePresets.json` into source control; gitignore `CMakeUserPresets.json`.

## Namespaced Alias Targets

```cmake
add_library(Bar STATIC bar.cpp)
add_library(Foo::Bar ALIAS Bar)
# Both internal and external consumers: target_link_libraries(app PRIVATE Foo::Bar)
```

## Installable Packages

Use `GNUInstallDirs`, `install(EXPORT ...)`, and `configure_package_config_file`
to generate config packages so consumers use `find_package(MyLib)` with proper
target-based linking.
