// The CSS half of the whole-header disclosure: does the row LOOK like the
// control it now is?
//
// Behaviour is pinned elsewhere (`disclosure-row.test.ts` for the surface,
// `tool-card.test.ts` for the tool card end to end, `fundamentals/
// turn-header.test.ts` for where the turn's surface stops). What those cannot
// see is the affordance, and an invisible hit target is the same defect in the
// other direction — vibekit-ui.md's "no dead zones" rule cuts both ways.
//
// The stylesheet is assembled from `css/MANIFEST` in declared order, the way
// `cmd/bundle` concatenates it, because equal-specificity ties in this app are
// decided by that order rather than by the selectors. Reading the built
// `static/style.css` instead would test a gitignored artifact that need not
// exist; this reads the sources and reproduces the cascade.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import manifest from "./css/MANIFEST?raw";

const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

// The manifest also pulls the ui-primitives base in from node_modules, and
// `import.meta.glob` needs a static pattern per directory.
const vendor = import.meta.glob<string>("./node_modules/@cplieger/ui-primitives/css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** The manifest's declared order, comments and blanks dropped. */
function manifestOrder(): string[] {
  return manifest
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"));
}

/** A manifest entry's text. Entries are relative to `css/`, so an entry that
 *  climbs out of it (the ui-primitives base) resolves against the package. */
function sheetFor(entry: string): string | undefined {
  if (entry.startsWith("../")) {
    return vendor[`./${entry.slice(3)}`];
  }
  return sheets[`./css/${entry}`];
}

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(async () => {
  const names = manifestOrder();
  const missing = names.filter((n) => sheetFor(n) === undefined);
  expect(missing, "every MANIFEST entry resolves to a stylesheet").toEqual([]);

  styleEl = document.createElement("style");
  styleEl.textContent = names.map((n) => sheetFor(n)).join("\n");
  document.head.appendChild(styleEl);

  host = document.createElement("div");
  document.body.appendChild(host);

  // The transcript's own ancestors, so a rule scoped to them still applies.
  for (const id of ["messages", "messages-wrap", "banner-stack"]) {
    if (document.getElementById(id) === null) {
      const el = document.createElement("div");
      el.id = id;
      document.body.appendChild(el);
    }
  }
});

afterAll(() => {
  styleEl?.remove();
  host?.remove();
});

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./editor-openers.js", () => ({
  openFile: () => {
    /* noop */
  },
  openFileDiff: () => {
    /* noop */
  },
  openFileGitDiff: () => {
    /* noop */
  },
}));
vi.mock("./tool-group.js", () => ({
  trackInProgress: () => {
    /* noop */
  },
}));

function mount(node: HTMLElement): HTMLElement {
  host.replaceChildren(node);
  return node;
}

function css(el: Element, prop: string): string {
  return getComputedStyle(el).getPropertyValue(prop);
}

/** Every rule that writes `background` for `el`, counting `:hover` rules as if
 *  hovered, in document order — so the LAST entry is what a hovering reader
 *  gets.
 *
 *  Computed style cannot answer this: a synthetic hover does not drive style
 *  recalc, and `CSS.forcePseudoState` is a devtools protocol call rather than
 *  something a test page can make. Walking the CSSOM is the honest oracle, and
 *  it reads the same cascade the browser would. */
function backgroundWriters(el: Element): { selector: string; value: string }[] {
  const out: { selector: string; value: string }[] = [];
  for (const sheet of document.styleSheets) {
    let rules: CSSRuleList;
    try {
      rules = sheet.cssRules;
    } catch {
      continue;
    }
    const walk = (list: CSSRuleList): void => {
      for (const r of list) {
        const grouping = r as CSSRule & { cssRules?: CSSRuleList };
        if (!(r instanceof CSSStyleRule)) {
          if (grouping.cssRules !== undefined) {
            walk(grouping.cssRules);
          }
          continue;
        }
        const value =
          r.style.getPropertyValue("background") || r.style.getPropertyValue("background-color");
        if (value === "") {
          continue;
        }
        // `:hover` stripped so the element matches as though the pointer were on
        // it. Nothing else in these selectors is state-dependent.
        try {
          if (el.matches(r.selectorText.replaceAll(":hover", ""))) {
            out.push({ selector: r.selectorText, value });
          }
        } catch {
          /* a selector this engine cannot parse writes nothing here */
        }
      }
    };
    walk(rules);
  }
  return out;
}

function winningBackground(el: Element): string | undefined {
  return backgroundWriters(el).at(-1)?.value;
}

describe("tool card header affordance", () => {
  it("a header with a toggle says it is clickable", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css1",
        title: "executePwsh",
        kind: "execute",
        status: "completed",
        input: { command: "ls" },
        live: false,
      }),
    );
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(css(header, "cursor")).toBe("pointer");
    // A drag across the row must not select its label instead of toggling —
    // the same call every other clickable header in the app makes.
    expect(css(header, "user-select")).toBe("none");
    // Never `transition: all` (vibekit-ui.md); the hover fill is the only thing
    // that animates here.
    expect(css(header, "transition-property")).toBe("background");
  });

  it("a claim-only header stays inert", async () => {
    // `readFile` has no depth 1, so it builds no toggle and no details region.
    // A pointer cursor there would advertise a control that opens nothing.
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css2",
        title: "readFile",
        kind: "read",
        status: "completed",
        input: { path: "src/main.ts" },
        live: false,
      }),
    );
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(css(header, "cursor")).toBe("auto");
  });

  it("hovering a toggle header lands on the shared interaction rung", async () => {
    // `--c-hover` rather than a local color-mix: one hover vocabulary, and a
    // translucent overlay composites over whatever fill the card has.
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css3",
        title: "executePwsh",
        kind: "execute",
        status: "completed",
        input: { command: "ls" },
        live: false,
      }),
    );
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(winningBackground(header)).toBe("var(--c-hover)");
  });

  it("nothing paints a claim-only header, hovered or not", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css4",
        title: "readFile",
        kind: "read",
        status: "completed",
        input: { path: "src/main.ts" },
        live: false,
      }),
    );
    const header = card.querySelector<HTMLElement>(".tool-header")!;
    expect(backgroundWriters(header)).toEqual([]);
  });
});

describe("turn card header affordance", () => {
  async function turn(folded: boolean): Promise<HTMLElement> {
    const { buildTurnHeader } = await import("./fundamentals/turn-header.js");
    const card = document.createElement("div");
    card.className = "turn";
    if (folded) {
      card.setAttribute("data-folded", "");
    }
    card.appendChild(
      buildTurnHeader({
        n: 1,
        outcome: "completed",
        ts: Date.now(),
        request: "a request",
        attachments: [],
      }),
    );
    return mount(card);
  }

  it("open: the meta row is the marked surface, the request is not", async () => {
    const card = await turn(false);
    expect(css(card.querySelector(".turn-head-row")!, "cursor")).toBe("pointer");
    expect(css(card.querySelector(".turn-head-row")!, "user-select")).toBe("none");
    // The prompt stays prose: a text cursor, and selectable.
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("auto");
    expect(css(card.querySelector(".turn-req-text")!, "user-select")).not.toBe("none");
  });

  it("folded: the whole band is the marked surface", async () => {
    const card = await turn(true);
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("pointer");
    expect(winningBackground(card.querySelector(".turn-header")!)).toBe("var(--c-hover)");
  });

  it("open: hovering the meta row lands on the shared rung", async () => {
    const card = await turn(false);
    expect(winningBackground(card.querySelector(".turn-head-row")!)).toBe("var(--c-hover)");
  });

  it("folded: the meta row's own fill is suppressed, so the band paints once", async () => {
    // Both rules match a hovered folded row, and two translucent overlays would
    // make that half of the band darker than the rest. The later rule wins.
    const card = await turn(true);
    expect(winningBackground(card.querySelector(".turn-head-row")!)).toBe("none");
  });

  it("the fold toggle clears the 24px hit-target floor", async () => {
    // It was 1rem. vibekit-ui.md: "24px minimum desktop", and this is the
    // measurement the whole change started from.
    const card = await turn(false);
    const btn = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const r = btn.getBoundingClientRect();
    expect(r.width).toBeGreaterThanOrEqual(24);
    expect(r.height).toBeGreaterThanOrEqual(24);
  });

  it("the fold toggle matches the copy button at the row's other end", async () => {
    // One row, two buttons, one size — and the copy button already set the
    // row's height, so growing the chevron costs no vertical space.
    const card = await turn(false);
    const fold = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const copy = card.querySelector<HTMLElement>(".turn-copy-req")!;
    copy.hidden = false;
    expect(fold.getBoundingClientRect().height).toBe(copy.getBoundingClientRect().height);
    expect(fold.getBoundingClientRect().width).toBe(copy.getBoundingClientRect().width);
  });

  it("the glyph did not grow with its hit target", async () => {
    // The extra 8px is hit area, not ink: the chevron stays 0.75rem.
    const card = await turn(false);
    const glyph = card.querySelector<HTMLElement>(".turn-fold-toggle > .disclosure-chevron")!;
    expect(css(glyph, "--chev-size").trim()).toBe("0.75rem");
  });
});
