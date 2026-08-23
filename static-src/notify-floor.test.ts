// ---------------------------------------------------------------------------
// The protected-approval floor (D103), client side.
//
// The decision was to REMOVE the notify_permission off switch, and the failure
// mode a lazy removal leaves behind is a hidden control: the markup gone but the
// key, the getter and the mirror still wired, so the stall is one hand-edited
// config.json away. These tests assert the switch is unreachable from every
// direction the client owns — the markup, the settings type, the notify module's
// surface, and the module's own restore path.
//
// The server's half is push.TestPermissionKindHasNoSettingsKey (structural),
// push.TestPermissionKindSurvivesEveryConfig (behavioural) and
// server.TestSyncPushPreferences_permissionIsAFloor (the write path).
// ---------------------------------------------------------------------------

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import notifySrc from "./notify.ts?raw";
import persistSrc from "./persist.ts?raw";
import domSrc from "./dom.ts?raw";
import turnHandlerSrc from "./handlers/turn.ts?raw";

describe("the permission notification has no off switch", () => {
  const html = indexHtml;

  it("has no permission toggle in the markup", () => {
    expect(html).not.toContain("notify-permission-toggle");
  });

  // Not just deleted: the row is REPLACED by a line saying it is always on and
  // pointing at the relaxation. A removed control with no explanation reads as a
  // regression, and the reader who wanted fewer interruptions still needs the
  // answer.
  it("says the channel is always on and names the real relaxation", () => {
    const section = html.slice(html.indexOf("notify-sub-options"));
    expect(section).toContain("Always on");
    expect(section).toContain("blocks the turn");
    expect(section).toContain("Permissions");
  });

  it("keeps the agent-finished toggle, which IS a preference", () => {
    expect(html).toContain("notify-finished-toggle");
  });

  it("offers the workspace relaxation as the control that replaced it", () => {
    expect(html).toContain("workspace-relax-checkbox");
  });
});

describe("no client code can address the removed setting", () => {
  it("notify.ts exports no per-kind permission getter or setter", () => {
    expect(notifySrc).not.toContain("isPermissionNeededEnabled");
    expect(notifySrc).not.toContain("setPermissionNeededEnabled");
  });

  // restoreNotifications is the one place a config.json value reaches the
  // client's notification state. A read here is what would let a stale key
  // silence the ask on this device even with the server's floor holding.
  it("notify.ts never reads notify_permission from the settings payload", () => {
    // The property read and the type field, not the bare word: the module names
    // the removed key in a comment explaining why it is gone, and a test that
    // forbade the word would be a test against its own documentation.
    expect(notifySrc).not.toMatch(/\.notify_permission\b/);
    expect(notifySrc).not.toMatch(/^\s*notify_permission\?/m);
  });

  it("AppSettings does not declare the key", () => {
    expect(persistSrc).not.toMatch(/^\s*notify_permission\?/m);
  });

  it("the DOM registry has no getter for the removed toggle", () => {
    expect(domSrc).not.toContain("notifyPermissionToggle");
  });

  // The three turn-blocking asks must notify with no per-kind gate. Asserted on
  // the source because the behavioural half lives in handlers/turn.test.ts and
  // this is the structural claim: no gate exists to reintroduce.
  it("the ask handlers carry no per-kind gate", () => {
    expect(turnHandlerSrc).not.toContain("isPermissionNeededEnabled");
  });
});

describe("the master switch still governs everything", () => {
  it("notifyIfHidden gates on the master enabled flag", async () => {
    const notify = await import("./notify.js");
    // Nothing has enabled notifications, so the ask notification is suppressed
    // by the master gate — the one switch that is still a preference.
    expect(notify.areNotificationsEnabled()).toBe(false);
    expect(notify.notifyIfHidden("Vibekit", "Permission needed")).toBe(false);

    // And a settings payload still carrying the removed key changes nothing
    // about the permission channel: there is no field left to land in.
    notify.restoreNotifications({
      notifications_enabled: false,
      notify_agent_finished: true,
    });
    expect(notify.areNotificationsEnabled()).toBe(false);
  });
});
