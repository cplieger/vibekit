// Two properties of `@cplieger/reactive` that vibekit code reasons from, neither
// stated in the library's own docs. A DEPENDENCY contract: its subject is the
// library, and its value is a version bump going red rather than a leak found later.

import { describe, it, expect } from "vitest";
import { signal, effect, reconcile } from "@cplieger/reactive";

describe("a nested effect's lifetime is its OWN", () => {
  // `mcp-ui.ts`'s foreign row list carried the opposite claim in a comment for as long
  // as it existed ("the enclosing list effect disposes nested effects on re-run"), and
  // the leak that belief hid was one live effect per row per re-run, each writing into
  // a detached node. The row's disposer is held in a map now, and this is the premise
  // that makes holding it necessary.
  it("survives its parent effect re-running", () => {
    const outer = signal(0);
    const inner = signal(0);
    const runs: number[] = [];
    const stop = effect(() => {
      void outer.value;
      effect(() => {
        runs.push(inner.value);
      });
    });
    expect(runs).toEqual([0]);

    outer.value = 1; // the parent re-runs and creates a SECOND nested effect

    inner.value = 1;
    // TWO handlers ran: the first nested effect is still live.
    expect(runs.filter((n) => n === 1)).toHaveLength(2);
    stop();
  });

  it("survives its parent effect being STOPPED", () => {
    const inner = signal(0);
    const runs: number[] = [];
    const stop = effect(() => {
      effect(() => {
        runs.push(inner.value);
      });
    });

    stop();
    inner.value = 1;

    expect(runs).toContain(1);
  });
});

// An element LEAVING the list leaves every element before it SEATED, which is what
// makes a key safe to derive from content again. It was the opposite until
// `@cplieger/reactive` 2.1.1: the placement walk ran before the departing elements were
// removed, so a predecessor's `nextSibling` pointed at a node about to vanish and the
// position guard re-seated a row nothing had changed, restarting its animations and
// dropping `:hover` and focus. Mechanism and the consumer rule: `web.md` "A KEYED
// RECONCILE IS NOT ENOUGH ON ITS OWN".
//
// A DEPENDENCY contract, so these four cases are what a downgrade or a regression in the
// library trips, and focus is the assertion because identity survives a re-seat.
describe("reconcile leaves a predecessor seated when a later element leaves", () => {
  const mount = (k: string): HTMLElement => {
    const b = document.createElement("button");
    b.textContent = k;
    return b;
  };

  function seated(items: string[]): { host: HTMLElement; first: HTMLButtonElement } {
    const host = document.createElement("div");
    document.body.appendChild(host);
    reconcile(host, items, { key: (k: string) => k, mount });
    const first = host.children[0] as HTMLButtonElement;
    first.focus();
    expect(document.activeElement).toBe(first);
    return { host, first };
  }

  it("when a later key CHANGES", () => {
    const { host, first } = seated(["a", "b", "c"]);
    try {
      reconcile(host, ["a", "b2", "c"], { key: (k: string) => k, mount });

      // Identity, order AND the reader's focus all survive.
      expect(host.children[0]).toBe(first);
      expect([...host.children].map((c) => c.textContent)).toEqual(["a", "b2", "c"]);
      expect(document.activeElement).toBe(first);
    } finally {
      host.remove();
    }
  });

  it("when a later element is simply REMOVED", () => {
    const { host, first } = seated(["a", "b", "c"]);
    try {
      reconcile(host, ["a", "c"], { key: (k: string) => k, mount });

      expect(host.children[0]).toBe(first);
      expect([...host.children].map((c) => c.textContent)).toEqual(["a", "c"]);
      expect(document.activeElement).toBe(first);
    } finally {
      host.remove();
    }
  });

  // An insertion was never exposed to the defect: an inserted element lands exactly
  // where the predecessor's `nextSibling` already points, so nothing before it moves
  // whichever order the library removes and places in.
  it("but NOT when an element is merely INSERTED", () => {
    const { host, first } = seated(["a", "c"]);
    try {
      reconcile(host, ["a", "b", "c"], { key: (k: string) => k, mount });

      expect(document.activeElement).toBe(first);
      expect([...host.children].map((c) => c.textContent)).toEqual(["a", "b", "c"]);
    } finally {
      host.remove();
    }
  });

  // Nor when the list is unchanged, which is the position guard's own property.
  it("nor when nothing about the list moved", () => {
    const { host, first } = seated(["a", "b", "c"]);
    try {
      reconcile(host, ["a", "b", "c"], { key: (k: string) => k, mount });

      expect(document.activeElement).toBe(first);
    } finally {
      host.remove();
    }
  });
});
