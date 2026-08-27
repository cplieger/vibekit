// The model pill's reasoning-tier readout: `nonDefaultEffortLabel`, which names
// the tier a chat runs at and ONLY when that tier departs from the model's own
// default.
//
// The default is never a local table. It arrives per model on the catalog
// (`default_effort_level`, from KAS `_meta.kiro.defaultEffortLevel`), so these
// tests hand it in the way the wire does and none of them hardcode which tier
// any real model defaults to.
import { beforeEach, describe, expect, it } from "vitest";
import { nonDefaultEffortLabel, setCatalogEfforts } from "./effort.js";
import type { ModelInfo, Session } from "./types.js";

/** A catalog entry: its default tier, and whether it advertises effort at all. */
function model(id: string, dflt?: string, hasEffort?: boolean): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
    ...(hasEffort === undefined ? {} : { has_effort: hasEffort }),
  };
}

/** Only the fields the resolver reads; the rest of Session is irrelevant here. */
function session(fields: {
  model: string;
  effort?: string;
  effort_active?: string;
  effort_levels?: { id: string; name?: string }[];
}): Session {
  return { id: "c1", effort: "", ...fields } as unknown as Session;
}

function fiveTiers(): { id: string; name?: string }[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

describe("the pill's reasoning-tier readout", () => {
  beforeEach(() => {
    setCatalogEfforts([], "");
  });

  it("says nothing when the chat runs at the model's own default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    // The ordinary case, and the pill has to stay quiet in it: a permanent
    // readout of the level everyone already gets marks nothing.
    expect(nonDefaultEffortLabel(s, models, "")).toBe("");
  });

  it("names the tier when the chat chose something other than the default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "max", effort_levels: fiveTiers() });

    expect(nonDefaultEffortLabel(s, models, "")).toBe("max");
  });

  it("names the chat's own choice over the level the session reports", () => {
    const models = [model("opus-5", "high")];
    const s = session({
      model: "opus-5",
      effort: "max",
      effort_active: "high",
      effort_levels: fiveTiers(),
    });

    // Same precedence the card's mark uses, and it has to be: one resolution
    // order is the whole reason this lives in one module. The choice leads so a
    // click shows on the pill through the optimistic store write, before KAS
    // answers with the new currentValue.
    expect(nonDefaultEffortLabel(s, models, "")).toBe("max");
  });

  it("falls through to what the session reports when the choice does not fit", () => {
    // Chosen on a model that had max; this one stops at high, and its default is
    // medium. A tier list is per model, so the choice is a level this service
    // rejects.
    const models = [model("sonnet-5", "medium")];
    const s = session({
      model: "sonnet-5",
      effort: "max",
      effort_active: "high",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // Naming max would claim a tier the session cannot reach. What it REPORTS is
    // the honest answer, and it is still a departure from the model's default, so
    // the pill does have something to say.
    expect(nonDefaultEffortLabel(s, models, "")).toBe("high");
  });

  it("says nothing when the chat chose nothing and the session runs the default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort_active: "high", effort_levels: fiveTiers() });

    expect(nonDefaultEffortLabel(s, models, "")).toBe("");
  });

  it("names the remembered pick on a chat with no session yet", () => {
    setCatalogEfforts(fiveTiers(), "high");
    const models = [model("opus-5", "high")];
    // A brand-new chat: no choice of its own, no session to report a level. The
    // server resolves the same seed into StartOpts.Effort, so the pill is stating
    // what the session will run at rather than guessing.
    const s = session({ model: "opus-5" });

    expect(nonDefaultEffortLabel(s, models, "max")).toBe("max");
  });

  it("ignores a remembered pick the current model does not offer", () => {
    const models = [model("sonnet-5", "medium")];
    const s = session({
      model: "sonnet-5",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // A remembered max is a level this model's service rejects, so the chat runs
    // at the model default and the pill has nothing to mark.
    expect(nonDefaultEffortLabel(s, models, "max")).toBe("");
  });

  it("says nothing when the catalog carries no default for this model", () => {
    const models = [model("older")];
    const s = session({ model: "older", effort: "max", effort_levels: fiveTiers() });

    // Without a default there is no way to know max is a departure. Withholding
    // is the honest answer; naming it would be a guess that reads as a fact.
    expect(nonDefaultEffortLabel(s, models, "")).toBe("");
  });

  it("says nothing for a model that advertises no reasoning effort", () => {
    // `auto` has no tiers at all (KAS hasEffort:false), which is also why the
    // card hides its tier row for it.
    const models = [model("auto", "", false), model("opus-5", "high", true)];
    const s = session({ model: "auto", effort: "max" });

    expect(nonDefaultEffortLabel(s, models, "max")).toBe("");
  });

  it("says nothing for a model the catalog does not know", () => {
    const models = [model("opus-5", "high", true)];
    const s = session({ model: "some-new-model", effort: "max", effort_levels: fiveTiers() });

    expect(nonDefaultEffortLabel(s, models, "")).toBe("");
  });

  it("labels the tier by the catalog's own name, else the house table", () => {
    const models = [model("opus-5", "high")];
    const named = session({
      model: "opus-5",
      effort: "xhigh",
      effort_levels: [{ id: "high" }, { id: "xhigh", name: "Extra high" }],
    });
    const unnamed = session({
      model: "opus-5",
      effort: "xhigh",
      effort_levels: [{ id: "high" }, { id: "xhigh" }],
    });

    expect(nonDefaultEffortLabel(named, models, "")).toBe("Extra high");
    // The house table exists so a bare `xhigh` reads as "x-high" rather than as
    // the id.
    expect(nonDefaultEffortLabel(unnamed, models, "")).toBe("x-high");
  });

  it("says nothing for a chat that has no session at all", () => {
    expect(nonDefaultEffortLabel(undefined, [model("opus-5", "high", true)], "")).toBe("");
  });
});
