// The effort section of the model card, which had two defects a user hit in the
// same click:
//
//  1. Nothing was ever marked. The section read the chat's OWN level and nothing
//     else, and a chat carries none until its first pick, so the control showed
//     five tiers with none selected and implied the session had no effort level.
//     It always has one, and kiro-cli reports it as the `effortLevel` config
//     option's currentValue.
//  2. The tiers were a hardcoded five. They are the same option's own choices,
//     which is exactly how kiro-cli 2.18.0's TUI builds its picker (and it
//     refuses the command when that list is empty), so a fixed five could offer a
//     tier the service rejects.
//
// These tests drive the real controller through the expand callback, because the
// rebuild happens on open — that is the moment a late catalog is picked up.
//
// The section is a stepped SLIDER now, so the readings changed and the behaviours
// did not: the rendered vocabulary is the tick list, the live tier is the knob, and
// a pick is a gesture on the knob rather than a click on one of five buttons. Every
// gesture here is a KEY, because this file mounts no stylesheet: with no layout the
// track and the knob measure the same width, so the pointer path's own
// snap-to-nearest has nothing to resolve against. `effort-slider.test.ts` mounts
// the real cascade and drives the pointer.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { ModelInfo } from "./types.js";
import type * as Strings from "./strings.js";

/** Catalog the module reads through picker.getCachedModels. */
let cachedModels: ModelInfo[] = [];

/** The stand-in line picker.ts hands out when there is no model list, staged here
 *  because the COPY is picker.ts's to own and this file's subject is what the card
 *  does with it. */
let notice: { text: string; busy: boolean } | null = null;

const { onExpand, effortDispatch, setLastEffortSpy } = vi.hoisted(() => ({
  onExpand: { fn: null as null | (() => void) },
  effortDispatch: vi.fn(),
  setLastEffortSpy: vi.fn(),
}));

/** The remembered last pick (`last_effort` + the model it was picked under) the
 *  module reads through session-context. The mocked setter writes the pair, so
 *  one holder both drives the model-scoped seed and records that a click
 *  remembered the level. */
let lastEffort = "";
let lastEffortModel = "";

vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _content: HTMLElement, opts?: { onExpand?: () => void }) => {
    onExpand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));

/** The minimum of a reactive signal this file needs, spelled locally so the mock
 *  factory does not reach for an `import()` type. */
interface SignalOf<T> {
  value: T;
}
type SignalFactory = <T>(initial: T) => SignalOf<T>;
interface TestSession {
  id: string;
  model: string;
  effort: string;
  effort_active?: string;
  effort_levels?: { id: string; name?: string }[];
}

// The store is real reactive state: the mark rides an effect over activeSession,
// so a fake signal would test the mock instead of the wiring.
vi.mock("./store.js", async () => {
  const { signal } = (await vi.importActual("@cplieger/reactive")) as { signal: SignalFactory };
  const activeSession = signal<TestSession | undefined>(undefined);
  return {
    activeSession,
    get: () => activeSession.value,
    getActive: () => activeSession.value,
    getActiveId: () => activeSession.value?.id ?? "",
    isEmptyChat: () => false,
    isThinking: () => false,
    setEffort: vi.fn(),
    setModel: vi.fn(),
  };
});

vi.mock("./picker.js", () => ({
  getCachedModels: () => cachedModels,
  refreshPickerIfVisible: vi.fn(),
  catalogNotice: () => notice,
  // Named because Browser Mode links ESM for real: a factory missing a name the
  // module under test imports fails COLLECTION, not the case that uses it. The
  // card's Retry is model-switcher-catalog.test.ts's subject.
  retryCatalog: vi.fn(),
  RETRY_LABEL: "Retry loading the model list",
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(),
  transportAction: () => ({ dispatch: effortDispatch }),
  retryNetwork: vi.fn(),
  RETRY_STANDARD: {},
}));
vi.mock("./actions/chat.js", () => ({ switchModel: { dispatch: vi.fn() } }));
vi.mock("./reconcile.js", () => ({ reconcile: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./session-context.js", () => ({
  setCurrentModel: vi.fn(),
  setLastModel: vi.fn(),
  getLastEffortFor: (model: string) =>
    model !== "" && model === lastEffortModel ? lastEffort : "",
  setLastEffort: (level: string, model: string) => {
    lastEffort = level;
    lastEffortModel = model;
    setLastEffortSpy(level, model);
  },
}));
// Only `humanName` is stubbed, to identity, so an assertion reads the model id it
// was given. Everything else stays REAL: `rateLabel` is pure and its whole job is
// deciding whether a credit readout exists at all, so a constant here would hide
// exactly the coercion this suite renders.
vi.mock("./strings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Strings>()),
  humanName: (s: string) => s,
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./icons.js", () => ({ ICON_MODEL: "" }));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn(), focusFirst: vi.fn(), dispose: vi.fn() }),
}));

import { activeSession } from "./store.js";
import { initModelSwitcher } from "./model-switcher.js";
import { setCatalogEfforts } from "./effort.js";

/** A catalog entry carrying the model's own default tier. */
function model(id: string, dflt?: string): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
  };
}

/** Open the card. Nothing draws the tiers, so the vocabulary reaches the DOM as the
 *  slider's ARIA range plus the tier the knob names; hence the two readers below. */
function openCard(): void {
  onExpand.fn?.();
}

/** How many tiers the card rendered, off the ARIA range. */
function tierCount(): number {
  openCard();
  return Number(knob().getAttribute("aria-valuemax")) + 1;
}

/** The tier ids at the two ends of the range — a count cannot say WHICH four. The
 *  walk dispatches and leaves the knob at the top tier, so callers re-open the card
 *  afterwards through `markedTier` rather than relying on order. */
async function tierEnds(): Promise<readonly [string, string]> {
  openCard();
  knob().focus();
  await userEvent.keyboard("{Home}");
  const low = knob().dataset["level"] ?? "";
  await userEvent.keyboard("{End}");
  return [low, knob().dataset["level"] ?? ""] as const;
}

/** The slider, after opening the card. One tab stop, so one `role="slider"`. */
function knob(): HTMLElement {
  const found = [...document.querySelectorAll<HTMLElement>('[role="slider"]')];
  expect(found, "the card renders exactly one slider").toHaveLength(1);
  return found[0] as HTMLElement;
}

/** The tier the knob is showing, cross-checked against the two channels it
 *  announces on: one function writes the position, `aria-valuenow` and
 *  `aria-valuetext` together, so a disagreement here is that rule breaking. */
function markedTier(): string {
  openCard();
  const k = knob();
  const now = Number(k.getAttribute("aria-valuenow"));
  const max = Number(k.getAttribute("aria-valuemax"));
  expect(k.getAttribute("aria-valuemin")).toBe("0");
  expect(now, "the reported index is inside the reported range").toBeGreaterThanOrEqual(0);
  expect(now).toBeLessThanOrEqual(max);
  expect(k.getAttribute("aria-valuetext"), "the announced tier is the word the caption names").toBe(
    document.querySelector(".effort-value")?.textContent ?? "",
  );
  return k.dataset["level"] ?? "";
}

/** Open the card, focus the knob, then send `keys` in order. A sequence has to run
 *  inside ONE open: every render re-applies the tier the store reports, and the
 *  store is mocked here, so re-opening between two keys would rewind the knob. */
async function press(...keys: readonly string[]): Promise<void> {
  openCard();
  knob().focus();
  for (const key of keys) {
    await userEvent.keyboard(key);
  }
}

function setSession(s: TestSession): void {
  (activeSession as unknown as { value: TestSession }).value = s;
}

/** The five-tier vocabulary as catalog entries. */
function fiveTiers(): { id: string; name?: string }[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

describe("the effort section", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    cachedModels = [];
    notice = null;
    onExpand.fn = null;
    lastEffort = "";
    lastEffortModel = "";
    setLastEffortSpy.mockClear();
    setCatalogEfforts([], "");
    initModelSwitcher();
  });

  it("marks the tier the session reports running at when the chat chose none", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    // Before this, a chat with no pick rendered five tiers and marked nothing.
    expect(markedTier()).toBe("xhigh");
  });

  it("marks the chat's own choice over the level the session reports", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "low",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    // The chat's choice leads so an optimistic set_effort write marks instantly,
    // before KAS answers with the new currentValue.
    expect(markedTier()).toBe("low");
  });

  it("renders the tiers the session offers, not a fixed five", async () => {
    cachedModels = [model("sonnet-5")];
    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_active: "medium",
      // No xhigh: this model does not offer it, and sending it would be a level
      // the service rejects.
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("rebuilds the tiers when the session's vocabulary changes", async () => {
    cachedModels = [model("opus-4.7"), model("sonnet-5")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });
    expect(tierCount()).toBe(5);

    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("uses the pre-session catalog and the model default when no session catalog exists", async () => {
    // A chat with no bridge: its header carries no tiers, so the template's
    // vocabulary is the only one available, and the model's own default is the
    // only evidence of a level.
    setCatalogEfforts([{ id: "low" }, { id: "high" }], "low");
    cachedModels = [model("opus-4.7", "high")];
    setSession({ id: "c1", model: "opus-4.7", effort: "" });

    expect(tierCount()).toBe(2);
    await expect(tierEnds()).resolves.toEqual(["low", "high"]);
    // The MODEL's default beats the template's currentValue: the template's is
    // the default model's level, and this chat is on another model.
    expect(markedTier()).toBe("high");
  });

  it("falls back to the canonical five when nothing has landed yet", async () => {
    cachedModels = [model("older")];
    setSession({ id: "c1", model: "older", effort: "" });

    expect(tierCount()).toBe(5);
    // The RESOLUTION still answers "" here — no level was advertised by any
    // source, and `effort.test.ts` is where that is pinned. A slider is always
    // somewhere, so the DOM cannot express it: the knob sits on the lowest tier,
    // and the settled design carries no UI for the unchosen state. The visible
    // consequence is recorded rather than hidden — in this one state
    // `#ctx-effort-pill` withholds itself while the knob names tier 0.
    expect(markedTier()).toBe("low");
    // Last, because the walk leaves the knob at the top tier and this is the one case
    // where re-opening cannot rewind it: no source advertised a level to re-apply.
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
  });

  it("labels a tier by the catalog's name, else the house table", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: [{ id: "low", name: "Low effort" }, { id: "xhigh" }],
    });

    expect(tierCount()).toBe(2);
    openCard();
    // The house table's rung, on the tier in force.
    expect(knob().getAttribute("aria-valuetext")).toBe("x-high");
    // The catalog's own name, on the other one.
    await press("{Home}");
    expect(knob().getAttribute("aria-valuetext")).toBe("Low effort");
  });

  it("dispatches the tier a keyboard step names", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }],
    });
    effortDispatch.mockClear();

    await press("{Home}");

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });

  it("steps one tier at a time and clamps at both ends", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    // Down from the lowest tier stays there; up walks the vocabulary.
    await press("{ArrowLeft}", "{ArrowRight}", "{ArrowUp}", "{End}");

    expect(effortDispatch.mock.calls.map((c) => (c[0] as { level: string }).level)).toEqual([
      "low",
      "medium",
      "high",
      "max",
    ]);
  });

  it("does not mark a chosen level the current model does not offer", () => {
    cachedModels = [model("sonnet-5", "medium")];
    setSession({
      id: "c1",
      // Chosen on a model that had max; this one stops at high.
      effort: "max",
      model: "sonnet-5",
      effort_active: "high",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // Marking max would claim the session runs at a tier it cannot reach. What it
    // REPORTS is the honest answer, and the choice stays on the record for a model
    // that offers it again.
    expect(markedTier()).toBe("high");
  });

  it("still marks a chosen level the model does offer", () => {
    cachedModels = [model("opus-5", "high")];
    setSession({
      id: "c1",
      effort: "max",
      model: "opus-5",
      effort_active: "high",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("max");
  });

  // --- The remembered last pick (`last_effort`) ---
  //
  // The level was per-chat with nothing remembering the last pick, so every NEW
  // chat silently reopened at the current model's default tier however many times
  // the user had chosen otherwise. Model had this memory (`last_model` rides into
  // every new chat) and effort had no equivalent.

  it("opens a new chat on the level the user last picked, under the same model", () => {
    lastEffort = "max";
    lastEffortModel = "opus-5";
    setCatalogEfforts(fiveTiers(), "high");
    cachedModels = [model("opus-5", "high")];
    // A brand-new chat: no choice of its own and no session to report a level.
    setSession({ id: "c1", model: "opus-5", effort: "" });

    // Not "high". The model's default is the answer only when nobody has ever
    // picked, and this user picked max — under this very model.
    expect(markedTier()).toBe("max");
  });

  it("a level picked under ANOTHER model yields the current model's default", () => {
    // The seed is model-scoped (user report, 2026-08-31): a tier chosen on
    // opus-5 must not override gpt-luna's own default.
    lastEffort = "max";
    lastEffortModel = "opus-5";
    setCatalogEfforts(fiveTiers(), "high");
    cachedModels = [model("gpt-luna", "medium")];
    setSession({ id: "c1", model: "gpt-luna", effort: "", effort_levels: fiveTiers() });

    expect(markedTier()).toBe("medium");
  });

  it("ignores a remembered level the current model does not offer", () => {
    lastEffort = "max";
    lastEffortModel = "sonnet-5";
    cachedModels = [model("sonnet-5", "medium")];
    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // A tier list is per model, so a remembered max here is a level the service
    // rejects; the model's own default is in its own list by construction.
    expect(markedTier()).toBe("medium");
  });

  it("marks the level the session reports over the remembered pick", () => {
    lastEffort = "low";
    lastEffortModel = "opus-5";
    cachedModels = [model("opus-5", "high")];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    // A live session reports what it is RUNNING at; the seed only answers for a
    // chat that has no session yet.
    expect(markedTier()).toBe("xhigh");
  });

  it("remembers a pick as the level the next new chat opens on", async () => {
    cachedModels = [model("opus-5", "high")];
    setSession({ id: "c1", model: "opus-5", effort: "", effort_levels: fiveTiers() });

    await press("{End}");

    expect(setLastEffortSpy).toHaveBeenCalledWith("max", "opus-5");
  });

  it("sends nothing when the pick is the level this chat already chose", async () => {
    cachedModels = [model("opus-5")];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "low",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    // Three gestures that each land where the knob already is.
    await press("{Home}", "{Home}", "{Home}");

    // A fast double or triple click sent one command each: measured on the live
    // instance, one pick of max produced three identical set_effort commands 80ms
    // apart.
    expect(effortDispatch).not.toHaveBeenCalled();
  });

  it("still sends when the pick is the marked tier but not this chat's choice", async () => {
    cachedModels = [model("opus-5", "max")];
    // Marked at the model's default, which means this chat has chosen NOTHING.
    setSession({ id: "c1", model: "opus-5", effort: "", effort_levels: fiveTiers() });
    effortDispatch.mockClear();
    expect(markedTier()).toBe("max");

    // End with the knob already on the last tier: the gesture completes where it
    // started, which is the slider's own version of clicking the marked button.
    await press("{End}");

    // Pinning the marked tier explicitly has to reach the server, or the chat keeps
    // following the model default and a later model switch moves it.
    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "max" });
  });
});
