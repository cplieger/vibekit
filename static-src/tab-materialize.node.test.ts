// The subject-to-spec factory. `.node.test.ts` because the factory is DOM-free
// by design and a test that never touches a document proves it; the two leaf
// stores it reads for names are mocked, so every assertion here is about the
// factory's own rules rather than about a store's contents.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { TabKind, TabSubject } from "./types.js";
import type { Route } from "./router.js";
import type { TabDotStatus, TabViewSpec } from "./tab-view.js";
import { TAB_ICONS } from "./tab-view.js";
import {
  materializeTab,
  registerTabOpeners,
  _resetTabOpenersForTest,
  type TabOpeners,
} from "./tab-materialize.js";

vi.mock("./store.js", () => ({ get: vi.fn(() => undefined) }));
vi.mock("./run-store.js", () => ({ peekRunState: vi.fn(() => undefined) }));

// The five singleton loaders are reached through a lazy import, so a mock of each
// module is what lets a test call `onShow` without pulling a page's worth of DOM
// in. Each mock deliberately also exports the module's TOGGLE, so "the factory
// never calls a toggle-style opener" is an assertion rather than a claim.
vi.mock("./settings-tabs.js", () => ({
  loadSettingsTabData: vi.fn(),
  forceSettingsTab: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./git.js", () => ({ loadGitRepos: vi.fn() }));
vi.mock("./files.js", () => ({
  loadFileBrowser: vi.fn(),
  resetFileBrowser: vi.fn(),
}));
vi.mock("./history.js", () => ({
  loadHistoryView: vi.fn(),
  teardownHistoryView: vi.fn(),
}));
vi.mock("./docs.js", () => ({
  loadDocsView: vi.fn(),
  toggleDocsView: vi.fn(),
}));

import { get } from "./store.js";
import { peekRunState } from "./run-store.js";
import { loadDocsView, toggleDocsView } from "./docs.js";
import { loadFileBrowser, resetFileBrowser } from "./files.js";
import { loadGitRepos } from "./git.js";
import { loadHistoryView, teardownHistoryView } from "./history.js";
import { loadSettingsTabData } from "./settings-tabs.js";

// --- Fixtures ---

function subject(over: Partial<TabSubject> & { kind: TabKind }): TabSubject {
  return {
    id: "t-1",
    ref: "",
    parent: "",
    pinned: false,
    owns: true,
    ...over,
  };
}

interface Spies {
  chatShow: ReturnType<typeof vi.fn>;
  chatClose: ReturnType<typeof vi.fn>;
  chatDot: ReturnType<typeof vi.fn>;
  editorShow: ReturnType<typeof vi.fn>;
  editorClose: ReturnType<typeof vi.fn>;
  runShow: ReturnType<typeof vi.fn>;
  runCancel: ReturnType<typeof vi.fn>;
}

let spies: Spies;

function register(dot: TabDotStatus | "" = ""): void {
  spies = {
    chatShow: vi.fn(),
    chatClose: vi.fn(),
    chatDot: vi.fn(() => dot),
    editorShow: vi.fn(),
    editorClose: vi.fn(),
    runShow: vi.fn(),
    runCancel: vi.fn(),
  };
  const openers: TabOpeners = {
    chat: { show: spies.chatShow, close: spies.chatClose, dot: spies.chatDot },
    editor: { show: spies.editorShow, close: spies.editorClose },
    run: { show: spies.runShow, cancel: spies.runCancel },
  };
  registerTabOpeners(openers);
}

beforeEach(() => {
  _resetTabOpenersForTest();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(peekRunState).mockReturnValue(undefined);
});

/** Everything about a spec that is not a callback, so two specs can be compared
 *  for "identical apart from behaviour". */
function shapeOf(spec: TabViewSpec): Record<string, unknown> {
  return {
    name: spec.name,
    icon: spec.icon,
    view: spec.view,
    route: spec.route,
    owns: spec.owns,
    parentId: spec.parentId,
    dotStatus: spec.dotStatus,
  };
}

// --- Totality ---

// One row per kind: the whole vocabulary, so a kind that stops producing a spec
// fails here rather than at the first reader who opens that tab. `ref` is what
// each kind's identity actually is — a chat id, a path, a workflow id, nothing at
// all for a singleton.
const CASES: readonly { kind: TabKind; ref: string; view: string; route: Route }[] = [
  { kind: "chat", ref: "c-abc", view: "#chat-view", route: { kind: "chat", id: "c-abc" } },
  {
    kind: "editor",
    ref: "/workspace/a/b.ts",
    view: "#editor-view",
    route: { kind: "file", path: "/workspace/a/b.ts" },
  },
  { kind: "run", ref: "wf-1", view: "#run-view", route: { kind: "run", id: "wf-1" } },
  {
    kind: "settings",
    ref: "",
    view: "#settings-view",
    route: { kind: "settings", tab: "general" },
  },
  { kind: "git", ref: "", view: "#git-view", route: { kind: "git", tab: "changes" } },
  { kind: "files", ref: "", view: "#files-view", route: { kind: "files", path: "." } },
  { kind: "history", ref: "", view: "#history-view", route: { kind: "history" } },
  { kind: "docs", ref: "", view: "#docs-view", route: { kind: "docs", tab: "steering" } },
];

describe("materializeTab is total over the eight kinds", () => {
  // The view selector is asserted against a LITERAL rather than against
  // TAB_VIEWS[kind], which would be tautological: reading the table to check the
  // table cannot see a kind pointing at another kind's view.
  it.each(CASES)("$kind produces its view, icon and route", ({ kind, ref, view, route }) => {
    register();
    const spec = materializeTab(subject({ kind, ref }));
    expect(spec.view).toBe(view);
    expect(spec.icon).toBe(TAB_ICONS[kind]);
    expect(spec.route).toEqual(route);
  });

  // The rule this asserts is narrow and load-bearing: `owns` is copied from the
  // SUBJECT for every kind, never inferred from the kind. Inferring it is exactly
  // the mistake that makes a run REVIEW cancel the run it is only watching, and
  // asserting it per kind is what stops a future case hardcoding `owns: true`
  // because "a chat always owns its bridge".
  it.each(CASES)("$kind takes owns from the subject, not from the kind", ({ kind, ref }) => {
    register();
    expect(materializeTab(subject({ kind, ref, owns: true })).owns).toBe(true);
    expect(materializeTab(subject({ kind, ref, owns: false })).owns).toBe(false);
  });

  it.each(CASES)("$kind names the tab", ({ kind, ref }) => {
    register();
    expect(materializeTab(subject({ kind, ref })).name).not.toBe("");
  });
});

// --- The run case, both ways ---

describe("an owned run and a review of the same run", () => {
  const OWNED = subject({ kind: "run", ref: "wf-7", owns: true });
  const REVIEW = subject({ kind: "run", ref: "wf-7", owns: false });

  it("differ in owns and in nothing else describable", () => {
    register();
    const owned = materializeTab(OWNED);
    const review = materializeTab(REVIEW);
    expect(shapeOf(owned)).toEqual({ ...shapeOf(review), owns: true });
  });

  it("differ in the CONSEQUENCE of owns: only the owned one cancels on close", () => {
    register();
    const owned = materializeTab(OWNED);
    const review = materializeTab(REVIEW);
    // A review carries no teardown at all rather than one the store would skip:
    // dismissing a view has nothing to tear down, and the absent field says so.
    expect("onClose" in review).toBe(false);
    owned.onClose?.();
    expect(spies.runCancel).toHaveBeenCalledWith("wf-7");
    expect(spies.runCancel).toHaveBeenCalledTimes(1);
  });

  it("show the same run, and tell the view which authority it has", () => {
    register();
    materializeTab(OWNED).onShow?.();
    materializeTab(REVIEW).onShow?.();
    expect(spies.runShow.mock.calls).toEqual([
      ["wf-7", true],
      ["wf-7", false],
    ]);
  });
});

// --- Sub-tab positioning ---

describe("a subject with a parent", () => {
  it("positions as a sub-tab of that parent", () => {
    register();
    const spec = materializeTab(subject({ kind: "run", ref: "wf-2", parent: "t-parent" }));
    expect(spec.parentId).toBe("t-parent");
  });

  // The store says "top level" with an ABSENT field and the wire says it with an
  // empty string. Setting `parentId: ""` instead would make `insertSpec` look for
  // a tab whose id is the empty string, miss, and fall through to its orphan
  // path — the right position for the wrong reason, and a real parent id would
  // then be indistinguishable from a missing one.
  it.each(CASES)("$kind with no parent carries no parentId at all", ({ kind, ref }) => {
    register();
    const spec = materializeTab(subject({ kind, ref, parent: "" }));
    expect("parentId" in spec).toBe(false);
  });

  it("carries the parent for every kind, because a sub-tab is not a chat feature", () => {
    register();
    for (const { kind, ref } of CASES) {
      expect(materializeTab(subject({ kind, ref, parent: "t-parent" })).parentId).toBe("t-parent");
    }
  });
});

// --- The injection seam ---

describe("the injection seam", () => {
  it.each(CASES)("$kind fails loudly when no openers are registered", ({ kind, ref }) => {
    expect(() => materializeTab(subject({ kind, ref }))).toThrow(/no openers registered/);
  });

  it("names the kind it was materializing, so the failure says what was open", () => {
    expect(() => materializeTab(subject({ kind: "chat", ref: "c-1" }))).toThrow(/"chat"/);
  });

  // The failure this test exists for is the SILENT one: a factory that shipped a
  // spec whose onShow was undefined would open a chat tab that renders and never
  // loads its transcript, with nothing in the console. So the assertion is that
  // no spec is produced at all.
  it("produces no spec rather than one with an inert onShow", () => {
    let escaped: TabViewSpec | undefined;
    try {
      escaped = materializeTab(subject({ kind: "chat", ref: "c-1" }));
    } catch {
      escaped = undefined;
    }
    expect(escaped).toBeUndefined();
  });

  it("materializes again once the openers arrive", () => {
    register();
    materializeTab(subject({ kind: "chat", ref: "c-1" })).onShow?.();
    expect(spies.chatShow).toHaveBeenCalledWith("c-1");
  });
});

// --- Delegation ---

describe("the injected behaviours receive the subject's ref", () => {
  it("chat show and close", () => {
    register();
    const spec = materializeTab(subject({ kind: "chat", ref: "c-9" }));
    spec.onShow?.();
    spec.onClose?.({ remote: true });
    expect(spies.chatShow).toHaveBeenCalledWith("c-9");
    expect(spies.chatClose).toHaveBeenCalledWith("c-9", { remote: true });
  });

  // The default is what the store's own contract needs: a caller that omits the
  // flag means LOCAL, because a missing flag must never suppress the server-side
  // teardown.
  it("chat close defaults remote to false", () => {
    register();
    materializeTab(subject({ kind: "chat", ref: "c-9" })).onClose?.();
    expect(spies.chatClose).toHaveBeenCalledWith("c-9", { remote: false });
  });

  it("editor show and close", () => {
    register();
    const spec = materializeTab(subject({ kind: "editor", ref: "/w/x.ts" }));
    spec.onShow?.();
    spec.onClose?.();
    expect(spies.editorShow).toHaveBeenCalledWith("/w/x.ts");
    expect(spies.editorClose).toHaveBeenCalledWith("/w/x.ts");
  });
});

// --- The dot ---

describe("the chat dot", () => {
  it("rides the spec so a row that is created already knows what to show", () => {
    register("working");
    expect(materializeTab(subject({ kind: "chat", ref: "c-1" })).dotStatus).toBe("working");
  });

  it("is ABSENT rather than empty when nothing is painted", () => {
    register("");
    const spec = materializeTab(subject({ kind: "chat", ref: "c-1" }));
    expect("dotStatus" in spec).toBe(false);
  });

  it("is not asked for on a kind that has no chat state", () => {
    register("working");
    materializeTab(subject({ kind: "docs" }));
    expect(spies.chatDot).not.toHaveBeenCalled();
  });
});

// --- Names ---

describe("names", () => {
  it("takes a chat's name from the chat store", () => {
    register();
    vi.mocked(get).mockReturnValue({ name: "Fix the parser" } as never);
    expect(materializeTab(subject({ kind: "chat", ref: "c-1" })).name).toBe("Fix the parser");
  });

  // The store row is missing for exactly the case the report calls out: a chat
  // resumed from History has no row yet, and today's opener passes KAS's row
  // title, which the factory cannot see.
  it("falls back for a chat with no store row", () => {
    register();
    expect(materializeTab(subject({ kind: "chat", ref: "c-1" })).name).toBe("New conversation");
  });

  it("prefers a run's launcher label over the recipe's name", () => {
    register();
    vi.mocked(peekRunState).mockReturnValue({
      workflowId: "wf-1",
      runLabel: "nightly sweep",
      workflowName: "sweep.yaml",
    });
    expect(materializeTab(subject({ kind: "run", ref: "wf-1" })).name).toBe("nightly sweep");
  });

  it("uses the recipe's name when the launcher gave none", () => {
    register();
    vi.mocked(peekRunState).mockReturnValue({ workflowId: "wf-1", workflowName: "sweep.yaml" });
    expect(materializeTab(subject({ kind: "run", ref: "wf-1" })).name).toBe("sweep.yaml");
  });

  it("falls back for a run this client has fetched nothing for", () => {
    register();
    expect(materializeTab(subject({ kind: "run", ref: "wf-1" })).name).toBe("Workflow run");
  });

  it("names an editor tab after the file's last path segment", () => {
    register();
    expect(materializeTab(subject({ kind: "editor", ref: "/workspace/a/b.ts" })).name).toBe("b.ts");
  });

  it.each([
    ["settings", "Settings"],
    ["git", "Source Control"],
    ["files", "Files"],
    ["history", "History"],
    ["docs", "Kiro docs"],
  ] as const)("names the %s singleton", (kind, name) => {
    register();
    expect(materializeTab(subject({ kind })).name).toBe(name);
  });
});

// --- Singleton loaders ---

describe("a singleton's onShow reaches its LOADER, never its toggle", () => {
  /** Let a lazy import settle. The loader modules are mocked, so the dynamic
   *  import resolves out of the module registry rather than off the network and
   *  one macrotask is enough. Cheaper than polling, and it keeps these cases
   *  under the suite's 100ms slow-test threshold. */
  async function settle(): Promise<void> {
    await new Promise((done) => {
      setTimeout(done, 0);
    });
  }

  // A toggle CLOSES the tab when it is already active, so a factory that reached
  // one would make materializing a subject destroy the tab it describes. The docs
  // module exports both verbs, which is what makes this assertable rather than
  // merely stated.
  it("docs", async () => {
    register();
    materializeTab(subject({ kind: "docs" })).onShow?.();
    await settle();
    expect(loadDocsView).toHaveBeenCalledWith("steering");
    expect(toggleDocsView).not.toHaveBeenCalled();
  });

  it("settings", async () => {
    register();
    materializeTab(subject({ kind: "settings" })).onShow?.();
    await settle();
    expect(loadSettingsTabData).toHaveBeenCalledWith("general");
  });

  it("git", async () => {
    register();
    materializeTab(subject({ kind: "git" })).onShow?.();
    await settle();
    expect(loadGitRepos).toHaveBeenCalled();
  });

  it("files, both directions", async () => {
    register();
    const spec = materializeTab(subject({ kind: "files" }));
    spec.onShow?.();
    await settle();
    expect(loadFileBrowser).toHaveBeenCalled();
    spec.onClose?.();
    await settle();
    expect(resetFileBrowser).toHaveBeenCalled();
  });

  it("history, both directions", async () => {
    register();
    const spec = materializeTab(subject({ kind: "history" }));
    spec.onShow?.();
    await settle();
    expect(loadHistoryView).toHaveBeenCalled();
    spec.onClose?.();
    await settle();
    expect(teardownHistoryView).toHaveBeenCalled();
  });

  // Docs is the one singleton with no teardown: it holds no dispatch, no
  // AbortController and no timer, unlike History.
  it("docs carries no onClose", () => {
    register();
    expect("onClose" in materializeTab(subject({ kind: "docs" }))).toBe(false);
  });
});
