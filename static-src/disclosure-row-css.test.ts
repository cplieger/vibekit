// The CSS half of the whole-header disclosure: does the row LOOK like the
// control it now is?
//
// Behaviour is pinned elsewhere (`disclosure-row.test.ts` for the surface,
// `tool-card.test.ts` for the tool card end to end, `fundamentals/
// turn-header.test.ts` for where the turn's surface stops). What those cannot
// see is the affordance, and an invisible hit target is the same defect in the
// other direction — vibekit-ui.md's "no dead zones" rule cuts both ways.
//
// `mountAppCSS` assembles the stylesheet from `css/MANIFEST` in declared order,
// the way `cmd/bundle` concatenates it, because equal-specificity ties in this
// app are decided by that order rather than by the selectors.
import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

let styleEl: HTMLStyleElement;
let host: HTMLElement;

beforeAll(() => {
  styleEl = mountAppCSS();

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

/** The content-visibility of a `<details>`' UA content box.
 *
 *  The ONLY local read measured to discriminate a skipped disclosure subtree: a
 *  skipped button still reports a full-size `getBoundingClientRect` (24x24 for
 *  these) and still computes `display: inline-flex`, so box and display
 *  assertions stay green while the subtree is unpainted and unhittable. Hit
 *  testing discriminates too but not in this file's synthetic host, where the
 *  transcript ancestors mounted above cover the probe point. */
function contentSkipped(details: Element): boolean {
  return getComputedStyle(details, "::details-content").contentVisibility === "hidden";
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

/** A `<details>` whose content must render INLINE above its media rule, with one
 *  real button inside so the box it reports is a paint box rather than an empty
 *  span's zero.
 *
 *  `display` on the elements is NOT the observable here: the UA hides a closed
 *  disclosure through `::details-content`, whose box survives `display: contents`
 *  on the details, so every element can read `display: inline-flex` while the
 *  subtree is content-visibility-skipped — unpainted, zero-size, out of the a11y
 *  tree. That is what shipped: four turn actions and both file-browser New
 *  actions were unreachable at every desktop width, with the summary hidden so
 *  nothing could open them. Assert the BOX. */
function inlineDisclosure(
  menuClass: string,
  contentClass: string,
  btnClass: string,
): HTMLDetailsElement {
  const details = document.createElement("details");
  details.className = menuClass;
  const summary = document.createElement("summary");
  summary.className = `${btnClass} ${menuClass === "turn-actions-more" ? "turn-action-more" : "fb-new-trigger"}`;
  const content = document.createElement("span");
  content.className = contentClass;
  const btn = document.createElement("button");
  btn.className = btnClass;
  btn.textContent = "x";
  content.appendChild(btn);
  details.append(summary, content);
  return details;
}

describe("turn action overflow", () => {
  it("renders the secondary actions inline with the More summary hidden on desktop", () => {
    const details = inlineDisclosure(
      "turn-actions-more",
      "turn-actions-secondary",
      "turn-action-btn",
    );
    const slot = document.createElement("span");
    slot.className = "turn-actions-buttons";
    slot.appendChild(details);
    mount(slot);

    const summary = details.querySelector("summary")!;

    expect(details.open).toBe(false);
    expect(css(summary, "display")).toBe("none");
    // Closed, summary-less, and NOT skipped: the four actions really render.
    expect(contentSkipped(details)).toBe(false);
  });
});

describe("file browser New menu", () => {
  it("renders both actions in the toolbar row with the trigger hidden on desktop", () => {
    const bar = document.createElement("div");
    bar.className = "view-toolbar-inner";
    const details = inlineDisclosure("fb-new-menu", "fb-new-actions", "icon-btn");
    const second = document.createElement("button");
    second.className = "icon-btn";
    second.textContent = "y";
    details.querySelector(".fb-new-actions")!.appendChild(second);
    bar.appendChild(details);
    mount(bar);

    const summary = details.querySelector("summary")!;
    const [a, b] = [...details.querySelectorAll<HTMLElement>(".fb-new-actions > .icon-btn")];

    // The trigger is phone-only. `.view-toolbar-inner .icon-btn` used to restate
    // `display` at (0,2,0) and outrank this hide, so it rendered on desktop.
    expect(css(summary, "display")).toBe("none");
    expect(contentSkipped(details)).toBe(false);
    // Side by side on one row, not stacked inside the UA's block content box.
    expect(b!.getBoundingClientRect().y).toBe(a!.getBoundingClientRect().y);
    expect(b!.getBoundingClientRect().x).toBeGreaterThan(a!.getBoundingClientRect().x);
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

  // THE SUBTITLE DEFERS TO A LABELLED BAR, and this is the dependency that makes
  // it work. `tab-bar-fit.ts` publishes `.tab-bar-named` while a bar is VISIBLE
  // and showing its labels, i.e. while it names its own active section, and
  // 12-chat.css suppresses the title bar's subtitle for exactly that condition —
  // otherwise the section name prints twice, twenty pixels apart. When the bar
  // drops its labels the class goes with them and the subtitle becomes the only
  // text naming the section.
  //
  // What this replaced: `.settings-title-row`, an element whose whole job was to
  // push the bar clear of a floating menu that no longer floats. Its own comment
  // recorded the measurement (the Sources segment lost its last 33px at 1440x900).
  // The bar is in flow now, so the row is deleted rather than repositioned.
  it("suppresses the title bar subtitle while the menu bar names its own section", () => {
    const area = document.createElement("div");
    area.id = "chat-area";
    const heading = document.createElement("div");
    heading.className = "titlebar-heading";
    const subtitle = document.createElement("span");
    subtitle.className = "titlebar-subtitle";
    subtitle.textContent = "Tools";
    heading.append(subtitle);
    const bar = document.createElement("nav");
    bar.className = "settings-tab-bar";
    area.append(heading, bar);
    mount(area);

    // Labelled bar: it names the section, so the subtitle stands down.
    bar.classList.add("tab-bar-named");
    expect(css(subtitle, "display")).toBe("none");

    // Labels dropped: the subtitle is the only name left on screen.
    bar.classList.remove("tab-bar-named");
    expect(css(subtitle, "display")).not.toBe("none");
  });
});

// The steer note is a CARD on the tool-card box, not a left rail. `#vibekit-ui`
// reserves a leading rail for work this agent did not do itself — the run card
// and the delegated-work card — so a steer carrying one was borrowing the wrong
// vocabulary, and the whole of Bug 4 was that it did.
describe("the mid-turn steer note's box", () => {
  function note(state: "read" | "dropped", origin: "user" | "agent"): HTMLElement {
    const el = document.createElement("div");
    el.className = "steer-note";
    el.dataset["state"] = state;
    el.dataset["origin"] = origin;
    const head = document.createElement("div");
    head.className = "steer-note-head";
    const label = document.createElement("span");
    label.className = "steer-note-label";
    label.textContent = "Your mid-turn message";
    head.append(label);
    const body = document.createElement("div");
    body.className = "steer-note-body";
    const text = document.createElement("div");
    text.className = "steer-note-text";
    text.textContent = "actually target main";
    body.append(text);
    el.append(head, body);
    return mount(el);
  }

  it("resolves to the tool card's own fill and radius", () => {
    const n = note("read", "user");
    const card = document.createElement("div");
    card.className = "tool-call";
    const reference = mount(card);
    // Read off `.tool-call` rather than hardcoded, so the two cannot drift: the
    // claim is that they are the SAME box, not that either is a given colour.
    const wantBG = css(reference, "background-color");
    const wantRadius = css(reference, "border-top-left-radius");

    mount(n);
    expect(css(n, "background-color")).toBe(wantBG);
    expect(css(n, "border-top-left-radius")).toBe(wantRadius);
  });

  it("carries a 1px border on every side, and no rail on any of them", () => {
    for (const state of ["read", "dropped"] as const) {
      const n = note(state, "user");
      for (const side of ["top", "right", "bottom", "left"] as const) {
        const w = Number.parseFloat(css(n, `border-${side}-width`));
        expect(w, `${state} border-${side}-width`).toBeCloseTo(1, 1);
      }
    }
  });

  // The rail's actual writer, so a re-added `border-inline-start: 3px` fails here
  // rather than only being noticed on screen.
  it("has no rule anywhere writing a leading border wider than 1px", () => {
    const sheet = loadCSS("13-messages.css");
    for (const sel of [".steer-note", '.steer-note[data-state="dropped"]']) {
      const body = ruleBody(sheet, sel);
      expect(body, sel).not.toMatch(/border-inline-start:\s*[2-9]/);
      expect(body, sel).not.toMatch(/border-(inline-start|left)-width:\s*[2-9]/);
    }
    // And nowhere else in the slice either: the rail could come back on any
    // selector, so the whole sheet is the honest scope for its absence.
    expect(sheet).not.toMatch(/\.steer-note[^{]*\{[^}]*border-inline-start:\s*[2-9]/);
  });

  // Both origins keep the accent-mixed border: both are genuinely input into a
  // running turn, and the origin is carried by the LABEL and the GLYPH rather
  // than by a hue, which WCAG 1.4.1 would forbid as the only channel anyway.
  it("gives the two origins the same border, since the label is what separates them", () => {
    const mine = note("read", "user");
    const mineBorder = css(mine, "border-top-color");
    const theirs = note("read", "agent");
    expect(css(theirs, "border-top-color")).toBe(mineBorder);
  });

  // The transcript has ONE measure and the card is it. This carried
  // `--content-max-w`, the same 122px-dead-gutter defect removed from
  // `.message.assistant` and `.turn-req-text`.
  it("takes the card's own width rather than capping its own measure", () => {
    host.style.inlineSize = "900px";
    const n = note("read", "user");
    expect(css(n, "max-width")).toBe("none");
    expect(n.getBoundingClientRect().width).toBeCloseTo(900, 0);
    host.style.removeProperty("inline-size");
  });
});
