// @vitest-environment happy-dom
// The /docs metadata filter (item 7's configuration-browser half).
//
// A FILTER, not a search: everything it matches on is already in memory, so there
// is no request, no truncation to report and no debounce worth waiting for. What
// it can REACH is the bound worth stating — a name, a description, a path, an
// inclusion mode, an agent's model and tool names, a hook's trigger — and never a
// document's BODY, which is the file browser's recursive grep one view away.
//
// A separate file from docs.test.ts on purpose: `initDocsView` is guarded by a
// module `inited` flag, so a second block in that file would open the page after
// the first had already claimed the flag and would then be asserting against a
// filter that was never built. A fresh module graph is the honest fixture.
import { describe, it, expect, vi, beforeAll } from "vitest";

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(), apiGetTyped: vi.fn() }));
vi.mock("./editor-openers.js", () => ({ openFile: vi.fn() }));
vi.mock("./tabs.js", () => ({
  setDocsTab: vi.fn(),
  toggleDocsView: vi.fn((_tab: string, onShow: () => void) => {
    onShow();
  }),
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
  renderRecipesPanel: vi.fn((c: HTMLElement) => {
    c.replaceChildren();
  }),
}));

type DocRecord = Record<string, unknown>;

let filterInput: HTMLInputElement;
let setDocs: (d: DocRecord[]) => void;
let render: () => void;
let forceTab: (t: string) => void;
let focusFilter: () => boolean;

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
      <select id="docs-tab-select"></select>
      <div id="docs-filter-host"></div>
      <div data-docs-panel="steering" class="list-container docs-panel"></div>
      <div data-docs-panel="skills" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="agents" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="specs" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="hooks" class="list-container docs-panel hidden"></div>
      <div data-docs-panel="workflows" class="list-container docs-panel hidden"></div>
    </div>`;
  const { apiGet, apiGetTyped } = await import("./api-client.js");
  vi.mocked(apiGet).mockResolvedValue({ docs: [] });
  vi.mocked(apiGetTyped).mockResolvedValue({ hooks: [] });

  const mod = await import("./docs.js");
  mod.showDocsView("steering");
  setDocs = mod._setDocsForTest as unknown as (d: DocRecord[]) => void;
  render = mod._renderActiveForTest;
  forceTab = mod.forceDocsTab as unknown as (t: string) => void;
  focusFilter = mod.focusDocsFilter;
  filterInput = document.getElementById("docs-filter-input") as HTMLInputElement;
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
  it("is a role=search landmark built into its host", () => {
    const region = document.getElementById("docs-filter");
    expect(region?.getAttribute("role")).toBe("search");
    expect(region?.getAttribute("aria-label")).toBe("Filter documents");
    expect(region?.parentElement?.id).toBe("docs-filter-host");
  });

  it("carries the shared field attributes rather than its own spelling of them", () => {
    expect(filterInput.getAttribute("autocomplete")).toBe("off");
    expect(filterInput.getAttribute("autocapitalize")).toBe("off");
    expect(filterInput.getAttribute("spellcheck")).toBe("false");
    expect(filterInput.getAttribute("enterkeyhint")).toBe("search");
    // A permanent box, so the platform's own clear affordance belongs on it.
    expect(filterInput.type).toBe("search");
  });

  it("has NO match-case toggle, matching the app's other three filters", () => {
    // #git-changes-filter, #git-prs-filter and the branch popover's all fold the
    // query and the row. A fourth that did not would be a knob one member of a set
    // of four carries, with nobody having asked for it.
    expect(document.querySelector('#docs-filter [aria-label="Match case"]')).toBeNull();
  });

  it("has no × either, because there is nothing to close", () => {
    expect(document.querySelector('#docs-filter [aria-label="Close find"]')).toBeNull();
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
    expect(note?.textContent).toBe("1 of 2 shown.");
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
    type("");
    expect(names()).toEqual(["alpha"]);
  });

  it("keeps the category's own empty text when nothing is filtered", () => {
    setDocs([]);
    render();
    type("");
    expect(panel("steering").textContent).toContain("No steering docs in .kiro/steering/.");
  });
});

describe("Workflows, the tab it cannot serve", () => {
  it("hides itself there, because a visible box would do nothing", () => {
    // That tab is RPC-sourced and escapes to recipes.ts before any docs logic
    // runs, so there is no inventory for a filter to narrow.
    const region = document.getElementById("docs-filter");
    forceTab("steering");
    render();
    expect(region?.classList.contains("hidden")).toBe(false);
    forceTab("workflows");
    render();
    expect(region?.classList.contains("hidden")).toBe(true);
    forceTab("steering");
    render();
    expect(region?.classList.contains("hidden")).toBe(false);
  });

  it("declines Ctrl-F there so the browser's own find still opens", () => {
    forceTab("workflows");
    render();
    expect(focusFilter()).toBe(false);
    forceTab("steering");
    render();
    expect(focusFilter()).toBe(true);
    expect(document.activeElement).toBe(filterInput);
  });
});

describe("dismissal", () => {
  it("clears back to the full list on Escape, because a permanent box has nothing to close", () => {
    setDocs([steering({ name: "alpha" }), steering({ name: "beta", path: "b.md" })]);
    render();
    type("alp");
    expect(names()).toEqual(["alpha"]);
    filterInput.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(filterInput.value).toBe("");
    expect(names()).toEqual(["alpha", "beta"]);
  });
});
