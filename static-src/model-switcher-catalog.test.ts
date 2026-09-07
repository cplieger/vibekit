// The model card with NO models, which used to render nothing at all: the scroller
// mounted zero children, so the pill opened on an effort row above an empty
// `role="listbox"` labelled "Available models" and said nothing.
//
// Its own file because the controller is a singleton that RETAINS its scroller,
// and `model-switcher.test.ts` replaces `document.body` per test — so that
// scroller has been detached since its second case. A separate file gets a fresh
// registry and one durable fixture. The notice's COPY is picker.ts's to own.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelInfo } from "./types.js";

let cachedModels: ModelInfo[] = [];
let notice: { text: string; busy: boolean; retry: boolean } | null = null;

const { onExpand } = vi.hoisted(() => ({ onExpand: { fn: null as null | (() => void) } }));

vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _content: HTMLElement, opts?: { onExpand?: () => void }) => {
    onExpand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));

interface SignalOf<T> {
  value: T;
}
type SignalFactory = <T>(initial: T) => SignalOf<T>;
interface TestSession {
  id: string;
  model: string;
  effort: string;
}

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

// `retryCatalog` is the picker's door, so a mock is what proves this card asks
// through it rather than growing a second handler. What the door itself DOES —
// announcing the press and the answer it settles on — is picker.test.ts's.
const { retried } = vi.hoisted(() => ({ retried: { calls: 0 } }));
vi.mock("./picker.js", () => ({
  getCachedModels: () => cachedModels,
  refreshPickerIfVisible: vi.fn(),
  catalogNotice: () => notice,
  retryCatalog: () => {
    retried.calls += 1;
  },
  RETRY_LABEL: "Retry loading the model list",
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(),
  transportAction: () => ({ dispatch: vi.fn() }),
  retryNetwork: vi.fn(),
  RETRY_STANDARD: {},
}));
vi.mock("./actions/chat.js", () => ({ switchModel: { dispatch: vi.fn() } }));
// The real reconcile, because a mocked one cannot show that a notice and a real
// option row coexist correctly — the notice is unkeyed and reconcile inserts each
// keyed row after every unkeyed sibling.
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./session-context.js", () => ({
  setCurrentModel: vi.fn(),
  setLastModel: vi.fn(),
  getLastEffortFor: () => "",
  setLastEffort: vi.fn(),
}));
vi.mock("./strings.js", () => ({ humanName: (s: string) => s }));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./icons.js", () => ({ ICON_MODEL: "" }));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn(), focusFirst: vi.fn(), dispose: vi.fn() }),
}));

import { activeSession } from "./store.js";
import { initModelSwitcher } from "./model-switcher.js";

function model(id: string): ModelInfo {
  return { model_id: id, model_name: id, rate_multiplier: 1 };
}

function scroller(): HTMLElement {
  const el = document.querySelector<HTMLElement>(".pill-model-scroll");
  if (el === null) {
    throw new Error("the card built no model scroller");
  }
  return el;
}

describe("the model card with no models", () => {
  // ONE fixture for the file: the controller keeps the scroller it builds, so a
  // per-test card would strand it.
  beforeAll(() => {
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    initModelSwitcher();
  });

  beforeEach(() => {
    cachedModels = [];
    notice = null;
    retried.calls = 0;
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "opus-4.7",
      effort: "",
    };
  });

  function retryBtn(): HTMLButtonElement | null {
    return scroller().querySelector<HTMLButtonElement>("button.pill-model-retry");
  }

  it("says why the list is empty instead of showing nothing", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(scroller().textContent).toContain("No models available yet.");
  });

  it("marks the scroller busy only while an answer is still coming", () => {
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();
    expect(scroller().hasAttribute("aria-busy")).toBe(true);

    // A settled verdict must drop it, or a screen reader is told the list is
    // loading for as long as the card exists.
    notice = { text: "Couldn't load the model list.", busy: false, retry: true };
    onExpand.fn?.();
    expect(scroller().hasAttribute("aria-busy")).toBe(false);
  });

  it("is not a listbox while it holds no options", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    // A listbox whose only child is a line of prose advertises a choice that is
    // not there.
    expect(scroller().getAttribute("role")).toBe(null);
    expect(scroller().getAttribute("aria-label")).toBe(null);
  });

  it("stops being a listbox when a re-read answers with no models", () => {
    // Reachable: a login re-runs the catalog fetch, and a `ready` verdict
    // carrying an empty list is applied rather than dropped, so the scroller can
    // legitimately go from holding options to holding none.
    cachedModels = [model("opus-4.7")];
    onExpand.fn?.();
    expect(scroller().getAttribute("role")).toBe("listbox");

    cachedModels = [];
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(scroller().getAttribute("role")).toBe(null);
    expect(scroller().getAttribute("aria-label")).toBe(null);
  });

  it("becomes a labelled listbox again once a catalog arrives", () => {
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();
    cachedModels = [model("opus-4.7")];
    notice = null;
    onExpand.fn?.();

    const el = scroller();
    expect(el.getAttribute("role")).toBe("listbox");
    expect(el.getAttribute("aria-label")).toBe("Available models");
    expect(el.textContent).not.toContain("Loading models");
    expect(el.querySelectorAll(".pill-model-item")).toHaveLength(1);
  });

  // The card used to render the notice and DROP the `retry` flag, so the pill was
  // a keyboard dead end in both states where the hero picker is one in neither.
  it("offers a way back when asking again can change the answer", () => {
    notice = { text: "Couldn't load the model list.", busy: false, retry: true };
    onExpand.fn?.();

    const btn = retryBtn();
    expect(btn).not.toBe(null);
    // The picker's door, not a second one: this card knows nothing about how the
    // catalog is fetched or what the press announces.
    btn?.click();
    expect(retried.calls).toBe(1);
  });

  it("names what the Retry retries", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    // "Retry" alone names no subject, and the button and the hero picker's must
    // agree — one constant, read from picker.ts.
    expect(retryBtn()?.getAttribute("aria-label")).toBe("Retry loading the model list");
  });

  it("offers no Retry while an answer is still coming", () => {
    // The bounded refresh is already asking and refuses a second caller, so the
    // button would be inert.
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();

    expect(retryBtn()).toBe(null);
  });

  it("takes the Retry away with the notice once a catalog lands", () => {
    notice = { text: "Couldn't load the model list.", busy: false, retry: true };
    onExpand.fn?.();
    expect(retryBtn()).not.toBe(null);

    cachedModels = [model("opus-4.7")];
    notice = null;
    onExpand.fn?.();

    // Both stand-ins go, or a real list keeps a control offering to reload it.
    expect(retryBtn()).toBe(null);
    expect(scroller().querySelector(".list-empty")).toBe(null);
  });
});
