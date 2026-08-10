# macOS Window Close Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the macOS red window button and Command-W close Agentico's main window without quitting the application or owned runtime, while preserving coordinated shutdown for explicit Quit.

**Architecture:** Keep `QuitCoordinator` as the sole owner of explicit application shutdown and add a pure platform policy beside it for main-window close behavior. The Electron entry point consults that policy before preventing a close event; the existing `WindowRegistry`, `activate` listener, and `showMainWindow` path recreate a destroyed renderer without adding a second lifecycle owner.

**Tech Stack:** Electron 43, TypeScript 5.9, Vitest 3, Playwright Electron packaged journeys, Go 1.24 verification gates.

## Global Constraints

- On macOS, a main-window close is a true `BrowserWindow` close, not hide and not application quit.
- Command-Q, application-menu Quit, Dock-menu Quit, tray Quit, and Electron quit events continue through `QuitCoordinator`.
- Windows and Linux keep their current close-to-quit behavior.
- Do not change renderer copy, roles, accessible names, or IPC/preload surfaces.
- Every modified test must name an observable regression and exercise real production behavior.
- The targeted packaged journey must prove close, continued process/runtime life, activation, recreation, and explicit quit.

---

### Task 1: Encode the platform close policy

**Files:**

- Modify: `desktop/src/main/quitCoordinator.ts`
- Test: `desktop/src/main/__tests__/quitCoordinator.test.ts`

**Interfaces:**

- Consumes: `NodeJS.Platform`, supplied explicitly by the Electron entry point or a unit test.
- Produces: `shouldRequestQuitOnMainWindowClose(platform: NodeJS.Platform): boolean`; `false` only for `darwin`, `true` for `win32` and `linux`.

- [ ] **Step 1: Write the failing platform-policy test**

Add `shouldRequestQuitOnMainWindowClose` to the existing import from
`../quitCoordinator`, then add this test before the `QuitCoordinator` describe:

```ts
describe("shouldRequestQuitOnMainWindowClose", () => {
  it.each([
    ["darwin", false],
    ["win32", true],
    ["linux", true],
  ] as const)("returns %s => %s", (platform, expected) => {
    expect(shouldRequestQuitOnMainWindowClose(platform)).toBe(expected);
  });
});
```

This catches the regression where macOS is routed back into quit coordination,
and the inverse regression where Windows or Linux silently remain running.

- [ ] **Step 2: Run the focused unit test and verify RED**

Run:

```bash
npm test --workspace desktop -- --project node src/main/__tests__/quitCoordinator.test.ts
```

Expected: FAIL because `shouldRequestQuitOnMainWindowClose` is not exported.

- [ ] **Step 3: Implement the minimal policy**

Add this export beside `hasActiveWork` in `quitCoordinator.ts`:

```ts
export function shouldRequestQuitOnMainWindowClose(
  platform: NodeJS.Platform,
): boolean {
  return platform !== "darwin";
}
```

- [ ] **Step 4: Run the focused unit test and verify GREEN**

Run the command from Step 2.

Expected: PASS for the new table and all existing quit-coordinator tests.

- [ ] **Step 5: Commit the independently tested policy**

```bash
git add desktop/src/main/quitCoordinator.ts desktop/src/main/__tests__/quitCoordinator.test.ts
git commit -m "Keep macOS window close distinct from application quit" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 2: Route macOS close through the native window lifecycle

**Files:**

- Modify: `desktop/test/e2e/journeys/background-lifecycle-commands.spec.ts`
- Modify: `desktop/src/main/index.ts`
- Modify: `desktop/src/main/menuTemplate.ts`

**Interfaces:**

- Consumes: `shouldRequestQuitOnMainWindowClose(process.platform)` from Task 1, `WindowRegistry` eviction/recreation, Electron `app` activation, and the existing `global.quit` native command.
- Produces: macOS close -> destroyed main window with live application/runtime; app activation -> fresh main window; explicit Quit -> unchanged `QuitCoordinator` flow.

- [ ] **Step 1: Rewrite the packaged lifecycle expectation before wiring production**

In the primary active-work journey:

- replace the close-to-`Keep Running` assertion with a call to a new
  `closeMainWindowAndReactivate(handle)` helper;
- assert the same owned server PID is alive after close;
- assert the recreated renderer can read the active feature and still sees an
  enabled `pause-stop` action;
- keep the explicit-quit cancellation and stop assertions, but trigger both
  through `'native-quit'`, never `'window-close'`.

Use these assertions after active work is observable:

```ts
await closeMainWindowAndReactivate(handle);
const restored = await handle.page.evaluate(
  (id) => window.agentico.getFeature(id),
  featureId,
);
expect(restored.actions).toContainEqual(
  expect.objectContaining({ id: "pause-stop", enabled: true }),
);

const cancelResult = await triggerQuitDecision(handle, [2], "native-quit");
expect(cancelResult.visible).toBe(true);
expect(JSON.stringify(cancelResult.captured[0])).toContain("Cancel");

const stopResult = await triggerQuitDecision(handle, [1], "native-quit");
expect(JSON.stringify(stopResult.captured[0])).toContain("Stop Work and Quit");
```

Split the existing idle-close journey by platform: the macOS case uses the same
close/reactivate contract followed by a native explicit quit that does exit,
while the non-macOS case retains the existing close-to-quit assertion.

```ts
await closeMainWindowAndReactivate(handle);
await clickNativeMenu(handle, "global.quit");
await waitForAppExit(handle);
handle.closed = true;
```

Add this same-file helper near `triggerQuitDecision`:

```ts
async function closeMainWindowAndReactivate(handle: AppHandle): Promise<void> {
  const previousId = handle.mainWebContentsId;
  const close = await handle.app.evaluate(
    async ({ BrowserWindow, dialog }, mainId) => {
      const window = BrowserWindow.getAllWindows().find(
        (candidate) => candidate.webContents.id === mainId,
      );
      if (window === undefined) throw new Error("main window missing");
      const original = dialog.showMessageBox;
      let promptCount = 0;
      dialog.showMessageBox = (async () => {
        promptCount += 1;
        return { response: 2, checkboxChecked: false };
      }) as typeof dialog.showMessageBox;
      window.close();
      const deadline = Date.now() + 2_000;
      while (!window.isDestroyed() && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      dialog.showMessageBox = original;
      return {
        promptCount,
        destroyed: window.isDestroyed(),
        openWindows: BrowserWindow.getAllWindows().length,
      };
    },
    previousId,
  );
  expect(close).toEqual({ promptCount: 0, destroyed: true, openWindows: 0 });
  expect(handle.appProcess.exitCode).toBeNull();
  expect(handle.appProcess.signalCode).toBeNull();
  expect(processAlive(readDiscovery(handle.world)!.pid)).toBe(true);

  const appeared = handle.app.waitForEvent("window", { timeout: 30_000 });
  await handle.app.evaluate(({ app }) => app.emit("activate"));
  const page = await appeared;
  page.setDefaultTimeout(
    Number(process.env["AGENTICO_E2E_ACTION_TIMEOUT"] ?? 60_000),
  );
  await expect(
    page.getByRole("status").filter({ hasText: "Runtime ready" }),
  ).toBeVisible({ timeout: 60_000 });
  const mainWebContentsId = await handle.app.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined)
      throw new Error("reactivated main window missing");
    return window.webContents.id;
  });
  expect(mainWebContentsId).not.toBe(previousId);
  handle.page = page;
  handle.mainWebContentsId = mainWebContentsId;
}
```

- [ ] **Step 2: Build the unchanged application and verify the packaged test is RED**

Run:

```bash
npm run package:verify
npm run test:e2e:packaged -- test/e2e/journeys/background-lifecycle-commands.spec.ts
```

Expected: FAIL in `closeMainWindowAndReactivate` because the current main-window
close handler initiates quit instead of destroying only the window and keeping
the process alive.

- [ ] **Step 3: Wire the policy into the main-window close handler**

Import `shouldRequestQuitOnMainWindowClose` from `./quitCoordinator`, then change
`handleWindowClose` to:

```ts
function handleWindowClose(event: ElectronEvent, window: BrowserWindow): void {
  if (
    quitCoordinator.shouldAllowClose() ||
    !shouldRequestQuitOnMainWindowClose(process.platform)
  ) {
    return;
  }
  event.preventDefault();
  void quitCoordinator.requestQuitDecision(window);
}
```

Update the `File -> Close Window` comment in `menuTemplate.ts` so it states that
Command-W closes the focused window, the macOS main window remains an active
application, and non-macOS main windows still enter quit coordination.

- [ ] **Step 4: Rebuild and verify the packaged lifecycle GREEN**

Run the two commands from Step 2 again.

Expected: both packaged tests cover close/recreate successfully; active work
survives recreation; explicit native Quit still cancels or performs coordinated
shutdown as selected.

- [ ] **Step 5: Run the focused unit test again**

```bash
npm test --workspace desktop -- --project node src/main/__tests__/quitCoordinator.test.ts
```

Expected: PASS, proving the entry point still consumes the tested policy.

- [ ] **Step 6: Commit the integrated behavior**

```bash
git add desktop/src/main/index.ts desktop/src/main/menuTemplate.ts desktop/test/e2e/journeys/background-lifecycle-commands.spec.ts
git commit -m "Honor native macOS window close semantics" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 3: Verify and publish the dedicated pull request

**Files:**

- Include: `docs/superpowers/specs/2026-08-10-macos-window-close-design.md`
- Include: `docs/superpowers/plans/2026-08-10-macos-window-close.md`
- Verify all production and test files committed in Tasks 1-2.

**Interfaces:**

- Consumes: the complete branch diff against `origin/main` and the canonical verification commands in `docs/testing-baseline.md`.
- Produces: a pushed `fix/macos-window-close-behavior` branch and one GitHub pull request whose body names every verification tier run.

- [ ] **Step 1: Run the required verification gates**

```bash
make test-fast
npm run check
npm test
npm run test:security
go vet ./...
go build ./...
```

Expected: every command exits 0. The targeted Desktop packaged E2E was already
run from a fresh package in Task 2 and is named separately in the PR.

- [ ] **Step 2: Review the exact branch diff**

```bash
git status --short --branch
git diff --check origin/main...HEAD
git diff --stat origin/main...HEAD
git log --oneline origin/main..HEAD
```

Expected: only the approved design/plan, quit policy, Electron close wiring,
menu comment, and lifecycle tests appear; no dependency or generated-file drift.

- [ ] **Step 3: Push the dedicated branch after confirming it is not main**

```bash
branch=$(git branch --show-current)
test "$branch" != main
test "$branch" != master
git push -u origin "$branch"
```

- [ ] **Step 4: Open the separate PR**

Check for an existing PR first with `gh pr view`, use the repository PR template
if present, and create the PR with a body file. The final body must include a
`Verification` section naming Fast suite, Desktop static checks, Desktop
unit/component/security tests, Desktop packaged E2E (targeted lifecycle spec),
Go static-analysis gate, and Go build gate. Its last line must be:

```text
Co-authored-by: Codex <noreply@openai.com>
```
