// The subject-to-spec factory. `.node.test.ts` because the factory is DOM-free
// by design and a test that never touches a document proves it; the two leaf
// stores it reads for names are mocked, so every assertion here is about the
// factory's own rules rather than about a store's contents.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Mock } from "vitest";
import type { TabKind, TabSubject } from "./types.js";
import type { Route } from "./router.js";
import type { TabDotStatus, TabViewSpec } from "./tab-view.js";
import { TAB_ICONS } from "./tab-view.js";
import {
  materializeTab,
  registerTabOpeners,
  subjectForRoute,
  _resetTabOpenersForTest,
  type TabOpeners,
} from "./tab-materialize.js";

vi.mock("./store.js", () => ({ get: vi.fn(() => undefined) }));
vi.mock("./run-store.js", () => ({
  // The run's label, resolved by the STORE: which of `runLabel` and `workflowName`
  // wins is a precedence over cached run state, so it lives there and is pinned in
  // run-store.test.ts. What the factory owns is the placeholder for `""`.
  runLabelOf: vi.fn(() => ""),
}));

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
  showDocsView: vi.fn(),
}));

import { get } from "./store.js";
import { runLabelOf } from "./run-store.js";
import { loadDocsView, showDocsView } from "./docs.js";
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
  chatShow: Mock<TabOpeners["chat"]["show"]>;
  chatClose: Mock<TabOpeners["chat"]["close"]>;
  chatDot: Mock<TabOpeners["chat"]["dot"]>;
  editorShow: Mock<TabOpeners["editor"]["show"]>;
  editorClose: Mock<TabOpeners["editor"]["close"]>;
  runShow: Mock<TabOpeners["run"]["show"]>;
  subagentShow: Mock<TabOpeners["subagent"]["show"]>;
}

let spies: Spies;

function register(dot: TabDotStatus | "" = ""): void {
  spies = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    chatDot: vi.fn<TabOpeners["chat"]["dot"]>(() => dot),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    subagentShow: vi.fn<TabOpeners["subagent"]["show"]>(),
  };
  const openers: TabOpeners = {
    chat: { show: spies.chatShow, close: spies.chatClose, dot: spies.chatDot },
    editor: { show: spies.editorShow, close: spies.editorClose },
    run: { show: spies.runShow },
    subagent: { show: spies.subagentShow },
  };
  registerTabOpeners(openers);
}

beforeEach(() => {
  _resetTabOpenersForTest();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(runLabelOf).mockReturnValue("");
});

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

  // `owns` is copied from the SUBJECT for every kind, never inferred from the kind,
  // which is what stops a future case hardcoding `owns: true` because "a chat always
  // owns its bridge".
  //
  // RUN and SUBAGENT are the two exceptions and they are hardcoded FALSE on purpose:
  // both are subpage VIEWS of work owned elsewhere (user decision, 2026-08), so their
  // × closes a view and stops nothing. Asserting the exception here is what stops it
  // being re-derived as a subject field — see the run case below.
  it.each(CASES.filter((c) => c.kind !== "run" && c.kind !== "subagent"))(
    "$kind takes owns from the subject, not from the kind",
    ({ kind, ref }) => {
      register();
      expect(materializeTab(subject({ kind, ref, owns: true })).owns).toBe(true);
      expect(materializeTab(subject({ kind, ref, owns: false })).owns).toBe(false);
    },
  );

  it.each(CASES.filter((c) => c.kind === "run" || c.kind === "subagent"))(
    "$kind is a VIEW whatever the subject claims",
    ({ kind, ref }) => {
      register();
      expect(materializeTab(subject({ kind, ref, owns: true })).owns).toBe(false);
      expect(materializeTab(subject({ kind, ref, owns: false })).owns).toBe(false);
    },
  );

  it.each(CASES)("$kind names the tab", ({ kind, ref }) => {
    register();
    expect(materializeTab(subject({ kind, ref })).name).not.toBe("");
  });
});

// --- The run case ---

// ONE shape, whatever door opened it. There used to be two — an owned tab whose ×
// cancelled the run and a review whose × did not — and these cases pinned the
// difference. The difference is gone (user decision, 2026-08): the subpage view is
// universal across a parentless workflow, a chat-triggered workflow and a subagent
// expansion, and a × that means "close this" on one door and "destroy the work" on
// another is a gesture a reader cannot learn.
//
// What replaces the assertion is its inverse: a run tab NEVER carries a teardown, so
// no door can be given one by setting a subject field.
describe("a run tab is always a view", () => {
  it("carries no teardown, whatever the subject says", () => {
    register();
    for (const owns of [true, false]) {
      const spec = materializeTab(subject({ kind: "run", ref: "wf-7", owns }));
      expect("onClose" in spec).toBe(false);
      expect(spec.owns).toBe(false);
    }
  });

  it("shows the run and tells the view nothing about authority", () => {
    register();
    materializeTab(subject({ kind: "run", ref: "wf-7", owns: true })).onShow?.();
    materializeTab(subject({ kind: "run", ref: "wf-7", owns: false })).onShow?.();
    // ONE argument. The view derives what it may offer from the RUN — its status and
    // whether it is parentless — rather than from which door was used.
    expect(spies.runShow.mock.calls).toEqual([["wf-7"], ["wf-7"]]);
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
    spec.onClose?.();
    expect(spies.chatShow).toHaveBeenCalledWith("c-9");
    // The ref alone: the teardown is client-local and identical whoever closed
    // the tab, so there is no provenance flag to thread through the factory.
    expect(spies.chatClose).toHaveBeenCalledWith("c-9");
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

  it("names a run from the store's label", () => {
    register();
    vi.mocked(runLabelOf).mockReturnValue("nightly sweep");
    expect(materializeTab(subject({ kind: "run", ref: "wf-1" })).name).toBe("nightly sweep");
  });

  // The one half of the run's name this module owns. The store answers `""` for a
  // run nothing has been fetched for, which is the normal state at the instant the
  // server's own tab offer arrives — so a row built then would be called nothing
  // at all without this.
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
    ["git", "Git"],
    ["files", "Files"],
    ["history", "History"],
    ["docs", "Kiro docs"],
  ] as const)("names the %s singleton", (kind, name) => {
    register();
    expect(materializeTab(subject({ kind })).name).toBe(name);
  });
});

// --- The route inverse ---

describe("subjectForRoute inverts the factory's route", () => {
  // A real inverse property, not a table read back: the two directions are
  // written independently in the same file, so a mapping that sends /run/{id} to
  // the wrong kind fails here. It is also what catches the one place the two
  // vocabularies differ — the route kind is `file` and the tab kind is `editor`.
  it.each(CASES)("$kind round-trips through its route", ({ kind, ref }) => {
    register();
    const route = materializeTab(subject({ kind, ref })).route;
    expect(subjectForRoute(route)).toEqual({ kind, ref });
  });

  // The OTHER direction deliberately does not round-trip. A singleton's route
  // carries a sub-position and its subject carries none, which is what makes
  // /settings/tools and /settings name one tab: the sub-position is corrected
  // after activation, by applyRoute.
  it.each([
    [{ kind: "settings", tab: "tools" } as Route, "settings"],
    [{ kind: "git", tab: "prs" } as Route, "git"],
    [{ kind: "docs", tab: "hooks" } as Route, "docs"],
    [{ kind: "files", path: "a/b" } as Route, "files"],
  ])("drops a singleton's sub-position: %o", (route, kind) => {
    expect(subjectForRoute(route)).toEqual({ kind, ref: "" });
  });

  // Same rule one axis along: a run route's `#node=` fragment names a POSITION
  // inside the tab, not a different tab, so it is dropped exactly like a
  // singleton's sub-tab. This is what keeps applyRoute's history-origin guard
  // honest — a Back press onto another node of an OPEN run must resolve to that
  // tab, not read as a tab nobody has open.
  it("drops a run route's node fragment", () => {
    const subject = { kind: "run", ref: "wf_1" };
    expect(subjectForRoute({ kind: "run", id: "wf_1", node: "wf_1/lint" })).toEqual(subject);
    expect(subjectForRoute({ kind: "run", id: "wf_1" })).toEqual(subject);
  });

  // The default "/" route names no chat, so it resolves to a subject nothing can
  // match — an empty ref belongs to a singleton. That answer is what makes the
  // back/forward guard redirect "/" to whatever is on screen rather than looking
  // for a chat tab with no id.
  it("answers an unmatchable subject for the default chat route", () => {
    expect(subjectForRoute({ kind: "chat", id: "" })).toEqual({ kind: "chat", ref: "" });
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
  // one would make materializing a subject destroy the tab it describes.
  // showDocsView is that hazard concretely: it delegates straight to tabs.ts's
  // toggleDocsView, and docs.js exports it beside the plain loader, which is what
  // makes this assertable rather than merely stated.
  it("docs", async () => {
    register();
    materializeTab(subject({ kind: "docs" })).onShow?.();
    await settle();
    expect(loadDocsView).toHaveBeenCalledWith("steering");
    expect(showDocsView).not.toHaveBeenCalled();
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
