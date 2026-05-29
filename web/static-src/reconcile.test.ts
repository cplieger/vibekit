// @vitest-environment happy-dom

import { describe, it, expect } from "vitest";
import { reconcile, type ReconcileSpec } from "./reconcile.js";
import { signal, effect } from "./lib/reactive/index.js";

// ---------------------------------------------------------------------------
// reconcile: keyed-list reconciliation tests.
//
// Item shape used throughout: { id: string; label: string }. The label is
// what `update` writes into each row's textContent; assertions compare the
// rendered DOM against a string list.
// ---------------------------------------------------------------------------

interface Item {
  id: string;
  label: string;
}

function mount(item: Item): HTMLElement {
  const li = document.createElement("li");
  li.textContent = item.label;
  return li;
}

function update(el: HTMLElement, item: Item): void {
  el.textContent = item.label;
}

const spec: ReconcileSpec<Item> = { key: (i) => i.id, mount, update };

function rendered(parent: ParentNode): string[] {
  const out: string[] = [];
  for (let n = parent.firstChild; n !== null; n = n.nextSibling) {
    if (n.nodeType === 1) {
      out.push((n as HTMLElement).textContent ?? "");
    }
  }
  return out;
}

function snapshotRefs(parent: ParentNode): HTMLElement[] {
  const out: HTMLElement[] = [];
  for (let n = parent.firstChild; n !== null; n = n.nextSibling) {
    if (n.nodeType === 1) {
      out.push(n as HTMLElement);
    }
  }
  return out;
}

function makeUL(): HTMLUListElement {
  return document.createElement("ul");
}

describe("reconcile: empty cases", () => {
  it("empty parent + empty items → no-op", () => {
    const ul = makeUL();
    reconcile(ul, [], spec);
    expect(ul.children.length).toBe(0);
  });

  it("populated parent + empty items → all removed", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "B"]);
    reconcile(ul, [], spec);
    expect(ul.children.length).toBe(0);
  });

  it("empty parent + items → all mounted in order", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "B", "C"]);
  });
});

describe("reconcile: identity preservation", () => {
  it("no-op call: every element kept by reference", () => {
    const ul = makeUL();
    const items: Item[] = [
      { id: "a", label: "A" },
      { id: "b", label: "B" },
      { id: "c", label: "C" },
    ];
    reconcile(ul, items, spec);
    const before = snapshotRefs(ul);
    reconcile(ul, items, spec);
    const after = snapshotRefs(ul);
    expect(after).toEqual(before);
  });

  it("update mutates in place; identity preserved", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      spec,
    );
    const [aBefore, bBefore] = snapshotRefs(ul);
    reconcile(
      ul,
      [
        { id: "a", label: "A!" },
        { id: "b", label: "B!" },
      ],
      spec,
    );
    const [aAfter, bAfter] = snapshotRefs(ul);
    expect(aAfter).toBe(aBefore);
    expect(bAfter).toBe(bBefore);
    expect(rendered(ul)).toEqual(["A!", "B!"]);
  });

  it("reorder: identity preserved across moves", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    const refs = new Map<string, HTMLElement>();
    for (const el of snapshotRefs(ul)) {
      refs.set(el.getAttribute("data-reconcile-key") ?? "", el);
    }
    reconcile(
      ul,
      [
        { id: "c", label: "C" },
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["C", "A", "B"]);
    for (const el of snapshotRefs(ul)) {
      const k = el.getAttribute("data-reconcile-key") ?? "";
      expect(el).toBe(refs.get(k));
    }
  });
});

describe("reconcile: insert / remove / mixed", () => {
  it("appends new items at end", () => {
    const ul = makeUL();
    reconcile(ul, [{ id: "a", label: "A" }], spec);
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "B", "C"]);
  });

  it("prepends new items at front", () => {
    const ul = makeUL();
    reconcile(ul, [{ id: "z", label: "Z" }], spec);
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "z", label: "Z" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "B", "Z"]);
  });

  it("inserts in the middle", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "B", "C"]);
  });

  it("removes from the middle", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["A", "C"]);
  });

  it("reverse order", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    reconcile(
      ul,
      [
        { id: "c", label: "C" },
        { id: "b", label: "B" },
        { id: "a", label: "A" },
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["C", "B", "A"]);
  });

  it("mixed: insert + remove + update + reorder", () => {
    const ul = makeUL();
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      spec,
    );
    reconcile(
      ul,
      [
        { id: "d", label: "D" }, // new
        { id: "b", label: "B!" }, // updated + moved
        { id: "a", label: "A" }, // moved
        // c removed
      ],
      spec,
    );
    expect(rendered(ul)).toEqual(["D", "B!", "A"]);
  });
});

describe("reconcile: update optionality", () => {
  it("omitting update leaves existing rows unchanged even if data shape differs", () => {
    const ul = makeUL();
    const noUpdateSpec: ReconcileSpec<Item> = { key: (i) => i.id, mount };
    reconcile(ul, [{ id: "a", label: "A" }], noUpdateSpec);
    reconcile(ul, [{ id: "a", label: "A — but ignored" }], noUpdateSpec);
    expect(rendered(ul)).toEqual(["A"]);
  });

  it("update is called only on existing items, not on freshly-mounted ones", () => {
    const ul = makeUL();
    let updateCalls = 0;
    const countingSpec: ReconcileSpec<Item> = {
      key: (i) => i.id,
      mount,
      update: (el, item) => {
        updateCalls++;
        update(el, item);
      },
    };
    reconcile(ul, [{ id: "a", label: "A" }], countingSpec);
    expect(updateCalls).toBe(0);
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      countingSpec,
    );
    expect(updateCalls).toBe(1); // only "a" was already mounted
  });
});

describe("reconcile: non-keyed siblings preserved", () => {
  it("ignores children without data-reconcile-key, leaves them in place", () => {
    const ul = makeUL();
    const header = document.createElement("li");
    header.className = "header";
    header.textContent = "HEADER";
    ul.appendChild(header);

    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      spec,
    );
    // Header stays — appended before the new items.
    expect(rendered(ul)).toEqual(["HEADER", "A", "B"]);
    expect(ul.querySelector(".header")).toBe(header);

    reconcile(ul, [{ id: "b", label: "B" }], spec);
    expect(rendered(ul)).toEqual(["HEADER", "B"]);
    expect(ul.querySelector(".header")).toBe(header);
  });
});

describe("reconcile: nested usage", () => {
  it("inner reconcile inside mount/update produces correctly-keyed sublists", () => {
    interface Section {
      id: string;
      rows: Item[];
    }
    const root = document.createElement("div");

    const renderRow: ReconcileSpec<Item> = {
      key: (i) => i.id,
      mount: (i) => {
        const li = document.createElement("li");
        li.textContent = i.label;
        return li;
      },
      update: (el, i) => {
        el.textContent = i.label;
      },
    };

    const renderSection: ReconcileSpec<Section> = {
      key: (s) => s.id,
      mount: (s) => {
        const sec = document.createElement("section");
        const ul = document.createElement("ul");
        sec.appendChild(ul);
        reconcile(ul, s.rows, renderRow);
        return sec;
      },
      update: (sec, s) => {
        const ul = sec.querySelector("ul");
        if (ul !== null) {
          reconcile(ul, s.rows, renderRow);
        }
      },
    };

    reconcile(
      root,
      [
        {
          id: "s1",
          rows: [
            { id: "a", label: "A" },
            { id: "b", label: "B" },
          ],
        },
        { id: "s2", rows: [{ id: "c", label: "C" }] },
      ],
      renderSection,
    );

    const sec1 = root.children[0]!;
    const sec2 = root.children[1]!;
    expect(rendered(sec1.querySelector("ul")!)).toEqual(["A", "B"]);
    expect(rendered(sec2.querySelector("ul")!)).toEqual(["C"]);

    // Mutate inner rows; outer identity preserved, inner DOM patched.
    const sec1Before = sec1;
    reconcile(
      root,
      [
        { id: "s1", rows: [{ id: "a", label: "A!" }] }, // b removed, a updated
        {
          id: "s2",
          rows: [
            { id: "c", label: "C" },
            { id: "d", label: "D" },
          ],
        }, // d added
      ],
      renderSection,
    );

    expect(root.children[0]).toBe(sec1Before);
    expect(rendered(root.children[0]!.querySelector("ul")!)).toEqual(["A!"]);
    expect(rendered(root.children[1]!.querySelector("ul")!)).toEqual(["C", "D"]);
  });
});

describe("reconcile: signal integration", () => {
  it("effect + reconcile patches DOM on every signal mutation", () => {
    const ul = makeUL();
    const items = signal<readonly Item[]>([]);
    const stop = effect(() => {
      reconcile(ul, items.value, spec);
    });

    items.value = [{ id: "a", label: "A" }];
    expect(rendered(ul)).toEqual(["A"]);

    items.value = [
      { id: "a", label: "A" },
      { id: "b", label: "B" },
    ];
    expect(rendered(ul)).toEqual(["A", "B"]);

    // Identity preserved across the signal mutation
    const aBefore = ul.children[0];
    items.value = [
      { id: "b", label: "B" },
      { id: "a", label: "A!" },
    ];
    expect(rendered(ul)).toEqual(["B", "A!"]);
    expect(ul.children[1]).toBe(aBefore);

    items.value = [];
    expect(ul.children.length).toBe(0);

    stop();
  });
});

describe("reconcile: idempotency", () => {
  it("calling twice with the same input is a no-op the second time", () => {
    const ul = makeUL();
    const items: Item[] = [
      { id: "a", label: "A" },
      { id: "b", label: "B" },
      { id: "c", label: "C" },
    ];
    reconcile(ul, items, spec);
    const before = snapshotRefs(ul);
    reconcile(ul, items, spec);
    const after = snapshotRefs(ul);
    expect(after).toEqual(before);
    expect(rendered(ul)).toEqual(["A", "B", "C"]);
  });
});

describe("reconcile: data-reconcile-key tagging", () => {
  it("mounted elements receive the key as data-reconcile-key", () => {
    const ul = makeUL();
    reconcile(ul, [{ id: "alpha", label: "A" }], spec);
    expect(ul.children[0]!.getAttribute("data-reconcile-key")).toBe("alpha");
  });

  it("subsequent mounts with the same key reuse the existing element", () => {
    const ul = makeUL();
    reconcile(ul, [{ id: "x", label: "X" }], spec);
    const first = ul.children[0];
    reconcile(ul, [{ id: "x", label: "X2" }], spec);
    expect(ul.children[0]).toBe(first);
    expect(first?.textContent).toBe("X2");
  });
});

describe("reconcile: onRemove hook", () => {
  it("fires for each orphaned element with element + key, before DOM removal", () => {
    const ul = makeUL();
    document.body.appendChild(ul);
    const removed: { key: string; connected: boolean }[] = [];
    const teardownSpec: ReconcileSpec<Item> = {
      key: (i) => i.id,
      mount,
      update,
      onRemove: (el, key) => {
        removed.push({ key, connected: el.isConnected });
      },
    };
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
        { id: "c", label: "C" },
      ],
      teardownSpec,
    );
    reconcile(ul, [{ id: "b", label: "B" }], teardownSpec);
    expect(removed.map((r) => r.key).sort()).toEqual(["a", "c"]);
    // Element is still in the DOM at the time of the callback.
    expect(removed.every((r) => r.connected)).toBe(true);
    ul.remove();
  });

  it("does not fire when an element is kept (just moved or updated)", () => {
    const ul = makeUL();
    const removed: string[] = [];
    const teardownSpec: ReconcileSpec<Item> = {
      key: (i) => i.id,
      mount,
      update,
      onRemove: (_, key) => {
        removed.push(key);
      },
    };
    reconcile(
      ul,
      [
        { id: "a", label: "A" },
        { id: "b", label: "B" },
      ],
      teardownSpec,
    );
    reconcile(
      ul,
      [
        { id: "b", label: "B!" },
        { id: "a", label: "A!" },
      ],
      teardownSpec,
    );
    expect(removed).toEqual([]);
  });

  it("not provided: behavior unchanged", () => {
    const ul = makeUL();
    reconcile(ul, [{ id: "a", label: "A" }], spec);
    reconcile(ul, [], spec);
    expect(ul.children.length).toBe(0);
  });
});
