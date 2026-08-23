// D101 / D104, client side.
//
// Three things are pinned here, and the first is a CROSS-LANGUAGE contract with no
// codegen behind it: the PR subject prefix is spelled in Go (vibekit.PRSubjectPrefix),
// in the service worker (which compiles standalone and cannot import) and in the
// page handler. Three copies of one literal is what the read below turns into a
// test rather than a hope.
import { describe, it, expect, beforeEach } from "vitest";
import pushTypesGo from "../internal/vibekit/push_types.go?raw";
import pushServiceGo from "../internal/push/service.go?raw";
import settingsDefaultsGo from "../internal/settings/defaults.go?raw";
import swSrc from "./sw.ts?raw";
import pushMessageSrc from "./handlers/push-message.ts?raw";

/** The two TypeScript copies of the Go literal, by the path a failure should name. */
const tsCopies: Record<string, string> = {
  "static-src/sw.ts": swSrc,
  "static-src/handlers/push-message.ts": pushMessageSrc,
};

describe("PR subject prefix", () => {
  it("is the same literal in Go, the service worker and the page handler", () => {
    const m = /PRSubjectPrefix = "([^"]+)"/.exec(pushTypesGo);
    expect(m, "vibekit.PRSubjectPrefix not found in internal/vibekit/push_types.go").not.toBeNull();
    const want = m?.[1] ?? "";
    expect(want).not.toBe("");
    for (const [rel, ts] of Object.entries(tsCopies)) {
      expect(ts, `${rel} does not carry the Go prefix ${want}`).toContain(
        `PR_SUBJECT_PREFIX = "${want}"`,
      );
    }
  });
});

// The keyed-kind table drives both the settings rows and the per-kind state, so
// it has to agree with the server's registry.
describe("keyed push kinds", () => {
  it("names every kind the server registry gives a settings key, and no other", async () => {
    const { KEYED_PUSH_KINDS } = await import("./notify.js");
    // Each keyed entry reads {vibekit.PushKind<Name>, settings.Key<Name>, <default>};
    // the floor is the one entry whose key is the empty string.
    const entries = [
      ...pushServiceGo.matchAll(/\{vibekit\.PushKind(\w+),\s*(settings\.Key\w+|""),/g),
    ];
    expect(entries.length, "no kindRegistry entries parsed").toBeGreaterThan(1);
    const keyedCount = entries.filter((e) => e[2] !== '""').length;
    expect(Object.keys(KEYED_PUSH_KINDS)).toHaveLength(keyedCount);
    // And the settings keys match the ones the server reads.
    for (const settingsKey of Object.values(KEYED_PUSH_KINDS)) {
      expect(settingsDefaultsGo, `${settingsKey} is not a declared settings key`).toContain(
        `= "${settingsKey}"`,
      );
    }
  });

  it("does not give the permission floor an off switch", async () => {
    const { KEYED_PUSH_KINDS, setKindEnabled, isKindEnabled } = await import("./notify.js");
    expect(Object.keys(KEYED_PUSH_KINDS)).not.toContain("permission");
    // Nothing can create one by passing the name: an ask blocks the turn and has
    // no per-tab marker, so a channel that could go dark on its own would stall
    // every later turn with nothing on screen to say why.
    setKindEnabled("permission", false);
    expect(isKindEnabled("permission")).toBe(true);
  });
});

describe("restoreNotifications", () => {
  beforeEach(async () => {
    const notify = await import("./notify.js");
    notify.setNotificationsEnabled(false);
  });

  it("reads every keyed kind from the payload", async () => {
    const notify = await import("./notify.js");
    notify.restoreNotifications({
      notifications_enabled: true,
      notify_agent_finished: false,
      notify_pr_status: true,
    });
    expect(notify.areNotificationsEnabled()).toBe(true);
    expect(notify.isKindEnabled("agent_finished")).toBe(false);
    expect(notify.isKindEnabled("pr_status")).toBe(true);
  });

  it("defaults an absent kind to on, matching the server registry", async () => {
    const notify = await import("./notify.js");
    // A config.json written before pr_status existed carries no value for it, and
    // the server's registry entry is DefaultOn.
    notify.restoreNotifications({ notifications_enabled: true, notify_agent_finished: true });
    expect(notify.isKindEnabled("pr_status")).toBe(true);
  });

  it("keeps agent_finished's own getter working", async () => {
    const notify = await import("./notify.js");
    notify.restoreNotifications({ notifications_enabled: true, notify_agent_finished: false });
    expect(notify.isAgentFinishedEnabled()).toBe(false);
  });
});
