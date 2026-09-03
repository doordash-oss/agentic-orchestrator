/**
 * The renderer-level owner-card registry: every ErrorSurface that renders a
 * durable error registers its root element under the error's reference, so
 * any presence surface — the cockpit status chip, an attention inbox jump —
 * can resolve the reference back to the owning card and focus it. Keys are
 * built from the reference's scope and keys plus the code, exactly the
 * fields the server projection and the explain-in-chat wiring share.
 */
import type { ErrorReference } from '../../../shared/ipc';
import { useSyncExternalStore } from 'react';

const cards = new Map<string, HTMLElement>();
const listeners = new Set<() => void>();

function notifyListeners(): void {
  for (const listener of listeners) listener();
}

/** The stable registry key for one durable error home. */
export function errorCardKey(ref: ErrorReference): string {
  return [
    ref.scope,
    ref.featureId ?? '',
    ref.repository ?? '',
    ref.taskKey ?? '',
    ref.snapshotId ?? '',
    ref.key ?? '',
    ref.code,
  ].join('|');
}

/**
 * Registers (element) or unregisters (null) the card that currently owns a
 * reference. Mount-time registration, unmount-time release.
 */
export function registerErrorCard(ref: ErrorReference, element: HTMLElement | null): void {
  const key = errorCardKey(ref);
  if (element === null) {
    if (cards.delete(key)) notifyListeners();
    return;
  }
  cards.set(key, element);
  notifyListeners();
}

/** True while a durable error already has an owning card in the mounted view. */
export function useRegisteredErrorCard(ref: ErrorReference | undefined): boolean {
  const key = ref === undefined ? null : errorCardKey(ref);
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => key !== null && cards.has(key),
    () => false,
  );
}

/**
 * Scrolls the registered card into view and focuses it. Returns false when
 * no card is registered under the reference yet (e.g. the hosting modal has
 * not mounted), so callers can retry.
 */
export function focusErrorCard(ref: ErrorReference): boolean {
  const card = cards.get(errorCardKey(ref));
  if (card === undefined) return false;
  if (typeof card.scrollIntoView === 'function') {
    card.scrollIntoView({ block: 'center' });
  }
  card.focus();
  return true;
}

/**
 * Focuses the card once it registers, for clicks that must first open the
 * hosting surface (the publish modal for repository entries). Gives up
 * quietly after the bound so a missing card never hangs a click.
 */
export function focusErrorCardWhenRegistered(
  ref: ErrorReference,
  timeoutMs = 4000,
  startedAt = Date.now(),
): void {
  if (focusErrorCard(ref)) {
    settleErrorCardFocus(ref, 2);
    return;
  }
  if (Date.now() - startedAt > timeoutMs) return;
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(() => focusErrorCardWhenRegistered(ref, timeoutMs, startedAt));
    return;
  }
  setTimeout(() => focusErrorCardWhenRegistered(ref, timeoutMs, startedAt), 50);
}

/**
 * A jump can open another surface alongside the card — the attention
 * deep-link's live-preview overlay focuses its first control on mount — so
 * re-assert the card for a couple of frames whenever focus left it. Bounded
 * to two frames: a user who deliberately moves focus is never fought.
 */
function settleErrorCardFocus(ref: ErrorReference, frames: number): void {
  if (frames <= 0) return;
  const tick = (): void => {
    const card = cards.get(errorCardKey(ref));
    if (card !== undefined && !card.contains(document.activeElement)) {
      card.focus();
    }
    settleErrorCardFocus(ref, frames - 1);
  };
  if (typeof requestAnimationFrame === 'function') {
    requestAnimationFrame(tick);
    return;
  }
  setTimeout(tick, 32);
}
