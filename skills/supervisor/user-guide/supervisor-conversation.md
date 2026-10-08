# Supervisor conversation

The **Supervisor** page keeps one conversation across process restarts. Choose a harness and model from the chip beside the composer, then send a message. The chip always shows the settings in force.

The page opens on the newest messages. Scroll toward the top to load earlier ones: **Loading earlier messages…** appears above the first row while a page loads, and the messages you were reading stay in place. If a page fails, **Couldn't load earlier messages** appears instead; choose **Retry** to load it again. After a reconnect or a server switch, the page shows only the newest messages again, and earlier ones load as you scroll.

If the server finds a damaged line in the saved conversation when it starts, it keeps every message before that line and adds a notice to the transcript. The notice says how many records could not be read and where the server saved the original file. That copy, `transcript.jsonl.corrupt-<time>`, is next to the conversation transcript in the server's state directory. Nothing in it is shown or sent to the harness. Keep it if you want to inspect or recover the unread records; deleting it does not affect the conversation.

The ring beside the chip shows how much of the current harness's context window is in use. It warns at 80% or above. The empty ring means the current process has not reported usage yet; after you end the process it returns to this state.

When Claude or Codex compacts its conversation, a **Conversation compacted** notice appears in the transcript. On Claude, open **Show summary** to read the condensed history. Codex keeps an opaque native checkpoint, so its notice has no summary to expand. Agentico saves the checkpoint for later relaunches and uses a readable Claude summary when moving to another harness. Agentico never starts a compaction itself.

You can change the model or effort while the supervisor is idle or working. Claude restarts its process for either change; OpenCode restarts for an effort change. The next message relaunches with the conversation history. Codex applies either change to its current thread, and OpenCode applies a model change to its current session.

To move this conversation to another harness, open the chip and choose **Switch to Claude…**, **Switch to Codex…**, or **Switch to OpenCode…**. Choosing a model under another harness opens the same dialog with that model selected. The dialog explains that the conversation is rebuilt for the new harness on the next message. While a process is active, its sub-agents and background shells do not carry over. Previous messages and tool history do carry over. Leave model and effort untouched to use the destination's defaults, or choose them in the dialog. Confirm with **Switch**.

During a turn or launch, the change appears in a tray above the composer. The tray names the target and when it will apply. **Cancel** discards it. **Stop turn & apply** interrupts the current turn so the change can apply sooner. The chip keeps showing the previous committed choice until the change succeeds. A notice in the transcript confirms a change or explains a refusal.

If the new harness fails to launch, the previous harness and its session are restored. The failure card names both harnesses. **Retry** tries the switch again and sends the text in the composer; you can edit that text first. Sending a new message normally continues on the restored harness.

Type `/model` or `/effort` in the composer to open the corresponding picker. Add a model id, alias, display name, or supported effort level to change directly, for example `/effort high`. A unique model on another harness opens the switch dialog with that model selected. Unknown or ambiguous model text opens the filtered picker. These commands do not send a message.

## Composing messages

Attach images and files with the **+** button beside the composer, or paste or drop them onto it. On a local server the files are attached by path; on a remote server they upload first, and **Send** waits until every upload is ready. Remove an attachment that failed or that shows **Staged on another server** to send. A message may be attachments alone. The server keeps a copy of each file with the conversation, so the harness can still read it after a relaunch or a harness switch. The transcript shows one chip per attachment under your message.

Your draft, its attachments and any queued messages are kept per server while the app is open. Opening a feature or switching servers and coming back restores them. Quitting the app clears them.

## Queued messages

A message you send while the supervisor is working waits in **Queued messages** above the composer instead of interrupting. When the turn finishes, the first queued message is sent, then the next one after that turn, and so on. If a turn is stopped, fails or is cut by a restart, the queue waits for you. A message you send from the composer goes ahead of queued ones, and the queue resumes after that turn finishes. Each queued message has three actions:

- **Edit** moves it back into the composer, after any text already there.
- **Send now** stops the current turn and sends this message next.
- **Remove** drops it.

While the supervisor is waiting for you to answer a request, answer it first; nothing is queued until then.

Press **Escape** while the supervisor is working to stop the turn, the same as **Stop**. Escape closes an open list, popover or dialog first and does not stop the turn in that case.

## New conversation

Choose **New conversation** in the toolbar, the command palette or the **Navigate** menu, or type `/new` in the composer, to start over. The harness, model and effort stay the same. The previous conversation stays in the server's state directory. If a turn is running or messages are queued, a dialog asks you to confirm: the turn is stopped and queued messages are discarded. Your draft carries over to the new conversation, and `/new <text>` leaves `<text>` in the composer without sending it.
