// The /docs metadata filter. A FILTER, not a search: everything it matches on is
// in memory, so there is no request and no debounce worth waiting for; the one
// coverage fact it carries is the inventory's own cap, which the reply states.
// What it can REACH is every string a row renders (the census below types each
// one back into the box) and never a document's BODY, which is the file
// browser's recursive grep one view away.
//
// A separate file from docs.test.ts on purpose: `initDocsView` is guarded by a
// module `inited` flag, so a second block in that file would open the page after
// the first had already claimed the flag and would then be asserting against a
// filter that was never built. A fresh module graph is the honest fixture.
import { describe, it, expect, vi, beforeAll, afterEach } from "vitest";
import type { PageFind } from "./find-registry.js";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn(), openFileDiff: undefined }));
vi.mock("./tabs.js", () => ({
  setDocsTab: vi.fn(),
  // No onShow argument any more — the tab factory reaches `showDocsTab` through its
  // own lazy import. This suite drives the page directly, so the toggle only has to
  // resolve.
  toggleDocsView: vi.fn(() => Promise.resolve()),
}));
// `onBus` as well as `onSSE`: the Workflows tab hands its panel to recipes.ts,
// which subscribes to the run bus, and one of the cases below switches to it.
vi.mock("./bus.js", () => ({
  onSSE: vi.fn(() => () => undefined),
  onBus: vi.fn(() => () => undefined),
  BUS_RUNS_CHANGED: "runs:changed",
}));
vi.mock("./git-status-store.js", () => ({
  initGitStatusStore: vi.fn(),
  onGitStatusChange: vi.fn(() => () => undefined),
  statusFor: vi.fn(() => ""),
}));
vi.mock("./actions/hooks.js", () => ({ setHookEnabled: { dispatch: vi.fn() } }));
vi.mock("./recipes.js", () => ({
  // The Workflows panel owns its own rows, so the page hands it the filter and
  // the panel reports back through the listener. Both halves are stubbed here so
  // the assertions below can watch the handoff.
  renderRecipesPanel: vi.fn((c: HTMLElement, filter?: string) => {
    c.replaceChildren();
    recipeFilter = filter ?? "";
  }),
  setRecipeCountsListener: vi.fn((fn: (c: { total: number; shown: number }) => void) => {
    reportCounts = fn;
  }),
}));

/** The filter the Workflows panel was last rendered with. */
let recipeFilter = "";
/** The panel's way back to the page's note. */
let reportCounts: ((c: { total: number; shown: number }) => void) | null = null;

type DocRecord = Record<string, unknown>;
type HookRecord = Record<string, unknown>;

let filterInput: HTMLInputElement;
let setDocs: (d: DocRecord[]) => void;
let setHooks: (h: HookRecord[]) => void;
let render: () => void;
let refresh: () => void;
let forceTab: (t: string) => void;
let find: PageFind;

/** The page's two GETs off the one `apiGetTyped` mock: the inventory through the
 *  caller's own decoder, the hook list empty. */
function serveDocsPage(
  inventory: unknown = { docs: [], truncated: false },
): (path: string, decode: (v: unknown) => unknown) => Promise<unknown> {
  return (path, decode) =>
    Promise.resolve(path === "/api/workspace/kiro-docs" ? decode(inventory) : { hooks: [] });
}

beforeAll(async () => {
  document.body.innerHTML = `
    <div id="docs-view">
      <nav id="docs-tab-bar">
        <button type="button" data-docs-tab="steering"></button>
        <button type="button" data-docs-tab="skills"></button>
        <button type="button" data-docs-tab="agents"></button>
        <button type="button" data-docs-tab="specs"></button>
        <button type="button" data-docs-tab="hooks"></button>
        <button type="button" data-docs-tab="workflows"></button>
      </nav>
      <div data-docs-panel="steering" class="list-container docs-panel"></div>
      <div data-docs-panel="skills" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="agents" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="specs" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="hooks" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="workflows" class="list-container docs-panel hidden"></div>
    </div>`;
  const { apiGetTyped } = await import("./api-client.js");
  vi.mocked(apiGetTyped).mockImplementation(serveDocsPage() as typeof apiGetTyped);

  const mod = await import("./docs.js");
  // The page's own doors, not `tabs.ts`'s `toggleDocsView`: that one toggles the TAB,
  // which is a round trip and registers no find.
  mod.showDocsTab();
  mod.forceDocsTab("steering");
  mod.refreshDocsView();
  setDocs = mod._setDocsForTest as unknown as (d: DocRecord[]) => void;
  setHooks = mod._setHooksForTest as unknown as (h: HookRecord[]) => void;
  render = mod._renderActiveForTest;
  refresh = mod.refreshDocsView;
  forceTab = mod.forceDocsTab as unknown as (t: string) => void;
  // The page hands its find to the leaf registry rather than exporting a focuser,
  // so this is how the box is reached — the same door Ctrl-F and the toolbar
  // magnifier use.
  const { pageFind } = await import("./find-registry.js");
  const registered = pageFind("docs");
  if (registered === undefined) {
    throw new Error("the docs page registered no find");
  }
  find = registered;
  // A POPUP: nothing is built until it is opened, so the field does not exist yet.
  find.open();
  filterInput = document.getElementById("docs-filter-input") as HTMLInputElement;
});

// A case that fails mid-way leaves its query armed and its tab selected, and the
// next case would then read a filtered panel or the wrong one; one failure should
// name one case.
afterEach(() => {
  type("");
  forceTab("steering");
  render();
});

function panel(name: string): HTMLElement {
  return document.querySelector(`[data-docs-panel="${name}"]`) as HTMLElement;
}

function names(name = "steering"): string[] {
  return [...panel(name).querySelectorAll(".list-row-name")].map((e) => e.textContent ?? "");
}

/** Type and apply. Enter rather than the debounce: the query is synchronous (the
 *  inventory is already here), so the shell renders in this same tick. */
function type(value: string): void {
  filterInput.value = value;
  filterInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
}

const steering = (over: DocRecord = {}): DocRecord => ({
  category: "steering",
  name: "alpha",
  path: "workspace/.kiro/steering/alpha.md",
  ...over,
});

describe("the box itself", () => {
  it("is a role=search landmark, and the same popup the transcript uses", () => {
    const region = document.getElementById("docs-filter");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Filter documents");
    // The shared skin plus the primitive's hook: one control, one position, on
    // every page that has a search box.
    expect(region?.className).toContain("page-find");
    expect(region?.className).toContain("search-pop");
    expect(region?.className).toContain("uip-popup");
  });

  it("carries the shared field attributes rather than its own spelling of them", () => {
    expect(filterInput.getAttribute("autocomplete")).toBe("off");
    expect(filterInput.getAttribute("autocapitalize")).toBe("off");
    expect(filterInput.getAttribute("spellcheck")).toBe("false");
    expect(filterInput.getAttribute("enterkeyhint")).toBe("search");
    // NOT type=search: the platform's own clear affordance belongs on a permanent
    // box, and this one carries its own ×. Two clear controls a thumb-width apart
    // doing different things is worse than one.
    expect(filterInput.type).toBe("text");
  });

  it("has NO match-case toggle, because every filter in the app folds both sides", () => {
    // The query AND the row it is matched against, so a toggle would be wired to
    // nothing.
    expect(document.querySelector('#docs-filter [aria-label="Match case"]')).toBeNull();
  });

  it("has a × now, because there is something to close, and it says FILTER", () => {
    expect(document.querySelector('#docs-filter [aria-label="Close filter"]')).not.toBeNull();
  });

  it("carries the FUNNEL, because it only narrows rows already here", () => {
    // The magnifier is for a box that reaches past the page — History's, which
    // reads every chat file on disk. This one cannot see a document's body.
    expect(document.querySelector("#docs-filter .page-find-icon polygon")).not.toBeNull();
    expect(document.querySelector("#docs-filter .page-find-icon circle")).toBeNull();
  });
});

describe("what it matches", () => {
  it("narrows by name, case-insensitively", () => {
    setDocs([steering({ name: "Alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    expect(names()).toEqual(["Alpha", "beta"]);
    type("ALP");
    expect(names()).toEqual(["Alpha"]);
    type("");
  });

  it("reaches a description, a path and an inclusion mode, not only the name", () => {
    setDocs([
      steering({ name: "one", description: "about redis caching", path: "one.md" }),
      steering({ name: "two", path: "workspace/.kiro/steering/deploy-notes.md" }),
      steering({ name: "three", inclusion: "fileMatch", path: "three.md" }),
    ]);
    render();
    type("redis");
    expect(names()).toEqual(["one"]);
    type("deploy-notes");
    expect(names()).toEqual(["two"]);
    type("filematch");
    expect(names()).toEqual(["three"]);
    type("");
  });

  it("reaches an agent's model and its tool names", () => {
    forceTab("agents");
    setDocs([
      {
        category: "agent",
        name: "reviewer",
        path: "r.md",
        model: "sonnet-4",
        tools: ["grepSearch", "readFile"],
      },
      {
        category: "agent",
        name: "planner",
        path: "p.md",
        model: "haiku",
        tools: ["listDirectory"],
      },
    ]);
    render();
    expect(names("agents")).toEqual(["reviewer", "planner"]);
    type("sonnet");
    expect(names("agents")).toEqual(["reviewer"]);
    type("listdirectory");
    expect(names("agents")).toEqual(["planner"]);
    type("");
    forceTab("steering");
    render();
  });

  it("cannot reach a document's BODY, which is the bound worth stating", () => {
    // The inventory carries front-matter and nothing else; searching bodies is the
    // file browser's recursive grep. A filter that appeared to search text it never
    // reads would be the silent miss chat-search.ts exists to prevent.
    setDocs([steering({ name: "alpha", description: "short summary" })]);
    render();
    type("some sentence deep inside the file");
    expect(names()).toEqual([]);
    type("");
  });
});

describe("what it says", () => {
  it("reports how much of the tab it is showing, and stays silent with no filter", () => {
    setDocs([steering({ name: "a", path: "a.md" }), steering({ name: "b", path: "b.md" })]);
    render();
    const note = document.getElementById("docs-filter-note");
    type("");
    expect(note?.textContent, "a count restating the whole list is noise").toBe("");
    type("a");
    expect(note?.textContent).toBe("1 document; 2 documents scanned");
    type("");
  });

  it("says NO MATCHES rather than the category's empty text", () => {
    // "No steering docs in .kiro/steering/." is a lie when the docs are one
    // keystroke away. git-changes-tab.ts draws the same distinction.
    setDocs([steering({ name: "alpha" })]);
    render();
    type("zzzz");
    expect(panel("steering").textContent).toContain("No documents match the filter");
    expect(panel("steering").textContent).not.toContain(".kiro/steering/");
    expect(document.getElementById("docs-filter-note")?.textContent).toBe("No matches");
    type("");
    expect(names()).toEqual(["alpha"]);
  });

  it("says where a match went when this tab has none", () => {
    // The box sits in a page-level toolbar over six tabs, so "no matches" on the
    // Steering tab reads as "nowhere" while the agent named zebra is one tab over.
    setDocs([
      steering({ name: "alpha" }),
      { category: "agent", name: "zebra", path: "workspace/.kiro/agents/zebra.md" },
    ]);
    render();
    type("zebra");
    expect(names()).toEqual([]);
    expect(document.getElementById("docs-filter-note")?.textContent).toBe("0 here, 1 on Agents");
    type("");
  });

  it("names every tab holding a match, with the whole count", () => {
    setDocs([
      steering({ name: "alpha" }),
      { category: "agent", name: "zebra", path: "workspace/.kiro/agents/zebra.md" },
      { category: "agent", name: "zebra-two", path: "workspace/.kiro/agents/zebra-two.md" },
      { category: "spec", name: "Zebra plan", path: "workspace/.kiro/specs/zebra/design.md" },
    ]);
    render();
    type("zebra");
    expect(document.getElementById("docs-filter-note")?.textContent).toBe(
      "0 here, 3 on Agents and Specs",
    );
    type("");
  });

  it("counts a synthesized global hook among the matches elsewhere", () => {
    // The Hooks tab's rows are not a pure projection of the inventory, so the
    // elsewhere scan has to read the tab's own row set rather than the category.
    setHooks([
      {
        id: "g1",
        name: "quokka",
        enabled: true,
        scope: "global",
        file_path: "~/.kiro/hooks/quokka.json",
        trigger: "SessionStart",
      },
    ]);
    setDocs([steering({ name: "alpha" })]);
    render();
    type("quokka");
    expect(document.getElementById("docs-filter-note")?.textContent).toBe("0 here, 1 on Hooks");
    type("");
    setHooks([]);
  });

  it("keeps the category's own empty text when nothing is filtered", () => {
    setDocs([]);
    render();
    type("");
    expect(panel("steering").textContent).toContain("No steering docs in .kiro/steering/.");
  });
});

describe("Workflows, the tab that used to be excluded", () => {
  // The box was HIDDEN there and Ctrl-F declined, on the reasoning that the tab is
  // RPC-sourced and escapes to recipes.ts before any docs logic runs. True about
  // where the rows come from, and not the same claim as "nothing to filter" — a
  // recipe has a name, a description, a source and declared inputs. The filter
  // reaches the panel now instead of hiding from it.
  it("hands its filter through to the panel that owns those rows", () => {
    forceTab("workflows");
    type("goal");
    expect(recipeFilter).toBe("goal");
    type("");
    expect(recipeFilter).toBe("");
    forceTab("steering");
    render();
  });

  it("accepts Ctrl-F there, and the box is the same one", () => {
    forceTab("workflows");
    render();
    expect(find.open()).toBe(true);
    expect(document.activeElement).toBe(filterInput);
    forceTab("steering");
    render();
  });

  it("takes the panel's own counts for the note, so it reads the same on six tabs", () => {
    forceTab("workflows");
    type("goal");
    reportCounts?.({ total: 9, shown: 2 });
    expect(document.getElementById("docs-filter-note")?.textContent).toBe(
      "2 documents; 9 documents scanned",
    );
    type("");
    forceTab("steering");
    render();
  });

  it("lets a count that lands after the reader left the tab stamp nothing", () => {
    // The panel's refetch answers long after the first paint, by which time the
    // reader may be on Steering reading its own note.
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    forceTab("workflows");
    type("a");
    forceTab("steering");
    render();
    expect(document.getElementById("docs-filter-note")?.textContent).toBe(
      "2 documents; 2 documents scanned",
    );
    reportCounts?.({ total: 9, shown: 2 });
    expect(document.getElementById("docs-filter-note")?.textContent).toBe(
      "2 documents; 2 documents scanned",
    );
    type("");
  });

  it("says where a match went when the Workflows tab has none", () => {
    // The elsewhere scan is the page's, so the RPC-sourced tab gets the same
    // sentence over the five inventory tabs.
    setDocs([{ category: "agent", name: "zebra", path: "workspace/.kiro/agents/zebra.md" }]);
    forceTab("workflows");
    type("zebra");
    reportCounts?.({ total: 9, shown: 0 });
    expect(document.getElementById("docs-filter-note")?.textContent).toBe("0 here, 1 on Agents");
    type("");
    forceTab("steering");
    render();
  });
});

describe("dismissal", () => {
  it("closes on Escape, and the CLOSE is what lifts the filter", async () => {
    // The rule a hidden box needs and a permanent one did not: a popup that closed
    // holding `alp` would leave the page showing one of two rows with nothing on
    // screen saying why, and the way back would be a box the reader has no reason
    // to think is still armed.
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    find.open();
    type("alp");
    expect(names()).toEqual(["alpha"]);
    filterInput.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    // The popup's leave lifecycle hides the panel on a transitionend (or its
    // 400ms fallback), and focus only leaves the field once it does — a real
    // browser does not move focus on the same tick the key was handled.
    await vi.waitFor(() => {
      expect(find.focused()).toBe(false);
    });
    expect(filterInput.value).toBe("");
    expect(names()).toEqual(["alpha", "beta"]);
  });

  it("lifts the filter on the toolbar button's second click too", () => {
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    find.open();
    type("alp");
    expect(names()).toEqual(["alpha"]);
    find.toggle();
    expect(names()).toEqual(["alpha", "beta"]);
  });
});

describe("a cut inventory", () => {
  // The server caps the scan per category and in total and says so on the reply.
  // A filter over a cut list is filtering less than the page implies, so the note
  // carries the fact — beside the rows, with or without a filter, and in the
  // empty answer, where "no matches" would otherwise mean "nowhere".
  function note(): string {
    return document.getElementById("docs-filter-note")?.textContent ?? "";
  }

  /** Re-arm the inventory GET and refetch through the page's own load path, so the
   *  flag reaches the note the way a live reply's does. */
  async function serve(docs: DocRecord[], truncated: boolean): Promise<void> {
    const { apiGetTyped } = await import("./api-client.js");
    vi.mocked(apiGetTyped).mockImplementation(
      serveDocsPage({ docs, truncated }) as typeof apiGetTyped,
    );
    // Emptied first, so the rows the wait sees are the reply's and not the
    // previous case's.
    setDocs([]);
    render();
    find.open();
    refresh();
    const onSteering = docs.filter((d) => d["category"] === "steering").map((d) => d["name"]);
    await vi.waitFor(() => {
      expect(names()).toEqual(onSteering);
    });
  }

  it("states the cut beside the rows, with and without a filter", async () => {
    await serve([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })], true);
    type("");
    expect(note()).toBe("2 documents; 2 documents scanned, not everything was read");
    type("alp");
    expect(note()).toBe("1 document; 2 documents scanned, not everything was read");
    type("");
  });

  it("says the search was partial when nothing matched anywhere", async () => {
    // The count is the whole inventory the query was checked against, every tab,
    // not the rows of the tab on screen.
    await serve(
      [
        steering({ name: "alpha" }),
        steering({ name: "beta", path: "b.md" }),
        { category: "agent", name: "gamma", path: "workspace/.kiro/agents/gamma.md" },
      ],
      true,
    );
    type("zzzz");
    expect(note()).toBe("No matches in 3 documents; not everything was searched");
    type("");
  });

  it("still names the tab holding a match, cut or not", async () => {
    // A match somewhere outranks the partial read: the reader has a row to go to.
    await serve(
      [
        steering({ name: "alpha" }),
        { category: "agent", name: "zebra", path: "workspace/.kiro/agents/zebra.md" },
      ],
      true,
    );
    type("zebra");
    expect(note()).toBe("0 here, 1 on Agents");
    type("");
  });

  it("leaves the Workflows tab's note alone, whose rows the cut never reached", async () => {
    await serve([steering({ name: "alpha" })], true);
    forceTab("workflows");
    type("goal");
    reportCounts?.({ total: 9, shown: 2 });
    expect(note()).toBe("2 documents; 9 documents scanned");
    type("");
    forceTab("steering");
    render();
  });

  it("goes quiet again once a whole inventory lands", async () => {
    await serve([steering({ name: "alpha" })], false);
    type("");
    expect(note()).toBe("");
    type("zzzz");
    expect(note()).toBe("No matches");
    type("");
  });
});

describe("the census: every string a row renders is matchable", () => {
  // One list feeds the render side and the haystack, and this walks every text
  // node the real page renders back into the real box, so a badge or chip added
  // without a haystack field fails here instead of going quietly unreachable.
  function renderedStrings(row: HTMLElement): string[] {
    const out: string[] = [];
    const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const text = node.textContent?.trim() ?? "";
      if (text !== "") {
        out.push(text);
      }
    }
    return out;
  }

  it("finds every rendered string of every row on every inventory tab", async () => {
    // The letter goes on the one row whose every other string is free of the
    // letter `m`, so the census reaches it through the letter alone rather than
    // through an `.md` in the path.
    const { statusFor } = await import("./git-status-store.js");
    vi.mocked(statusFor).mockImplementation(((_repo: string, rel: string) =>
      rel === "hooks/guard.json" ? "M" : "") as typeof statusFor);
    setDocs([
      steering({
        name: "alpha",
        description: "about redis caching",
        inclusion: "fileMatch",
        file_match: "actions/**",
      }),
      {
        category: "skill",
        name: "judgement",
        path: "workspace/.kiro/skills/judgement/SKILL.md",
        inclusion: "manual",
        steering_override: true,
      },
      {
        category: "agent",
        name: "reviewer",
        path: "workspace/.kiro/agents/reviewer.md",
        model: "sonnet-4",
        tools: ["grepSearch", "readFile"],
      },
      {
        category: "spec",
        name: "Search design",
        path: "workspace/.kiro/specs/search/design.md",
        group: "search",
      },
      {
        category: "hook",
        name: "guard",
        path: "workspace/.kiro/hooks/guard.json",
        group: "guard.json",
        trigger: "PreToolUse",
        action: "python3 scripts/scan.py",
      },
      {
        category: "hook",
        name: "alias",
        path: "workspace/.kiro/hooks/alias.json",
        group: "alias.json",
        trigger: "SessionStart",
        action: "echo hi",
        delete_protected: true,
      },
    ]);
    setHooks([
      {
        id: "h-guard",
        name: "guard",
        enabled: false,
        scope: "workspace",
        file_path: ".kiro/hooks/guard.json",
        trigger: "PreToolUse",
        command: "python3 scripts/scan.py",
        matcher: "fsWrite|executeBash",
        matcher_warning: "missing_tool_matcher",
        disabled_reason: "no such file",
      },
      {
        id: "h-global",
        name: "quokka",
        enabled: true,
        scope: "global",
        file_path: "~/.kiro/hooks/quokka.json",
        trigger: "SessionStart",
        command: "say hi",
      },
    ]);

    const seen = new Set<string>();
    for (const tab of ["steering", "skills", "agents", "specs", "hooks"]) {
      forceTab(tab);
      render();
      for (const row of panel(tab).querySelectorAll<HTMLElement>(".docs-row")) {
        const name = row.querySelector(".list-row-name")?.textContent ?? "";
        for (const text of renderedStrings(row)) {
          seen.add(text);
          type(text);
          expect(names(tab), `typing "${text}" on ${tab} should keep ${name}`).toContain(name);
        }
        type("");
      }
    }
    // The premise: the fixture renders strings only a badge or a chip carries, so
    // the loop above is a census and not a walk over names.
    for (const literal of ["M", "override", "2 tools", "fsWrite|executeBash", "every tool"]) {
      expect(seen).toContain(literal);
    }
    for (const literal of ["disabled", "link", "global"]) {
      expect(seen).toContain(literal);
    }

    setHooks([]);
    forceTab("steering");
    render();
  });
});
