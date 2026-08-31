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

/** Every rule that writes one of `props` for `el`, counting `:hover` rules as
 *  if hovered, in document order — so the LAST entry is what a hovering reader
 *  gets.
 *
 *  Computed style cannot answer this: a synthetic hover does not drive style
 *  recalc, and `CSS.forcePseudoState` is a devtools protocol call rather than
 *  something a test page can make. Walking the CSSOM is the honest oracle, and
 *  it reads the same cascade the browser would. */
function propertyWriters(el: Element, props: string[]): { selector: string; value: string }[] {
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
        let value = "";
        for (const p of props) {
          value = r.style.getPropertyValue(p);
          if (value !== "") {
            break;
          }
        }
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

function backgroundWriters(el: Element): { selector: string; value: string }[] {
  return propertyWriters(el, ["background", "background-color"]);
}

function winningBackground(el: Element): string | undefined {
  return backgroundWriters(el).at(-1)?.value;
}

describe("tool card summary affordance", () => {
  it("a summary with a toggle says its whole area is clickable", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css1",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "vibekit" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(css(summary, "cursor")).toBe("pointer");
    // A drag across either line must not select its label instead of toggling.
    expect(css(summary, "user-select")).toBe("none");
    // Never `transition: all` (vibekit-ui.md); the hover fill is the only thing
    // that animates here.
    expect(css(summary, "transition-property")).toBe("background");
  });

  it("a claim-only summary stays inert", async () => {
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
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(card.querySelector(".tool-disclosure")).toBeNull();
    expect(css(summary, "cursor")).toBe("auto");
  });

  it("hovering a toggle summary paints both title and description", async () => {
    // The hover paints their common parent, so no dead strip can remain between
    // the title row and the description line below it.
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css3",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "vibekit" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    const subtitle = card.querySelector<HTMLElement>(".tool-subtitle")!;
    expect(summary.contains(subtitle)).toBe(true);
    expect(winningBackground(summary)).toBe("var(--c-hover)");
    expect(backgroundWriters(subtitle)).toEqual([]);
  });

  it("centres the chevron against the whole two-line summary", async () => {
    const { buildToolCard } = await import("./tool-card.js");
    const card = mount(
      buildToolCard({
        id: "css-chevron",
        title: "remote_web_search",
        kind: "fetch",
        status: "completed",
        input: { query: "vibekit" },
        live: false,
      }),
    );
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure")!;
    const s = summary.getBoundingClientRect();
    const t = toggle.getBoundingClientRect();
    expect(Math.abs(t.y + t.height / 2 - (s.y + s.height / 2))).toBeLessThanOrEqual(1);
  });

  it("nothing paints a claim-only summary", async () => {
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
    const summary = card.querySelector<HTMLElement>(".tool-summary")!;
    expect(backgroundWriters(summary)).toEqual([]);
  });
});

describe("turn action overflow", () => {
  it("keeps secondary actions inline and the More summary hidden on desktop", () => {
    const details = document.createElement("details");
    details.className = "turn-actions-more";
    const summary = document.createElement("summary");
    summary.className = "turn-action-btn turn-action-more";
    const secondary = document.createElement("span");
    secondary.className = "turn-actions-secondary";
    details.append(summary, secondary);
    mount(details);

    expect(details.open).toBe(false);
    expect(css(summary, "display")).toBe("none");
    expect(css(secondary, "display")).toBe("inline-flex");
  });
});

describe("turn card header affordance", () => {
  async function turn(state: "open" | "folded" | "running" | "no-fold"): Promise<HTMLElement> {
    const { buildTurnHeader } = await import("./fundamentals/turn-header.js");
    const card = document.createElement("div");
    card.className = "turn";
    if (state === "folded") {
      card.setAttribute("data-folded", "");
    }
    if (state === "running") {
      card.setAttribute("data-running", "");
    }
    if (state === "no-fold") {
      card.setAttribute("data-no-fold", "");
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

  it("the whole band is the marked surface, open and folded alike", async () => {
    // One gesture wherever the reader clicks it, matching the tool and
    // delegate cards. It used to admit only the meta row while open, which
    // read as the target shrinking when a turn was expanded (user report,
    // 2026-08-31).
    for (const state of ["open", "folded"] as const) {
      const card = await turn(state);
      expect(css(card.querySelector(".turn-header")!, "cursor"), state).toBe("pointer");
    }
  });

  it("the prompt stays selectable; the meta row does not", async () => {
    // The band is a target WITHOUT eating text selection: a drag over the
    // request keeps its selection (disclosure-row.ts skips a click that ends
    // one), so `user-select: none` stops at the meta row.
    const card = await turn("open");
    expect(css(card.querySelector(".turn-head-row")!, "user-select")).toBe("none");
    expect(css(card.querySelector(".turn-req-text")!, "user-select")).not.toBe("none");
  });

  it("a running turn's header claims nothing", async () => {
    // It has no fold (the toggle is display: none), so a pointer cursor there
    // would advertise a control that opens nothing.
    const card = await turn("running");
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("auto");
    expect(propertyWriters(card.querySelector(".turn-header")!, ["background-image"])).toEqual([]);
    expect(css(card.querySelector(".turn-fold-toggle")!, "display")).toBe("none");
  });

  it("a no-fold turn's header claims nothing either", async () => {
    // The newest turn, and a turn whose fold would hide nothing: the fold plan
    // stamps `data-no-fold`, the toggle disappears, and the band stops
    // advertising a control that would animate and change nothing (user
    // report, 2026-08-31: turn 1's toggle "plays an animation but at the end
    // nothing changes").
    const card = await turn("no-fold");
    expect(css(card.querySelector(".turn-header")!, "cursor")).toBe("auto");
    expect(propertyWriters(card.querySelector(".turn-header")!, ["background-image"])).toEqual([]);
    expect(css(card.querySelector(".turn-fold-toggle")!, "display")).toBe("none");
  });

  it("hovering the band washes it as a LAYER, keeping the tint", async () => {
    // `background-image: var(--layer-hover)`, per the interaction-ladder
    // contract (01-tokens.css): written into `background` the wash would
    // REPLACE the band's tertiary tint with a 15% film over the card.
    for (const state of ["open", "folded"] as const) {
      const header = (await turn(state)).querySelector(".turn-header")!;
      const writers = propertyWriters(header, ["background-image"]);
      expect(writers.at(-1)?.value, state).toBe("var(--layer-hover)");
      expect(winningBackground(header), `${state}: the tint stays`).toBe("var(--c-bg-tertiary)");
    }
  });

  it("the meta row paints no fill of its own — the band paints once", async () => {
    // Two translucent overlays would make that half of the band darker than
    // the rest under a hover.
    for (const state of ["open", "folded"] as const) {
      const row = (await turn(state)).querySelector(".turn-head-row")!;
      expect(backgroundWriters(row), state).toEqual([]);
      expect(propertyWriters(row, ["background-image"]), state).toEqual([]);
    }
  });

  it("the fold toggle clears the 24px hit-target floor", async () => {
    // It was 1rem. vibekit-ui.md: "24px minimum desktop", and this is the
    // measurement the whole change started from.
    const card = await turn("open");
    const btn = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const r = btn.getBoundingClientRect();
    expect(r.width).toBeGreaterThanOrEqual(24);
    expect(r.height).toBeGreaterThanOrEqual(24);
  });

  it("the fold toggle matches the copy button at the row's other end", async () => {
    // One row, two buttons, one size — and the copy button already set the
    // row's height, so growing the chevron costs no vertical space.
    const card = await turn("open");
    const fold = card.querySelector<HTMLElement>(".turn-fold-toggle")!;
    const copy = card.querySelector<HTMLElement>(".turn-copy-req")!;
    copy.hidden = false;
    expect(fold.getBoundingClientRect().height).toBe(copy.getBoundingClientRect().height);
    expect(fold.getBoundingClientRect().width).toBe(copy.getBoundingClientRect().width);
  });

  it("the glyph did not grow with its hit target", async () => {
    // The extra 8px is hit area, not ink: the chevron stays 0.75rem.
    const card = await turn("open");
    const glyph = card.querySelector<HTMLElement>(".turn-fold-toggle > .disclosure-chevron")!;
    expect(css(glyph, "--chev-size").trim()).toBe("0.75rem");
  });
});

describe("folded turn face", () => {
  function face(kind: "turn-face-prose" | "turn-face-error", lines: number): HTMLElement {
    const card = document.createElement("div");
    card.className = "turn";
    card.setAttribute("data-folded", "");
    const faceEl = document.createElement("div");
    faceEl.className = "turn-face";
    const content = document.createElement("div");
    if (kind === "turn-face-prose") {
      // The real shape: buildAssistantBubble's root with the face class added.
      content.className = "message assistant turn-face-prose";
      for (let i = 0; i < lines; i++) {
        const p = document.createElement("p");
        p.textContent = `line ${String(i)}`;
        content.appendChild(p);
      }
    } else {
      // The real shape: one text node, newlines rendered by pre-wrap.
      content.className = kind;
      content.textContent = Array.from({ length: lines }, (_, i) => `line ${String(i)}`).join("\n");
    }
    faceEl.appendChild(content);
    card.appendChild(faceEl);
    mount(card);
    return content;
  }

  it("the answer renders in full — the fold hides work, never the reply", () => {
    // A 3-line clamp shipped here once and was overruled (user ruling,
    // 2026-08-31): the folded turn keeps the WHOLE final answer, and the
    // fold's compactness comes from hiding tool cards, reasoning and delegate
    // output. A turn with none of those offers no fold at all (data-no-fold),
    // so an unclamped face can no longer read as "collapse does not work".
    for (const kind of ["turn-face-prose", "turn-face-error"] as const) {
      const content = face(kind, 40);
      expect(content.scrollHeight, `${kind}: nothing clipped`).toBeLessThanOrEqual(
        content.clientHeight + 1,
      );
    }
  });
});

describe("prompt pill centring", () => {
  it("centres an icon when the touch floor makes its pill wider", () => {
    const pill = document.createElement("button");
    pill.className = "pill";
    pill.style.width = "44px";
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.setAttribute("width", "14");
    icon.setAttribute("height", "14");
    pill.appendChild(icon);
    mount(pill);

    const p = pill.getBoundingClientRect();
    const i = icon.getBoundingClientRect();
    expect(Math.abs(i.x + i.width / 2 - (p.x + p.width / 2))).toBeLessThanOrEqual(1);
  });
});

describe("turn body surface", () => {
  it("uses a dedicated body token while header and footer keep their tint", () => {
    const card = document.createElement("div");
    card.className = "turn";
    const header = document.createElement("div");
    header.className = "turn-header";
    const face = document.createElement("div");
    face.className = "turn-face";
    const footer = document.createElement("div");
    footer.className = "turn-footer";
    card.append(header, face, footer);
    mount(card);

    expect(winningBackground(card)).toBe("var(--c-turn-body)");
    expect(winningBackground(face)).toBe("var(--c-turn-body)");
    expect(winningBackground(header)).toBe("var(--c-bg-tertiary)");
    expect(winningBackground(footer)).toBe("var(--c-bg-tertiary)");
  });
});

describe("sub-page menu bars", () => {
  it("shows icon before label until measured icon-only mode", () => {
    const bar = document.createElement("nav");
    bar.className = "settings-tab-bar";
    const tab = document.createElement("button");
    tab.className = "settings-tab";
    const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    icon.classList.add("settings-tab-icon");
    const label = document.createElement("span");
    label.className = "settings-tab-label";
    label.textContent = "General";
    tab.append(icon, label);
    bar.appendChild(tab);
    mount(bar);

    expect(css(tab, "display")).toBe("flex");
    expect(css(icon, "display")).toBe("block");
    expect(css(label, "display")).not.toBe("none");
    expect(tab.firstElementChild).toBe(icon);

    bar.classList.add("tab-bar-icons");
    expect(css(icon, "display")).toBe("block");
    expect(css(label, "display")).toBe("none");
  });

  it("places the active-section title after the menu bar", () => {
    const header = document.createElement("header");
    header.className = "settings-header";
    const title = document.createElement("div");
    title.className = "settings-title-row";
    const bar = document.createElement("nav");
    bar.className = "settings-tab-bar";
    header.append(title, bar);
    mount(header);

    expect(Number(css(title, "order"))).toBeGreaterThan(Number(css(bar, "order")));
  });
});
