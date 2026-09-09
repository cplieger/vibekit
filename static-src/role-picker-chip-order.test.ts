// THE SCOPE CHIP HOLDS THE ROW'S FINAL SLOT, so the word "workspace" sits on one
// x down the whole card and a reader can compare it without reading each row.
//
// It used to take `margin-inline-start: auto` and come LAST, so on a row that also
// carried a shadow badge the auto margin was spent by the scope chip and the badge
// took the final slot — moving the scope chip left by the badge's own width on
// exactly the rows that have one. The name grows instead, and the badge is built
// ahead of the scope chip.
//
// Driven through the real render path (seed the catalog, expand the pill) rather
// than by a hand-built row, because a fixture in production's own order cannot
// fail when production reorders.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

vi.mock("./chat.js", () => ({ createSession: vi.fn() }));
vi.mock("./actions/chat.js", () => ({ setMode: { dispatch: vi.fn() } }));
// Two workspace agents, one of which collides with the global entry seeded below,
// so the card renders a marked row and an unmarked one — the two the alignment is
// measured across.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async () => ({
    items: [
      { name: "reviewer", type: "agent" },
      { name: "only-here", type: "agent" },
    ],
  })),
}));
vi.mock("./store.js", () => ({
  getActive: vi.fn(() => undefined),
  activeSession: { value: undefined },
  // Present-but-inert so real-ESM linking succeeds; no case here calls them.
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _list: HTMLElement, opts: { onExpand: () => void }) => {
    expandList = opts.onExpand;
  },
  collapseAll: vi.fn(),
}));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn() }),
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./dom.js", () => ({
  $: {
    get rolePill() {
      return document.getElementById("role-pill") as HTMLElement;
    },
    get roleList() {
      return document.getElementById("role-list") as HTMLElement;
    },
  },
  byId: (id: string) => document.getElementById(id) as HTMLElement,
}));

let expandList: (() => void) | undefined;

const { initRolePicker } = await import("./role-picker.js");
const { setCatalogModes } = await import("./roles.js");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.documentElement.dataset["pointer"] = "fine";
});

afterAll(() => {
  style.remove();
  document.documentElement.removeAttribute("data-pointer");
});

/** The pill and its card as `static/index.html` mounts them: a `.pill-slot`
 *  holding the trigger and, as its SIBLING, the card that IS `#role-list`.
 *  `is-open` is the reveal class, and the async re-render is gated on it. */
function mountPill(): HTMLElement {
  document.body.innerHTML = `
    <span class="pill-slot">
      <button id="role-pill" class="pill pill-expandable" type="button">
        <span id="role-pill-icon"></span><span id="role-pill-label"></span>
      </button>
      <span id="role-list" class="pill-expand-content pill-role-list is-open"></span>
    </span>
  `;
  return document.getElementById("role-list") as HTMLElement;
}

/** Expand, then wait for the kiro-config seed to land and re-render. */
async function openList(): Promise<HTMLElement> {
  const list = mountPill();
  initRolePicker();
  expandList?.();
  await vi.waitFor(() => {
    expect(list.querySelector(".pill-role-shadow")).not.toBeNull();
  });
  return list;
}

/** By the LABEL a row renders: nothing on the row carries the mode id, and
 *  `displayModeName` title-cases it (`reviewer` renders as `Reviewer`). */
function rowFor(list: HTMLElement, label: string): HTMLElement {
  const row = Array.from(list.querySelectorAll<HTMLElement>(".pill-role-item")).find(
    (r) => r.querySelector(".pill-role-name")?.textContent === label,
  );
  expect(row, `the card must render a row labelled ${label}`).not.toBeUndefined();
  return row as HTMLElement;
}

beforeEach(() => {
  // A same-named GLOBAL entry is what makes `reviewer` a shadowing row.
  setCatalogModes([
    { id: "vibe", name: "Default", description: "General", source: "bundled" },
    { id: "reviewer", name: "reviewer", description: "The global one", source: "global" },
    // A row long enough to size the card past its 14rem floor, so the two rows
    // measured below have real free space in them. Without it the widest row IS a
    // measured row, every auto margin resolves to 0, and both placements agree.
    { id: "an-agent-whose-name-runs-past-the-floor-on-its-own", name: "", source: "bundled" },
  ]);
});

describe("the mode card's trailing chips", () => {
  it("builds the shadow badge ahead of the scope chip", async () => {
    const list = await openList();
    const classes = Array.from(rowFor(list, "Reviewer").children).map((c) => c.className);

    expect(classes.indexOf("pill-role-shadow")).toBeGreaterThan(-1);
    expect(
      classes.indexOf("pill-role-shadow"),
      "the scope chip owns the row's final slot, so the badge is built before it",
    ).toBeLessThan(classes.indexOf("pill-role-scope"));
  });

  it("ends every scope chip on one x, badge or no badge", async () => {
    const list = await openList();
    const marked = rowFor(list, "Reviewer").querySelector<HTMLElement>(".pill-role-scope");
    const plain = rowFor(list, "Only Here").querySelector<HTMLElement>(".pill-role-scope");
    expect(marked, "the marked row must render a scope chip").not.toBeNull();
    expect(plain, "the unmarked row must render a scope chip").not.toBeNull();

    const right = (n: HTMLElement): number => Math.round(n.getBoundingClientRect().right);
    expect(
      right(marked as HTMLElement),
      "a badge on the row must not move the scope chip: the name takes the free " +
        "space, so the chips end where the card's content box does",
    ).toBe(right(plain as HTMLElement));
  });

  it("keeps the badge beside the scope chip, not beside the name", async () => {
    // What the name's `flex-grow` buys that the reorder alone does not: an auto
    // margin on the scope chip would put the row's whole free space BETWEEN the
    // two chips, stranding the badge against the name. Adjacent flex siblings sit
    // exactly one `gap` apart, so the row's own gap is the expectation.
    const list = await openList();
    const row = rowFor(list, "Reviewer");
    const badge = row.querySelector<HTMLElement>(".pill-role-shadow");
    const scope = row.querySelector<HTMLElement>(".pill-role-scope");
    expect(badge, "the marked row must render a badge").not.toBeNull();
    expect(scope, "the marked row must render a scope chip").not.toBeNull();

    const between =
      (scope as HTMLElement).getBoundingClientRect().left -
      (badge as HTMLElement).getBoundingClientRect().right;
    expect(Math.round(between)).toBe(Math.round(parseFloat(getComputedStyle(row).columnGap)));
  });
});
