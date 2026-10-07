# Supervisor conversation

The **Supervisor** page keeps one conversation across process restarts. Choose a harness and model from the chip beside the composer, then send a message. The chip always shows the settings in force.

You can change the model or effort while the supervisor is idle or working. Claude restarts its process for either change; OpenCode restarts for an effort change. The next message relaunches with the conversation history. Codex applies either change to its current thread, and OpenCode applies a model change to its current session.

During a turn or launch, the change appears in a tray above the composer. The tray names the target and when it will apply. **Cancel** discards it. **Stop turn & apply** interrupts the current turn so the change can apply sooner. The chip keeps showing the previous committed choice until the change succeeds. A notice in the transcript confirms a change or explains a refusal.

Type `/model` or `/effort` in the composer to open the corresponding picker. Add a model id, alias, display name, or supported effort level to change directly, for example `/effort high`. Unknown or ambiguous model text opens the filtered picker. These commands do not send a message.
