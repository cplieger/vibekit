// ---------------------------------------------------------------------------
// One tray slot per SUBJECT, asserted through notifyIfHidden.
//
// pushTargetTag's own values are pinned in push-subject.test.ts; the claim here is
// the BINDING, which that unit test cannot make: that notifyIfHidden hands the
// per-subject tag to the Notification constructor, so an agent-finished note on one
// chat cannot silently replace the note on another. A constant tag would coalesce
// them into one slot and lose every note but the last.
//
// Notification is shadowed rather than driven, for notify-permission.test.ts's
// reason: a real notification in a headless browser is auto-dismissed, which would
// make this pass for the wrong reason.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { chatTarget } from "./push-subject.js";

vi.mock("./persist.js", () => ({
  patchSettings: async (): Promise<Record<string, unknown>> => ({ ok: true }),
}));

vi.mock("./actions/notify.js", () => ({
  registerPush: { dispatch: async (): Promise<null> => null, cancel: vi.fn() },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

const notify = await import("./notify.js");

/** The options every constructed notification was given, newest last. */
let opts: NotificationOptions[] = [];

/** A stand-in for window.Notification: a constructor recording its options, plus the
 *  two statics notifyIfHidden reads. */
function shadowNotification(): void {
  const fake = function fakeNotification(_title: string, o?: NotificationOptions): void {
    opts.push(o ?? {});
  } as unknown as {
    (title: string, o?: NotificationOptions): void;
    permission: string;
    prototype: { addEventListener: (t: string, f: () => void) => void };
  };
  fake.permission = "granted";
  // The real constructor returns an EventTarget and notifyIfHidden subscribes to its
  // click, so the instance has to accept a listener.
  fake.prototype = { addEventListener: vi.fn() };
  vi.stubGlobal("Notification", fake);
}

beforeEach(() => {
  opts = [];
  shadowNotification();
  // The page has to be in the background, or notifyIfHidden declines before it
  // constructs anything.
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  notify.setNotificationsEnabled(true);
});

describe("notifyIfHidden tags a notification by its subject", () => {
  it("gives two chats two different tags", () => {
    expect(notify.notifyIfHidden("Vibekit", "Agent finished", chatTarget("c1"))).toBe(true);
    expect(notify.notifyIfHidden("Vibekit", "Agent finished", chatTarget("c2"))).toBe(true);
    expect(opts).toHaveLength(2);
    expect(opts[0]?.tag).toBe("vibekit:c1");
    expect(opts[1]?.tag).toBe("vibekit:c2");
    expect(opts[0]?.tag).not.toBe(opts[1]?.tag);
  });

  it("gives the workspace-global subject the constant tag", () => {
    // `chatTarget("")` is how production reaches the workspace: an ask with no
    // envelope chat id resolves there through askTarget.
    expect(notify.notifyIfHidden("Vibekit", "Permission needed", chatTarget(""))).toBe(true);
    expect(opts[0]?.tag).toBe("vibekit");
  });
});
