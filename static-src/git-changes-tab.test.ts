// The Changes tab's file list, which is where "what is staged" is answered.
//
// It used to be ONE flat list sorted staged-first, and the only thing marking a
// staged row was a 6% teal wash on its background: measured 1.09:1 against that
// background in dark and 1.03:1 in light, under the >= 1.25:1 floor
// 01-tokens.css states for a step on its own ramp and far under WCAG 1.4.11's
// 3:1 for a state boundary. The status cell read "Modified" on both sides of the
// index and the per-row Unstage button was hidden until hover, so at rest a
// staged row and an unstaged one were the same row. The reported symptom was
// that the panel had nowhere to view staged files at all.
//
// So this suite's subject is the GROUPING and its counts, and the counts are the
// half with a real bug behind them: entries are per side of the index, a person
// counts files, and the two differ on exactly the file that is staged and then
// edited again. That file made a destructive confirm offer to discard "2
// uncommitted changes".

import { describe, it, expect, vi, beforeEach } from "vitest";
import { loadCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import type * as ModChanges from "./git-changes-tab.js";
import type { GitFileEntry, GitRepoStatus } from "./git-types.js";

// Cache-buster for the re-imports below: `vi.resetModules()` does not
// re-evaluate a module in Browser Mode (the module map is URL-keyed), and this
// suite depends on fresh module state — `refreshGeneration` and the abort
// controller are module-level.
let bootSeq = 0;

const apiGet = vi.fn();
// Typed with the real signature so `mock.calls[0][0]` is the message rather
// than an index into an empty tuple.
const confirmDialog = vi.fn(
  async (_message: string, _label?: string, _variant?: "destructive" | "normal") => true,
);
const openChange = vi.fn();

const dispatches: { name: string; args: unknown }[] = [];

/** The two callbacks the tab hands to its filter popup. */
interface FilterSeam {
  query: (q: string, ctx: unknown) => unknown;
  render: (result: unknown, q: string) => void;
}

let filterSeam: FilterSeam | null = null;

/** Type into the filter box, exactly as the popup does: `query` records the
 *  text, `render` repaints. */
function applyFilter(q: string): void {
  if (filterSeam === null) {
    throw new Error("filter seam not captured — the module never built its popup");
  }
  const result = filterSeam.query(q, {});
  filterSeam.render(result, q);
}

/** A stand-in action that records what the panel asked for. Every git
 *  mutation resolves truthy, which is what `assertOk` requires. */
function recorder(name: string): { dispatch: (args: unknown) => Promise<unknown> } {
  return {
    dispatch: async (args: unknown) => {
      dispatches.push({ name, args });
      return { output: "" };
    },
  };
}

vi.mock("./api-client.js", () => ({ apiGet, apiPost: vi.fn() }));
vi.mock("./bus.js", () => ({ onSSE: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: confirmDialog }));
vi.mock("./navigate.js", () => ({ openChange }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
vi.mock("./actions/git-changes.js", () => ({
  stage: recorder("stage"),
  unstage: recorder("unstage"),
  discard: recorder("discard"),
  pull: recorder("pull"),
  push: recorder("push"),
  stash: recorder("stash"),
  stashPop: recorder("stashPop"),
  commit: recorder("commit"),
  generateCommitMessage: recorder("generateCommitMessage"),
}));
vi.mock("./search-popup.js", () => ({
  createSearchPopup: vi.fn((spec: unknown) => {
    // The filter is module state written by the popup's `query` callback and
    // published by its `render` callback. Capturing the spec lets a test drive
    // that exact seam instead of reaching into the module.
    filterSeam = spec as FilterSeam;
    return { open: vi.fn(), close: vi.fn(), toggle: vi.fn() };
  }),
}));
// Pass-through: the feedback wrapper's own ✓/✗ behaviour is not the subject, and
// a cancelled confirm throws through it by design.
vi.mock("./async-button.js", () => ({
  withAsyncFeedback: async (_b: HTMLElement, fn: () => Promise<unknown>) => {
    try {
      return await fn();
    } catch {
      return undefined;
    }
  },
}));
vi.mock("./git-scroll.js", () => ({
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));
// The real disclosure animates a height and owns aria-hidden/inert; the parts
// this suite reads are the trigger's aria-expanded and the body staying in the
// tree, so the stub supplies exactly those.
vi.mock("@cplieger/ui-primitives/disclosure", () => ({
  createDisclosure: (trigger: HTMLElement, _body: HTMLElement, opts: { open: boolean }) => {
    trigger.setAttribute("aria-expanded", String(opts.open));
    return { open: vi.fn(), close: vi.fn(), toggle: vi.fn() };
  },
}));
vi.mock("./chevron.js", () => ({
  chevronEl: () => document.createElement("span"),
}));

// --- Fixtures ---

function file(path: string, status: string, staged = false, orig?: string): GitFileEntry {
  const labels: Record<string, string> = {
    M: "Modified",
    A: "Added",
    D: "Deleted",
    R: "Renamed",
    C: "Copied",
    T: "Typechange",
    "?": "Untracked",
    U: "Unmerged",
  };
  const e: GitFileEntry = { path, status, staged, display: labels[status] ?? "Unknown" };
  if (orig !== undefined) {
    e.orig_path = orig;
  }
  return e;
}

function repo(files: GitFileEntry[], over: Partial<GitRepoStatus> = {}): GitRepoStatus {
  return {
    repo: "demo",
    is_repo: true,
    branch: "main",
    remote: "https://example.invalid/demo.git",
    ahead: 0,
    behind: 0,
    files,
    has_dirty: files.length > 0,
    stashes: 0,
    ...over,
  };
}

async function load(): Promise<typeof ModChanges> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./git-changes-tab.ts?boot=${String(bootSeq)}`
  )) as typeof ModChanges;
}

/** Fetch the given repos and paint. Returns the mount. */
async function paintRepos(repos: GitRepoStatus[]): Promise<HTMLElement> {
  apiGet.mockResolvedValue({ repos });
  const { refreshChanges } = await load();
  await refreshChanges();
  const mount = document.getElementById("git-changes-mount");
  if (mount === null) {
    throw new Error("mount missing");
  }
  return mount;
}

function group(mount: HTMLElement, kind: "staged" | "unstaged"): HTMLElement | null {
  return mount.querySelector<HTMLElement>(`.git-file-group-${kind}`);
}

function rowPaths(scope: HTMLElement | null): string[] {
  if (scope === null) {
    return [];
  }
  return [...scope.querySelectorAll(".git-file-path")].map((e) => e.textContent ?? "");
}

/** Click the button whose visible label matches, within a scope. */
async function clickBtn(scope: HTMLElement, label: string): Promise<void> {
  const btn = [...scope.querySelectorAll("button")].find((b) => b.textContent === label);
  if (btn === undefined) {
    throw new Error(
      `no button "${label}" in scope; saw: ${[...scope.querySelectorAll("button")]
        .map((b) => JSON.stringify(b.textContent))
        .join(", ")}`,
    );
  }
  btn.click();
  // Let the click handler's promise chain settle.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(() => {
  apiGet.mockReset();
  confirmDialog.mockReset();
  confirmDialog.mockResolvedValue(true);
  dispatches.length = 0;
  filterSeam = null;
  document.body.innerHTML = `<div id="git-view"><div id="git-changes-mount" class="git-multirepo-mount" aria-live="polite"></div></div>`;
});

describe("the file list is grouped by side of the index", () => {
  it("splits staged files from unstaged ones under their own headings", async () => {
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["a.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["b.ts", "c.ts"]);
    expect(group(mount, "staged")?.querySelector(".git-file-group-label")?.textContent).toBe(
      "Staged",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-label")?.textContent).toBe(
      "Changes",
    );
  });

  it("states each group's file count in its heading", async () => {
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(group(mount, "staged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "2 files",
    );
  });

  it("renders no empty second heading when everything is staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M", true)])]);

    expect(group(mount, "staged")).not.toBeNull();
    expect(group(mount, "unstaged")).toBeNull();
  });

  it("renders no staged heading when nothing is staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(group(mount, "staged")).toBeNull();
    expect(group(mount, "unstaged")).not.toBeNull();
  });

  it("no longer marks staged-ness with a class on the row", async () => {
    // The class existed only to hang the invisible tint on. Staged-ness is the
    // group heading now, so a row carrying it again would be a second, weaker
    // channel for the same fact.
    const mount = await paintRepos([repo([file("a.ts", "M", true)])]);

    expect(mount.querySelector(".git-file-row.staged")).toBeNull();
  });

  it("announces each group to a screen reader, with its size", async () => {
    // The heading is a sibling div, so nothing connects it to the list. A row's
    // own status cell reads "Status: Modified" on both sides of the index, so
    // without a label on the list the grouping is visual only and staged-ness
    // stays unannounced — the same defect this rework fixes for a sighted
    // reader, one reader over.
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M"), file("c.ts", "?")]),
    ]);

    expect(group(mount, "staged")?.querySelector("ul")?.getAttribute("aria-label")).toBe(
      "Staged, 1 file",
    );
    expect(group(mount, "unstaged")?.querySelector("ul")?.getAttribute("aria-label")).toBe(
      "Changes, 2 files",
    );
  });

  it("sorts within a group rather than across the whole list", async () => {
    const mount = await paintRepos([
      repo([file("z.ts", "M", true), file("a.ts", "M", true), file("m.ts", "M")]),
    ]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["a.ts", "z.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["m.ts"]);
  });
});

describe("a file staged and then changed again", () => {
  // git reports `MM p.ts`, which the server splits into one entry per side so
  // each can be staged or discarded independently. Grouped, that puts the same
  // filename in both groups — correct, and indistinguishable from a duplicate
  // unless the panel says so.
  const partial = () => repo([file("p.ts", "M", true), file("p.ts", "M"), file("q.ts", "M")]);

  it("appears in both groups", async () => {
    const mount = await paintRepos([partial()]);

    expect(rowPaths(group(mount, "staged"))).toEqual(["p.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["p.ts", "q.ts"]);
  });

  it("carries a partially-staged mark on BOTH of its rows", async () => {
    const mount = await paintRepos([partial()]);

    const marked = [...mount.querySelectorAll(".git-file-row")].filter(
      (r) => r.querySelector(".git-file-partial") !== null,
    );
    expect(marked).toHaveLength(2);
    for (const r of marked) {
      expect(r.querySelector(".git-file-path")?.textContent).toBe("p.ts");
    }
  });

  it("leaves an ordinary file unmarked", async () => {
    const mount = await paintRepos([partial()]);

    const q = [...mount.querySelectorAll(".git-file-row")].find(
      (r) => r.querySelector(".git-file-path")?.textContent === "q.ts",
    );
    expect(q?.querySelector(".git-file-partial")).toBeNull();
  });

  it("counts as ONE file in each group, not as two changes", async () => {
    // The bug this pins: entries are per side of the index, and a count a
    // person reads has to be per file.
    const mount = await paintRepos([partial()]);

    expect(group(mount, "staged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "2 files",
    );
  });
});

describe("each group owns the bulk action that acts on it", () => {
  it("offers Unstage all on the staged group, scoped to it", async () => {
    // A new capability: staging everything was one click and reversing it was
    // one click PER ROW, each hidden until that row was hovered.
    //
    // The fixture MIXES the two sides deliberately. With everything staged,
    // `files` and `r.files` are the same array and the test cannot tell a
    // group-scoped action from one that reaches the whole repo.
    const mount = await paintRepos([
      repo([file("a.ts", "M", true), file("b.ts", "M", true), file("c.ts", "?")]),
    ]);
    const head = group(mount, "staged")?.querySelector<HTMLElement>(".git-file-group-head");
    expect(head).not.toBeNull();

    await clickBtn(head as HTMLElement, "Unstage all");

    expect(dispatches).toEqual([
      { name: "unstage", args: { repo: "demo", files: ["a.ts", "b.ts"] } },
    ]);
  });

  it("offers Stage all and Discard all on the unstaged group", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M"), file("b.ts", "?")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    const labels = [...(head?.querySelectorAll("button") ?? [])].map((b) => b.textContent);
    expect(labels).toEqual(["Stage all", "Discard all"]);
  });

  it("keeps the repo action bar to sync operations", async () => {
    // Stage all and Discard all used to lead that bar, where their scope was
    // "the whole repo" and invisible.
    const mount = await paintRepos([repo([file("a.ts", "M"), file("b.ts", "M", true)])]);
    const bar = mount.querySelector<HTMLElement>(".git-repo-action-bar");

    const labels = [...(bar?.querySelectorAll("button") ?? [])].map((b) => b.textContent);
    expect(labels).not.toContain("Stage all");
    expect(labels).not.toContain("Discard all");
  });

  it("stages exactly the unstaged group, deduping a partially-staged path", async () => {
    const mount = await paintRepos([
      repo([file("p.ts", "M", true), file("p.ts", "M"), file("q.ts", "M")]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Stage all");

    expect(dispatches).toEqual([
      { name: "stage", args: { repo: "demo", files: ["p.ts", "q.ts"] } },
    ]);
  });
});

describe("Discard all", () => {
  it("discards the unstaged group only, and sends each path once", async () => {
    // It used to send `r.files.map(f => f.path)` — every entry, staged
    // included — so a partially-staged path went out twice.
    const mount = await paintRepos([
      repo([file("p.ts", "M", true), file("p.ts", "M"), file("s.ts", "A", true)]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(dispatches).toEqual([{ name: "discard", args: { repo: "demo", files: ["p.ts"] } }]);
  });

  it("names the unstaged file count in its confirm, not the entry count", async () => {
    const mount = await paintRepos([repo([file("p.ts", "M", true), file("p.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    const msg = String(confirmDialog.mock.calls[0]?.[0] ?? "");
    expect(msg).toContain("Discard 1 unstaged change in demo?");
    expect(msg).not.toContain("2 unstaged");
  });

  it("tells the reader the staged files survive it", async () => {
    // The scope has to be stated: a reader who expects a clean tree afterwards
    // is about to not get one, and the confirm is the last place to say so.
    const mount = await paintRepos([
      repo([file("a.ts", "M"), file("s.ts", "A", true), file("t.ts", "A", true)]),
    ]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(String(confirmDialog.mock.calls[0]?.[0] ?? "")).toContain(
      "Your 2 staged files stay untouched.",
    );
  });

  it("says nothing about staged files when none are staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(String(confirmDialog.mock.calls[0]?.[0] ?? "")).not.toContain("untouched");
  });

  it("dispatches nothing when the confirm is declined", async () => {
    confirmDialog.mockResolvedValue(false);
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const head = group(mount, "unstaged")?.querySelector<HTMLElement>(".git-file-group-head");

    await clickBtn(head as HTMLElement, "Discard all");

    expect(dispatches).toEqual([]);
  });
});

describe("the status cell", () => {
  it("is git's letter, carrying the shared per-letter colour class", async () => {
    // The app already had a `git-st-*` palette that the file browser emits and
    // this panel did not, so the same change read as a coloured letter in one
    // view and a grey word in the other.
    const mount = await paintRepos([repo([file("a.ts", "M")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("M");
    expect(cell?.className).toContain("git-st-m");
  });

  it("keeps the word as the accessible name and the tooltip", async () => {
    // Nothing is lost by dropping the word from the cell: it printed
    // "Untracked" beside "M", so every row's filename began at a different x.
    const mount = await paintRepos([repo([file("a.ts", "?")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("?");
    expect(cell?.getAttribute("aria-label")).toBe("Status: Untracked");
    expect(cell?.getAttribute("data-tooltip")).toBe("Untracked");
  });

  it("prefers the server's label over the local table", async () => {
    // The server owns the status vocabulary; a letter it grows before this
    // client does must still read as a word.
    const mount = await paintRepos([
      repo([{ path: "a.ts", status: "Z", staged: false, display: "Something New" }]),
    ]);

    expect(mount.querySelector(".git-file-status")?.getAttribute("aria-label")).toBe(
      "Status: Something New",
    );
  });

  it("gives a typechange its word rather than a bare T", async () => {
    // ` T`/`T ` is what git reports for a file swapped with a symlink. It used
    // to reach the label table on neither side and rendered as "Unknown".
    const mount = await paintRepos([repo([file("link.txt", "T")])]);
    const cell = mount.querySelector<HTMLElement>(".git-file-status");

    expect(cell?.textContent).toBe("T");
    expect(cell?.getAttribute("aria-label")).toBe("Status: Typechange");
    expect(cell?.className).toContain("git-st-t");
  });
});

describe("a renamed or copied file says where it came from", () => {
  it("shows the origin path beside the new one", async () => {
    // The server parsed this field out of porcelain's second NUL record and
    // threw it away, so a move rendered as "Renamed new.ts" with no way to see
    // what had moved — the one status whose meaning IS the pair of paths.
    const mount = await paintRepos([repo([file("new.ts", "R", true, "old.ts")])]);

    expect(mount.querySelector(".git-file-path")?.textContent).toBe("new.ts");
    expect(mount.querySelector(".git-file-orig")?.textContent).toBe("\u2190 old.ts");
    expect(mount.querySelector(".git-file-orig")?.getAttribute("data-tooltip")).toBe(
      "Renamed from old.ts",
    );
  });

  it("calls a copy a copy", async () => {
    const mount = await paintRepos([repo([file("dup.ts", "C", true, "src.ts")])]);

    expect(mount.querySelector(".git-file-orig")?.getAttribute("data-tooltip")).toBe(
      "Copied from src.ts",
    );
  });

  it("adds nothing to an ordinary change", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(mount.querySelector(".git-file-orig")).toBeNull();
  });
});

describe("the commit affordance", () => {
  it("names how many files it will commit", async () => {
    // The index IS the selection, so a bare "Commit" left the one control that
    // writes history saying nothing about what it was about to write.
    const mount = await paintRepos([repo([file("a.ts", "M", true), file("b.ts", "A", true)])]);

    const labels = [...mount.querySelectorAll(".git-commit-area button")].map((b) => b.textContent);
    expect(labels).toContain("Commit 2 files");
  });

  it("counts a partially-staged file once", async () => {
    const mount = await paintRepos([repo([file("p.ts", "M", true), file("p.ts", "M")])]);

    const labels = [...mount.querySelectorAll(".git-commit-area button")].map((b) => b.textContent);
    expect(labels).toContain("Commit 1 file");
  });

  it("does not exist with nothing staged", async () => {
    const mount = await paintRepos([repo([file("a.ts", "M")])]);

    expect(mount.querySelector(".git-commit-area")).toBeNull();
  });
});

describe("the shipped stylesheet, for the three facts the DOM cannot show", () => {
  // The test page links no app stylesheet, so `getComputedStyle` has no cascade
  // to report on. These read the sheet as source, the same way
  // search-centring.test.ts and tab-dot.test.ts do.
  const multirepo = loadCSS("22-git-multirepo.css");
  const tools = loadCSS("14-tools.css");

  it("carries no per-row staged tint at all", () => {
    // The tint was `color-mix(in srgb, var(--c-teal) 6%, var(--c-bg-primary))`
    // on `.git-file-row.staged > .git-file-row-top`: 1.09:1 against that
    // background in dark, 1.03:1 in light, so it was the whole of the staged
    // signal and it was invisible. Staged-ness is a heading now.
    expect(multirepo).not.toContain(".git-file-row.staged");
  });

  it("leaves hover as the one background a row row-top declares for a state", () => {
    // The deleted rule scored (0,4,0) against this one's (0,3,0), so the one row
    // in the list that could not respond to hover was the staged one — while
    // still being clickable.
    const hover = ruleContaining(multirepo, ".git-repo-section-body .git-file-row-top:hover");
    expect(hover.body).toContain("background: var(--c-bg-secondary)");
  });

  it("reveals a row's actions on keyboard focus as well as on hover", () => {
    // `.git-file-actions` sits at `opacity: 0`. With only `:hover` to lift it,
    // tabbing through a file list put the caret on Stage and Discard buttons
    // that could not be seen (WCAG 2.4.7), Discard being destructive.
    const reveal = ruleContaining(
      multirepo,
      ".git-repo-section-body .git-file-row:focus-within .git-file-actions",
    );
    expect(reveal.selector).toContain(":hover .git-file-actions");
    expect(reveal.body).toContain("opacity: 1");
  });

  it("gives the status cell a FIXED width so every filename starts at one x", () => {
    // It used to size to the status WORD with an 18px floor, and the words run
    // from "M" to "Untracked", so the column width varied per row and the list
    // had a ragged left edge exactly where a reader scans.
    const cell = ruleContaining(multirepo, ".git-file-status");
    expect(cell.body).toContain("width: 1rem");
    expect(cell.body).not.toContain("min-width");
    // No colour of its own: the per-letter class supplies it.
    expect(cell.body).not.toContain("color:");
  });

  it("has a colour for every status letter the server can emit", () => {
    // 'C' and 'T' had no rule, so a copy and a typechange were the two statuses
    // with no colour channel, inheriting the surrounding grey.
    for (const letter of ["m", "a", "d", "r", "c", "t", "u"]) {
      expect(tools, `.git-st-${letter} missing`).toContain(`.git-st-${letter}`);
    }
  });

  it("keeps no separator rule now that nothing emits one", () => {
    // `.action-bar-sep` existed for the boundary between the file-op cluster and
    // the sync cluster in the repo action bar. The file ops moved onto their
    // groups, so the bar holds one cluster and the separator has no emitter.
    expect(tools).not.toContain(".action-bar-sep");
  });
});

describe("the path filter", () => {
  it("keeps only the matching paths", async () => {
    const mount = await paintRepos([repo([file("src/a.ts", "M"), file("docs/b.md", "M")])]);
    expect(rowPaths(mount)).toEqual(["docs/b.md", "src/a.ts"]);

    applyFilter("docs/");

    expect(rowPaths(mount)).toEqual(["docs/b.md"]);
  });

  it("shows every file in a repo whose NAME matches", async () => {
    // The path filter used to run regardless of a repo-name match, so naming a
    // repo kept its section and then emptied it: a repo whose changed paths did
    // not happen to repeat the repo name rendered "No paths match the filter."
    // under its own heading, which is the opposite of what naming it asked for.
    const mount = await paintRepos([
      repo([file("src/a.ts", "M"), file("src/b.ts", "M")], { repo: "vibekit" }),
    ]);

    // Neither path contains "vibekit".
    applyFilter("vibekit");

    expect(rowPaths(mount)).toEqual(["src/a.ts", "src/b.ts"]);
  });

  it("says Clean only when the repo really is clean", async () => {
    // The one empty state left inside a section. A filter that hides everything
    // drops the section instead of claiming the repo is clean, so this sentence
    // can be trusted.
    const mount = await paintRepos([
      repo([], { repo: "quiet", has_dirty: false }),
      repo([file("a.ts", "M")], { repo: "busy" }),
    ]);

    expect(mount.querySelector('[data-repo="quiet"] .git-repo-row-clean')?.textContent).toBe(
      "Clean.",
    );
    expect(mount.querySelector('[data-repo="busy"] .git-repo-row-clean')).toBeNull();

    applyFilter("zzz-matches-nothing");

    // Neither repo survives, so neither is described as clean.
    expect(mount.querySelector(".git-repo-row-clean")).toBeNull();
    expect(mount.querySelector(".git-multirepo-empty-title")?.textContent).toBe(
      "No matching changes",
    );
  });

  it("drops a repo that matches on neither its name nor any path", async () => {
    const mount = await paintRepos([
      repo([file("src/a.ts", "M")], { repo: "one" }),
      repo([file("docs/b.md", "M")], { repo: "two" }),
    ]);

    applyFilter("docs/");

    expect(mount.querySelector('[data-repo="one"]')).toBeNull();
    expect(mount.querySelector('[data-repo="two"]')).not.toBeNull();
  });

  it("groups the filtered survivors, and counts only them", async () => {
    const mount = await paintRepos([
      repo([file("src/a.ts", "M", true), file("src/b.ts", "M"), file("docs/c.md", "M")]),
    ]);

    applyFilter("src/");

    expect(rowPaths(group(mount, "staged"))).toEqual(["src/a.ts"]);
    expect(rowPaths(group(mount, "unstaged"))).toEqual(["src/b.ts"]);
    expect(group(mount, "unstaged")?.querySelector(".git-file-group-count")?.textContent).toBe(
      "1 file",
    );
  });
});
