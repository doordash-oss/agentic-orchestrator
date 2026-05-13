# Dependency Management

## Comparison

| Factor | vcpkg | Conan 2 | FetchContent | Git Submodules |
|--------|-------|---------|--------------|----------------|
| Binary caching | Yes | Yes | No | No |
| Package ecosystem | 2000+ | 1900+ | CMake projects only | Any |
| Version locking | `builtin-baseline` | `conanfile.lock` | `GIT_TAG` hash | Commit hash |
| CI complexity | Medium | Medium-High | Low | Medium |

## vcpkg: Manifest Mode

```json
{
  "name": "my-project",
  "version": "1.0.0",
  "builtin-baseline": "3426db05b996...",
  "dependencies": [
    "fmt",
    "spdlog",
    { "name": "boost-filesystem", "version>=": "1.83.0" }
  ]
}
```

CMake integration via toolchain file:
```cmake
set(CMAKE_TOOLCHAIN_FILE "$ENV{VCPKG_ROOT}/scripts/buildsystem/vcpkg.cmake")
```

After install, standard `find_package()` works transparently.

## Conan 2

```python
from conan import ConanFile
from conan.tools.cmake import CMake, cmake_layout

class MyProject(ConanFile):
    settings = "os", "compiler", "build_type", "arch"
    generators = "CMakeToolchain", "CMakeDeps"

    def layout(self):
        cmake_layout(self)

    def requirements(self):
        self.requires("fmt/10.1.0")
        self.requires("spdlog/1.12.0")
```

```bash
conan install . --output-folder=build --build=missing
cmake --preset conan-debug
```

CMakeLists.txt remains Conan-agnostic — uses `find_package()`.

## FetchContent

Best for test frameworks and small CMake-native dependencies:

```cmake
include(FetchContent)
FetchContent_Declare(googletest
    GIT_REPOSITORY https://github.com/google/googletest.git
    GIT_TAG v1.14.0
    GIT_SHALLOW TRUE
)
FetchContent_MakeAvailable(googletest)
target_link_libraries(tests PRIVATE GTest::gtest_main)
```

**Limitations**: requires internet at configure time, builds everything from
source, no binary caching.

## Git Submodules

Use only when: offline builds required, significant patches needed, or
air-gapped environments. Developers frequently forget
`git submodule update --init --recursive`.

## Guidelines

- **vcpkg**: many third-party deps, binary caching, large package registry
- **Conan 2**: fine-grained build settings, multi-platform profiles, packaging own libraries
- **FetchContent**: test frameworks, handful of CMake-native deps, self-contained CI
- **Submodules**: avoid for large dependency graphs in new projects
