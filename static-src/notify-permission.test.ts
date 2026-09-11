// ---------------------------------------------------------------------------
// The permission ask wired to the real browser: a cue that could not fire arms it,
// the reader's next click raises the prompt, and a grant turns the settings switch on.
//
// The reported gap is that vibekit never asked at all — the master switch defaults to
// not-opted-in, so `notifyIfHidden` refused every cue and nothing on any code path
// raised the browser prompt outside the Settings panel. So the claims here are the
// three links of that chain, end to end through the real `notifyIfHidden`.
//
// `notify-ask.node.test.ts` is the sibling that owns the DECISIONS with no DOM at
// all; this file owns the BINDING, which is the half a fake env cannot prove: that
// the arm sits where every cue passes, that a real click reaches the gesture, and
// that a grant reaches the settings write.
//
// `Notification` is shadowed rather than driven, because a real prompt in a headless
// browser is auto-dismissed and would make every case below pass for the wrong
// reason. `persist.js` and the push action are the two unmanaged edges (a settings
// write and a subscription), so they are the two things mocked.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { LS_NOTIFY_ASK_KEY } from "./ls-keys.js";

const mocks = vi.hoisted(() => ({
  patches: [] as Record<string, unknown>[],
  /** What the settings write answers: a record on success, null on refusal. */
  patchResult: { ok: true } as Record<string, unknown> | null,
  pushes: 0,
  cancels: 0,
}));

vi.mock("./persist.js", () => ({
  patchSettings: async (
    patch: Record<string, unknown>,
  ): Promise<Record<string, unknown> | null> => {
    mocks.patches.push(patch);
    return mocks.patchResult;
  },
}));

vi.mock("./actions/notify.js", () => ({
  registerPush: {
    dispatch: async (): Promise<null> => {
      mocks.pushes += 1;
      return null;
    },
    cancel: (): void => {
      mocks.cancels += 1;
    },
  },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

const notify = await import("./notify.js");

/** A stand-in for `window.Notification`: the two statics the ask reads, plus a
 *  settled answer the case chooses.
 *
 *  A FUNCTION with statics attached, because the real global is a constructor and the
 *  binding tests for one — an object here would pass the shadow while failing to
 *  represent what the browser exposes. */
interface FakeNotification {
  (): void;
  permission: string;
  requestPermission: () => Promise<string>;
  requests: number;
}

function shadowNotification(permission: string, answer: string | null): FakeNotification {
  const fake = function fakeNotification(): void {
    /* the constructor itself is never reached here; `notifyIfHidden` needs a grant */
  } as FakeNotification;
  fake.permission = permission;
  fake.requests = 0;
  fake.requestPermission = async (): Promise<string> => {
    fake.requests += 1;
    if (answer === null) {
      throw new Error("this browser refuses to be asked");
    }
    fake.permission = answer;
    return answer;
  };
  vi.stubGlobal("Notification", fake);
  return fake;
}

/** The prompt is raised from a click handler and adopted from a promise chain, so a
 *  case reading the outcome has to let both settle. */
async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

function click(): void {
  document.body.dispatchEvent(new MouseEvent("click", { bubbles: true }));
}

let teardown: (() => void) | undefined;

beforeEach(() => {
  mocks.patches = [];
  mocks.patchResult = { ok: true };
  mocks.pushes = 0;
  mocks.cancels = 0;
  localStorage.removeItem(LS_NOTIFY_ASK_KEY);
  notify._resetNotifyAskForTest();
  notify.setNotificationsEnabled(false);
  teardown = notify.installNotifyAskGesture();
});

afterEach(() => {
  teardown?.();
  localStorage.removeItem(LS_NOTIFY_ASK_KEY);
});

describe("a cue that could not fire arms the ask", () => {
  it("raises the prompt on the reader's next click", async () => {
    expect.assertions(3);
    const fake = shadowNotification("default", "granted");
    // The master switch is off, which is exactly why nothing was ever asked before:
    // this call refuses the cue and used to refuse silently.
    expect(notify.notifyIfHidden("Vibekit", "Agent finished")).toBe(false);
    expect(fake.requests).toBe(0); // the prompt needs a gesture, so not yet
    click();
    await settle();
    expect(fake.requests).toBe(1);
  });

  it("arms even while the page is in front of the reader", async () => {
    expect.assertions(2);
    // `notifyIfHidden` declines a visible page, and this is the ordering claim: the
    // arm leads every gate, so the moment a cue wanted to notify counts whether or
    // not it could. The reader is present, which is the best moment to be asked.
    expect(document.visibilityState).toBe("visible");
    const fake = shadowNotification("default", "granted");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(1);
  });

  it("raises nothing on a click with no cue behind it", async () => {
    expect.assertions(1);
    const fake = shadowNotification("default", "granted");
    click();
    click();
    await settle();
    expect(fake.requests).toBe(0);
  });

  it("raises nothing on a device whose ask is already answered", async () => {
    expect.assertions(1);
    localStorage.setItem(LS_NOTIFY_ASK_KEY, "1");
    const fake = shadowNotification("default", "granted");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(0);
  });

  it("raises nothing after the switch was turned off on purpose", async () => {
    expect.assertions(1);
    const fake = shadowNotification("default", "granted");
    // What the Settings master switch does on its way off. Both reach the client as
    // `notifications_enabled: false`, so this marker is the only thing separating a
    // refusal from "never opted in".
    notify.spendNotifyAsk();
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(0);
  });
});

describe("a grant turns the settings switch on", () => {
  it("writes the master key and subscribes push", async () => {
    expect.assertions(4);
    shadowNotification("default", "granted");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(mocks.patches).toEqual([{ notifications_enabled: true }]);
    expect(notify.areNotificationsEnabled()).toBe(true);
    expect(mocks.pushes).toBe(1);
    // The per-kind switches are their own choices and are not swept along.
    expect(Object.keys(mocks.patches[0] ?? {})).toEqual(["notifications_enabled"]);
  });

  it("leaves the switch off when the server refuses the write", async () => {
    expect.assertions(3);
    mocks.patchResult = null;
    shadowNotification("default", "granted");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(mocks.patches).toHaveLength(1);
    expect(notify.areNotificationsEnabled()).toBe(false);
    // No subscription either: one that outlived a refused write would deliver
    // notifications the settings page shows as off.
    expect(mocks.pushes).toBe(0);
  });

  it("writes nothing on a denial", async () => {
    expect.assertions(3);
    const fake = shadowNotification("default", "denied");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(1);
    expect(mocks.patches).toEqual([]);
    expect(notify.areNotificationsEnabled()).toBe(false);
  });

  it("records the device's answer so a reload does not ask again", async () => {
    expect.assertions(1);
    shadowNotification("default", "denied");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(localStorage.getItem(LS_NOTIFY_ASK_KEY)).toBe("1");
  });
});

describe("the Settings door and the automatic one share the ask", () => {
  it("spends the device's ask when Settings raises the prompt itself", () => {
    expect.assertions(2);
    const fake = shadowNotification("default", "granted");
    expect(notify.requestPermission()).toBe(null);
    expect(fake.requests).toBe(1);
  });

  it("leaves nothing for the automatic door after Settings has asked", async () => {
    expect.assertions(1);
    const fake = shadowNotification("default", "granted");
    notify.requestPermission();
    fake.permission = "default"; // the reader dismissed it rather than answering
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(1);
  });
});

describe("a browser that cannot be asked", () => {
  it("declines when the Notification API is absent", async () => {
    expect.assertions(2);
    vi.stubGlobal("Notification", undefined);
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(mocks.patches).toEqual([]);
    expect(localStorage.getItem(LS_NOTIFY_ASK_KEY)).toBe(null);
  });

  it("declines when permission was already denied, which no prompt reopens", async () => {
    expect.assertions(1);
    const fake = shadowNotification("denied", "granted");
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(0);
  });

  it("survives a browser that throws rather than be asked", async () => {
    expect.assertions(2);
    const fake = shadowNotification("default", null);
    notify.notifyIfHidden("Vibekit", "Agent finished");
    click();
    await settle();
    expect(fake.requests).toBe(1);
    expect(notify.areNotificationsEnabled()).toBe(false);
  });
});
