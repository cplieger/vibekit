// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";
import { signal, effect, batch, flushSync, reconcile, KEY_ATTR, patch } from "./index.js";

// ---------------------------------------------------------------------------
// signal + effect + batch
// ---------------------------------------------------------------------------

describe("signal", () => {
  it("reads and writes value", () => {
    expect.assertions(2);
    const s = signal(0);
    expect(s.value).toBe(0);
    s.value = 5;
    expect(s.peek()).toBe(5);
  });

  it("no-op on same value (Object.is)", () => {
    expect.assertions(1);
    const s = signal(1);
    const spy = vi.fn();
    effect(() => {
      void s.value;
      spy();
    });
    spy.mockClear();
    s.value = 1;
    expect(spy).not.toHaveBeenCalled();
  });

  it("NaN equality", () => {
    expect.assertions(1);
    const s = signal(NaN);
    const spy = vi.fn();
    effect(() => {
      void s.value;
      spy();
    });
    spy.mockClear();
    s.value = NaN;
    expect(spy).not.toHaveBeenCalled();
  });
});

describe("effect", () => {
  it("runs immediately on creation", () => {
    expect.assertions(1);
    const spy = vi.fn();
    effect(() => {
      spy();
    });
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("re-runs when dependency changes", () => {
    expect.assertions(2);
    const s = signal("a");
    const spy = vi.fn();
    effect(() => {
      spy(s.value);
    });
    expect(spy).toHaveBeenCalledWith("a");
    s.value = "b";
    expect(spy).toHaveBeenCalledWith("b");
  });

  it("tracks dynamic deps", () => {
    expect.assertions(1);
    const cond = signal(true);
    const a = signal(1);
    const b = signal(2);
    const spy = vi.fn();
    effect(() => {
      spy(cond.value ? a.value : b.value);
    });
    spy.mockClear();
    // b is not tracked when cond=true
    b.value = 99;
    expect(spy).not.toHaveBeenCalled();
  });

  it("cleanup runs before re-execution", () => {
    expect.assertions(2);
    const s = signal(0);
    const order: string[] = [];
    effect(() => {
      const v = s.value;
      order.push(`run:${v}`);
      return () => {
        order.push(`cleanup:${v}`);
      };
    });
    s.value = 1;
    expect(order).toEqual(["run:0", "cleanup:0", "run:1"]);
    expect(order.length).toBe(3);
  });

  it("disposal stops re-runs and calls cleanup", () => {
    expect.assertions(2);
    const s = signal(0);
    let cleaned = false;
    const dispose = effect(() => {
      void s.value;
      return () => {
        cleaned = true;
      };
    });
    dispose();
    expect(cleaned).toBe(true);
    s.value = 1;
    // Should not have re-run (no error, no extra call)
    expect(cleaned).toBe(true);
  });

  it("does not leak stale subscriptions on re-run", () => {
    expect.assertions(1);
    const a = signal(1);
    const b = signal(2);
    const cond = signal(true);
    const spy = vi.fn();
    effect(() => {
      spy(cond.value ? a.value : b.value);
    });
    // Switch to b
    cond.value = false;
    spy.mockClear();
    // a should no longer trigger
    a.value = 99;
    expect(spy).not.toHaveBeenCalled();
  });
});

describe("batch", () => {
  it("coalesces multiple signal writes", () => {
    expect.assertions(2);
    const s = signal(0);
    const spy = vi.fn();
    effect(() => {
      spy(s.value);
    });
    spy.mockClear();
    batch(() => {
      s.value = 1;
      s.value = 2;
      s.value = 3;
    });
    flushSync();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy).toHaveBeenCalledWith(3);
  });

  it("nested batch flushes only after outermost", () => {
    expect.assertions(2);
    const s = signal(0);
    const spy = vi.fn();
    effect(() => {
      spy(s.value);
    });
    spy.mockClear();
    batch(() => {
      batch(() => {
        s.value = 1;
      });
      // Still inside outer batch — effect should not have run
      expect(spy).not.toHaveBeenCalled();
    });
    // After outermost batch ends, pending effects are scheduled via MessageChannel.
    // flushSync drains them synchronously for testing.
    flushSync();
    expect(spy).toHaveBeenCalledWith(1);
  });
});

// ---------------------------------------------------------------------------
// createStore
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// reconcile (keyed-list)
// ---------------------------------------------------------------------------

describe("reconcile", () => {
  it("mounts items into empty parent", () => {
    expect.assertions(2);
    const parent = document.createElement("div");
    reconcile(parent, [{ id: "a" }, { id: "b" }], {
      key: (i) => i.id,
      mount: (i) => {
        const el = document.createElement("span");
        el.textContent = i.id;
        return el;
      },
    });
    expect(parent.children.length).toBe(2);
    expect(parent.children[0]!.getAttribute(KEY_ATTR)).toBe("a");
  });

  it("removes orphans", () => {
    expect.assertions(1);
    const parent = document.createElement("div");
    reconcile(parent, [{ id: "a" }, { id: "b" }], {
      key: (i) => i.id,
      mount: (i) => {
        const el = document.createElement("span");
        el.textContent = i.id;
        return el;
      },
    });
    reconcile(parent, [{ id: "a" }], {
      key: (i) => i.id,
      mount: (i) => {
        const el = document.createElement("span");
        el.textContent = i.id;
        return el;
      },
    });
    expect(parent.children.length).toBe(1);
  });

  it("calls onRemove for orphans", () => {
    expect.assertions(1);
    const parent = document.createElement("div");
    const removed: string[] = [];
    const spec = {
      key: (i: { id: string }) => i.id,
      mount: (i: { id: string }) => {
        const el = document.createElement("span");
        el.textContent = i.id;
        return el;
      },
      onRemove: (_el: HTMLElement, key: string) => {
        removed.push(key);
      },
    };
    reconcile(parent, [{ id: "a" }, { id: "b" }], spec);
    reconcile(parent, [{ id: "b" }], spec);
    expect(removed).toEqual(["a"]);
  });

  it("reorders without remounting", () => {
    expect.assertions(2);
    const parent = document.createElement("div");
    const spec = {
      key: (i: { id: string }) => i.id,
      mount: (i: { id: string }) => {
        const el = document.createElement("div");
        el.textContent = i.id;
        return el;
      },
    };
    reconcile(parent, [{ id: "a" }, { id: "b" }, { id: "c" }], spec);
    const origB = parent.children[1]!;
    reconcile(parent, [{ id: "c" }, { id: "b" }, { id: "a" }], spec);
    expect(parent.children[1]).toBe(origB); // same DOM node
    expect(parent.children[0]!.textContent).toBe("c");
  });

  it("calls update on existing items", () => {
    expect.assertions(1);
    const parent = document.createElement("div");
    const updated: string[] = [];
    const spec = {
      key: (i: { id: string; v: number }) => i.id,
      mount: (i: { id: string; v: number }) => {
        const el = document.createElement("div");
        el.textContent = String(i.v);
        return el;
      },
      update: (el: HTMLElement, i: { id: string; v: number }) => {
        el.textContent = String(i.v);
        updated.push(i.id);
      },
    };
    reconcile(parent, [{ id: "a", v: 1 }], spec);
    reconcile(parent, [{ id: "a", v: 2 }], spec);
    expect(updated).toEqual(["a"]);
  });
});

// ---------------------------------------------------------------------------
// patch (tree-diff)
// ---------------------------------------------------------------------------

describe("patch", () => {
  it("patches text content", () => {
    expect.assertions(1);
    const parent = document.createElement("div");
    parent.appendChild(document.createTextNode("old"));
    patch(parent, "new");
    expect(parent.textContent).toBe("new");
  });

  it("patches attributes", () => {
    expect.assertions(2);
    const parent = document.createElement("div");
    const child = document.createElement("span");
    child.setAttribute("class", "a");
    parent.appendChild(child);
    const newChild = document.createElement("span");
    newChild.setAttribute("class", "b");
    patch(parent, newChild);
    expect(parent.children[0]!.getAttribute("class")).toBe("b");
    expect(parent.children.length).toBe(1);
  });

  it("adds and removes children", () => {
    expect.assertions(2);
    const parent = document.createElement("div");
    patch(parent, document.createElement("p"), document.createElement("p"));
    expect(parent.children.length).toBe(2);
    patch(parent, document.createElement("p"));
    expect(parent.children.length).toBe(1);
  });
});
