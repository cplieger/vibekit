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
vi.mock("./search-popup.js", () => ({
  createSearchPopup: vi.fn(() => ({ open: vi.fn(), close: vi.fn(), toggle: vi.fn() })),
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
