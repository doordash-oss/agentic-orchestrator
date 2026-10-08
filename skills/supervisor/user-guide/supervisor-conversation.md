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

## Monitoring a feature on Slack

Ask “Monitor this feature and update me on Slack” to request DMs for phase changes, blockers, questions, permissions and review decisions. The supervisor also listens for your replies: answer in the update's thread or send a message in the same DM to direct the work. You can ask it to pause, retry, change configuration or take another supported action, and it confirms the result in Slack. You do not need to repeat the instruction in the app.

The default check interval is about a minute, and the watch continues through questions, failures and pauses until the feature is done or you stop monitoring. The supervisor confirms the actual cadence and monitor lifetime when it starts. Slack access and a supported monitoring mechanism must be available; a session-scoped watch can expire or end on a restart or harness switch. A stopped supervisor cannot listen for Slack replies, so return to the app to resume it. “Stop monitoring” ends updates without stopping the feature.

The supervisor's [Slack monitoring playbook](../slack-monitoring.md) defines these defaults and how it handles replies.

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

## Supervisor status outside the page

The pinned **Supervisor** row in the sidebar shows the supervisor's state from any page:

- **Needs response** — the supervisor is waiting on you. The row is marked for attention and its subline says **Approve 1 request** for a permission or **Answer 1 question** for a question.
- **Working** — the supervisor is starting or running a turn. The row shows a progress marker and the subline **Working**.
- **Error** — the supervisor's process failed. The row is marked as an error and the subline is the failure title.
- **Unread** — a turn finished while the Supervisor page was not selected or the window was not focused. The row shows a dot, which clears when you select the Supervisor page with the window focused. Stopped, failed and interrupted turns never set it. The dot is kept in memory for each server and is cleared when you quit the app.

Needs response takes precedence over working, working over error, and error over unread. An idle, stopped or paused supervisor shows no marker and no subline.

When a turn finishes while the window is unfocused or hidden, the app waits 10 seconds and then sends a notification. With notification previews off, it says **Supervisor finished a turn.** With previews on, it says **Supervisor ·** and then the first line of the reply. The notification is dropped if you focus the window, a new turn starts, the process stops or you switch servers within those 10 seconds. Stopped and failed turns never notify. Clicking the notification shows the window on the Supervisor page. Permissions and questions still notify at once.

The tray tooltip and tray menu item read **Supervisor working**, **Supervisor waiting for you**, or **Supervisor idle**. Quit, install and update dialogs say **The supervisor is working** or **The supervisor is waiting for your answer**.

## Updates and the supervisor

While the supervisor has a process, the update surfaces say: "Updating restarts the supervisor; your conversation is saved. Sub-agents and background shells will be lost." This text appears on the Settings app-update card and its **Stop Work and Install Now?** dialog, in the update popover, and on the connected-server update card and its confirmation dialog.

An install scheduled for when work is idle does not wait for a supervisor that is waiting on you. If no feature is active, the install ends the supervisor and restarts. The pending permission or question is marked **Interrupted** in the transcript. After the restart, the conversation is paused, and your next message relaunches it. A supervisor that is starting or running a turn still holds up an idle install. **Install now** and quitting the app still ask for confirmation while the supervisor is waiting on you.

## Finishing setup later

If at least one provider is ready but setup is not complete, the app opens the Supervisor page instead of the full-page setup wizard. For example, another provider might be missing or the configuration might be invalid. A banner above the page shows the first blocking issue, with an **Open setup** action. **Open setup**, the **Setup…** command in the command palette, and the **Navigate** menu item all open the setup wizard as a sheet over the app. The sheet never opens by itself; close it with **Close**, Escape or a click outside it. **New feature** stays available but reports that the runtime is not ready. After you fix the issue, choose **Check again**: the banner closes, and **Setup…** leaves the palette and turns off in the menu. The full-page wizard appears only when no provider is ready.
