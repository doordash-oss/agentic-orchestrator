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
