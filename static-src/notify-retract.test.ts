// Tests for notify.ts's retraction half: a page notification tagged with its target
// is closed when that target's ask is settled, and so is every banner the service
// worker showed under the same tag.
//
// `Notification` is shadowed with a constructor that records instances, because the
// real one needs a granted permission a headless browser will not give, and
// `document.visibilityState` is forced hidden, which is the one branch that shows a
// page notification at all.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { NotificationRegistration } from "./notify.js";
import { chatTarget, runTarget } from "./push-subject.js";

vi.mock("./persist.js", () => ({
  patchSettings: (): Promise<{ ok: boolean }> => Promise.resolve({ ok: true }),
}));
vi.mock("./actions/notify.js", () => ({
  registerPush: { dispatch: (): Promise<null> => Promise.resolve(null) },
  unsubscribePush: { dispatch: (): Promise<null> => Promise.resolve(null) },
}));

const notify = await import("./notify.js");

/** A notification the fake constructor minted: its tag, whether it was closed, and
 *  the listeners it registered (close() fires "close", as the real one does). */
class FakeNotification extends EventTarget {
  static permission = "granted";
  static instances: FakeNotification[] = [];
  readonly tag: string;
  closed = false;
  constructor(_title: string, opts?: { tag?: string }) {
    super();
    this.tag = opts?.tag ?? "";
    FakeNotification.instances.push(this);
  }
  close(): void {
    this.closed = true;
    this.dispatchEvent(new Event("close"));
  }
}

/** A registration whose notifications are the tags handed in; close() records. */
function fakeRegistration(tags: string[]): {
  reg: NotificationRegistration;
  closed: string[];
  asked: string[];
} {
  const closed: string[] = [];
  const asked: string[] = [];
  const reg: NotificationRegistration = {
    getNotifications: (filter?: GetNotificationOptions) => {
      const want = filter?.tag;
      asked.push(want ?? "");
      return Promise.resolve(
        tags
          .filter((t) => want === undefined || t === want)
          .map((t) => ({ tag: t, close: () => closed.push(t) }) as unknown as Notification),
      );
    },
  };
  return { reg, closed, asked };
}

beforeEach(() => {
  FakeNotification.instances = [];
  vi.stubGlobal("Notification", FakeNotification);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
  notify.setNotificationsEnabled(true);
});

afterEach(() => {
  notify._setRegistrationForTest(null);
  notify.setNotificationsEnabled(false);
});

describe("notifyIfHidden's tag", () => {
  it("is the target's tag, so a chat's banner and the workspace cue take different slots", () => {
    expect(notify.notifyIfHidden("Vibekit", "ask", chatTarget("c1"))).toBe(true);
    expect(notify.notifyIfHidden("Vibekit", "done", chatTarget(""))).toBe(true);
    expect(FakeNotification.instances.map((n) => n.tag)).toEqual(["vibekit:c1", "vibekit"]);
  });
});

describe("closeNotificationsFor", () => {
  it("closes the page notification carrying the target's tag and no other", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyIfHidden("Vibekit", "ask on c1", chatTarget("c1"));
    notify.notifyIfHidden("Vibekit", "ask on c2", chatTarget("c2"));

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(FakeNotification.instances.map((n) => [n.tag, n.closed])).toEqual([
      ["vibekit:c1", true],
      ["vibekit:c2", false],
    ]);
  });

  it("closes exactly the registration's notifications with that tag", async () => {
    const { reg, closed, asked } = fakeRegistration(["vibekit:c1", "vibekit:c2", "vibekit"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(asked).toEqual(["vibekit:c1"]);
    expect(closed).toEqual(["vibekit:c1"]);
  });

  it("closes a run-keyed banner by the run target", async () => {
    const { reg, closed } = fakeRegistration(["vibekit:run:wf_1", "vibekit:c1"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsFor(runTarget("wf_1"));

    expect(closed).toEqual(["vibekit:run:wf_1"]);
  });

  it("is a no-op with nothing shown and no registration", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    await expect(notify.closeNotificationsFor(chatTarget("c9"))).resolves.toBeUndefined();
  });

  it("forgets a page notification the reader closed, so a later retraction does not close it twice", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyIfHidden("Vibekit", "ask", chatTarget("c1"));
    const first = FakeNotification.instances[0];
    first?.close();
    const closeSpy = vi.spyOn(first as FakeNotification, "close");

    await notify.closeNotificationsFor(chatTarget("c1"));

    expect(closeSpy).not.toHaveBeenCalled();
  });
});

// The fresh-hello sweep: the pending set is the whole truth about live asks, so every
// chat and run banner it does not name is stale, whether or not this page ever
// rendered the ask it announced.
describe("closeNotificationsExcept", () => {
  it("closes every chat and run banner the live set does not name, in one registration read", async () => {
    const { reg, closed, asked } = fakeRegistration([
      "vibekit:c1",
      "vibekit:c2",
      "vibekit:run:wf_1",
      "vibekit:run:wf_2",
    ]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsExcept(new Set(["vibekit:c2", "vibekit:run:wf_2"]));

    expect(asked, "one unfiltered read").toEqual([""]);
    expect(closed).toEqual(["vibekit:c1", "vibekit:run:wf_1"]);
  });

  it("leaves a pull request's banner and the constant-tag cue alone", async () => {
    // Neither has an ask the set could list: a PR's verdict stays until clicked, and
    // the constant tag is the agent-finished cue this page showed for no one chat.
    const { reg, closed } = fakeRegistration(["vibekit:pr:github:x#1", "vibekit", "vibekit:c1"]);
    notify._setRegistrationForTest(() => Promise.resolve(reg));

    await notify.closeNotificationsExcept(new Set());

    expect(closed).toEqual(["vibekit:c1"]);
  });

  it("closes the page's own chat banner the set does not name and keeps the one it does", async () => {
    notify._setRegistrationForTest(() => Promise.resolve(null));
    notify.notifyIfHidden("Vibekit", "ask on c1", chatTarget("c1"));
    notify.notifyIfHidden("Vibekit", "ask on c2", chatTarget("c2"));
    notify.notifyIfHidden("Vibekit", "done", chatTarget(""));

    await notify.closeNotificationsExcept(new Set(["vibekit:c2"]));

    expect(FakeNotification.instances.map((n) => [n.tag, n.closed])).toEqual([
      ["vibekit:c1", true],
      ["vibekit:c2", false],
      ["vibekit", false],
    ]);
  });
});

describe("the default registration", () => {
  it("is the current registration or none, never a promise that waits for one", async () => {
    // `serviceWorker.ready` never settles where registration failed or was refused, so a
    // retraction awaiting it would hang for the page's life; getRegistration answers now.
    const getRegistration = vi.fn(() => Promise.resolve(undefined));
    vi.stubGlobal("navigator", {
      serviceWorker: { getRegistration, ready: new Promise(() => undefined) },
    });
    notify._setRegistrationForTest(null);
    notify.notifyIfHidden("Vibekit", "ask on c1", chatTarget("c1"));

    await expect(notify.closeNotificationsFor(chatTarget("c1"))).resolves.toBeUndefined();

    expect(getRegistration).toHaveBeenCalledTimes(1);
    expect(FakeNotification.instances.map((n) => n.closed)).toEqual([true]);
  });
});
