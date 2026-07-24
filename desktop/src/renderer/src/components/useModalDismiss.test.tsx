import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useCallback, useRef, useState } from 'react';
import { afterEach, expect, it, vi } from 'vitest';
import { useModalDismiss } from './useModalDismiss';

afterEach(cleanup);

it('dismisses only the nested modal on the first Escape', async () => {
  const outerClosed = vi.fn();
  const nestedClosed = vi.fn();
  render(<NestedModalHarness outerClosed={outerClosed} nestedClosed={nestedClosed} />);

  fireEvent.keyDown(window, { key: 'Escape' });

  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Nested dialog' })).not.toBeInTheDocument(),
  );
  expect(screen.getByRole('dialog', { name: 'Outer dialog' })).toBeVisible();
  expect(nestedClosed).toHaveBeenCalledOnce();
  expect(outerClosed).not.toHaveBeenCalled();
});

it('wraps Tab focus within the nested modal while the outer modal stays open', () => {
  render(<OuterFirstFocusTrapHarness />);
  const first = screen.getByRole('button', { name: 'Nested first action' });
  const last = screen.getByRole('button', { name: 'Nested last action' });

  last.focus();
  fireEvent.keyDown(window, { key: 'Tab' });
  expect(first).toHaveFocus();
  expect(screen.getByRole('dialog', { name: 'Outer dialog' })).toBeVisible();

  first.focus();
  fireEvent.keyDown(window, { key: 'Tab', shiftKey: true });
  expect(last).toHaveFocus();
  expect(screen.getByRole('dialog', { name: 'Outer dialog' })).toBeVisible();
});

it('wraps Tab focus within the active modal', () => {
  render(<FocusTrapHarness />);
  const first = screen.getByRole('button', { name: 'First action' });
  const last = screen.getByRole('button', { name: 'Last action' });

  last.focus();
  fireEvent.keyDown(window, { key: 'Tab' });
  expect(first).toHaveFocus();

  first.focus();
  fireEvent.keyDown(window, { key: 'Tab', shiftKey: true });
  expect(last).toHaveFocus();
});

function NestedModalHarness({
  outerClosed,
  nestedClosed,
}: {
  outerClosed(): void;
  nestedClosed(): void;
}) {
  const [outerOpen, setOuterOpen] = useState(true);
  const [nestedOpen, setNestedOpen] = useState(true);
  const outerRef = useRef<HTMLElement>(null);
  const closeOuter = useCallback(() => {
    outerClosed();
    setOuterOpen(false);
  }, [outerClosed]);
  const closeNested = useCallback(() => {
    nestedClosed();
    setNestedOpen(false);
  }, [nestedClosed]);
  useModalDismiss(outerRef, closeOuter, outerOpen);

  return outerOpen ? (
    <section ref={outerRef} role="dialog" aria-modal="true" aria-label="Outer dialog" tabIndex={-1}>
      <button type="button">Outer action</button>
      {nestedOpen ? <NestedDialog onClose={closeNested} /> : null}
    </section>
  ) : null;
}

function NestedDialog({ onClose }: { onClose(): void }) {
  const ref = useRef<HTMLDivElement>(null);
  useModalDismiss(ref, onClose);

  return (
    <div ref={ref} role="dialog" aria-modal="true" aria-label="Nested dialog" tabIndex={-1}>
      <button type="button">Nested action</button>
    </div>
  );
}

function FocusTrapHarness() {
  const ref = useRef<HTMLDivElement>(null);
  const close = useCallback(() => undefined, []);
  useModalDismiss(ref, close);

  return (
    <div ref={ref} role="dialog" aria-modal="true" aria-label="Focus trap" tabIndex={-1}>
      <button type="button">First action</button>
      <button type="button">Last action</button>
    </div>
  );
}

function OuterFirstFocusTrapHarness() {
  const outerRef = useRef<HTMLElement>(null);
  const nestedRef = useRef<HTMLDivElement>(null);
  const close = useCallback(() => undefined, []);
  useModalDismiss(outerRef, close);
  useModalDismiss(nestedRef, close);

  return (
    <section ref={outerRef} role="dialog" aria-modal="true" aria-label="Outer dialog" tabIndex={-1}>
      <button type="button">Outer action</button>
      <div ref={nestedRef} role="dialog" aria-modal="true" aria-label="Nested dialog" tabIndex={-1}>
        <button type="button">Nested first action</button>
        <button type="button">Nested last action</button>
      </div>
    </section>
  );
}
