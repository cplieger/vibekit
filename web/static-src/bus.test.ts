import { describe, it, expect, vi } from "vitest";
import {
  onSSE,
  dispatch,
  onBus,
  emitBus,
  BUS_KEYS_ESCAPE,
  BUS_TRANSPORT_GAP,
  BUS_TURN_IDLE,
} from "./bus.js";
import type { ServerEvent } from "./types.js";

// ---------------------------------------------------------------------------
// Table-driven tests for bus.ts dispatch/onSSE and onBus/emitBus.
// ---------------------------------------------------------------------------

describe("dispatch (SSE routing)", () => {
  const cases: {
    name: string;
    setup: () => { handler: ReturnType<typeof vi.fn>; unsub: () => void };
    event: ServerEvent;
    expectedCalls: unknown[][];
  }[] = [
    {
      name: "single subscriber receives event with payload",
      setup: () => {
        const handler = vi.fn();
        const unsub = onSSE("chat_created", handler);
        return { handler, unsub };
      },
      event: { type: "chat_created", chat_id: "c1", payload: { id: "c1", title: "Test" } },
      expectedCalls: [["c1", { id: "c1", title: "Test" }]],
    },
    {
      name: "subscriber receives empty string chatID when chat_id is undefined",
      setup: () => {
        const handler = vi.fn();
        const unsub = onSSE("settings_updated", handler);
        return { handler, unsub };
      },
      event: { type: "settings_updated" },
      expectedCalls: [["", undefined]],
    },
    {
      name: "unknown event type is a no-op (no handlers fire)",
      setup: () => {
        const handler = vi.fn();
        const unsub = onSSE("chat_created", handler);
        return { handler, unsub };
      },
      event: { type: "nonexistent_event" as ServerEvent["type"], chat_id: "x", payload: {} },
      expectedCalls: [],
    },
    {
      name: "unsubscribed handler does not fire",
      setup: () => {
        const handler = vi.fn();
        const unsub = onSSE("turn_ended", handler);
        unsub();
        return { handler, unsub };
      },
      event: { type: "turn_ended", chat_id: "c2", payload: { chat_id: "c2" } },
      expectedCalls: [],
    },
  ];

  it.each(cases)("$name", ({ setup, event, expectedCalls }) => {
    const { handler, unsub } = setup();
    try {
      dispatch(event);
      if (expectedCalls.length === 0) {
        expect(handler).not.toHaveBeenCalled();
      } else {
        expect(handler).toHaveBeenCalledTimes(expectedCalls.length);
        for (const args of expectedCalls) {
          expect(handler).toHaveBeenCalledWith(...args);
        }
      }
    } finally {
      unsub();
    }
  });

  it("multiple subscribers all fire for the same event", () => {
    const h1 = vi.fn();
    const h2 = vi.fn();
    const h3 = vi.fn();
    const unsub1 = onSSE("error", h1);
    const unsub2 = onSSE("error", h2);
    const unsub3 = onSSE("error", h3);
    try {
      const evt: ServerEvent = {
        type: "error",
        chat_id: "c1",
        payload: { code: "test", message: "fail" },
      };
      dispatch(evt);
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).toHaveBeenCalledTimes(1);
      expect(h3).toHaveBeenCalledTimes(1);
      expect(h1).toHaveBeenCalledWith("c1", { code: "test", message: "fail" });
    } finally {
      unsub1();
      unsub2();
      unsub3();
    }
  });

  it("handler error does not break other handlers", () => {
    const h1 = vi.fn();
    const throwing = vi.fn(() => {
      throw new Error("boom");
    });
    const h3 = vi.fn();
    const unsub1 = onSSE("forges_changed", h1);
    const unsub2 = onSSE("forges_changed", throwing);
    const unsub3 = onSSE("forges_changed", h3);
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {
      /* noop */
    });
    try {
      dispatch({ type: "forges_changed", chat_id: "" });
      expect(h1).toHaveBeenCalledTimes(1);
      expect(throwing).toHaveBeenCalledTimes(1);
      expect(h3).toHaveBeenCalledTimes(1);
      expect(consoleSpy).toHaveBeenCalledWith(
        expect.stringContaining("forges_changed"),
        expect.any(Error),
      );
    } finally {
      unsub1();
      unsub2();
      unsub3();
      consoleSpy.mockRestore();
    }
  });

  it("handler that unsubscribes a later handler does not skip it", () => {
    const h2 = vi.fn();
    let unsub2: () => void = () => {
      /* placeholder until assigned below */
    };
    const h1 = vi.fn(() => {
      unsub2();
    });
    const unsub1 = onSSE("forges_changed", h1);
    unsub2 = onSSE("forges_changed", h2);
    try {
      dispatch({ type: "forges_changed", chat_id: "" });
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).toHaveBeenCalledTimes(1);
    } finally {
      unsub1();
      unsub2!();
    }
  });

  it("handler that unsubscribes itself does not affect others", () => {
    const h2 = vi.fn();
    let unsub1: () => void = () => {
      /* placeholder until assigned below */
    };
    const h1 = vi.fn(() => {
      unsub1();
    });
    unsub1 = onSSE("forges_changed", h1);
    const unsub2 = onSSE("forges_changed", h2);
    try {
      dispatch({ type: "forges_changed", chat_id: "" });
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).toHaveBeenCalledTimes(1);
    } finally {
      unsub1();
      unsub2();
    }
  });
});

describe("onBus / emitBus (typed cross-module bus)", () => {
  it("subscriber receives event with no payload", () => {
    const handler = vi.fn();
    const unsub = onBus(BUS_KEYS_ESCAPE, handler);
    try {
      emitBus(BUS_KEYS_ESCAPE);
      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler).toHaveBeenCalledWith();
    } finally {
      unsub();
    }
  });

  it("subscriber receives event with payload", () => {
    const handler = vi.fn();
    const unsub = onBus(BUS_TRANSPORT_GAP, handler);
    try {
      const payload = { lastSeen: 7, floor: 2, head: 9 };
      emitBus(BUS_TRANSPORT_GAP, payload);
      expect(handler).toHaveBeenCalledTimes(1);
      expect(handler).toHaveBeenCalledWith(payload);
    } finally {
      unsub();
    }
  });

  it("event with no subscribers is a no-op", () => {
    const handler = vi.fn();
    const unsub = onBus(BUS_KEYS_ESCAPE, handler);
    try {
      // Emit an unrelated event; handler must stay quiet.
      emitBus(BUS_TURN_IDLE, "c1");
      expect(handler).not.toHaveBeenCalled();
    } finally {
      unsub();
    }
  });

  it("multiple subscribers all fire", () => {
    const h1 = vi.fn();
    const h2 = vi.fn();
    const unsub1 = onBus(BUS_TURN_IDLE, h1);
    const unsub2 = onBus(BUS_TURN_IDLE, h2);
    try {
      emitBus(BUS_TURN_IDLE, "c1");
      expect(h1).toHaveBeenCalledWith("c1");
      expect(h2).toHaveBeenCalledWith("c1");
    } finally {
      unsub1();
      unsub2();
    }
  });

  it("unsubscribe removes handler", () => {
    const handler = vi.fn();
    const unsub = onBus(BUS_TURN_IDLE, handler);
    unsub();
    emitBus(BUS_TURN_IDLE, "c1");
    expect(handler).not.toHaveBeenCalled();
  });

  it("handler error does not break other handlers", () => {
    const h1 = vi.fn();
    const throwing = vi.fn(() => {
      throw new Error("oops");
    });
    const h3 = vi.fn();
    const unsub1 = onBus(BUS_KEYS_ESCAPE, h1);
    const unsub2 = onBus(BUS_KEYS_ESCAPE, throwing);
    const unsub3 = onBus(BUS_KEYS_ESCAPE, h3);
    const consoleSpy = vi.spyOn(console, "error").mockImplementation(() => {
      /* noop */
    });
    try {
      emitBus(BUS_KEYS_ESCAPE);
      expect(h1).toHaveBeenCalledTimes(1);
      expect(throwing).toHaveBeenCalledTimes(1);
      expect(h3).toHaveBeenCalledTimes(1);
      expect(consoleSpy).toHaveBeenCalledWith(
        expect.stringContaining(BUS_KEYS_ESCAPE),
        expect.any(Error),
      );
    } finally {
      unsub1();
      unsub2();
      unsub3();
      consoleSpy.mockRestore();
    }
  });

  it("handler that unsubscribes a later handler does not skip it", () => {
    const h2 = vi.fn();
    let unsub2: () => void = () => {
      /* placeholder until assigned below */
    };
    const h1 = vi.fn(() => {
      unsub2();
    });
    const unsub1 = onBus(BUS_KEYS_ESCAPE, h1);
    unsub2 = onBus(BUS_KEYS_ESCAPE, h2);
    try {
      emitBus(BUS_KEYS_ESCAPE);
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).toHaveBeenCalledTimes(1);
    } finally {
      unsub1();
      unsub2!();
    }
  });

  it("handler that unsubscribes itself does not affect others", () => {
    const h2 = vi.fn();
    let unsub1: () => void = () => {
      /* placeholder until assigned below */
    };
    const h1 = vi.fn(() => {
      unsub1();
    });
    unsub1 = onBus(BUS_KEYS_ESCAPE, h1);
    const unsub2 = onBus(BUS_KEYS_ESCAPE, h2);
    try {
      emitBus(BUS_KEYS_ESCAPE);
      expect(h1).toHaveBeenCalledTimes(1);
      expect(h2).toHaveBeenCalledTimes(1);
    } finally {
      unsub1();
      unsub2();
    }
  });
});
