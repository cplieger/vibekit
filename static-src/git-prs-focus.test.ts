// ---------------------------------------------------------------------------
// The PRs tab's one-shot focus request: a notification naming a pull request
// lands on that pull request's row.
//
// A FILE of its own rather than a describe in git-prs-tab.test.ts, because the
// harness is materially different in the two ways this behaviour is about. That
// suite fakes the timers and stubs `preserveGitScroll` to a pass-through, and the
// scheduling ruling here is exactly a claim about REAL frames and about that
// module's own rAF scroll restore.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import type * as ModPRs from "./git-prs-tab.js";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
import { prIdentity } from "./push-subject.js";

/** Cache-buster for the re-imports below: `vi.resetModules()` does not
 *  re-evaluate a module in Browser Mode, and every case needs a fresh
 *  `pendingFocus` slot. */
let bootSeq = 0;

const apiGet = vi.fn();
const ensureForges = vi.fn();

vi.mock("./api-client.js", () => ({ apiGet, apiPost: vi.fn() }));
vi.mock("./forge-store.js", () => ({ ensureForges }));
vi.mock("./bus.js", () => ({ onSSE: vi.fn() }));
vi.mock("./confirm.js", () => ({ confirm: vi.fn(async () => true) }));
vi.mock("./merge-dialog.js", () => ({ openMergeMethodDialog: vi.fn(async () => "rebase") }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  bindLoadingState: vi.fn(() => vi.fn()),
}));
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
  createSearchPopup: vi.fn(() => ({
    open: vi.fn(),
    close: vi.fn(),
    toggle: vi.fn(),
    shell: { setNote: vi.fn() },
  })),
}));
vi.mock("@cplieger/ui-primitives/dialog", () => ({
  createDialog: vi.fn(() => ({ open: vi.fn(), close: vi.fn() })),
}));

const FORGE_ID = "github:github.com";

const forge = { id: FORGE_ID, kind: "github" as const, host: "github.com", connected: true };
const repos = [
  { owner: "cplieger", name: "one", full_name: "cplieger/one" },
  { owner: "cplieger", name: "two", full_name: "cplieger/two" },
];

function pr(number: number): Record<string, unknown> {
  return {
    number,
    title: `change ${String(number)}`,
    state: "open",
    source_branch: "feat",
    target_branch: "main",
    url: `https://example.test/pr/${String(number)}`,
  };
}

/** Answer the whole fan-out immediately: one PR per repo, numbered 1 and 2. */
function routeAPI(): void {
  ensureForges.mockImplementation(() =>
    Promise.resolve({ forges: [forge], kinds: ["github"] as const }),
  );
  apiGet.mockImplementation((url: string) => {
    if (url.includes("/repos?") || url.endsWith("/repos")) {
      return Promise.resolve({ repos });
    }
    return Promise.resolve({ prs: [pr(url.includes("/one/") ? 1 : 2)] });
  });
}

async function load(): Promise<typeof ModPRs> {
  bootSeq += 1;
  return (await import(
    /* @vite-ignore */ `./git-prs-tab.ts?focus=${String(bootSeq)}`
  )) as typeof ModPRs;
}

/** The identity a row of `repo` carries, built the way the tab builds it. */
function identityFor(repo: string, number: number): string {
  return prIdentity(FORGE_ID, `cplieger/${repo}`, number);
}

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => {
      requestAnimationFrame(() => {
        r(null);
      });
    });
  }
}

/** Poll `check` once a frame until it answers, bounded. Asserts on the way out
 *  either way, so a case whose whole subject is a poll still states one. */
async function until(check: () => boolean, what: string, max = 30): Promise<void> {
  for (let i = 0; i < max; i++) {
    if (check()) {
      break;
    }
    await frames(1);
  }
  expect(check(), what).toBe(true);
}

function mount(): HTMLElement {
  const el = document.getElementById("git-prs-mount");
  if (el === null) {
    throw new Error("mount missing");
  }
  return el;
}

function view(): HTMLElement {
  const el = document.getElementById("git-view");
  if (el === null) {
    throw new Error("view missing");
  }
  return el;
}

function toggleFor(repo: string): HTMLElement | null {
  return mount().querySelector<HTMLElement>(
    `[data-repo="cplieger/${repo}"] .git-repo-section-header-toggle`,
  );
}

function rowFor(identity: string): HTMLElement | null {
  return mount().querySelector<HTMLElement>(`[data-pr="${CSS.escape(identity)}"]`);
}

beforeEach(() => {
  apiGet.mockReset();
  ensureForges.mockReset();
  // The real `#git-view` scroll container, tall content above the mount, so a
  // scroll into view has somewhere to go and `preserveGitScroll` has a scrollTop
  // to save and restore.
  document.body.innerHTML = `
    <div id="git-view" style="position:fixed;top:0;left:0;inline-size:600px;block-size:200px;overflow-y:auto">
      <div style="block-size:600px"></div>
      <div id="git-prs-mount" class="git-multirepo-mount" aria-live="polite"></div>
      <div style="block-size:600px"></div>
    </div>`;
});

describe("requestPRFocus", { timeout: testTimeoutFor(framesBudgetMs(30)) }, () => {
  it("finds the row its identity names", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    const identity = identityFor("two", 2);
    // The PREMISE: the attribute exists and carries `prIdentity`'s spelling, which
    // is the tab's ONE DOM row identity.
    expect(rowFor(identity)).not.toBeNull();

    requestPRFocus(identity);
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the named row was marked",
    );
    // ONE row, and it is that one: nothing else in the pane is marked.
    expect(mount().querySelectorAll(".deep-link-flash")).toHaveLength(1);
  });

  it("compares case-insensitively, because the two sides come from two sources", async () => {
    // The subject's repo slug is parsed from the LOCAL origin URL while the tab's
    // `full_name` is the forge's own listing field, so a case difference is a real
    // arrival rather than a hypothetical.
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    const identity = identityFor("two", 2);
    requestPRFocus(identity.toUpperCase());
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the differently-cased identity found its row",
    );
  });

  it("force-opens a reader-collapsed section and leaves their collapse standing", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();

    toggleFor("two")?.click();
    expect(toggleFor("two")?.getAttribute("aria-expanded")).toBe("false");

    const identity = identityFor("two", 2);
    requestPRFocus(identity);
    // The force-open reaches the disclosure through the PAINT, which is why
    // `requestPRFocus` repaints rather than only scheduling a frame.
    await until(
      () => toggleFor("two")?.getAttribute("aria-expanded") === "true",
      "the section holding the request opened",
    );
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the row inside it was marked",
    );

    // And the request wrote no `readerToggled` entry, so the next paint returns the
    // section to where the reader left it.
    await refreshPRs();
    expect(toggleFor("two")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("scrolls a resident, painted tab with no refresh of its own", async () => {
    // The case a paint-driven-only reading loses: on a second notification the tab
    // is already open on prs with rows painted, so nothing else would paint.
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    ensureForges.mockClear();

    const identity = identityFor("one", 1);
    requestPRFocus(identity);
    await until(
      () => rowFor(identity)?.classList.contains("deep-link-flash") === true,
      "the resident row was marked",
    );
    expect(ensureForges).not.toHaveBeenCalled();
  });

  it("heals once on an unknown identity and then gives up silently", async () => {
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    ensureForges.mockClear();

    requestPRFocus(identityFor("three", 9));
    await until(() => ensureForges.mock.calls.length === 1, "one healing refresh went out");
    await frames(10);

    // Bounded to one refresh per request, and the reader is left on the PRs tab
    // with no selection — never an error and never a wrong row.
    expect(ensureForges).toHaveBeenCalledTimes(1);
    expect(mount().querySelectorAll(".deep-link-flash")).toHaveLength(0);
    expect(mount().querySelector(".git-multirepo-error")).toBeNull();
  });

  it("survives preserveGitScroll's own scroll restore", async () => {
    // `preserveGitScroll` saves `#git-view`'s scrollTop and restores it in its own
    // requestAnimationFrame, which is its last statement. The focus attempt is
    // registered AFTER that call returns, so it is second in the frame's list and
    // the restore runs first. Registered before it, the restore would undo the
    // scroll one frame later — and the assertion below is what fails then.
    routeAPI();
    const { refreshPRs, requestPRFocus } = await load();
    await refreshPRs();
    view().scrollTop = 0;

    requestPRFocus(identityFor("two", 2));
    await until(() => view().scrollTop > 0, "the scroll into view survived the restore");
  });
});
