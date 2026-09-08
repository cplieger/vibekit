// The model pill's reasoning-tier readout: `effortPillLabel`, which names the
// tier a chat runs at, withholding only when the model advertises no reasoning
// effort or when no level resolved at all.
//
// The tier a model would have picked by itself is NOT something this readout
// branches on any more, and retiring that comparison is what most of these cases
// were rewritten for: `default_effort_level` arrives per model on the catalog,
// that catalog is empty on any bridgeless chat, so the readout used to be a
// function of whether a boot fetch had landed rather than of the level. It is
// still read, as the last rung of the live-tier chain, so these tests hand it in
// the way the wire does and none of them hardcodes which tier any real model
// defaults to. A catalog with NO default is a first-class case here rather than a
// degenerate one — it is what a chat with no bridge has.
import { beforeEach, describe, expect, it } from "vitest";
import { effortPillLabel, setCatalogEfforts } from "./effort.js";
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

  it("names the tier even when it IS the model's own default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    // The case the withholding was built for, and the reason it went: the default
    // is only knowable from the per-model catalog, which is empty on any
    // bridgeless chat, so whether this chat's tier appeared depended on whether a
    // boot fetch had landed. The pill is a readout of the level in force.
    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the tier when the chat chose something other than the default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("max");
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
    expect(effortPillLabel(s, models, "")).toBe("max");
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
    // the honest answer.
    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the level the session reports, default or not", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort_active: "high", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the remembered pick on a chat with no session yet", () => {
    setCatalogEfforts(fiveTiers(), "high");
    const models = [model("opus-5", "high")];
    // A brand-new chat: no choice of its own, no session to report a level. The
    // server resolves the same seed into StartOpts.Effort, so the pill is stating
    // what the session will run at rather than guessing.
    const s = session({ model: "opus-5" });

    expect(effortPillLabel(s, models, "max")).toBe("max");
  });

  it("falls back to the model's own default when the remembered pick does not fit", () => {
    const models = [model("sonnet-5", "medium")];
    const s = session({
      model: "sonnet-5",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // The REJECTION is what this asserts: a remembered max is a level this model's
    // service refuses, so the chain falls through to the model's own default and
    // the pill names that instead of a tier the session cannot reach.
    expect(effortPillLabel(s, models, "max")).toBe("medium");
  });

  it("names a chosen tier even when the catalog carries no default", () => {
    const models = [model("older")];
    const s = session({ model: "older", effort: "max", effort_levels: fiveTiers() });

    // `ifOffered` has already checked this model offers it, so the pill states
    // what the session will run at.
    expect(effortPillLabel(s, models, "")).toBe("max");
  });

  it("names a chosen tier with an EMPTY catalog", () => {
    // The worst case and the ordinary one: the pre-session feed answered with no
    // models at all, which it does on any failure and whenever KAS has not
    // resolved its model list yet.
    const s = session({ model: "opus-5", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "")).toBe("max");
  });

  it("names the remembered pick with an EMPTY catalog", () => {
    // Same reasoning one rung down the resolution order: the server resolves this
    // same seed into StartOpts.Effort under the same per-model gate.
    const s = session({ model: "opus-5", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "max")).toBe("max");
  });

  it("names a service-resolved tier with no default to compare it to", () => {
    // The chat chose nothing and remembered nothing, so `high` is the level the
    // service resolved. It is still the level this chat runs at, and that is what
    // the pill reports — who decided it is not a question the readout asks.
    const s = session({ model: "opus-5", effort_active: "high", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "")).toBe("high");
  });

  it("says nothing when NO level resolves at all", () => {
    // Every rung of the chain is empty: no choice, no reported level, no
    // remembered pick, no per-model default, no pre-session template. Naming a
    // tier here would invent one.
    const models = [model("opus-5", undefined, true)];
    const s = session({ model: "opus-5", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("");
  });

  it("says nothing for a model that advertises no reasoning effort", () => {
    // `auto` has no tiers at all (KAS hasEffort:false), which is also why the
    // card hides its tier row for it.
    const models = [model("auto", "", false), model("opus-5", "high", true)];
    const s = session({ model: "auto", effort: "max" });

    expect(effortPillLabel(s, models, "max")).toBe("");
  });

  it("says nothing for a model whose capability is plumbed false even with a chosen tier", () => {
    // The case above leaves the tier list to the fallback vocabulary; this one
    // hands the session its own, so nothing about level RESOLUTION is in question
    // and the gate is provably what withholds.
    const models = [model("auto", "", false), model("opus-5", "high", true)];
    const s = session({ model: "auto", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("");
  });

  it("says nothing for a model the catalog does not know", () => {
    const models = [model("opus-5", "high", true)];
    const s = session({ model: "some-new-model", effort: "max", effort_levels: fiveTiers() });

    // The `hasEffort` gate answers this one: the catalog plumbs the capability and
    // says nothing about this model, so as far as the client knows the model
    // offers no tiers at all. A chosen tier is named for a model the catalog KNOWS
    // but has no default for (above); a model it does not know is a different
    // question.
    expect(effortPillLabel(s, models, "")).toBe("");
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

    expect(effortPillLabel(named, models, "")).toBe("Extra high");
    // The house table exists so a bare `xhigh` reads as "x-high" rather than as
    // the id.
    expect(effortPillLabel(unnamed, models, "")).toBe("x-high");
  });

  it("says nothing for a chat that has no session at all", () => {
    expect(effortPillLabel(undefined, [model("opus-5", "high", true)], "")).toBe("");
  });
});
