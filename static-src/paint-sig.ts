/** Rebuild a subtree only when the state it renders moved.
 *
 *  For a subtree whose shape is derived wholesale from data. For a list whose
 *  members have identities, reach for `@cplieger/reactive`'s keyed `reconcile`
 *  first. Rules and the two disqualifiers: `web.md` "A `replaceChildren` DRIVEN
 *  BY AN EVENT THAT USUALLY CHANGES NOTHING".
 */

import { join } from "@cplieger/keyenc";

/** Where the last painted signature is recorded. Distinct from `reconcile`'s
 *  `data-reconcile-key`, so a reconciled row can carry both. */
const SIG_ATTR = "data-sig";

/**
 * Replace `host`'s children with `build()`'s output when `parts` differ from the
 * last call's. Answers whether it painted.
 *
 * `build` is a thunk so an unchanged subtree costs no construction either.
 */
export function paintIfChanged(
  host: Element,
  parts: readonly string[],
  build: () => readonly Node[],
): boolean {
  const sig = join(...parts);
  if (host.getAttribute(SIG_ATTR) === sig) {
    return false;
  }
  host.setAttribute(SIG_ATTR, sig);
  host.replaceChildren(...build());
  return true;
}

/**
 * The same guard without the paint, for a caller whose repaint is not one
 * `replaceChildren`. Answers whether `parts` moved, recording them either way.
 */
export function sigChanged(host: Element, parts: readonly string[]): boolean {
  const sig = join(...parts);
  if (host.getAttribute(SIG_ATTR) === sig) {
    return false;
  }
  host.setAttribute(SIG_ATTR, sig);
  return true;
}

/**
 * A whole DECODED wire value as one signature part.
 *
 * Total by construction: `JSON.stringify` over a value from `JSON.parse` walks
 * the keys in the producer's order, and Go's `encoding/json` emits struct fields
 * in declaration order. Only for a decoded value — a locally built object's key
 * order is its construction path's, so name its fields through a total
 * `Record<keyof T, string>` instead.
 */
export function wireSignature(value: object): string {
  // `object` rather than `unknown`: the three values `JSON.stringify` answers
  // `undefined` for are not assignable, so this needs no nullish fallback the
  // type system cannot see is live (`typescript.md` "typing untrusted input
  // DELETES the guards you need").
  return JSON.stringify(value);
}
