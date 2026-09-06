//
// The picker's visibility is an EFFECT over store state, so these tests drive
// the store and read the `hidden` class the way a person reads the screen.
//
// Why this file exists: the picker is a full-bleed overlay, and its visibility
// used to be two imperative calls in chat.ts. Every sender that did not route
// through one of them left it covering the transcript, which is what `/goal` in
// a fresh chat did. Each case below is one of those paths.
import { describe, it, expect, beforeAll, beforeEach, vi } from "vitest";

// The shared live region, captured rather than driven: `announce()` clears and
// then sets its region behind a 100ms timer, so reading the DOM would be
// asserting on that timer instead of on what the picker decided to say.
const { announced } = vi.hoisted(() => ({ announced: { said: [] as string[] } }));
vi.mock("@cplieger/ui-primitives/announce", () => ({
  announce: (message: string) => {
    announced.said.push(message);
  },
}));

import { setSessions, setActive, setThinking } from "./store.js";
import {
  initModelPicker,
  setPickerModels,
  setCatalogPhase,
  refreshPickerIfVisible,
} from "./picker.js";
import type { Session, Message, ModelInfo } from "./types.js";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "chat-1",
    name: "test",
    model: "claude-opus",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...overrides,
  };
}

function userMessage(text: string): Message {
  return { id: "m-1", role: "user", content: text, ts: 0 } as Message;
}

function hidden(): boolean {
  const el = document.getElementById("model-picker");
  if (el === null) {
    throw new Error("no #model-picker in the fixture");
  }
  return el.classList.contains("hidden");
}

/** How many times the injected Retry has been fired. `initModelPicker` binds
 *  once for the whole file, so the counter has to outlive every case too. */
let retries = 0;

/** What the next Retry-driven read LANDS, applied while that read is in flight.
 *  app.ts's handler installs the catalog from inside its own fetch, so a case
 *  that sets the models before the click never exercises when the picker reads
 *  the answer back. */
let nextReadLands: ModelInfo[] | null = null;

describe("model picker visibility", () => {
  // The module captures its host through the DOM registry on first use and the
  // effect is installed once, so the fixture has to outlive every case.
  beforeAll(() => {
    document.body.innerHTML = `
      <div id="model-picker" class="hidden">
        <div class="picker-label"></div>
        <div class="picker-grid"></div>
      </div>`;
    setPickerModels([
      { model_id: "claude-opus", model_name: "Claude Opus", rate_multiplier: 1 },
      { model_id: "claude-sonnet", model_name: "Claude Sonnet", rate_multiplier: 1 },
    ]);
    initModelPicker(
      () => {
        // Selection is model-switcher's job; this file only tests visibility.
      },
      // The real handler is app.ts's catalog fetch, so this one answers with a
      // promise too: that is what picker.ts waits on before reporting the answer.
      // It also applies its result mid-flight, exactly where that fetch does.
      () => {
        retries += 1;
        return Promise.resolve().then(() => {
          if (nextReadLands !== null) {
            setPickerModels(nextReadLands);
            nextReadLands = null;
          }
        });
      },
    );
  });

  beforeEach(() => {
    setSessions([makeSession()]);
    setActive("chat-1");
  });

  it("shows for an empty idle chat", () => {
    expect(hidden()).toBe(false);
  });

  // The overlay must not sit over a conversation. `message_count` is the
  // server's count and `messages` the paginated window; either one being
  // non-empty means there is something to read underneath.
  it("hides once the chat holds a message", () => {
    setSessions([makeSession({ messages: [userMessage("hello")] })]);
    setActive("chat-1");
    expect(hidden()).toBe(true);
  });

  it("hides for a chat the server already counts messages for", () => {
    setSessions([makeSession({ message_count: 4 })]);
    setActive("chat-1");
    expect(hidden()).toBe(true);
  });

  // THE REGRESSION. A send sets `thinking` synchronously, well before the
  // server's message_appended echo arrives, so this is what closes the overlay
  // for a sender that never calls a hide function: the goal row, the tangent
  // row, and anything added later.
  it("hides as soon as a turn starts, before any message lands", () => {
    setThinking("chat-1", true);
    expect(hidden()).toBe(true);
  });

  // A failed send leaves the server idle and the chat promptable, and by then
  // the user message is persisted — so the picker must not come back and cover
  // the transcript when thinking clears.
  it("stays hidden when a turn ends on a chat that now has a message", () => {
    setSessions([makeSession({ messages: [userMessage("/goal do the thing")] })]);
    setActive("chat-1");
    setThinking("chat-1", true);
    setThinking("chat-1", false);
    expect(hidden()).toBe(true);
  });

  // `isEmptyChat(undefined)` is true by its own contract, so the absent case
  // needs its own guard: a chat that does not exist has no model to choose, and
  // the pre-session surface is the model pill's inline list.
  it("hides when no chat is active", () => {
    setActive("");
    expect(hidden()).toBe(true);
  });
});

// The empty grid, which used to be one placeholder for four different outcomes.
// It appended a non-keyed `div.picker-btn.picker-loading` carrying
// `aria-busy="true"` and `role="option"` into a `role="listbox"` — so the
// listbox's only option was a div that rovingFocus and focusTarget both exclude
// (show()'s focus move no-opped), and aria-busy never cleared, telling a screen
// reader the list was loading forever. It said "Loading models…" for a
// permanently-failed fetch and for a legitimately empty catalog alike.
describe("the model picker with no models", () => {
  function grid(): HTMLElement {
    const el = document.querySelector<HTMLElement>(".picker-grid");
    if (el === null) {
      throw new Error("no .picker-grid in the fixture");
    }
    return el;
  }

  beforeEach(() => {
    setPickerModels([]);
    setCatalogPhase("unknown");
    setSessions([makeSession()]);
    setActive("chat-1");
    announced.said.length = 0;
    nextReadLands = null;
  });

  it("says it is loading, and marks the grid busy, while nothing has answered", () => {
    expect(hidden()).toBe(false);
    expect(grid().textContent).toContain("Loading models…");
    expect(grid().hasAttribute("aria-busy")).toBe(true);
  });

  it("settles to a line that claims no cause once a verdict lands", () => {
    setCatalogPhase("ready");

    const text = grid().textContent ?? "";
    expect(text).toContain("No models available yet.");
    // aria-busy off: the answer arrived, and an empty catalog is a real answer.
    expect(grid().hasAttribute("aria-busy")).toBe(false);
    // vibekit cannot know WHY the catalog is empty — KAS omits its `model` option
    // identically for a stale cache and for an account entitled to nothing — so
    // copy naming an account or an entitlement would be inventing a cause.
    expect(text.toLowerCase()).not.toContain("account");
    expect(text.toLowerCase()).not.toContain("entitle");
  });

  it("says the fetch failed once the bounded refresh is exhausted", () => {
    setCatalogPhase("unavailable");

    expect(grid().textContent).toContain("Couldn't load the model list.");
    expect(grid().hasAttribute("aria-busy")).toBe(false);
  });

  // Without a Retry the `unavailable` state is a keyboard dead end: the grid
  // holds one line of text, no focusable element, and the only way back into the
  // catalog is a page reload.
  it("offers a focusable Retry once the fetch has failed, and re-runs it", () => {
    setCatalogPhase("unavailable");

    const retry = grid().querySelector<HTMLButtonElement>("button.picker-retry");
    expect(retry).not.toBe(null);
    // Reachable: focus lands on it, since it is the grid's only control.
    expect(document.activeElement).toBe(retry);

    const before = retries;
    retry?.click();
    expect(retries).toBe(before + 1);
  });

  it("offers the Retry for a settled EMPTY catalog too", () => {
    // That read succeeded, but it landed nothing: KAS resolves its model list
    // asynchronously and the server reports a merely cold cache as `empty`, so
    // asking again can genuinely differ. Withholding the door here left the same
    // dead end the `unavailable` state had.
    setCatalogPhase("ready");

    const retry = grid().querySelector<HTMLButtonElement>("button.picker-retry");
    expect(retry).not.toBe(null);
    const before = retries;
    retry?.click();
    expect(retries).toBe(before + 1);
  });

  // Pressing Retry repaints nothing when the re-read lands the same verdict —
  // `setCatalogPhase` returns early on an unchanged phase — so without an
  // announcement a screen-reader user gets no signal that anything happened at
  // all, and the control is the one thing on the grid they can act on.
  it("says it is asking again, then says what the answer was", async () => {
    setCatalogPhase("unavailable");
    announced.said.length = 0;

    grid().querySelector<HTMLButtonElement>("button.picker-retry")?.click();

    // The press, reported before the read resolves.
    expect(announced.said).toEqual(["Reloading the model list…"]);
    // The ANSWER, read off the settled notice rather than off a phase transition:
    // this read failed again, which is a real outcome and has to be reported.
    await vi.waitFor(() => {
      expect(announced.said).toEqual([
        "Reloading the model list…",
        "Couldn't load the model list.",
      ]);
    });
  });

  it("reports a catalog that arrived, not the line it replaced", async () => {
    setCatalogPhase("ready");
    announced.said.length = 0;
    // The catalog lands DURING the read, which is the only arrangement that can
    // tell "read the notice back afterwards" from "read it at the press": at the
    // press the line still says the list is empty.
    nextReadLands = [{ model_id: "claude-opus", model_name: "Claude Opus", rate_multiplier: 1 }];

    grid().querySelector<HTMLButtonElement>("button.picker-retry")?.click();

    await vi.waitFor(() => {
      expect(announced.said).toEqual(["Reloading the model list…", "Model list loaded."]);
    });
  });

  // "Retry" alone names no subject, and a keyboard user reaching the button hears
  // its accessible name without the notice sitting beside it.
  it("names what the Retry retries", () => {
    setCatalogPhase("unavailable");

    const retry = grid().querySelector<HTMLButtonElement>("button.picker-retry");
    expect(retry?.getAttribute("aria-label")).toBe("Retry loading the model list");
  });

  it("offers no Retry while an answer is still coming", () => {
    // "Loading" is a state the bounded refresh is already working on, and it
    // refuses a second caller anyway, so the button would be inert.
    setCatalogPhase("unknown");

    expect(grid().querySelector(".picker-retry")).toBe(null);
  });

  // The Retry is a control in a grid whose other members are model cards, so it
  // must not be counted as one: `.picker-btn` is what the grid's own queries mean
  // by "a model option".
  it("keeps the Retry out of the model-option set", () => {
    setCatalogPhase("unavailable");

    expect(grid().querySelectorAll(".picker-btn:not(.picker-note)")).toHaveLength(0);
    expect(grid().querySelector('[role="option"]')).toBe(null);
  });

  it("advertises no option and no listbox while it holds neither", () => {
    // A listbox with one unselectable, unfocusable option is worse than no
    // listbox: it promises a choice that does not exist.
    expect(grid().getAttribute("role")).toBe(null);
    expect(grid().querySelector('[role="option"]')).toBe(null);
  });

  it("keeps a cached catalog on screen when a later read fails or lands nothing", () => {
    // The second half of "a degraded read replaces nothing": app.ts installs no
    // empty list over a populated one, and the notice stands down while there is a
    // list to show. Without this one, a failed re-probe — or a restarted server's
    // cold cache — would replace a working grid with a line of text and a Retry.
    setPickerModels([{ model_id: "claude-opus", model_name: "Claude Opus", rate_multiplier: 1 }]);

    setCatalogPhase("ready");
    refreshPickerIfVisible();
    expect(grid().querySelectorAll("button.picker-btn")).toHaveLength(1);
    expect(grid().querySelector(".picker-note")).toBe(null);
    expect(grid().querySelector(".picker-retry")).toBe(null);

    setCatalogPhase("unavailable");
    refreshPickerIfVisible();
    expect(grid().querySelectorAll("button.picker-btn")).toHaveLength(1);
    expect(grid().querySelector(".picker-note")).toBe(null);
    expect(grid().querySelector(".picker-retry")).toBe(null);
  });

  it("is a labelled listbox of real buttons once a catalog lands", () => {
    setPickerModels([{ model_id: "claude-opus", model_name: "Claude Opus", rate_multiplier: 1 }]);
    setCatalogPhase("ready");
    refreshPickerIfVisible();

    const g = grid();
    expect(g.getAttribute("role")).toBe("listbox");
    expect(g.hasAttribute("aria-busy")).toBe(false);
    expect(g.querySelectorAll("button.picker-btn")).toHaveLength(1);
    expect(g.textContent).not.toContain("Loading models");
  });
});
