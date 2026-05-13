# pathlib and File Operations

## Path Basics

```python
from pathlib import Path

# Construction
path = Path("src") / "mypackage" / "config.yaml"
home = Path.home()
cwd = Path.cwd()

# Properties
path.name         # "config.yaml"
path.stem         # "config"
path.suffix       # ".yaml"
path.parent       # Path("src/mypackage")
path.parts        # ("src", "mypackage", "config.yaml")
```

## Reading and Writing

```python
# Simple read/write (handles open/close automatically)
content = path.read_text(encoding="utf-8")
path.write_text("new content", encoding="utf-8")

data = path.read_bytes()
path.write_bytes(b"binary data")

# For large files, use open() context manager
with path.open(encoding="utf-8") as f:
    for line in f:
        process(line)
```

Always specify `encoding="utf-8"` — the default varies by platform.

## File System Operations

```python
# Check existence and type
path.exists()
path.is_file()
path.is_dir()
path.is_symlink()

# Create directories
path.mkdir(parents=True, exist_ok=True)

# Delete
path.unlink()                    # delete file
path.unlink(missing_ok=True)     # no error if missing (3.8+)
path.rmdir()                     # delete empty directory

# Move/rename
path.rename(new_path)            # same filesystem
path.replace(new_path)           # cross-platform, overwrites target

# Copy (use shutil for this — pathlib has no copy)
import shutil
shutil.copy2(src, dst)           # preserves metadata
shutil.copytree(src_dir, dst_dir)
```

## Globbing

```python
# Find all Python files in a directory
for py_file in Path("src").glob("*.py"):
    print(py_file)

# Recursive glob
for py_file in Path("src").rglob("*.py"):
    print(py_file)

# Multiple patterns
yaml_files = list(Path(".").glob("**/*.yaml"))
json_files = list(Path(".").glob("**/*.json"))
```

## Path Manipulation

```python
# Resolve to absolute path
absolute = path.resolve()

# Relative path
relative = path.relative_to(Path.cwd())

# Change extension
new_path = path.with_suffix(".json")      # config.json
no_ext = path.with_suffix("")             # config

# Change name
new_path = path.with_name("settings.yaml")

# Expand user (~)
path = Path("~/Documents").expanduser()
```

## Anti-Patterns

```python
# Don't use os.path
import os
path = os.path.join("src", "config.yaml")  # use Path("src") / "config.yaml"
exists = os.path.exists(path)               # use Path(path).exists()

# Don't use string concatenation for paths
path = "src/" + "config.yaml"               # breaks on Windows

# Don't forget encoding
content = Path("file.txt").read_text()      # platform-dependent encoding!
content = Path("file.txt").read_text(encoding="utf-8")  # explicit
```

## Temporary Files

```python
import tempfile

# Using pathlib with tempfile
with tempfile.TemporaryDirectory() as tmpdir:
    tmp_path = Path(tmpdir)
    config = tmp_path / "config.yaml"
    config.write_text("key: value")
    process(config)
# Directory and all contents deleted on exit

# In pytest, use the tmp_path fixture instead
def test_output(tmp_path):
    output = tmp_path / "result.txt"
    generate_output(output)
    assert output.read_text() == "expected"
```
