// @vitest-environment happy-dom
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
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelInfo } from "./types.js";

/** Catalog the module reads through picker.getCachedModels. */
let cachedModels: ModelInfo[] = [];

const { onExpand, effortDispatch } = vi.hoisted(() => ({
  onExpand: { fn: null as null | (() => void) },
  effortDispatch: vi.fn(),
}));

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
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), setLastModel: vi.fn() }));
vi.mock("./strings.js", () => ({ humanName: (s: string) => s }));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./icons.js", () => ({ ICON_MODEL: "" }));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn(), focusFirst: vi.fn(), dispose: vi.fn() }),
}));

import { activeSession } from "./store.js";
import { initModelSwitcher, setCatalogEfforts } from "./model-switcher.js";

/** A catalog entry carrying the model's own default tier. */
function model(id: string, dflt?: string): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
  };
}

/** Open the card and return the tier buttons it rendered. */
function openCard(): HTMLButtonElement[] {
  onExpand.fn?.();
  return [...document.querySelectorAll<HTMLButtonElement>(".effort-btn")];
}

/** The tier marked live, by both channels. Empty when nothing is marked. */
function markedTier(): string {
  const on = openCard().filter((b) => b.classList.contains("active"));
  expect(on.length, "exactly one tier may be marked live").toBeLessThanOrEqual(1);
  const btn = on[0];
  if (btn === undefined) {
    return "";
  }
  expect(btn.getAttribute("aria-pressed")).toBe("true");
  return btn.dataset["level"] ?? "";
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
    onExpand.fn = null;
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

  it("renders the tiers the session offers, not a fixed five", () => {
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

    expect(openCard().map((b) => b.dataset["level"])).toEqual(["low", "medium", "high", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("rebuilds the tiers when the session's vocabulary changes", () => {
    cachedModels = [model("opus-4.7"), model("sonnet-5")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });
    expect(openCard()).toHaveLength(5);

    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(openCard().map((b) => b.dataset["level"])).toEqual(["low", "medium", "high", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("uses the pre-session catalog and the model default when no session catalog exists", () => {
    // A chat with no bridge: its header carries no tiers, so the template's
    // vocabulary is the only one available, and the model's own default is the
    // only evidence of a level.
    setCatalogEfforts([{ id: "low" }, { id: "high" }], "low");
    cachedModels = [model("opus-4.7", "high")];
    setSession({ id: "c1", model: "opus-4.7", effort: "" });

    expect(openCard().map((b) => b.dataset["level"])).toEqual(["low", "high"]);
    // The MODEL's default beats the template's currentValue: the template's is
    // the default model's level, and this chat is on another model.
    expect(markedTier()).toBe("high");
  });

  it("falls back to the canonical five when nothing has landed yet", () => {
    cachedModels = [model("older")];
    setSession({ id: "c1", model: "older", effort: "" });

    expect(openCard().map((b) => b.dataset["level"])).toEqual([
      "low",
      "medium",
      "high",
      "xhigh",
      "max",
    ]);
    // Nothing marked is honest here: no level was advertised by any source.
    expect(markedTier()).toBe("");
  });

  it("labels a tier by the catalog's name, else the house table", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: [{ id: "low", name: "Low effort" }, { id: "xhigh" }],
    });

    expect(openCard().map((b) => b.textContent)).toEqual(["Low effort", "x-high"]);
  });

  it("dispatches the tier a click names", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }],
    });
    effortDispatch.mockClear();

    openCard()[0]?.click();

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });
});
