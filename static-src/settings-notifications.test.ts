// ---------------------------------------------------------------------------
// The master switch going ON: it enables the DEFAULT-ON kinds, not all of them.
//
// The four `true` writes enableEverything used to make are not interchangeable, so
// each of the three the reader can observe is asserted here — the wire value, the
// checkbox, and the in-memory state. Moving only the wire value leaves the pr_status
// box rendered checked and isKindEnabled answering true while the server has it off,
// which is the client-disagrees-with-server drift the OFF default exists inside.
//
// The failure arm is the other half: it writes false for EVERY kind, because it is a
// teardown of the whole feature rather than a re-application of defaults.
//
// persist.js and the push actions are the two unmanaged edges (a settings write and a
// subscription), so they are what is mocked; the disclosure primitive, the DOM and
// notify.ts's own state are real.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  patches: [] as Record<string, unknown>[],
  /** What the settings write answers: a record on success, null on refusal. */
  patchResult: { ok: true } as Record<string, unknown> | null,
  unregisters: 0,
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
    dispatch: async (): Promise<null> => null,
    cancel: (): void => {
      mocks.unregisters += 1;
    },
  },
  unsubscribePush: { dispatch: async (): Promise<null> => null },
}));

vi.mock("./actions/index.js", () => ({
  bindLoadingState: (): (() => void) => vi.fn(),
  registerCleanup: vi.fn(),
}));

const notify = await import("./notify.js");
const { initNotificationToggles } = await import("./settings-notifications.js");

const KIND_IDS = {
  agent_finished: "notify-finished-toggle",
  pr_status: "notify-pr-status-toggle",
  run_outcome: "notify-run-outcome-toggle",
} as const;

/** The panel's own markup, reduced to what initNotificationToggles reads. The three
 *  kind toggles are authored with the CHECKED state index.html gives them, so the
 *  pr_status box starts unchecked like the real first frame. */
function mountPanel(): void {
  const host = document.createElement("div");
  host.innerHTML = `
    <input type="checkbox" id="notify-toggle">
    <p id="notify-hint" class="hidden"></p>
    <div id="notify-sub-options" class="hidden">
      <input type="checkbox" id="notify-finished-toggle" checked>
      <input type="checkbox" id="notify-pr-status-toggle">
      <input type="checkbox" id="notify-run-outcome-toggle" checked>
    </div>`;
  document.body.replaceChildren(host);
}

function input(id: string): HTMLInputElement {
  const el = document.getElementById(id);
  expect(el, `${id} is not mounted`).not.toBeNull();
  return el as HTMLInputElement;
}

/** Flip the master switch the way a reader does, then let the write settle. */
async function turnMasterOn(): Promise<void> {
  const master = input("notify-toggle");
  master.checked = true;
  master.dispatchEvent(new Event("change"));
  for (let i = 0; i < 6; i++) {
    await Promise.resolve();
  }
}

beforeEach(() => {
  mocks.patches = [];
  mocks.patchResult = { ok: true };
  mocks.unregisters = 0;
  // Granted, so requestPermission neither prompts nor returns a hint — the prompt is
  // notify-permission.test.ts's subject, not this file's.
  vi.stubGlobal("Notification", { permission: "granted" });
  notify.setNotificationsEnabled(false);
  for (const kind of Object.keys(KIND_IDS)) {
    notify.setKindEnabled(kind, false);
  }
  mountPanel();
  initNotificationToggles();
});

describe("the master switch enables the default-ON kinds", () => {
  it("patches each kind's settings key with that kind's own default", async () => {
    await turnMasterOn();
    expect(mocks.patches).toHaveLength(1);
    expect(mocks.patches[0]).toEqual({
      notifications_enabled: true,
      notify_agent_finished: true,
      notify_pr_status: false,
      notify_run_outcome: true,
    });
  });

  it("leaves the pull-request checkbox unchecked and checks its two siblings", async () => {
    await turnMasterOn();
    expect(input(KIND_IDS.agent_finished).checked).toBe(true);
    expect(input(KIND_IDS.pr_status).checked).toBe(false);
    expect(input(KIND_IDS.run_outcome).checked).toBe(true);
  });

  it("leaves the in-memory state agreeing with the wire value", async () => {
    await turnMasterOn();
    expect(notify.areNotificationsEnabled()).toBe(true);
    expect(notify.isKindEnabled("agent_finished")).toBe(true);
    expect(notify.isKindEnabled("pr_status")).toBe(false);
    expect(notify.isKindEnabled("run_outcome")).toBe(true);
  });
});

describe("a refused write tears the feature down", () => {
  it("switches every kind off, including the default-ON ones", async () => {
    mocks.patchResult = null;
    await turnMasterOn();
    expect(notify.areNotificationsEnabled()).toBe(false);
    for (const kind of Object.keys(KIND_IDS)) {
      expect(notify.isKindEnabled(kind), `${kind} survived the teardown`).toBe(false);
    }
    expect(mocks.unregisters).toBe(1);
  });
});
