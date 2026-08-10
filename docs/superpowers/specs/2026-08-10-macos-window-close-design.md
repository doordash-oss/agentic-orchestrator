# macOS Window Close Behavior

## Context

Agentico currently routes closing its main window through the same shutdown
coordinator as an explicit application quit. When the runtime is idle, clicking
the red window button or pressing Command-W terminates the application. With
active work, the same action opens the quit-decision dialog.

That behavior conflicts with the macOS window model. Apple's current user guide
states that closing one or all windows does not quit an application, and
distinguishes Command-W from Command-Q. Electron's current lifecycle guidance
likewise recommends keeping the application active on macOS after
`window-all-closed` and recreating a window on `activate`.

References:

- [Apple: Move and arrange app windows on Mac](https://support.apple.com/guide/mac-help/work-with-app-windows-mchlp2469/mac)
- [Apple: Quit apps on Mac](https://support.apple.com/en-euro/guide/mac-help/mchl834d18c2/mac)
- [Electron: Keyboard shortcuts](https://www.electronjs.org/docs/latest/tutorial/keyboard-shortcuts/)
- [Electron: `app` lifecycle](https://www.electronjs.org/docs/latest/api/app)

## Decision

On macOS, closing the main window closes that `BrowserWindow` without initiating
application shutdown. The Agentico process, owned runtime, event supervision,
notifications, native menu, and tray integration remain active. No active-work
dialog appears because a window close is no longer a request to stop work or
quit.

Agentico recreates the main window through its existing `WindowRegistry` when
the user activates the application from the Dock, chooses Show Agentico, opens
a notification or route, or launches Agentico again. This is a true window
close, not a hide operation: the renderer is destroyed and rebuilt, while
authoritative work continues in the main process and server.

Explicit quit paths remain unchanged. Command-Q, the application-menu Quit
command, the Dock-menu Quit command, tray Quit, and other Electron quit events
continue through `QuitCoordinator`, including active-work detection, stop and
retry controls, and orderly runtime shutdown.

Windows and Linux retain their current behavior: closing the main window
continues to request application quit through `QuitCoordinator`.

## Implementation Shape

Add a small, pure platform policy to the existing quit-coordination module. It
will answer whether closing the main window should initiate quit for a supplied
`NodeJS.Platform`. The main-window close handler will persist geometry as it
does today, then:

- allow the close event to proceed on macOS;
- prevent the event and request a quit decision on other platforms; and
- allow every platform's window close to proceed when `QuitCoordinator` has
  already authorized shutdown.

The existing `closed` listener will evict the destroyed window and revoke its
renderer trust. The existing `activate` and `showMainWindow` paths will create a
fresh trusted main window, so no new window-lifecycle owner is introduced.

## Failure and State Handling

Window geometry persistence remains best-effort and cannot block close.
Recreating the renderer uses the existing crash recovery, runtime connection,
and readiness paths. If no window is open, app-level streams and background
state continue to run exactly as they do for an intentionally hidden window.

An explicit quit that closes windows does not get mistaken for a red-button
close: once the coordinator authorizes shutdown, its existing force-quit state
allows window destruction while the main process shuts down.

## Verification

Unit coverage will prove the platform policy requests quit for Windows and
Linux but not for macOS. The affected packaged lifecycle journey will prove the
observable contract on macOS:

1. close the main window with the native close action;
2. observe no quit dialog and confirm both application and owned server remain
   alive;
3. activate the app and confirm a fresh main window becomes visible and usable;
4. invoke explicit Quit and confirm the normal coordinated shutdown still
   exits the application and owned server.

Required handoff gates are the Fast suite, Desktop static checks, Desktop
unit/component/security tests, the targeted Desktop packaged E2E lifecycle
spec, `go vet ./...`, and `go build ./...`.

## Non-Goals

- Changing close behavior on Windows or Linux.
- Preserving renderer-local UI state across a true window close.
- Changing active-work prompts or shutdown semantics for explicit Quit.
- Adding preferences for close-to-hide or close-to-quit behavior.
