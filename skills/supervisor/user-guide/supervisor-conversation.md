# Supervisor conversation

The **Supervisor** page keeps one conversation across process restarts. Choose a harness and model from the chip beside the composer, then send a message. The chip always shows the settings in force.

You can change the model or effort while the supervisor is idle or working. Claude restarts its process for either change; OpenCode restarts for an effort change. The next message relaunches with the conversation history. Codex applies either change to its current thread, and OpenCode applies a model change to its current session.

To move this conversation to another harness, open the chip and choose **Switch to Claude…**, **Switch to Codex…**, or **Switch to OpenCode…**. Choosing a model under another harness opens the same dialog with that model selected. The dialog explains that the conversation is rebuilt for the new harness on the next message. While a process is active, its sub-agents and background shells do not carry over. Previous messages and tool history do carry over. Leave model and effort untouched to use the destination's defaults, or choose them in the dialog. Confirm with **Switch**.

During a turn or launch, the change appears in a tray above the composer. The tray names the target and when it will apply. **Cancel** discards it. **Stop turn & apply** interrupts the current turn so the change can apply sooner. The chip keeps showing the previous committed choice until the change succeeds. A notice in the transcript confirms a change or explains a refusal.

If the new harness fails to launch, the previous harness and its session are restored. The failure card names both harnesses. **Retry** tries the switch again and sends the text in the composer; you can edit that text first. Sending a new message normally continues on the restored harness.

Type `/model` or `/effort` in the composer to open the corresponding picker. Add a model id, alias, display name, or supported effort level to change directly, for example `/effort high`. A unique model on another harness opens the switch dialog with that model selected. Unknown or ambiguous model text opens the filtered picker. These commands do not send a message.
