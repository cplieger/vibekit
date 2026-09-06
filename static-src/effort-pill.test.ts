// ---------------------------------------------------------------------------
// The model pill's readout, driven end to end: a tier click or a store write goes
// in and the pill's two spans come out.
//
// This file exists because no other spec covers the WIRING. `effort.test.ts`
// calls the resolver directly, and `context-ui.test.ts` replaces `./effort.js`
// with `() => ""` and `./picker.js` with `[]`, so both are blind to the one
// condition that decides whether a pick shows at all: what the catalog knows
// about the current model. `model-switcher.test.ts` reaches the click but mocks
// the store, `context-ui.js` AND the action framework, so the optimistic write
// cannot run there. Everything here is real — the store, `effort.ts`,
// `context-ui.ts`, `status.ts`, the model card, the action, the DOM write — and
// only the INPUTS are staged: the catalog (`picker.js`), the remembered pick
// (`session-context.js`) and the card's reveal (`pill-expand.js`).
//
// The active-session effect is registered here rather than through
// `chat.ts.installStoreSubscribers`: it is one `effect` over `activeSession`,
// its wiring is already pinned by `chat-tab-strip.test.ts`, and importing
// `chat.ts` would drag the whole app graph in behind it.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";

// The catalog and the seed are the staged inputs. Mutable module state rather
// than per-test factories, because the real modules are read through a live
// effect that can fire at any point after a store write.
const staged = vi.hoisted(() => ({ models: [] as ModelInfo[], seed: "" }));

vi.mock("./picker.js", () => ({
  getCachedModels: () => staged.models,
  refreshPickerIfVisible: vi.fn(),
  // The model card's stand-in line for an empty catalog. Null here because every
  // case below stages a catalog, and this file's subject is the effort readout.
  catalogNotice: () => null,
  // Unreached with no notice, and named anyway: Browser Mode links ESM for real,
  // so a factory missing a name the module under test imports fails COLLECTION.
  retryCatalog: vi.fn(),
  RETRY_LABEL: "Retry loading the model list",
}));
// Every name the real module exports, because Browser Mode links ESM for real:
// a partial factory fails collection on whatever a sibling in the graph imports.
vi.mock("./session-context.js", () => ({
  getLastEffortFor: (model: string) => (model === "" ? "" : staged.seed),
  getCurrentModel: () => "",
  setCurrentModel: vi.fn(),
  getLastModel: () => "",
  setLastModel: vi.fn(),
  restoreLastModel: vi.fn(),
  setLastEffort: vi.fn(),
  restoreLastEffort: vi.fn(),
}));
// Not part of the chain: `contextFull` is the composer's advisory, and
// prompt-input.ts owns a send-state machine this file has nothing to say about.
vi.mock("./prompt-input.js", () => ({
  contextFull: { value: false },
  setSendState: vi.fn(),
  initPromptInput: vi.fn(),
  sendComposer: vi.fn(),
}));

// The card's own reveal, staged so a case can open it without driving the popup.
// `onExpand` is what the pill's click calls, and the card is where the tier buttons
// live — so capturing it is the click surface, not a substitute for one.
const expand = vi.hoisted(() => ({ fn: null as null | (() => void) }));
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _card: HTMLElement, opts?: { onExpand?: () => void }) => {
    expand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));
// `./actions/chat.js` is deliberately NOT mocked: composer-state.ts already pulls
// it into this graph, so a partial factory fails collection on whatever a sibling
// imports — and no case here clicks the card's model half.

// The pill's own markup, plus every element `updateContextBar` writes to. It
// paints the whole context bar in one pass, so a missing id is a throw rather
// than a skipped assertion.
document.body.innerHTML = `
  <button id="switch-model-btn">
    <span id="ctx-model-pill"></span><span id="ctx-effort-pill" class="hidden"></span>
  </button>
  <div id="model-switch-list"></div>
  <span id="context-ring-fill"></span>
  <span id="context-label"></span>
  <span id="ctx-tokens"></span>
  <span id="ctx-credits"></span>
  <span id="ctx-turns"></span>
  <span id="ctx-last-turn"></span>
  <span id="ctx-msgs"></span>
  <span id="ctx-tools"></span>
  <span id="ctx-metering"></span>`;

const { effect } = await import("@cplieger/reactive");
const store = await import("./store.js");
const { setCatalogEfforts } = await import("./effort.js");
const { refreshContextUI } = await import("./context-ui.js");
const { configure, configureTransport } = await import("./actions/index.js");
const { initModelSwitcher } = await import("./model-switcher.js");

// The real action framework, headless: `configure({})` is its documented silent
// mode, and the transport has to ANSWER or every dispatch fails and the rollback
// undoes the optimistic write this file is here to follow.
configure({});
configureTransport(() => Promise.resolve({ ok: true, status: 200 }));
initModelSwitcher();

// The one subscriber under test: exactly what installStoreSubscribers registers.
effect(() => {
  const active = store.activeSession.value;
  if (active !== undefined) {
    refreshContextUI(active);
  }
});

function fiveTiers(): SessionEffortLevel[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

/** A catalog entry: its default tier, and whether it advertises effort at all. */
function model(id: string, dflt?: string): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    has_effort: true,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
  };
}

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    model: "claude-opus-5",
    messages: [],
    message_count: 0,
    has_more: false,
    effort: "",
    effort_levels: fiveTiers(),
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 200_000,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
    },
    ...over,
  } as unknown as Session;
}

/** Mount one chat as the active one. */
function mount(s: Session): void {
  store.setSessions([s]);
  store.setActive(s.id);
}

/** A value the renderer always overwrites, written into both spans before a
 *  store write.
 *
 *  Load-bearing rather than tidy: `updateContextBar` coalesces through
 *  requestAnimationFrame, so a read taken before this case's frame lands returns
 *  the PREVIOUS case's paint — and for the cases that expect the pill to say
 *  nothing, an untouched DOM is indistinguishable from a correct answer. Leaving
 *  the sentinel is what proves a paint happened at all. */
const UNPAINTED = "unpainted";

function arm(): void {
  const tierEl = document.getElementById("ctx-effort-pill")!;
  tierEl.textContent = UNPAINTED;
  tierEl.classList.remove("hidden");
  document.getElementById("ctx-model-pill")!.textContent = UNPAINTED;
}

/** Wait out the coalescing frame. */
async function nextFrame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** The pill's rendered tier, once this case's own paint has landed. */
async function tier(): Promise<{ text: string; hidden: boolean }> {
  const el = document.getElementById("ctx-effort-pill")!;
  await vi.waitFor(
    () => {
      expect(el.textContent).not.toBe(UNPAINTED);
    },
    { timeout: FRAME_BUDGET_MS },
  );
  return { text: el.textContent ?? "", hidden: el.classList.contains("hidden") };
}

/** The pill's rendered model name, once this case's own paint has landed. */
async function modelName(): Promise<string> {
  const el = document.getElementById("ctx-model-pill")!;
  await vi.waitFor(
    () => {
      expect(el.textContent).not.toBe(UNPAINTED);
    },
    { timeout: FRAME_BUDGET_MS },
  );
  return el.textContent ?? "";
}

beforeEach(async () => {
  // Let a frame the previous case scheduled land BEFORE this one stages its
  // inputs, or that paint would arrive after the sentinel and read as this
  // case's answer.
  await nextFrame();
  staged.models = [];
  staged.seed = "";
  setCatalogEfforts([], "");
  store.setSessions([]);
  arm();
});

describe("the model pill's reasoning tier", () => {
  // CASE A. A bridgeless chat has no per-model catalog: GET /api/config-template
  // runs on a lazily-spawned utility bridge, degrades to empty lists on any
  // failure, and may legitimately answer with no models because KAS resolves
  // ListAvailableModels asynchronously — and nothing retries. So "no default is
  // known" is the ORDINARY state of a new chat, and it is exactly when a user
  // clicks a tier and expects to see it.
  it("names a tier the chat CHOSE even with nothing in the catalog", async () => {
    mount(session("a", { effort: "max" }));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // CASE B. The remembered pick is what a NEW chat opens on, and the server
  // resolves the same seed into StartOpts.Effort under the same per-model gate.
  // So the pill states what the session will run at rather than guessing.
  it("names the remembered pick even with nothing in the catalog", async () => {
    staged.seed = "max";
    mount(session("b"));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // CASE C, the control. Without it a passing A and B would prove only that the
  // harness reaches SOME code — this is the path that already worked, so it
  // proves the chain is the real one.
  it("names a chosen tier that departs from a default the catalog does know", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("c", { effort: "max" }));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // The guard that keeps the pill an EXCEPTION marker. A level the SERVICE
  // resolved is what everyone gets anyway, so naming it with no default to
  // compare against would put a permanent readout on every model — which is the
  // thing the withholding rule exists to prevent.
  it("says nothing about a level only the service resolved", async () => {
    mount(session("d", { effort_active: "high" }));
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  it("says nothing when the chosen tier IS the model's own default", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("e", { effort: "high" }));
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  // A pick made under a model that offered `max` is a level this model's service
  // rejects, so marking it would claim a tier the session cannot reach.
  it("says nothing about a chosen tier the current model does not offer", async () => {
    mount(
      session("f", {
        effort: "max",
        effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
      }),
    );
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  it("repaints the tier when a later store write changes the chat's choice", async () => {
    mount(session("g"));
    expect(await tier()).toEqual({ text: "", hidden: true });

    arm();
    store.setEffort("g", "max");

    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });
});

describe("the model pill's model half", () => {
  // The model half reads `humanName(s.model)` off the chat record with no catalog
  // dependency, so it does not share the tier half's defect. Asserted rather than
  // taken on trust, because the two halves are written in one pass and a reader
  // seeing the tier miss would suspect both.
  it("names the model with nothing in the catalog", async () => {
    mount(session("h"));
    expect(await modelName()).toBe("claude opus 5");
  });

  it("repaints the model on a local pick, with no bridge and no message sent", async () => {
    mount(session("i", { model: "claude-sonnet-5" }));
    expect(await modelName()).toBe("claude sonnet 5");

    arm();
    store.setModel("i", "claude-opus-5");

    expect(await modelName()).toBe("claude opus 5");
  });

  // The whole readout the report asked for: "claude opus 5 · max", straight away
  // on selection, on a new chat with no bridge and no message sent.
  it("paints the model and the tier together on selection", async () => {
    mount(session("j", { model: "claude-sonnet-5" }));
    await modelName();

    arm();
    store.setModel("j", "claude-opus-5");
    store.setEffort("j", "max");

    expect(await modelName()).toBe("claude opus 5");
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });
});

// The GESTURE end of the chain. Every case above starts at a store write, and the
// one thing between a user's click and that write is `setEffortAction`'s optimistic
// callback — module-private, so nothing above can reach it and a change there would
// leave every assertion in this file green while the pill stopped moving.
describe("a click on a tier", () => {
  /** Open the card and return the tier buttons it rendered. */
  function tierButtons(): HTMLButtonElement[] {
    expand.fn?.();
    return [...document.querySelectorAll<HTMLButtonElement>(".effort-btn")];
  }

  it("paints the pill, through the real action", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("k", { model: "claude-opus-5" }));
    // The model's own default, so the pill says nothing until a pick departs from it.
    expect(await tier()).toEqual({ text: "", hidden: true });

    arm();
    const max = tierButtons().find((b) => b.dataset["level"] === "max");
    expect(max, "the card rendered no max tier, so the click has no subject").toBeDefined();
    max?.click();

    expect(await tier()).toEqual({ text: "· max", hidden: false });
    // The store is what the pill reads, so the write has to have landed there
    // rather than only in the pill's own paint.
    expect(store.get("k")?.effort).toBe("max");
  });
});
