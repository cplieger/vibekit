// ---------------------------------------------------------------------------
// Focus trap: keeps Tab cycling within a container. Used for modal dialogs
// (permission dialog, confirm dialog) per WAI-ARIA dialog pattern.
//
// Usage:
//   const release = trapFocus(dialogEl);
//   // ... dialog is open ...
//   release(); // restore focus to the element that was focused before
// ---------------------------------------------------------------------------

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Trap focus within `container`. Returns a release function that
 *  restores focus to the previously-focused element. */
export function trapFocus(container: HTMLElement): () => void {
  const previousFocus = document.activeElement as HTMLElement | null;

  function getFocusable(): HTMLElement[] {
    return [...container.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
      (el) => el.offsetParent !== null, // visible
    );
  }

  // Focus the first focusable element.
  const items = getFocusable();
  if (items.length > 0) {
    items[0]!.focus();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key !== "Tab") {
      return;
    }
    const focusable = getFocusable();
    if (focusable.length === 0) {
      return;
    }
    const first = focusable[0]!;
    const last = focusable[focusable.length - 1]!;

    if (e.shiftKey) {
      if (document.activeElement === first) {
        e.preventDefault();
        last.focus();
      }
    } else {
      if (document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  }

  container.addEventListener("keydown", onKeyDown);

  return (): void => {
    container.removeEventListener("keydown", onKeyDown);
    previousFocus?.focus();
  };
}
