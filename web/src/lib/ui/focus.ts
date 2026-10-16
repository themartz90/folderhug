import { writable } from 'svelte/store';

export const openDialogs = writable(0);

let clicked: HTMLElement | null = null;
let clickedAt = 0;
let outsideDialog: HTMLElement | null = null;

export function rememberOpener(event: MouseEvent) {
  const element =
    event.target instanceof Element
      ? event.target.closest<HTMLElement>('button, a, [data-dialog-trigger]')
      : null;
  if (element) {
    clicked = element;
    clickedAt = performance.now();
  }
}

export function clearRememberedFocus() {
  clicked = null;
  outsideDialog = null;
}

export function dialogOpener(): HTMLElement | null {
  const candidate =
    clicked?.isConnected && performance.now() - clickedAt < 1000
      ? clicked
      : document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
  if (candidate && candidate !== document.body && !candidate.closest('dialog')) {
    outsideDialog = candidate;
    return candidate;
  }
  return outsideDialog?.isConnected ? outsideDialog : candidate;
}
