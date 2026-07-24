# Expandable AMA Modal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an accessible near-full-window AMA overlay that preserves the existing dock preference, draft, attachments, transcript, and live subscription.

**Architecture:** Keep `AmaDock` as the sole owner and renderer of AMA state. A non-persisted `maximized` flag elevates the existing dock DOM through an always-present modal layer, so opening from the expanded drawer does not remount the transcript or composer; shared modal focus behavior moves into a renderer hook reused by the live preview and AMA.

**Tech Stack:** React 19, TypeScript 5.9, Vitest, Testing Library, CSS, Electron renderer

## Global Constraints

- The expanded AMA is an in-app near-full-window modal, not a separate Electron `BrowserWindow`.
- Expansion is temporary and must not update the persisted compact/expanded dock preference.
- Render only one transcript, pending-question surface, attachment list, and composer.
- Preserve the unsent draft, attachments, transcript DOM, notices, and active output subscription across expansion.
- The modal is labelled `Expanded AMA`, traps keyboard focus, closes by Escape, backdrop, or explicit control, and restores focus to the expand trigger.
- New source files with `.go`, `.sh`, or `.py` extensions require the repository Apache 2.0 header; this plan creates only TypeScript.

---

### Task 1: Add the reusable modal behavior and expanded AMA presentation

**Files:**
- Create: `desktop/src/renderer/src/components/useModalDismiss.ts`
- Modify: `desktop/src/renderer/src/features/CurrentRunInspection.tsx:945-987`
- Modify: `desktop/src/renderer/src/components/AmaDock.tsx:1-524`
- Modify: `desktop/src/renderer/src/components/AmaDock.test.tsx:1-211`
- Modify: `desktop/src/renderer/src/styles/app.css:284-442`

**Interfaces:**
- Consumes: the existing `AmaDock` state, `MaximizeIcon`, `CloseIcon`, and the live-preview modal focus behavior.
- Produces: `useModalDismiss(ref: React.RefObject<HTMLElement | null>, onClose: () => void, active?: boolean): void`, an `Expand AMA`/`Close expanded AMA` control, and the `ama-dock__modal-layer` styling contract.

- [ ] **Step 1: Write the failing renderer tests**

Add these tests to `desktop/src/renderer/src/components/AmaDock.test.tsx`:

```tsx
it('opens one expanded AMA dialog and keeps the draft when it closes', async () => {
  installAgenticoMock();
  renderDock();
  const input = screen.getByRole('textbox', { name: 'Ask Agentico' });
  await userEvent.type(input, 'Keep this draft');

  const expand = screen.getByRole('button', { name: 'Expand AMA' });
  await userEvent.click(expand);

  const dialog = screen.getByRole('dialog', { name: 'Expanded AMA' });
  expect(dialog).toBeVisible();
  expect(screen.getAllByRole('textbox', { name: 'Ask Agentico' })).toHaveLength(1);
  expect(screen.getAllByRole('region', { name: 'AMA transcript' })).toHaveLength(1);

  await userEvent.click(screen.getByRole('button', { name: 'Close expanded AMA' }));

  expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument();
  expect(screen.getByRole('textbox', { name: 'Ask Agentico' })).toHaveValue('Keep this draft');
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus(),
  );
});

it('dismisses expanded AMA with Escape without changing the saved drawer mode', async () => {
  const mock = installAgenticoMock();
  renderDock();

  await userEvent.click(screen.getByRole('button', { name: 'Expand AMA' }));
  expect(screen.getByRole('dialog', { name: 'Expanded AMA' })).toBeVisible();

  fireEvent.keyDown(window, { key: 'Escape' });

  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Expanded AMA' })).not.toBeInTheDocument(),
  );
  expect(mock.api.updateSettings).not.toHaveBeenCalled();
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Expand AMA' })).toHaveFocus(),
  );
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/components/AmaDock.test.tsx
```

Expected: FAIL because no button named `Expand AMA` exists.

- [ ] **Step 3: Extract the existing modal dismissal hook**

Create `desktop/src/renderer/src/components/useModalDismiss.ts` with the existing
focus trap, Escape handling, focus restoration, and body-scroll lock from
`CurrentRunInspection.tsx`, adding an `active` guard:

```ts
import { useEffect, type RefObject } from 'react';

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Escape-to-close, Tab focus trap, focus restoration, and body-scroll lock. */
export function useModalDismiss(
  ref: RefObject<HTMLElement | null>,
  onClose: () => void,
  active = true,
): void {
  useEffect(() => {
    if (!active) return;
    const node = ref.current;
    const previouslyFocused =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const focusable = (): HTMLElement[] =>
      node === null ? [] : Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    (focusable()[0] ?? node)?.focus();

    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || node === null) return;
      const items = focusable();
      if (items.length === 0) {
        event.preventDefault();
        node.focus();
        return;
      }
      const first = items[0]!;
      const last = items[items.length - 1]!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = previousOverflow;
      requestAnimationFrame(() => previouslyFocused?.focus());
    };
  }, [active, onClose, ref]);
}
```

Import this hook in `CurrentRunInspection.tsx`:

```ts
import { useModalDismiss } from '../components/useModalDismiss';
```

Delete the local `FOCUSABLE_SELECTOR` and `useModalDismiss` definitions from
`CurrentRunInspection.tsx`; keep both existing call sites unchanged.

- [ ] **Step 4: Add the maximized AMA behavior without duplicating the surface**

In `AmaDock.tsx`, import the shared controls:

```ts
import { CloseIcon, MaximizeIcon } from './icons';
import { useModalDismiss } from './useModalDismiss';
```

Add local modal state and refs beside the existing drawer state and refs:

```ts
const [maximized, setMaximized] = useState(false);
const modalRef = useRef<HTMLElement>(null);
const maximizeRef = useRef<HTMLButtonElement>(null);

const closeMaximized = useCallback(() => setMaximized(false), []);
useModalDismiss(modalRef, closeMaximized, maximized);
```

Wrap the existing `<aside>` in an always-present layer so the `<aside>`,
transcript, and composer retain identity:

```tsx
<div
  className="ama-dock__modal-layer"
  data-open={maximized}
  onMouseDown={maximized ? closeMaximized : undefined}
>
  <aside
    ref={modalRef}
    className="ama-dock"
    data-mode={maximized ? 'expanded' : drawer}
    aria-label={maximized ? 'Expanded AMA' : 'Ask Agentico'}
    role={maximized ? 'dialog' : undefined}
    aria-modal={maximized ? true : undefined}
    tabIndex={maximized ? -1 : undefined}
    onMouseDown={maximized ? (event) => event.stopPropagation() : undefined}
  >
    {/* existing AMA header, drawer, notice, confirmation, and composer */}
  </aside>
</div>
```

Add the non-persisted control to the existing header after the status text:

```tsx
<button
  ref={maximizeRef}
  type="button"
  className="ama-dock__icon-button"
  aria-label={maximized ? 'Close expanded AMA' : 'Expand AMA'}
  title={maximized ? 'Close expanded AMA' : 'Expand AMA'}
  onClick={() => setMaximized((current) => !current)}
>
  {maximized ? <CloseIcon /> : <MaximizeIcon />}
</button>
```

Render the drawer whenever either presentation needs it, while leaving
`persistDrawer` tied only to the `AMA` toggle:

```tsx
{drawer === 'expanded' || maximized ? (
  <div className="ama-dock__drawer" data-has-attention={amaAttentionItems.length > 0}>
    {/* existing pending-question and ConversationTranscript markup */}
  </div>
) : null}
```

Do not call `persistDrawer` from the expand/close path. The shared hook restores
focus to the clicked expand control after the modal closes; `maximizeRef`
documents and types that stable trigger.

- [ ] **Step 5: Add the near-full-window layout**

Add to the AMA section of `desktop/src/renderer/src/styles/app.css`:

```css
.ama-dock__modal-layer {
  display: contents;
}

.ama-dock__modal-layer[data-open='true'] {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: grid;
  place-items: center;
  padding: clamp(var(--space-3), 4vw, var(--space-6));
  background: rgb(5 9 8 / 74%);
}

.ama-dock__modal-layer[data-open='true'] .ama-dock {
  width: min(88rem, 100%);
  height: min(88vh, 980px);
  max-height: 100%;
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius);
  box-shadow: 0 24px 70px rgb(0 0 0 / 40%);
}

.ama-dock__modal-layer[data-open='true'] .ama-dock:focus-visible {
  outline: none;
}

.ama-dock__icon-button {
  display: inline-grid;
  min-width: 32px;
  min-height: 32px;
  place-items: center;
  border: 1px solid var(--color-hairline);
  border-radius: var(--radius);
  background: transparent;
  color: var(--color-text);
  cursor: pointer;
}
```

Include `.ama-dock__icon-button` in the existing disabled/focus-visible control
rules where applicable. Keep the transcript's existing internal overflow and
the composer's existing final grid row, which pins it to the bottom.

- [ ] **Step 6: Run the focused tests and verify GREEN**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/components/AmaDock.test.tsx src/renderer/src/features/CurrentRunInspection.test.tsx
```

Expected: both test files PASS with no uncaught errors or warnings.

- [ ] **Step 7: Run renderer static checks and build**

Run:

```bash
npm run check --workspace desktop
npm run build --workspace desktop
```

Expected: both commands exit 0.

- [ ] **Step 8: Run repository-required verification**

Run:

```bash
make test-fast
go vet ./...
go build ./...
```

Expected: the Fast suite, Go vet, and Go build all exit 0. No extended tier is
required because this change does not modify process launch, packaging, server
lifecycle, state-machine behavior, or concurrency.

- [ ] **Step 9: Review and commit the implementation**

Review:

```bash
git diff --check
git status --short
git diff -- desktop/src/renderer/src/components/useModalDismiss.ts desktop/src/renderer/src/features/CurrentRunInspection.tsx desktop/src/renderer/src/components/AmaDock.tsx desktop/src/renderer/src/components/AmaDock.test.tsx desktop/src/renderer/src/styles/app.css
```

Stage only the implementation files and commit:

```bash
git add desktop/src/renderer/src/components/useModalDismiss.ts desktop/src/renderer/src/features/CurrentRunInspection.tsx desktop/src/renderer/src/components/AmaDock.tsx desktop/src/renderer/src/components/AmaDock.test.tsx desktop/src/renderer/src/styles/app.css
git commit -m "Give AMA conversations room to breathe"
```
