// @vitest-environment node
//
// The out-of-page attention system's DECISIONS: the fold, the two orders it
// depends on, the acknowledgement store, the sinks, and the raise rule.
//
// Deliberately `node`, with no DOM at all. That is the property under test as
// much as any assertion here: every capability the sinks use arrives through an
// injected env, so if a decision ever starts reading `document` or `navigator`
// directly this file stops loading. The browser binding and the wiring are the
// sibling attention-wiring.test.ts's subject, under happy-dom.
//
// Four things here are vibekit's own rather than the reference's, and each is
// pinned because a port gets them wrong silently:
//
//  1. THE SEVERITY ORDER puts `input` above `failed`, inverting
//     @cplieger/web-terminal-ui. It has to match `tabStatusFor` (store.ts), or
//     the tab icon and the tab dot would disagree about which chat matters most.
//  2. THE ICON MAPPING folds `waiting` onto the `input` asset, because the dot's
//     hollow-versus-solid distinction cannot survive a 16px badge. Three
//     variants ship; a fourth would 404.
//  3. THE CANDIDATE SET is chats only. The list also holds editor tabs whose
//     `dirty` mark rides the same dot element, and `dirty` reaching the count
//     would report an unsaved file as a chat wanting attention.
//  4. THE RAISE RULE needs both halves of "watched". Keyed on the active chat
//     alone it swallows the cue of the one chat a single-chat user left running,
//     which is the case these surfaces exist for.

import { describe, it, expect, vi } from "vitest";

// The four modules the WIRING half imports, stubbed to nothing. They are what
// pull a DOM into the graph (tabs.ts reaches router.ts, which adds a popstate
// listener at load), and no function under test here touches any of them. With
// them stubbed and no `document` or `navigator` in scope at all, this file
// passing IS the statement that every decision below arrives at its capabilities
// through an injected env.
vi.mock("./store.js", () => ({ getActiveId: (): string => "" }));
vi.mock("./tabs.js", () => ({
  cueCandidates: (): [] => [],
  subscribeTabCues: (): (() => void) => (): void => undefined,
  setOnTabClosed: (): void => undefined,
}));
vi.mock("./bus.js", () => ({
  BUS_TAB_CHANGED: "tabs:changed",
  onBus: (): (() => void) => (): void => undefined,
}));
vi.mock("./dom.js", () => ({ $: {} }));

import {
  CUE_SEVERITY,
  CUE_SEEN_KEY,
  MAX_PERSISTED_CUE_SEEN,
  NO_ATTENTION,
  createAttention,
  createAttentionController,
  createCueSeen,
  cueIconName,
  iconVariantHref,
  isCueStatus,
  isUnseenCue,
  parseCueSeen,
  serializeCueSeen,
  summarize,
  titlePrefixFor,
  worseCue,
  type Attention,
  type AttentionEnv,
  type CueCandidate,
  type CueSeenStorage,
  type CueStatus,
} from "./attention.js";

function seen(entries: Record<string, CueStatus> = {}): Map<string, CueStatus> {
  return new Map(Object.entries(entries));
}

// ---------------------------------------------------------------------------
// 1. The cue set and its two orders.
// ---------------------------------------------------------------------------

describe("the cue set", () => {
  it("holds exactly the four states that want the reader", () => {
    // vibekit's dot vocabulary is idle | working | waiting | input | failed |
    // done | dirty. Only four of those are things to tell someone about: the
    // other three are ongoing, absent, or an editor's business.
    expect([...CUE_SEVERITY]).toEqual(["input", "failed", "waiting", "done"]);
  });

  it("refuses every non-cue dot state, the editor's mark included", () => {
    for (const status of ["idle", "working", "dirty", "", "crashed", "INPUT"]) {
      expect(isCueStatus(status), `${status} must not be a cue`).toBe(false);
    }
    for (const status of CUE_SEVERITY) {
      expect(isCueStatus(status)).toBe(true);
    }
  });

  it("ranks a pending ask above a parked failure, unlike the reference", () => {
    // THE divergence. web-terminal-ui ranks `failed` first; here an ask BLOCKS
    // the turn while a failure is a result the agent will not revisit, which is
    // the same reasoning tabStatusFor already used. Inverting this would make
    // the tab icon name a different chat than the dot column does.
    expect(worseCue("input", "failed")).toBe("input");
    expect(worseCue("failed", "input")).toBe("input");
  });

  it("ranks the whole order transitively", () => {
    expect(worseCue("failed", "waiting")).toBe("failed");
    expect(worseCue("waiting", "done")).toBe("waiting");
    expect(worseCue("input", "done")).toBe("input");
    expect(worseCue("done", "waiting")).toBe("waiting");
  });

  it("treats no cue as the identity, so an empty fold has no worst", () => {
    expect(worseCue("", "done")).toBe("done");
    expect(worseCue("done", "")).toBe("done");
    expect(worseCue("", "")).toBe("");
  });
});

describe("the icon mapping", () => {
  it("paints `waiting` with the `input` asset", () => {
    // Both mean "this chat wants you", and the tab dot separates them by fill
    // and ring at 9px. The favicon badge is 5.5 units in a 32-unit space, so it
    // cannot carry that; sharing the asset is honest where drawing a fourth
    // would claim a fidelity the icon does not have.
    expect(cueIconName("waiting")).toBe("input");
    expect(cueIconName("input")).toBe("input");
  });

  it("paints a failure with `alert` and a finished turn with `done`", () => {
    expect(cueIconName("failed")).toBe("alert");
    expect(cueIconName("done")).toBe("done");
  });

  it("names only assets that ship, so no cue can 404 the tab icon", () => {
    // static/favicon-{input,done,alert}.svg are the three that exist, guarded by
    // favicon-variants.test.ts. A cue naming a fourth would blank the icon with
    // nothing logged anywhere.
    const shipped = new Set(["input", "done", "alert"]);
    for (const cue of CUE_SEVERITY) {
      expect(shipped.has(cueIconName(cue)), `${cue} names a missing asset`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// 2. The fold.
// ---------------------------------------------------------------------------

describe("summarize folds the chat tabs into one value", () => {
  it("counts nothing when nothing is latched", () => {
    expect(
      summarize(
        [
          { id: "a", status: "idle" },
          { id: "b", status: "working" },
        ],
        seen(),
      ),
    ).toEqual(NO_ATTENTION);
  });

  it("counts one per chat holding an unacknowledged cue", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "waiting" },
          { id: "c", status: "working" },
        ],
        seen(),
      ),
    ).toEqual({ count: 2, worst: "waiting" });
  });

  it("reports the most severe cue as the worst, not the last one seen", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "input" },
          { id: "c", status: "failed" },
        ],
        seen(),
      ),
    ).toEqual({ count: 3, worst: "input" });
  });

  it("drops a chat whose current cue this reader already acknowledged", () => {
    expect(
      summarize(
        [
          { id: "a", status: "done" },
          { id: "b", status: "failed" },
        ],
        seen({ a: "done" }),
      ),
    ).toEqual({ count: 1, worst: "failed" });
  });

  it("re-raises a chat whose cue MOVED to another cue", () => {
    // An acknowledgement is of a state, not of a chat: a chat that finished, was
    // acknowledged, then failed is news again.
    expect(summarize([{ id: "a", status: "failed" }], seen({ a: "done" }))).toEqual({
      count: 1,
      worst: "failed",
    });
  });

  it("ignores an acknowledgement for a chat that is no longer latched", () => {
    expect(summarize([{ id: "a", status: "working" }], seen({ a: "done" }))).toEqual(NO_ATTENTION);
  });

  it("never counts the editor's dirty mark, whatever reaches it", () => {
    // Belt for the candidate filter (tabs.ts cueCandidates): the dot element is
    // shared with editor tabs, so `dirty` is the one non-chat state that could
    // arrive here at all.
    expect(summarize([{ id: "editor:/a.go", status: "dirty" }], seen())).toEqual(NO_ATTENTION);
  });

  it("counts one per chat and never twice for the same id", () => {
    // The count is set-valued, which is what makes it needs-no-tiebreak. Ids are
    // unique in the tab store, and this is the assertion that the fold does not
    // reintroduce a duplicate by, say, summing per status.
    expect(summarize([{ id: "a", status: "done" }], seen()).count).toBe(1);
  });
});

describe("isUnseenCue is the one predicate behind both surfaces", () => {
  it("agrees with the fold on every case", () => {
    const candidates: CueCandidate[] = [
      { id: "a", status: "done" },
      { id: "b", status: "idle" },
      { id: "c", status: "input" },
    ];
    const ack = seen({ a: "done" });
    const byPredicate = candidates.filter((c) => isUnseenCue(c.status, c.id, ack)).length;
    expect(summarize(candidates, ack).count).toBe(byPredicate);
  });
});

// ---------------------------------------------------------------------------
// 3. The sinks.
// ---------------------------------------------------------------------------

describe("the title format", () => {
  it("puts the count FIRST, because a tab strip truncates the tail", () => {
    expect(titlePrefixFor(3)).toBe("(3) ");
  });

  it("writes no prefix at zero, so the bookmark name stays clean", () => {
    expect(titlePrefixFor(0)).toBe("");
  });
});

interface SinkLog {
  titles: string[];
  badges: number[];
  icons: (string | null)[];
}

function fakeEnv(log: SinkLog, opts: { badge?: boolean; icon?: boolean } = {}): AttentionEnv {
  const env: AttentionEnv = {
    titlePrefix: (text) => log.titles.push(text),
  };
  if (opts.badge !== false) {
    env.setBadge = (count) => log.badges.push(count);
  }
  if (opts.icon !== false) {
    env.setIcon = (variant) => log.icons.push(variant);
  }
  return env;
}

function emptyLog(): SinkLog {
  return { titles: [], badges: [], icons: [] };
}

describe("createAttention drives each sink only on a real change", () => {
  it("applies everything on the first call, whatever the value", () => {
    const log = emptyLog();
    createAttention(fakeEnv(log)).apply(NO_ATTENTION);
    expect(log).toEqual({ titles: [""], badges: [0], icons: [null] });
  });

  it("touches nothing when the same value is applied again", () => {
    // Idempotence rather than a debounce. The title doubles as the bookmark name
    // and re-assigning an icon href makes some browsers re-fetch it, so a sweep
    // that re-derives the same answer must be silent.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    const value: Attention = { count: 2, worst: "done" };
    surfaces.apply(value);
    surfaces.apply({ ...value });
    expect(log).toEqual({ titles: ["(2) "], badges: [2], icons: ["done"] });
  });

  it("moves the title and badge on a count change without touching the icon", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.apply({ count: 2, worst: "done" });
    expect(log.titles).toEqual(["(1) ", "(2) "]);
    expect(log.badges).toEqual([1, 2]);
    expect(log.icons).toEqual(["done"]);
  });

  it("moves the icon on a worst change without touching the title or badge", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "done" });
    surfaces.apply({ count: 1, worst: "input" });
    expect(log.titles).toEqual(["(1) "]);
    expect(log.badges).toEqual([1]);
    expect(log.icons).toEqual(["done", "input"]);
  });

  it("hands the badge the SAME number as the title", () => {
    // Two surfaces disagreeing about how many things want you is worse than
    // either being absent, which is why both read one fold.
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    for (const count of [1, 4, 0, 7]) {
      surfaces.apply({ count, worst: count > 0 ? "done" : "" });
    }
    expect(log.badges).toEqual([1, 4, 0, 7]);
    expect(log.titles).toEqual(["(1) ", "(4) ", "", "(7) "]);
  });

  it("clears the icon with null rather than a variant name", () => {
    const log = emptyLog();
    const surfaces = createAttention(fakeEnv(log));
    surfaces.apply({ count: 1, worst: "failed" });
    surfaces.apply(NO_ATTENTION);
    expect(log.icons).toEqual(["alert", null]);
  });

  it("still writes the title when the badge and icon are both absent", () => {
    // The anti-ladder rule. A badge resolves on Linux where nothing is painted
    // and Safari caches the first icon it fetched, so both can fail invisibly;
    // the title is gated on no capability and is therefore the floor. Arranging
    // these as a fallback chain would leave those platforms with nothing.
    const log = emptyLog();
    createAttention(fakeEnv(log, { badge: false, icon: false })).apply({ count: 3, worst: "done" });
    expect(log.titles).toEqual(["(3) "]);
    expect(log.badges).toEqual([]);
    expect(log.icons).toEqual([]);
  });
});

describe("iconVariantHref follows the asset generator's naming", () => {
  it("rewrites vibekit's one icon link", () => {
    expect(iconVariantHref("/favicon.svg", "input")).toBe("/favicon-input.svg");
    expect(iconVariantHref("/favicon.svg", "alert")).toBe("/favicon-alert.svg");
  });

  it("preserves a size suffix and the extension, so a link keeps its format", () => {
    expect(iconVariantHref("/favicon-32x32.png", "done")).toBe("/favicon-done-32x32.png");
  });

  it("leaves a link whose name is not favicon alone rather than 404ing it", () => {
    expect(iconVariantHref("/apple-touch-icon.png", "input")).toBeNull();
    expect(iconVariantHref("/logo.svg", "input")).toBeNull();
  });

  it("matches only a whole path segment named favicon", () => {
    expect(iconVariantHref("/assets/favicon.svg", "done")).toBe("/assets/favicon-done.svg");
    expect(iconVariantHref("/myfavicon.svg", "done")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 4. The acknowledgement store.
// ---------------------------------------------------------------------------

describe("the acknowledgement store's key", () => {
  it("lives beside the UI state rather than inside it", () => {
    // Its own key on purpose: `vibekit.ui-state` is the window's ARRANGEMENT,
    // written on structural change, and this is written whenever a cue is
    // observed. Different cadence, different subject.
    expect(CUE_SEEN_KEY).toBe("vibekit.cue-seen");
    expect(CUE_SEEN_KEY).not.toBe("vibekit.ui-state");
  });
});

describe("parseCueSeen distrusts everything it reads", () => {
  it("round-trips what serializeCueSeen wrote", () => {
    const map = seen({ a: "done", b: "input" });
    expect([...parseCueSeen(serializeCueSeen(map))]).toEqual([...map]);
  });

  it("reads an absent or empty document as nothing acknowledged", () => {
    expect(parseCueSeen(null).size).toBe(0);
    expect(parseCueSeen("").size).toBe(0);
  });

  it("drops a whole document it cannot parse", () => {
    // Degrading to "nothing acknowledged" is always safe: a lost
    // acknowledgement only re-lights a cue the reader can dismiss again.
    expect(parseCueSeen("{oh no").size).toBe(0);
  });

  it("refuses a JSON value that is not an object of entries", () => {
    // A POPULATED array matters, not just an empty one: Object.entries on
    // ["done"] yields the pair ["0", "done"], which passes every per-entry check
    // and would land a cue for a chat called "0".
    for (const raw of ["[]", '["done"]', '["done","input"]', "null", '"done"', "42", "true"]) {
      expect(parseCueSeen(raw).size, `${raw} must not parse as a cue map`).toBe(0);
    }
  });

  it("keeps the trustworthy entries of a partly-corrupt document", () => {
    const map = parseCueSeen('{"a":"done","b":"nonsense","":"input","c":7,"d":"failed"}');
    expect([...map]).toEqual([
      ["a", "done"],
      ["d", "failed"],
    ]);
  });

  it("stops at the cap, so a hostile document cannot make the restore unbounded", () => {
    const hostile: Record<string, string> = {};
    for (let i = 0; i < MAX_PERSISTED_CUE_SEEN * 3; i++) {
      hostile[`c${String(i)}`] = "done";
    }
    expect(parseCueSeen(JSON.stringify(hostile)).size).toBe(MAX_PERSISTED_CUE_SEEN);
  });
});

function fakeStorage(initial: string | null = null): CueSeenStorage & { raw: () => string | null } {
  let raw = initial;
  return {
    read: () => raw,
    write: (next) => {
      raw = next;
    },
    raw: () => raw,
  };
}

describe("createCueSeen persists and bounds the acknowledgements", () => {
  it("starts from what the reader already dismissed", () => {
    // The whole reason this is persisted: the latches behind every cue are
    // rebuilt from server state on each reconnect, so without this a dismissed
    // count came back on a phone simply returning to a backgrounded page.
    const store = createCueSeen(fakeStorage('{"a":"done"}'));
    expect(store.map().get("a")).toBe("done");
  });

  it("writes through on every mark", () => {
    const storage = fakeStorage();
    createCueSeen(storage).mark("a", "input");
    expect(storage.raw()).toBe('{"a":"input"}');
  });

  it("ignores a non-cue status rather than storing it", () => {
    const storage = fakeStorage();
    const store = createCueSeen(storage);
    store.mark("a", "working");
    store.mark("editor:/x.go", "dirty");
    expect(store.map().size).toBe(0);
    expect(storage.raw()).toBeNull();
  });

  it("does not re-write an acknowledgement it already holds", () => {
    const storage = fakeStorage();
    const store = createCueSeen(storage);
    store.mark("a", "done");
    const after = storage.raw();
    store.mark("a", "done");
    expect(storage.raw()).toBe(after);
  });

  it("evicts oldest-first so the live map obeys the parser's cap", () => {
    // A chat that vanished while the page was CLOSED leaves an entry nothing
    // prunes. Unbounded, that eventually pushes the map past the cap and makes
    // the parser discard whatever it read last — dropping fresh
    // acknowledgements to keep dead ones.
    const store = createCueSeen(fakeStorage());
    for (let i = 0; i <= MAX_PERSISTED_CUE_SEEN; i++) {
      store.mark(`c${String(i)}`, "done");
    }
    expect(store.map().size).toBe(MAX_PERSISTED_CUE_SEEN);
    expect(store.map().has("c0")).toBe(false);
    expect(store.map().has(`c${String(MAX_PERSISTED_CUE_SEEN)}`)).toBe(true);
  });

  it("forgets an entry and writes that through", () => {
    const storage = fakeStorage('{"a":"done"}');
    const store = createCueSeen(storage);
    store.forget("a");
    expect(store.map().size).toBe(0);
    expect(storage.raw()).toBe("{}");
  });

  it("does not write when there was nothing to forget", () => {
    const storage = fakeStorage();
    createCueSeen(storage).forget("nobody");
    expect(storage.raw()).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 5. The raise rule and the acknowledgement gestures.
// ---------------------------------------------------------------------------

interface Harness {
  applied: Attention[];
  latest: () => Attention;
  setCandidates: (next: CueCandidate[]) => void;
  setActive: (id: string) => void;
  setVisible: (v: boolean) => void;
  setRowsInView: (ids: string[]) => void;
  stored: () => string | null;
  controller: ReturnType<typeof createAttentionController>;
}

function harness(opts: { stored?: string } = {}): Harness {
  let candidates: CueCandidate[] = [];
  let active = "";
  let visible = true;
  let rows: string[] = [];
  const storage = fakeStorage(opts.stored ?? null);
  const applied: Attention[] = [];
  const controller = createAttentionController({
    candidates: () => candidates,
    activeChatID: () => active,
    pageVisible: () => visible,
    rowsInView: () => rows,
    storage,
    surfaces: {
      apply: (next) => applied.push(next),
    },
  });
  return {
    applied,
    latest: () => applied[applied.length - 1] ?? NO_ATTENTION,
    setCandidates: (next) => {
      candidates = next;
    },
    setActive: (id) => {
      active = id;
    },
    setVisible: (v) => {
      visible = v;
    },
    setRowsInView: (ids) => {
      rows = ids;
    },
    stored: storage.raw,
    controller,
  };
}

describe("the raise rule", () => {
  it("raises a cue on a background chat", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "idle" },
      { id: "b", status: "done" },
    ]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("raises nothing for the chat the reader is watching", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("RAISES the active chat's cue while the page is hidden", () => {
    // Both halves of "watched" are required. Keyed on "is this the active chat"
    // alone this swallowed the cue of the very chat the reader left running —
    // which is the single-chat case, since one chat is necessarily active, and
    // precisely what these surfaces exist for.
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("acknowledges the active chat's cue once the reader can see it again", () => {
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest().count).toBe(1);

    h.setVisible(true);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("keeps an acknowledgement across a hide, so it does not re-raise", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    h.setVisible(false);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("un-acknowledges a chat whose state moved off the cue", () => {
    // So its NEXT cue is fresh. Without this, a chat that finished, was
    // acknowledged, then started and finished another turn would stay silent.
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.stored()).toBe('{"a":"done"}');

    h.setCandidates([{ id: "a", status: "working" }]);
    h.controller.refresh();
    expect(h.stored()).toBe("{}");

    h.setActive("");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("treats an unpainted tab as no information, not as a cleared cue", () => {
    // A tab exists for a tick before the store effect paints its dot, and
    // `tabStatusFor` answers "" for a chat the store does not know. Reading
    // either as "the cue ended" dropped the acknowledgement on every reload,
    // because the boot restore opens the tab before the sweep runs.
    const h = harness({ stored: '{"a":"done"}' });
    h.setCandidates([{ id: "a", status: "" }]);
    h.controller.refresh();
    expect(h.stored()).toBe('{"a":"done"}');

    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("does un-acknowledge on a real non-cue state", () => {
    // The contrast with the case above: `idle` and `working` are states the chat
    // is genuinely in, and they mean the cue is over.
    const h = harness({ stored: '{"a":"done"}' });
    h.setCandidates([{ id: "a", status: "idle" }]);
    h.controller.refresh();
    expect(h.stored()).toBe("{}");
  });

  it("reports zero once the last latched chat closes", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    expect(h.latest().count).toBe(1);

    h.setCandidates([]);
    h.controller.refresh();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("drops a departed chat's acknowledgement", () => {
    const h = harness();
    h.setActive("a");
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.refresh();
    h.setCandidates([]);
    h.controller.forget("a");
    expect(h.stored()).toBe("{}");
  });
});

describe("acknowledging what the reader can see", () => {
  it("acknowledges every chat whose row is in view", () => {
    const h = harness();
    h.setActive("");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "input" },
    ]);
    h.setRowsInView(["a", "b"]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual(NO_ATTENTION);
  });

  it("KEEPS the cue of a chat scrolled out of the list", () => {
    // The case that matters most. `#tab-list` scrolls, and the forgotten
    // background chat is precisely the one likely to be below the fold, so a
    // wholesale clear on becoming visible would blank a cue nobody ever saw.
    const h = harness();
    h.setActive("");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "input" },
    ]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual({ count: 1, worst: "input" });
  });

  it("acknowledges the active chat even when no row is in view", () => {
    // Mobile with the drawer closed: no sidebar row is visible, but the active
    // chat's transcript is what fills the screen.
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "done" },
      { id: "b", status: "done" },
    ]);
    h.setRowsInView([]);
    h.controller.ackSeen();
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("acknowledges nothing at all while the page is hidden", () => {
    // A closed drawer reports no rows, but so would a page nobody is looking at;
    // acknowledging there would blank every cue on a background page.
    const h = harness();
    h.setActive("a");
    h.setVisible(false);
    h.setCandidates([{ id: "a", status: "done" }]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.stored()).toBeNull();
  });

  it("ignores an in-view row holding no cue", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "working" }]);
    h.setRowsInView(["a"]);
    h.controller.ackSeen();
    expect(h.stored()).toBeNull();
  });
});

describe("switching to a chat acknowledges it", () => {
  it("acknowledges the incoming chat before the store's active id has moved", () => {
    // The tab store announces a switch from inside its own emit, BEFORE
    // store.setActive runs, so the refresh pass's watched-chat rule still names
    // the OUTGOING chat at that moment. This is why the switch has its own hook.
    const h = harness();
    h.setActive("a");
    h.setCandidates([
      { id: "a", status: "idle" },
      { id: "b", status: "done" },
    ]);
    h.controller.ackSwitch("b");
    expect(h.latest()).toEqual(NO_ATTENTION);
    expect(h.stored()).toBe('{"b":"done"}');
  });

  it("does not acknowledge a switch made while the page is hidden", () => {
    // The boot restore activates a tab too, and a page restored into a
    // background browser tab must not have its cue swallowed by that.
    const h = harness();
    h.setVisible(false);
    h.setCandidates([{ id: "b", status: "done" }]);
    h.controller.ackSwitch("b");
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });

  it("ignores a switch to a tab that is not a chat", () => {
    const h = harness();
    h.setCandidates([{ id: "a", status: "done" }]);
    h.controller.ackSwitch("__settings__");
    expect(h.latest()).toEqual({ count: 1, worst: "done" });
  });
});
