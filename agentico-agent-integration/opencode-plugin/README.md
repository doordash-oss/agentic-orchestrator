# OpenCode Plugin Wrapper

This is a thin wrapper around the shared `../skills` content.

Expose the shared `agentico-create-feature` and `agentico-manage-feature` skills to OpenCode without copying their instructions. The wrapper may provide OpenCode-specific manifest metadata, but runtime behavior must stay in the shared skill files and must use only `agentico ... --json` commands.
