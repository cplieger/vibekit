// The subject-to-spec factory. `.node.test.ts` because the factory is DOM-free
// by design and a test that never touches a document proves it; the two leaf
// stores it reads for names are mocked, so every assertion here is about the
// factory's own rules rather than about a store's contents.

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Mock } from "vitest";
import type { TabKind, TabSubject } from "./types.js";
import type { Route } from "./route-path.js";
import type { TabDotStatus, TabViewSpec } from "./tab-view.js";
// The mocked module's own type, for the partial factory below. A top-level
// `import type` rather than an inline `import()` annotation, which the lint forbids.
import type * as StoreModule from "./store.js";
import { TAB_ICONS } from "./tab-view.js";
import {
  materializeTab,
  registerTabOpeners,
  subjectForRoute,
  _resetTabOpenersForTest,
  type TabOpeners,
} from "./tab-materialize.js";

// PARTIAL, so `subagentStatusFor` is the real mapper. Faking it would put a second
// copy of the ToolStatus-to-dot mapping in this file and the assertion would be about
// that copy; what the factory owns is routing the invocation's status THROUGH the
// mapper, so only the store READ is replaced.
vi.mock("./store.js", async (orig) => ({
  ...(await orig<typeof StoreModule>()),
  get: vi.fn(() => undefined),
}));
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
  refreshSettingsPanel: vi.fn(),
  forceSettingsTab: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getActive: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./git.js", () => ({ loadGitRepos: vi.fn(), refreshGitView: vi.fn() }));
vi.mock("./files.js", () => ({
  showFilesTab: vi.fn(),
  releaseFilesTab: vi.fn(),
}));
vi.mock("./history.js", () => ({
  loadHistoryView: vi.fn(),
  refreshHistoryView: vi.fn(),
  teardownHistoryView: vi.fn(),
}));
vi.mock("./docs.js", () => ({
  showDocsTab: vi.fn(),
  refreshDocsView: vi.fn(),
  forceDocsTab: vi.fn(),
}));

import { get } from "./store.js";
import { runLabelOf } from "./run-store.js";
import { showDocsTab, refreshDocsView, forceDocsTab } from "./docs.js";
import { showFilesTab, releaseFilesTab } from "./files.js";
import { loadGitRepos, refreshGitView } from "./git.js";
import { loadHistoryView, refreshHistoryView, teardownHistoryView } from "./history.js";
import { loadSettingsTabData, refreshSettingsPanel } from "./settings-tabs.js";

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
  chatRefresh: Mock<TabOpeners["chat"]["refresh"]>;
  chatClose: Mock<TabOpeners["chat"]["close"]>;
  chatDot: Mock<TabOpeners["chat"]["dot"]>;
  editorShow: Mock<TabOpeners["editor"]["show"]>;
  editorRefresh: Mock<TabOpeners["editor"]["refresh"]>;
  editorClose: Mock<TabOpeners["editor"]["close"]>;
  runShow: Mock<TabOpeners["run"]["show"]>;
  runRefresh: Mock<TabOpeners["run"]["refresh"]>;
  subagentShow: Mock<TabOpeners["subagent"]["show"]>;
  subagentRefresh: Mock<TabOpeners["subagent"]["refresh"]>;
}

let spies: Spies;

function register(dot: TabDotStatus | "" = ""): void {
  spies = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatRefresh: vi.fn<TabOpeners["chat"]["refresh"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    chatDot: vi.fn<TabOpeners["chat"]["dot"]>(() => dot),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorRefresh: vi.fn<TabOpeners["editor"]["refresh"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    runRefresh: vi.fn<TabOpeners["run"]["refresh"]>(),
    subagentShow: vi.fn<TabOpeners["subagent"]["show"]>(),
    subagentRefresh: vi.fn<TabOpeners["subagent"]["refresh"]>(),
  };
  const openers: TabOpeners = {
    chat: {
      show: spies.chatShow,
      refresh: spies.chatRefresh,
      close: spies.chatClose,
      dot: spies.chatDot,
    },
    editor: { show: spies.editorShow, refresh: spies.editorRefresh, close: spies.editorClose },
    run: { show: spies.runShow, refresh: spies.runRefresh },
    subagent: { show: spies.subagentShow, refresh: spies.subagentRefresh },
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
  {
    kind: "files",
    ref: "/workspace/x",
    view: "#files-view",
    route: { kind: "files", path: "/workspace/x" },
  },
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

  // `refresh` is REQUIRED, so a case that omitted it would already fail typecheck.
  // What this reaches that the compiler cannot: a case satisfying the type with a
  // field that is not callable.
  it.each(CASES)("$kind carries a refresh", ({ kind, ref }) => {
    register();
    expect(typeof materializeTab(subject({ kind, ref })).refresh).toBe("function");
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

  // The DATA half of each injected kind, and it must reach its own opener rather
  // than the show beside it: a refresh that activated would push a route from the
  // dispatcher, and a show that fetched would double every activation.
  it("chat refresh", () => {
    register();
    materializeTab(subject({ kind: "chat", ref: "c-9" })).refresh();
    expect(spies.chatRefresh).toHaveBeenCalledWith("c-9");
    expect(spies.chatShow).not.toHaveBeenCalled();
  });

  it("editor refresh", () => {
    register();
    materializeTab(subject({ kind: "editor", ref: "/w/x.ts" })).refresh();
    expect(spies.editorRefresh).toHaveBeenCalledWith("/w/x.ts");
    expect(spies.editorShow).not.toHaveBeenCalled();
  });

  it("run refresh", () => {
    register();
    materializeTab(subject({ kind: "run", ref: "wf-7" })).refresh();
    expect(spies.runRefresh).toHaveBeenCalledWith("wf-7");
    expect(spies.runShow).not.toHaveBeenCalled();
  });

  // Both halves of the composite ref, split by the factory's own codec.
  it("subagent refresh", () => {
    register();
    materializeTab(subject({ kind: "subagent", ref: "c-3/task-8" })).refresh();
    expect(spies.subagentRefresh).toHaveBeenCalledWith("c-3", "task-8");
    expect(spies.subagentShow).not.toHaveBeenCalled();
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

// --- The subagent dot ---

// Seeded from the SAME invocation the row's name comes from, which is the whole point:
// a row cannot read `wf-workflow-creator` beside an empty dot slot. `subagent-dots.ts`
// keeps it live afterwards, but on the door that matters — a transcript link, where the
// invocation is already resident — the effect is a frame late, and 12-tabs.css no
// longer reserves a slot for this kind to cover that frame.
describe("the subagent dot", () => {
  /** A chat row holding one delegate invocation, stamped with the subtask the refs
   *  below name. `findSubagentInvocation` matches on that id AND on the title, so both
   *  have to be real or the factory correctly finds nothing. */
  function withDelegate(status: string): void {
    vi.mocked(get).mockReturnValue({
      messages: [
        {
          tool_calls: [
            {
              id: "invoke_subagent_x",
              title: "Sub-agent: wf-workflow-creator",
              status,
              agent_subtask_id: "sub-1",
            },
          ],
        },
      ],
    } as never);
  }

  it("rides the spec, off the same invocation as the name", () => {
    register();
    withDelegate("completed");
    const spec = materializeTab(subject({ kind: "subagent", ref: "c-1/sub-1" }));
    expect(spec.dotStatus).toBe("done");
    expect(spec.name).toBe("wf-workflow-creator");
  });

  it("carries a running delegate's own state", () => {
    register();
    withDelegate("in_progress");
    expect(materializeTab(subject({ kind: "subagent", ref: "c-1/sub-1" })).dotStatus).toBe(
      "working",
    );
  });

  // The case the report was about: the launching chat holds no invocation for this
  // delegate, so there is no status to seed and none is invented. The row then carries
  // no dot AND no reserved slot, rather than a hole that never fills.
  it("is ABSENT when the launching chat holds no invocation for it", () => {
    register();
    const spec = materializeTab(subject({ kind: "subagent", ref: "c-1/sub-gone" }));
    expect("dotStatus" in spec).toBe(false);
    expect(spec.name).toBe("Subagent");
  });

  it("is ABSENT for a malformed ref, which never resolves a chat to read", () => {
    register();
    withDelegate("completed");
    expect("dotStatus" in materializeTab(subject({ kind: "subagent", ref: "no-slash" }))).toBe(
      false,
    );
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
    ["history", "History"],
    ["docs", "Kiro docs"],
  ] as const)("names the %s singleton", (kind, name) => {
    register();
    expect(materializeTab(subject({ kind })).name).toBe(name);
  });

  // Files is NOT in that table any more: it is one tab per folder, so its label is
  // the folder's last segment rather than a constant.
  it("names a files tab after the folder it was opened at", () => {
    register();
    expect(materializeTab(subject({ kind: "files", ref: "/workspace/x" })).name).toBe("x");
  });

  it("names a files tab at the mounts listing", () => {
    register();
    expect(materializeTab(subject({ kind: "files", ref: "/" })).name).toBe("Files");
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

  // The OTHER direction deliberately does not round-trip, for the three kinds whose
  // route carries a SUB-TAB their subject cannot: that is what makes /settings/tools
  // and /settings name one tab, the sub-position being corrected after activation by
  // applyRoute. A files path is NOT one of those — it is the folder a route MINTS a
  // browser at, so it survives into the ref.
  it.each([
    [{ kind: "settings", tab: "tools" } as Route, "settings"],
    [{ kind: "git", tab: "prs" } as Route, "git"],
    [{ kind: "docs", tab: "hooks" } as Route, "docs"],
  ])("drops a singleton's sub-position: %o", (route, kind) => {
    expect(subjectForRoute(route)).toEqual({ kind, ref: "" });
  });

  it("keeps a files route's folder, because that is what a mint opens the tab at", () => {
    expect(subjectForRoute({ kind: "files", path: "/workspace/x" })).toEqual({
      kind: "files",
      ref: "/workspace/x",
    });
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
  //
  // `forceDocsTab` is the SECOND thing this pins: the activation must not force the
  // canonical sub-tab, which is what discarded the reader's own on every switch back.
  it("docs", async () => {
    register();
    materializeTab(subject({ kind: "docs" })).onShow?.();
    await settle();
    expect(showDocsTab).toHaveBeenCalled();
    expect(forceDocsTab).not.toHaveBeenCalled();
  });

  // Settings and files have NO activation half left: each one's whole `onShow` was
  // the data half, so the field is dropped rather than emptied — an `onShow` that
  // did nothing would read as a door somebody forgot to wire. For files the data
  // half is also the BIND, which is why it may not be split across the two hooks.
  it("settings has no onShow at all", () => {
    register();
    expect(materializeTab(subject({ kind: "settings" })).onShow).toBeUndefined();
  });

  it("files has no onShow at all", () => {
    register();
    expect(materializeTab(subject({ kind: "files", ref: "/workspace/x" })).onShow).toBeUndefined();
  });

  it("git", async () => {
    register();
    materializeTab(subject({ kind: "git" })).onShow?.();
    await settle();
    expect(loadGitRepos).toHaveBeenCalled();
  });

  it("files releases the tab it names", async () => {
    register();
    materializeTab(subject({ kind: "files", ref: "/workspace/x" })).onClose?.();
    await settle();
    expect(releaseFilesTab).toHaveBeenCalledWith("/workspace/x");
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

  // The five singletons' DATA half, each reaching its own module's refresh and not
  // the loader beside it.
  it("settings refresh loads the active panel", async () => {
    register();
    materializeTab(subject({ kind: "settings" })).refresh();
    await settle();
    expect(refreshSettingsPanel).toHaveBeenCalled();
    expect(loadSettingsTabData).not.toHaveBeenCalled();
  });

  it("git refresh dispatches the active sub-tab", async () => {
    register();
    materializeTab(subject({ kind: "git" })).refresh();
    await settle();
    expect(refreshGitView).toHaveBeenCalled();
  });

  // A files refresh is BIND-then-load, addressed by the subject's own ref: N browsers
  // share one view element, so an activation is what re-points it, and the ref is
  // what says which one.
  it("files refresh binds and loads the tab it names", async () => {
    register();
    materializeTab(subject({ kind: "files", ref: "/workspace/x" })).refresh();
    await settle();
    expect(showFilesTab).toHaveBeenCalledWith("/workspace/x");
  });

  it("history refresh refetches the list", async () => {
    register();
    materializeTab(subject({ kind: "history" })).refresh();
    await settle();
    expect(refreshHistoryView).toHaveBeenCalled();
    expect(loadHistoryView).not.toHaveBeenCalled();
  });

  // Forces no sub-tab, for the activation's reason: a refresh that forced the
  // canonical panel would discard the reader's own on every gap.
  it("docs refresh refetches the inventory", async () => {
    register();
    materializeTab(subject({ kind: "docs" })).refresh();
    await settle();
    expect(refreshDocsView).toHaveBeenCalled();
    expect(forceDocsTab).not.toHaveBeenCalled();
    expect(showDocsTab).not.toHaveBeenCalled();
  });

  // Docs is the one singleton with no teardown: it holds no dispatch, no
  // AbortController and no timer, unlike History.
  it("docs carries no onClose", () => {
    register();
    expect("onClose" in materializeTab(subject({ kind: "docs" }))).toBe(false);
  });
});

// A ref arrives from the persisted set bounded only by MaxRefBytes, so a trailing
// slash and an interior double slash are both legal spellings of one folder. The
// factory normalises once and spends that value on the name, the route and the lazy
// call, so all three name the same folder; without it the label and the URL would
// carry the spelling while the state loaded the canonical folder.
describe("a non-canonical files ref resolves to ONE folder", () => {
  async function settle(): Promise<void> {
    await new Promise((done) => {
      setTimeout(done, 0);
    });
  }

  const refs: [string, string][] = [
    ["/workspace/x/", "a trailing slash"],
    ["/workspace//x", "an interior double slash"],
    ["workspace/x", "no leading slash"],
  ];

  for (const [ref, why] of refs) {
    it(`agrees on route, label and loaded folder for ${why}`, async () => {
      register();
      const spec = materializeTab(subject({ kind: "files", ref }));
      expect(spec.route).toEqual({ kind: "files", path: "/workspace/x" });
      expect(spec.name).toBe("x");
      spec.refresh();
      await settle();
      expect(showFilesTab).toHaveBeenCalledWith("/workspace/x");
    });
  }

  it("releases the canonical folder a non-canonical ref opened", async () => {
    register();
    materializeTab(subject({ kind: "files", ref: "/workspace/x/" })).onClose?.();
    await settle();
    expect(releaseFilesTab).toHaveBeenCalledWith("/workspace/x");
  });
});
