// ---------------------------------------------------------------------------
// The notification-permission ask: when the app may raise the browser prompt.
//
// The `node` project with no DOM, and that is the property under test as much as any
// assertion here: every capability arrives through `NotifyAskEnv`, so if a decision
// ever starts reading `Notification` or `localStorage` directly this file stops
// loading. `notify.ts` owns the browser binding and `notify-permission.test.ts` is
// its sibling in the browser project.
//
// Two rules are vibekit's own rather than the reference's, and both fail silently if
// a port gets them wrong:
//
//  1. ONE ASK PER DEVICE, not one per page. Settings is a standing door to the same
//     prompt here, so an automatic ask returning on every reload is a nag.
//  2. A REFUSAL IS AN ANSWER. `notifications_enabled` reaches the client as a
//     resolved boolean, so spending the ask when the switch goes off is the only
//     thing that stops a later grant reversing it.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach } from "vitest";
import { createNotifyAsk, type NotifyAsk, type NotifyAskEnv } from "./notify-ask.js";

interface Harness {
  ask: NotifyAsk;
  env: NotifyAskEnv;
  /** How many times the prompt was raised. */
  requests: () => number;
  /** How many times a grant was adopted. */
  grants: () => number;
  /** Whether this device's ask has been recorded as answered. */
  spent: () => boolean;
  supported: (v: boolean) => void;
  permission: (v: string) => void;
  /** What the next prompt resolves to, or a throw when null. */
  answer: (v: string | null) => void;
}

function harness(): Harness {
  let supported = true;
  let permission = "default";
  let spent = false;
  let answer: string | null = "granted";
  let requests = 0;
  let grants = 0;

  const env: NotifyAskEnv = {
    supported: () => supported,
    permission: () => permission,
    request: async () => {
      requests += 1;
      if (answer === null) {
        throw new Error("this browser refuses to be asked");
      }
      permission = answer;
      return answer;
    },
    spent: () => spent,
    markSpent: () => {
      spent = true;
    },
    granted: () => {
      grants += 1;
    },
  };

  return {
    ask: createNotifyAsk(env),
    env,
    requests: () => requests,
    grants: () => grants,
    spent: () => spent,
    supported: (v) => {
      supported = v;
    },
    permission: (v) => {
      permission = v;
    },
    answer: (v) => {
      answer = v;
    },
  };
}

/** The request is fire-and-forget inside `gesture()`, so a case reading `grants()`
 *  has to let its promise chain settle first. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

describe("the ask needs both an arm and a gesture", () => {
  let h: Harness;
  beforeEach(() => {
    h = harness();
  });

  it("raises nothing on a gesture with nothing armed", () => {
    expect.assertions(2);
    h.ask.gesture();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
    expect(h.spent()).toBe(false);
  });

  it("raises nothing on an arm alone, because the prompt needs user activation", () => {
    expect.assertions(1);
    h.ask.arm();
    expect(h.requests()).toBe(0);
  });

  it("raises the prompt on the first gesture after an arm", () => {
    expect.assertions(1);
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(1);
  });

  it("raises it once per page whatever else happens", () => {
    expect.assertions(1);
    h.ask.arm();
    h.ask.gesture();
    h.ask.arm();
    h.ask.gesture();
    h.ask.gesture();
    expect(h.requests()).toBe(1);
  });
});

describe("what makes an ask worth raising", () => {
  let h: Harness;
  beforeEach(() => {
    h = harness();
  });

  it("declines a browser with no Notification API at all", () => {
    expect.assertions(1);
    h.supported(false);
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines an already-granted permission — there is nothing to ask", () => {
    expect.assertions(1);
    h.permission("granted");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines a denied permission, which a prompt cannot reopen", () => {
    expect.assertions(1);
    h.permission("denied");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("declines a permission value it does not recognise", () => {
    expect.assertions(1);
    h.permission("something-newer");
    h.ask.arm();
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });

  it("re-reads the gates at the gesture, so a sibling tab's answer wins", () => {
    expect.assertions(1);
    h.ask.arm();
    h.permission("granted"); // answered in another tab while this one waited for a click
    h.ask.gesture();
    expect(h.requests()).toBe(0);
  });
});

describe("one ask per device", () => {
  it("declines a FRESH page on a device whose ask is already spent", () => {
    expect.assertions(1);
    const h = harness();
    h.ask.spend();
    // A new page load: its own flags are clean, so the marker is the only thing left
    // to decline on. Asking the SAME instance would prove nothing, because `spend`
    // sets its per-page flag too.
    const reloaded = createNotifyAsk(h.env);
    reloaded.arm();
    reloaded.gesture();
    expect(h.requests()).toBe(0);
  });

  it("spends the device's ask on a DISMISSAL, which is the case a page flag misses", () => {
    expect.assertions(3);
    const h = harness();
    // Dismissed rather than answered: `permission` stays at `"default"`, so nothing
    // about the browser's own state stops the next page asking again. This is the
    // whole reason the marker outlives the page.
    h.answer("default");
    h.ask.arm();
    h.ask.gesture();
    expect(h.spent()).toBe(true);
    expect(h.requests()).toBe(1);
    const reloaded = createNotifyAsk(h.env);
    reloaded.arm();
    reloaded.gesture();
    expect(h.requests()).toBe(1);
  });

  it("spends it on a refusal recorded through no prompt at all", () => {
    expect.assertions(2);
    const h = harness();
    h.ask.spend();
    expect(h.spent()).toBe(true);
    expect(h.requests()).toBe(0);
  });

  it("spends it even when the browser throws rather than be asked", async () => {
    expect.assertions(3);
    const h = harness();
    h.answer(null);
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.requests()).toBe(1);
    expect(h.grants()).toBe(0);
    expect(h.spent()).toBe(true);
  });
});

describe("adopting the answer", () => {
  it("adopts a grant", async () => {
    expect.assertions(1);
    const h = harness();
    h.answer("granted");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(1);
  });

  it("adopts nothing on a denial", async () => {
    expect.assertions(1);
    const h = harness();
    h.answer("denied");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(0);
  });

  it("adopts nothing on a dismissal, which leaves the permission at default", async () => {
    expect.assertions(2);
    const h = harness();
    h.answer("default");
    h.ask.arm();
    h.ask.gesture();
    await settle();
    expect(h.grants()).toBe(0);
    // And the device's ask is still spent, so the dismissal is not re-raised on the
    // next load. Settings is the recovery path.
    expect(h.spent()).toBe(true);
  });
});
