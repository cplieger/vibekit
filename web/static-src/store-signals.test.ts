import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { SignalMap } from "./store-signals.js";

describe("SignalMap", () => {
  it("ensure on new key creates signal with initial value", () => {
    const map = new SignalMap<string>();
    const sig = map.ensure("a", "hello");
    expect(sig.peek()).toBe("hello");
  });

  it("ensure on existing key returns same signal, ignores new initial", () => {
    const map = new SignalMap<string>();
    const sig1 = map.ensure("a", "first");
    const sig2 = map.ensure("a", "second");
    expect(sig1).toBe(sig2);
    expect(sig2.peek()).toBe("first");
  });

  it("get on missing key returns undefined", () => {
    const map = new SignalMap<string>();
    expect(map.get("missing")).toBeUndefined();
  });

  it("get after ensure returns the signal", () => {
    const map = new SignalMap<string>();
    const sig = map.ensure("x", "val");
    expect(map.get("x")).toBe(sig);
  });

  it("clear removes the key", () => {
    const map = new SignalMap<string>();
    map.ensure("x", "val");
    map.clear("x");
    expect(map.get("x")).toBeUndefined();
  });

  it("clearAll removes all keys", () => {
    const map = new SignalMap<string>();
    map.ensure("a", "1");
    map.ensure("b", "2");
    map.ensure("c", "3");
    map.clearAll();
    expect(map.get("a")).toBeUndefined();
    expect(map.get("b")).toBeUndefined();
    expect(map.get("c")).toBeUndefined();
  });

  it("ensure after clear creates fresh signal", () => {
    const map = new SignalMap<string>();
    const sig1 = map.ensure("x", "first");
    map.clear("x");
    const sig2 = map.ensure("x", "second");
    expect(sig2).not.toBe(sig1);
    expect(sig2.peek()).toBe("second");
  });

  it("property: arbitrary operation sequences maintain consistency", () => {
    fc.assert(
      fc.property(
        fc.array(
          fc.oneof(
            fc.record({ op: fc.constant("ensure" as const), id: fc.string({ minLength: 1, maxLength: 5 }), val: fc.string() }),
            fc.record({ op: fc.constant("get" as const), id: fc.string({ minLength: 1, maxLength: 5 }) }),
            fc.record({ op: fc.constant("clear" as const), id: fc.string({ minLength: 1, maxLength: 5 }) }),
            fc.record({ op: fc.constant("clearAll" as const) }),
          ),
          { minLength: 1, maxLength: 50 },
        ),
        (ops) => {
          const map = new SignalMap<string>();
          const ref = new Map<string, string>();

          for (const op of ops) {
            switch (op.op) {
              case "ensure": {
                const o = op as { op: "ensure"; id: string; val: string };
                map.ensure(o.id, o.val);
                if (!ref.has(o.id)) {
                  ref.set(o.id, o.val);
                }
                break;
              }
              case "get": {
                const o = op as { op: "get"; id: string };
                const sig = map.get(o.id);
                if (ref.has(o.id)) {
                  expect(sig).toBeDefined();
                  expect(sig!.peek()).toBe(ref.get(o.id));
                } else {
                  expect(sig).toBeUndefined();
                }
                break;
              }
              case "clear": {
                const o = op as { op: "clear"; id: string };
                map.clear(o.id);
                ref.delete(o.id);
                break;
              }
              case "clearAll": {
                map.clearAll();
                ref.clear();
                break;
              }
            }
          }
        },
      ),
      { numRuns: 200 },
    );
  });
});
