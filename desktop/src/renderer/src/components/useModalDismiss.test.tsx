/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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

it('preserves focus when an open modal rerenders with a new close callback', () => {
  render(<RerenderingModalHarness />);
  const refresh = screen.getByRole('button', { name: 'Refresh content' });

  refresh.focus();
  fireEvent.click(refresh);

  expect(screen.getByText('Refreshes: 1')).toBeVisible();
  expect(refresh).toHaveFocus();
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

function RerenderingModalHarness() {
  const [open, setOpen] = useState(true);
  const [refreshes, setRefreshes] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  useModalDismiss(ref, () => setOpen(false), open);

  return open ? (
    <div ref={ref} role="dialog" aria-modal="true" aria-label="Refreshing dialog" tabIndex={-1}>
      <button type="button">First action</button>
      <button type="button" onClick={() => setRefreshes((count) => count + 1)}>
        Refresh content
      </button>
      <span>Refreshes: {refreshes}</span>
    </div>
  ) : null;
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
