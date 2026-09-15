import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";

// Cache-buster for the re-imports below: `vi.resetModules()` does not
// re-evaluate a module in Browser Mode (the module map is URL-keyed), and this
// suite depends on fresh module state — `refreshGen` and the abort controller are
// module-level. Only the module under test is busted, so its dependencies keep
// their plain specifiers and stay interceptable by `vi.mock`.
let bootSeq = 0;

const apiGet = vi.fn();

vi.mock("./api-client.js", () => ({ apiGet, apiPost: vi.fn() }));
// The tab reads the forge list through the shared store now, not with an
// apiGet of its own. Stubbing the store here is what keeps the routing table
// below about the two legs that are still this module's: the per-forge repo
// listing and the per-repo PR listing.
const ensureForges = vi.fn();
vi.mock("./forge-store.js", () => ({ ensureForges }));
vi.mock("./bus.js", () => ({ onSSE: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn(async () => true) }));
vi.mock("./merge-dialog.js", () => ({ openMergeMethodDialog: vi.fn(async () => "rebase") }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
// The PR mutations reach the action framework; none of them fire here.
vi.mock("./actions/git-prs.js", () => {
  const stub = { dispatch: vi.fn(), cancel: vi.fn() };
  return {
    mergePR: stub,
    closePR: stub,
    createPR: stub,
    armAutoMerge: stub,
    reopenPR: stub,
    rerunChecks: stub,
    refreshPRs: stub,
  };
});
/** The two callbacks the tab hands to its filter popup. */
interface FilterSeam {
  note?: boolean;
  query: (q: string, ctx: unknown) => unknown;
  render: (result: unknown, q: string) => void;
}

let filterSeam: FilterSeam | null = null;
/** Every note the tab wrote; the last one is what the box shows. */
const notes: string[] = [];

/** Type into the filter box, exactly as the popup does: `query` records the
 *  text, `render` repaints. The popup trims a filter before this seam, so a test
 *  passes what a reader's keystrokes produce AFTER that rule. */
function applyFilter(q: string): void {
  if (filterSeam === null) {
    throw new Error("filter seam not captured — the module never built its popup");
  }
  const result = filterSeam.query(q, {});
  filterSeam.render(result, q);
}

vi.mock("./search-popup.js", () => ({
  createSearchPopup: vi.fn((spec: unknown) => {
    // The filter is module state written by the popup's `query` callback and
    // published by its `render` callback. Capturing the spec lets a test drive
    // that exact seam instead of reaching into the module.
    filterSeam = spec as FilterSeam;
    return {
      open: vi.fn(),
      close: vi.fn(),
      toggle: vi.fn(),
      shell: {
        setNote: (text: string) => {
          notes.push(text);
        },
      },
    };
  }),
}));
vi.mock("@cplieger/ui-primitives/dialog", () => ({
  createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })),
}));
// The scroll preserver is a pass-through here; its own behaviour is not the
// subject and it reads layout this suite does not stage.
vi.mock("./git-scroll.js", () => ({
  preserveGitScroll: (fn: () => void) => {
    fn();
  },
}));

const forge = {
  id: "github:github.com",
  kind: "github" as const,
  host: "github.com",
  connected: true,
};

const repos = [
  { owner: "cplieger", name: "one", full_name: "cplieger/one" },
  { owner: "cplieger", name: "two", full_name: "cplieger/two" },
  { owner: "cplieger", name: "three", full_name: "cplieger/three" },
];

/** Resolvers for the per-repo PR requests, in call order. */
let prResolvers: ((v: unknown) => void)[] = [];

/** Route each URL the fan-out fetches. The per-repo leg is deferred so the test
 *  can inspect the mount while the fan-out is still in flight. */
function routeAPI(opts: { forgesNull?: boolean } = {}): void {
  ensureForges.mockImplementation(() =>
    Promise.resolve(opts.forgesNull === true ? null : { forges: [forge], kinds: ["github"] }),
  );
  apiGet.mockImplementation((url: string) => {
    if (url.includes("/repos?") || url.endsWith("/repos")) {
      return Promise.resolve({ repos });
    }
    return new Promise((resolve) => {
      prResolvers.push(resolve);
    });
  });
}

async function load(): Promise<typeof ModPRs> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./git-prs-tab.ts?boot=${String(bootSeq)}`
  )) as typeof ModPRs;
}

function mount(): HTMLElement {
  const el = document.getElementById("git-prs-mount");
  if (el === null) {
    throw new Error("mount missing");
  }
  return el;
}

beforeEach(async () => {
  vi.useFakeTimers();
  apiGet.mockReset();
  ensureForges.mockReset();
  prResolvers = [];
  filterSeam = null;
  notes.length = 0;
  document.body.innerHTML = `<div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>`;
  const { setPRGroups } = await import("./git-prs-state.js");
  setPRGroups([]);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("PRs tab loading state", () => {
  it("paints a skeleton with a repo count while the fan-out is in flight", async () => {
    routeAPI();
    const { refreshPRs } = await load();
    const done = refreshPRs();

    // The show delay is 150ms, so nothing is painted before it elapses.
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    await vi.advanceTimersByTimeAsync(150);

    const skel = mount().querySelector(".git-repo-skeleton");
    expect(skel).not.toBeNull();
    // aria-hidden: the mount is aria-live, so placeholders must not be announced.
    expect(skel?.getAttribute("aria-hidden")).toBe("true");
    expect(skel?.querySelectorAll(".skeleton").length).toBeGreaterThan(0);
    expect(mount().querySelector(".git-repo-skel-label")?.textContent).toContain(
      "0 of 3 repositories",
    );

    // A repo landing moves the count — that is what separates a slow refresh
    // from a wedged one.
    prResolvers[0]?.({ prs: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(mount().querySelector(".git-repo-skel-label")?.textContent).toContain(
      "1 of 3 repositories",
    );

    for (const resolve of prResolvers.slice(1)) {
      resolve({ prs: [] });
    }
    await done;
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
  });

  it("skips the skeleton when the mount already holds keyed rows", async () => {
    routeAPI();
    const row = document.createElement("section");
    row.setAttribute("data-reconcile-key", "cplieger/one");
    mount().appendChild(row);

    const { refreshPRs } = await load();
    void refreshPRs();
    await vi.advanceTimersByTimeAsync(150);

    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    expect(mount().querySelector("[data-reconcile-key]")).not.toBeNull();
  });

  it("arms nothing for a repo set with NO open PRs once the fan-out has answered", async () => {
    // No open PRs anywhere is an ANSWER, and the container cannot tell it from a set
    // this client has never read. A gap reaches this refresh with no tab switch behind
    // it, so without the answered flag it would shimmer over a settled pane.
    routeAPI();
    const { refreshPRs } = await load();
    const first = refreshPRs();
    // The repo leg has to settle before the per-repo resolvers exist to answer.
    await vi.advanceTimersByTimeAsync(150);
    for (const resolve of prResolvers.splice(0)) {
      resolve({ prs: [] });
    }
    await first;
    expect(mount().querySelector("[data-reconcile-key]")).toBeNull();

    const second = refreshPRs();
    await vi.advanceTimersByTimeAsync(150);
    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    for (const resolve of prResolvers.splice(0)) {
      resolve({ prs: [] });
    }
    await second;
  });

  it("hands the fan-out its label while the placeholder stands, and detaches it after", async () => {
    // The build CLOSURE is what gives the caller the element `paintPlaceholder`'s
    // `() => Element` signature cannot return, and the whole reason for it is that the
    // count has to keep moving into a node built 150ms into the flight.
    routeAPI();
    const { refreshPRs } = await load();
    const done = refreshPRs();
    await vi.advanceTimersByTimeAsync(150);
    const label = mount().querySelector(".git-repo-skel-label");
    expect(label?.textContent).toContain("0 of 3 repositories");

    prResolvers[0]?.({ prs: [] });
    await vi.advanceTimersByTimeAsync(0);
    // The pointer is live: a repo landing moves the count in the element the closure
    // captured, which is what separates a slow refresh from a wedged one.
    expect(label?.textContent).toContain("1 of 3 repositories");

    for (const resolve of prResolvers.slice(1)) {
      resolve({ prs: [] });
    }
    await done;

    expect(mount().querySelector(".git-repo-skeleton")).toBeNull();
    expect(label?.isConnected).toBe(false);
  });

  it("paints an error into the mount when the forge list cannot be read", async () => {
    routeAPI({ forgesNull: true });
    const { refreshPRs } = await load();

    await expect(refreshPRs()).rejects.toThrow(/forges/i);

    // The action's toast is transient, so a blank pane would be the only lasting
    // record of the failure.
    const err = mount().querySelector(".git-multirepo-error");
    expect(err).not.toBeNull();
    expect(err?.textContent).toContain("Couldn't load pull requests");
  });
});

describe("PRs tab row identity across paints", () => {
  /** Run one full refresh, answering every repo leg with the given PR lists. */
  async function paintOnce(
    refreshPRs: typeof ModPRs.refreshPRs,
    perRepo: Record<string, unknown>[][],
  ): Promise<void> {
    prResolvers = [];
    const done = refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    prResolvers.forEach((resolve, i) => {
      resolve({ prs: perRepo[i] ?? [] });
    });
    await done;
  }

  const onePR = (over: Record<string, unknown> = {}): Record<string, unknown>[] => [
    {
      number: 7,
      title: "a change",
      state: "open",
      source_branch: "feat",
      target_branch: "main",
      url: "https://example.test/pr/7",
      ...over,
    },
  ];

  it("paints each PR exactly once when the tab repaints", async () => {
    routeAPI();
    const { refreshPRs } = await load();

    // Arriving at the tab, then any second paint at all: pressing refresh, one
    // keystroke in the filter, a forges_changed frame, leaving and coming back.
    await paintOnce(refreshPRs, [onePR()]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);

    await paintOnce(refreshPRs, [onePR()]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);

    // A row reconcile cannot see, match or remove a row that carries no key,
    // so an unkeyed row is a row the next paint duplicates.
    for (const row of mount().querySelectorAll(".git-pr-row")) {
      expect(row.getAttribute("data-reconcile-key")).toBe("github:github.com:7");
    }
  });

  it("repaints a surviving row from the newer fetch", async () => {
    routeAPI();
    const { refreshPRs } = await load();

    // merge_blocked is per-fetch: the forge answers `unknown` while it is still
    // computing mergeability and "" once the PR is mergeable.
    // The selector locates Merge WITHOUT depending on the accent: item 17 took
    // `btn-primary` off this per-row button (the accent marks the one thing to do
    // on a surface, and a per-row count scales with the number of open PRs), so
    // `.git-pr-row .btn-primary` matched nothing. `:not(.btn-danger)` excludes
    // Close, and Merge is the first button appended to the actions row, so
    // `querySelector` answers it ahead of the conditional Merge-when-green and
    // Re-run siblings. Both `disabled` assertions are unchanged — the subject here
    // is still that a surviving row repaints from the newer fetch.
    await paintOnce(refreshPRs, [onePR({ merge_blocked: "unknown" })]);
    const blocked = mount().querySelector<HTMLButtonElement>(
      ".git-pr-row-actions .btn-small:not(.btn-danger)",
    );
    expect(blocked?.disabled).toBe(true);

    await paintOnce(refreshPRs, [onePR({ merge_blocked: "" })]);
    expect(mount().querySelectorAll(".git-pr-row")).toHaveLength(1);
    const live = mount().querySelector<HTMLButtonElement>(
      ".git-pr-row-actions .btn-small:not(.btn-danger)",
    );
    expect(live?.disabled).toBe(false);
  });
});

describe("PRs tab cache bypass", () => {
  it("asks the server for a live read only when the refresh is forced", async () => {
    routeAPI();
    const { refreshPRs } = await load();

    // Arriving at the tab: no refresh=1, so the server may answer both listings
    // from its cache and the visit costs no forge subprocess.
    const first = refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    for (const resolve of prResolvers) {
      resolve({ prs: [] });
    }
    await first;
    const arrival = apiGet.mock.calls.map((c: unknown[]) => String(c[0]));
    expect(arrival).not.toHaveLength(0);
    expect(arrival.some((u) => u.includes("refresh=1"))).toBe(false);

    apiGet.mockClear();
    prResolvers = [];

    // Pressing refresh: every leg carries it, or the button would report a
    // check verdict the cache is holding.
    const forced = refreshPRs(undefined, true);
    await vi.advanceTimersByTimeAsync(0);
    for (const resolve of prResolvers) {
      resolve({ prs: [] });
    }
    await forced;
    const pressed = apiGet.mock.calls.map((c: unknown[]) => String(c[0]));
    expect(pressed).not.toHaveLength(0);
    expect(pressed.every((u) => u.includes("refresh=1"))).toBe(true);
  });
});

describe("the PR filter", () => {
  const DAY_MS = 86_400_000;

  /** One full refresh over the three repos, answered with the given PR lists. */
  async function paintGroups(
    refreshPRs: typeof ModPRs.refreshPRs,
    perRepo: Record<string, unknown>[][],
  ): Promise<void> {
    prResolvers = [];
    const done = refreshPRs();
    await vi.advanceTimersByTimeAsync(0);
    prResolvers.forEach((resolve, i) => {
      resolve({ prs: perRepo[i] ?? [] });
    });
    await done;
  }

  function pr(over: Record<string, unknown>): Record<string, unknown> {
    return {
      number: 1,
      title: "a change",
      state: "open",
      source_branch: "feat",
      target_branch: "main",
      url: "https://example.test/pr/1",
      ...over,
    };
  }

  /** The row set on screen, by PR number, in DOM order. */
  function shownNumbers(): string[] {
    return [...mount().querySelectorAll(".git-pr-row-number")].map((e) => e.textContent ?? "");
  }

  /** Every non-blank text node under `row`, which is what a reader sees. */
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

  /** Six rows across two repos, between them rendering every string a row can:
   *  a draft tag, a failing chip with its Re-run, a running chip with its
   *  Merge-when-green, an armed auto-merge, a closed PR's Reopen, an author, an
   *  age, and both branches. */
  const fixture = (): Record<string, unknown>[][] => [
    [
      pr({
        number: 1234,
        title: "Tighten the upload policy",
        draft: true,
        author: "renovate-bot",
        updated_at: Date.now() - 3 * DAY_MS,
        source_branch: "feat/upload-policy",
        check_status: "failing",
        checks_total: 5,
        checks_failing: 3,
        merge_blocked: "checks_failing",
      }),
      pr({
        number: 1235,
        title: "Bump keyenc",
        author: "cplieger",
        check_status: "pending",
        checks_total: 2,
        merge_blocked: "checks_running",
      }),
      pr({
        number: 1236,
        title: "Armed already",
        check_status: "pending",
        auto_merge_armed: true,
        merge_blocked: "checks_running",
      }),
    ],
    [
      pr({ number: 9, title: "Green and quiet", check_status: "passing", checks_total: 1 }),
      pr({ number: 10, title: "Closed last week", state: "closed" }),
      pr({ number: 11, title: "Zebra crossing", source_branch: "fix/zebra", target_branch: "dev" }),
    ],
    [],
  ];

  it("reaches every string a row renders, and the census walks them all back in", async () => {
    // ONE list feeds both the row and the haystack, so a reader typing any string
    // they can see (the author, the number, a branch) keeps the row; this types
    // every text node of every row into the box and asserts the row survives.
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());
    const rows = [...mount().querySelectorAll<HTMLElement>(".git-pr-row")];
    expect(rows).toHaveLength(6);

    const seen = new Set<string>();
    for (const row of rows) {
      const num = row.querySelector(".git-pr-row-number")?.textContent ?? "";
      for (const text of renderedStrings(row)) {
        seen.add(text);
        applyFilter(text.toLowerCase());
        expect(shownNumbers(), `typing "${text}" should keep ${num}`).toContain(num);
      }
    }
    applyFilter("");
    // The premise: the fixture renders strings only a badge, an author or a branch
    // carries, so the loop above is a census and not a walk over titles.
    for (const literal of [
      "#1234",
      "draft",
      "3 failing",
      "checks running",
      "auto-merge",
      "by @renovate-bot · 3d ago · feat/upload-policy → main",
      "Merge when green",
      "Re-run",
      "Reopen",
      "Close",
    ]) {
      expect(seen).toContain(literal);
    }
  });

  it("matches a substring of the authorship line: an author, a branch, an age", async () => {
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());

    applyFilter("renovate");
    expect(shownNumbers()).toEqual(["#1234"]);
    applyFilter("fix/zebra");
    expect(shownNumbers()).toEqual(["#11"]);
    applyFilter("→ dev");
    expect(shownNumbers()).toEqual(["#11"]);
    applyFilter("3d ago");
    expect(shownNumbers()).toEqual(["#1234"]);
    applyFilter("1235");
    expect(shownNumbers()).toEqual(["#1235"]);
  });

  it("keeps every PR of a repo whose NAME matches", async () => {
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());

    applyFilter("cplieger/two");
    expect(shownNumbers()).toEqual(["#9", "#10", "#11"]);
  });

  it("states how many rows the filter kept, in the shared grammar", async () => {
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());
    // Silent with no filter: a count restating the list is noise.
    expect(notes.at(-1)).toBe("");

    applyFilter("check");
    // `checks running` ×2 (the armed row's chip too) plus `checks passed`; the
    // failing row's chip reads `3 failing` and does not carry the word.
    expect(notes.at(-1)).toBe("3 pull requests; 6 pull requests scanned");

    applyFilter("zebra");
    expect(notes.at(-1)).toBe("1 pull request; 6 pull requests scanned");
  });

  it("says No matches when the filter drops every row, and offers no advice", async () => {
    // Every rendered string is reachable, so a query that drops every row matched
    // nothing on screen, and the note says what was found rather than advising a
    // change the reader has no field to make.
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());

    applyFilter("zzz-matches-nothing");
    expect(notes.at(-1)).toBe("No matches");
    expect(mount().querySelector(".git-multirepo-empty-title")?.textContent).toBe(
      "No matching pull requests",
    );
    expect(mount().querySelector(".git-multirepo-empty-hint")).toBeNull();
    expect(shownNumbers()).toEqual([]);
  });

  it("opens a section the reader had collapsed when it holds a matching row", async () => {
    // `reconcile` keeps the section ELEMENT across paints and only the body was
    // repainted, so the mount's open-state decision was the only one a section
    // ever got: a filter typed after the reader collapsed a section kept its
    // selected row inside an aria-hidden, inert region. The open state is decided
    // again on every paint now, a filter outranks the reader's latch while it
    // stands, and the latch is read again once the box is empty.
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());
    const toggle = (): HTMLElement | null =>
      mount().querySelector<HTMLElement>(
        '[data-repo="cplieger/one"] .git-repo-section-header-toggle',
      );
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");

    toggle()?.click();
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");

    applyFilter("upload-policy");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
    expect(shownNumbers()).toEqual(["#1234"]);

    applyFilter("");
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");
  });

  it("keeps a section the reader collapsed closed across a refresh", async () => {
    // The re-decision must not undo the reader: a refresh with no filter reads
    // the latch, so a collapsed section stays collapsed however many times the
    // fan-out lands.
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, fixture());
    const toggle = mount().querySelector<HTMLElement>(
      '[data-repo="cplieger/one"] .git-repo-section-header-toggle',
    );
    toggle?.click();
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");

    await paintGroups(refreshPRs, fixture());
    expect(
      mount()
        .querySelector('[data-repo="cplieger/one"] .git-repo-section-header-toggle')
        ?.getAttribute("aria-expanded"),
    ).toBe("false");
  });

  it("re-decides a section's default when its PR count changes", async () => {
    // A repo that mounted with no PRs mounted closed, and stayed closed when a PR
    // arrived; the update path never asked again.
    routeAPI();
    const { refreshPRs } = await load();
    await paintGroups(refreshPRs, [[pr({ number: 1 })], [], []]);
    const toggle = (): HTMLElement | null =>
      mount().querySelector<HTMLElement>(
        '[data-repo="cplieger/two"] .git-repo-section-header-toggle',
      );
    expect(toggle()?.getAttribute("aria-expanded")).toBe("false");

    await paintGroups(refreshPRs, [[pr({ number: 1 })], [pr({ number: 2 })], []]);
    expect(toggle()?.getAttribute("aria-expanded")).toBe("true");
  });
});
