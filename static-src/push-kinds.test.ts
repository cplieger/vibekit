// D101 / D104, client side.
//
// Three things are pinned here, and the first is a CROSS-LANGUAGE contract with no
// codegen behind it: the PR subject prefix is spelled in Go (vibekit.PRSubjectPrefix)
// and once in TypeScript (push-subject.ts, which is DOM-free, so the worker and the
// page both take it from there). Two copies of one literal is what the read below
// turns into a test rather than a hope.
import { describe, it, expect, beforeEach } from "vitest";
import { settingsPayload } from "./__test-helpers__/settings.js";
import pushTypesGo from "../internal/vibekit/push_types.go?raw";
import pushServiceGo from "../internal/push/service.go?raw";
import settingsDefaultsGo from "../internal/settings/defaults.go?raw";
import settingsEffectiveGo from "../internal/settings/effective.go?raw";
import pushSubjectSrc from "./push-subject.ts?raw";

/** The ONE TypeScript copy of the Go literal, by the path a failure should name. */
const tsCopies: Record<string, string> = {
  "static-src/push-subject.ts": pushSubjectSrc,
};

/** Each subject-key prefix, by the Go constant that declares it and the TypeScript
 *  constant the copy has to spell. */
const prefixes = [
  { goConst: "PRSubjectPrefix", tsConst: "PR_SUBJECT_PREFIX" },
  { goConst: "RunSubjectPrefix", tsConst: "RUN_SUBJECT_PREFIX" },
];

describe("subject prefixes", () => {
  for (const { goConst, tsConst } of prefixes) {
    it(`${goConst} is the same literal in Go and in the one TypeScript module`, () => {
      const m = new RegExp(`${goConst} = "([^"]+)"`).exec(pushTypesGo);
      expect(m, `vibekit.${goConst} not found in internal/vibekit/push_types.go`).not.toBeNull();
      const want = m?.[1] ?? "";
      expect(want).not.toBe("");
      for (const [rel, ts] of Object.entries(tsCopies)) {
        expect(ts, `${rel} does not carry the Go prefix ${want}`).toContain(
          `${tsConst} = "${want}"`,
        );
      }
    });
  }
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

  // The declared table, read as values: the three polarities are NOT uniform, and a
  // client default that disagrees with the server's is the drift class this repo has
  // already paid for once.
  it("declares pr_status OFF while its two siblings are ON", async () => {
    const { KEYED_PUSH_DEFAULTS } = await import("./notify.js");
    expect(KEYED_PUSH_DEFAULTS).toEqual({
      agent_finished: true,
      pr_status: false,
      run_outcome: true,
    });
  });

  // The seed, which is the half a table alone does not state: a client that has not
  // loaded /api/settings yet answers the table rather than "on" for everything.
  //
  // ORDERING PREMISE: this case reads state no earlier case in this file has written
  // to — the module instance is shared across the file, so a state write above it
  // would make it read that write instead of the seed.
  it("seeds the in-memory state from that table on a fresh client", async () => {
    const { KEYED_PUSH_DEFAULTS, isKindEnabled } = await import("./notify.js");
    for (const [kind, want] of Object.entries(KEYED_PUSH_DEFAULTS)) {
      expect(isKindEnabled(kind), `${kind} is seeded ${String(!want)}`).toBe(want);
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

/** Each keyed kind against the Go constant that declares its polarity. The Go name
 *  is spelled rather than derived from the settings key: any snake-to-Pascal rule
 *  yields `NotifyPrStatus` where the server declares `NotifyPRStatus`. */
const polarities = [
  { kind: "agent_finished", goConst: "DefaultNotifyAgentFinished" },
  { kind: "pr_status", goConst: "DefaultNotifyPRStatus" },
  { kind: "run_outcome", goConst: "DefaultNotifyRunOutcome" },
] as const;

// The client half of the polarity contract. `internal/push/kind_defaults_test.go`
// pins the send gate against the same constants; this pins the TS twin, so a flip
// on either side of the language boundary fails one case rather than shipping a
// client whose pre-fetch answer disagrees with what the server delivers.
describe("KEYED_PUSH_DEFAULTS against the Go constants", () => {
  it.each(polarities)("$kind carries settings.$goConst's value", async ({ kind, goConst }) => {
    // Anchored to a whole declaration line, and required to match exactly once: an
    // unanchored first-match would let a REFERENCE to the constant answer for its
    // declaration, and a second declaration would be read as agreement with the first.
    const decl = new RegExp(`^\\s*${goConst}\\s*=\\s*(true|false)\\s*$`, "gm");
    const hits = [...settingsEffectiveGo.matchAll(decl)];
    expect(
      hits,
      `settings.${goConst} is not declared exactly once in internal/settings/effective.go`,
    ).toHaveLength(1);
    const want = hits[0]?.[1] === "true";
    const { KEYED_PUSH_DEFAULTS } = await import("./notify.js");
    expect(KEYED_PUSH_DEFAULTS[kind]).toBe(want);
  });
});

describe("restoreNotifications", () => {
  beforeEach(async () => {
    const notify = await import("./notify.js");
    notify.setNotificationsEnabled(false);
  });

  it("reads every keyed kind from the payload", async () => {
    const notify = await import("./notify.js");
    notify.restoreNotifications(
      settingsPayload({
        notifications_enabled: true,
        notify_agent_finished: false,
        notify_pr_status: true,
      }),
    );
    expect(notify.areNotificationsEnabled()).toBe(true);
    expect(notify.isKindEnabled("agent_finished")).toBe(false);
    expect(notify.isKindEnabled("pr_status")).toBe(true);
  });

  // Every field of the effective payload is REQUIRED, so "absent" means absent from
  // config.json: the server resolved it to settings.Default* before answering, and
  // these two cases are the two polarities that resolution can carry.
  it("takes agent_finished's resolved default, which is ON", async () => {
    const notify = await import("./notify.js");
    notify.restoreNotifications(settingsPayload({ notifications_enabled: true }));
    expect(notify.isKindEnabled("agent_finished")).toBe(true);
  });

  it("takes pr_status's resolved default, which is OFF", async () => {
    const notify = await import("./notify.js");
    // A pull request's CI verdict is already on the forge and in the PRs tab, so this
    // is the one keyed kind a fresh install does not deliver.
    notify.restoreNotifications(settingsPayload({ notifications_enabled: true }));
    expect(notify.isKindEnabled("pr_status")).toBe(false);
  });

  it("keeps agent_finished's own getter working", async () => {
    const notify = await import("./notify.js");
    notify.restoreNotifications(
      settingsPayload({ notifications_enabled: true, notify_agent_finished: false }),
    );
    expect(notify.isAgentFinishedEnabled()).toBe(false);
  });
});
