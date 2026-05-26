// ---------------------------------------------------------------------------
// reconcile: keyed-list DOM reconciliation for imperative views.
//
// Patches a parent's children to match a target items array in-order,
// identified by a stable key. Existing children are kept (and optionally
// patched in place), missing ones are mounted, orphans are removed,
// misplaced ones are moved. One in-place pass; no full rebuild.
//
// Identity: each managed child carries a `data-reconcile-key`. Children
// without that attribute are ignored, so reconcile can run alongside
// hand-managed siblings (headers, footers, sticky action rows, etc.).
//
// Pair with signals: read signals inside `mount`/`update`, run inside
// `effect()`, and the panel surgically reflects every state change without
// a full DOM rebuild.
//
//   const items = signal<readonly Item[]>([]);
//   effect(() => reconcile(list, items.value, spec));
//
// Mutations bump the signal; the effect re-runs and reconcile patches.
// ---------------------------------------------------------------------------

/** The DOM attribute reconcile uses to track keyed children. Exported
 *  so callers can `el.getAttribute(KEY_ATTR)` to recover the key for a
 *  reconciled child without depending on the literal string. */
export const KEY_ATTR = "data-reconcile-key";

export interface ReconcileSpec<T> {
  /** Stable identity per item. Must be unique within `items` for any
   *  given reconcile() call; collisions produce undefined behavior. */
  key: (item: T) => string;
  /** Build a fresh element for an item not currently mounted. The
   *  returned element is tagged with `data-reconcile-key` automatically. */
  mount: (item: T) => HTMLElement;
  /** Patch an already-mounted element to reflect the latest item state.
   *  Optional: omit when items are immutable per key (the mounted DOM
   *  is always correct as-is). */
  update?: (el: HTMLElement, item: T) => void;
  /** Cleanup hook called just before an orphaned element is removed
   *  from the DOM. Use this to unbind external subscriptions that the
   *  element holds (e.g. action-framework loading-state listeners,
   *  resize observers, etc.). The element is still in the DOM at the
   *  time of the call. */
  onRemove?: (el: HTMLElement, key: string) => void;
}

/**
 * Reconcile `parent`'s keyed children to match `items`, in order.
 * Children without `data-reconcile-key` are ignored.
 *
 * Algorithm: walk items in reverse, `insertBefore(el, target)` where
 * target is the previously-placed sibling (or null for the last item).
 * `insertBefore` is a no-op when el is already at the requested position,
 * so the common cases (no-op, append, prepend, in-place edit) cause no
 * DOM thrash.
 */
export function reconcile<T>(
  parent: ParentNode,
  items: readonly T[],
  spec: ReconcileSpec<T>,
): void {
  // Index existing managed children by key.
  const existing = new Map<string, HTMLElement>();
  for (let n = parent.firstChild; n !== null; n = n.nextSibling) {
    if (n.nodeType !== 1) continue;
    const el = n as HTMLElement;
    const k = el.getAttribute(KEY_ATTR);
    if (k !== null) existing.set(k, el);
  }

  // Walk items in reverse so insertBefore(el, target) builds the
  // sequence forward correctly.
  let target: Node | null = null;
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i] as T;
    const k = spec.key(item);
    let el = existing.get(k);
    if (el === undefined) {
      el = spec.mount(item);
      el.setAttribute(KEY_ATTR, k);
    } else {
      existing.delete(k);
      if (spec.update !== undefined) spec.update(el, item);
    }
    parent.insertBefore(el, target);
    target = el;
  }

  // Anything left in `existing` is an orphan; remove from DOM.
  for (const [k, el] of existing) {
    spec.onRemove?.(el, k);
    el.remove();
  }
}
