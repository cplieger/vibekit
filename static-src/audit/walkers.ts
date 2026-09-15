// ---------------------------------------------------------------------------
// The ui-qa audit walkers: ONE implementation, in the app, for TWO consumers.
// vitest runs them here against fixtures as a CI gate, and the ui-qa runners
// (`.kiro/skills/ui-qa/references/{control-height,radius,contrast}-audit.mjs`)
// inject the same source into a live page over CDP — `cdp.mjs auditSource`
// imports this file and calls `pageSource`, which is why the module lives in the
// app rather than beside those runners: a walker change is an app change with a
// red-green test, and the number a runner prints is the number vitest pinned.
//
// FOUR CONSTRAINTS, every one of them from the injection side:
//
//  1. A WALKER IS SELF-CONTAINED. It is serialised with
//     `Function.prototype.toString()`, so it may reference nothing from module
//     scope — only its own body, the page's globals, and the helpers
//     `pageSource` emits beside it (see HELPERS).
//  2. ONLY ERASABLE TYPE SYNTAX. node imports this `.ts` file with type
//     stripping, which BLANKS annotations rather than compiling them (measured:
//     `(o: Opts)` comes back as `(o      )`), so an enum, a decorator or a
//     parameter property would reach the page as a syntax error.
//  3. EVERY RESULT IS JSON. `Runtime.evaluate` returns by value, so no element
//     reference can cross the boundary and a finding carries a DESCRIPTION.
//  4. NOTHING TOUCHES THE DOM AT MODULE LOAD. node imports this file with no
//     document at all.
//
// The findings are deduped with a count, because a repeated row (fifty list
// items with one wrong control) is one finding a reader acts on once.
// ---------------------------------------------------------------------------

/** A page-side audit. Takes its options by value and returns JSON. */
type Walker = (opts: never) => unknown;

// --- helpers, emitted into the page beside the walker -----------------------
//
// The KEY is the name the emitted `const` takes, so a walker calls
// `describeEl(...)` and gets this function. Do not rename a key without renaming
// its call sites: the binding is by string, which no type checker can see.

/** `tag#id.class.class`, bounded, so a finding names its element in one line. */
function describeEl(el: Element): string {
  const tag = el.tagName.toLowerCase();
  const id = el.id ? `#${el.id}` : "";
  const cls = [...el.classList]
    .slice(0, 4)
    .map((c) => `.${c}`)
    .join("");
  const role = el.getAttribute("role");
  const extra = role !== null && id === "" ? `[role=${role}]` : "";
  return `${tag}${id}${cls}${extra}`.slice(0, 120);
}

/** A computed length in px. Absent, `auto` and `none` all read 0, which is what
 *  every caller here wants: they are asking "how much", not "what did it say". */
function lenPx(v: string): number {
  const n = Number.parseFloat(v);
  return Number.isFinite(n) ? n : 0;
}

/** The page's own route, for the report header. */
function routeOf(): string {
  return `${location.pathname}${location.search}${location.hash}`;
}

/** A token's value in px, read through a real box so the whole cascade decides
 *  it. 0 means the token does not resolve: an invalid `var()` drops the
 *  declaration, leaving a shrink-to-fit box at zero. */
function tokenPx(name: string): number {
  const probe = document.createElement("div");
  probe.style.cssText = `position:fixed;left:-9999px;top:0;inline-size:var(${name});block-size:var(${name})`;
  document.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

/** Hidden by a stylesheet rather than merely off-screen: no boxes at all. A
 *  control in a view this page never mounted, or one another viewport hides. */
function hasNoBox(el: Element): boolean {
  return el.getClientRects().length === 0;
}

/** The nearest ancestor whose transform is not a pure TRANSLATION, or null.
 *
 *  Stricter than "is it scaled", and deliberately so: a rect under a rotation is
 *  the AABB of a tilted box rather than the box, and at 90 degrees its height IS
 *  the neighbouring width — so every geometry judgement below refuses to measure
 *  rather than reporting a distorted number as a finding. `icon-crisp.ts` makes
 *  the same call for the same reason ("anything not axis-aligned is DECLINED").
 *
 *  A pure translation is exempt because it moves the box without resizing it,
 *  which is what that module writes on every glyph in the app.
 *
 *  The four individual transform properties are read separately: Chromium does
 *  NOT fold `scale`, `rotate` and `translate` into the computed `transform`, and
 *  two live surfaces use them (the effort knob's hover scale, every icon's snap
 *  offset). */
function distortedUnder(el: Element): Element | null {
  const flatMatrix = (v: string): boolean => {
    if (v === "" || v === "none") {
      return true;
    }
    const m = new DOMMatrixReadOnly(v);
    const off = [m.m11 - 1, m.m22 - 1, m.m33 - 1, m.m12, m.m13, m.m21, m.m23, m.m31, m.m32];
    return off.every((n) => Math.abs(n) < 0.001);
  };
  const allOnes = (v: string): boolean => {
    if (v === "" || v === "none" || v === "normal") {
      return true;
    }
    return v
      .trim()
      .split(/[\s,]+/u)
      .every((part) => {
        const n = Number.parseFloat(part);
        return !Number.isFinite(n) || Math.abs(n - 1) < 0.001;
      });
  };
  const noAngle = (v: string): boolean => {
    if (v === "" || v === "none") {
      return true;
    }
    // `<axis>? <angle>`, so the angle is the last token.
    const toks = v.trim().split(/[\s,]+/u);
    const deg = Number.parseFloat(toks[toks.length - 1] ?? "");
    return !Number.isFinite(deg) || Math.abs(deg) < 0.001;
  };

  let cur: Element | null = el.parentElement;
  while (cur !== null && cur !== document.documentElement) {
    const cs = getComputedStyle(cur);
    if (!flatMatrix(cs.transform)) {
      return cur;
    }
    if (!allOnes(cs.getPropertyValue("scale"))) {
      return cur;
    }
    if (!allOnes(cs.getPropertyValue("zoom"))) {
      return cur;
    }
    if (!noAngle(cs.getPropertyValue("rotate"))) {
      return cur;
    }
    cur = cur.parentElement;
  }
  return null;
}

/** Group by a key, keeping the FIRST payload and counting the rest. Every
 *  finding list below is bucketed this way. */
function bucket<T>(rows: readonly (readonly [string, T])[]): (T & { count: number })[] {
  const seen = new Map<string, T & { count: number }>();
  for (const [key, payload] of rows) {
    const hit = seen.get(key);
    if (hit === undefined) {
      seen.set(key, { ...payload, count: 1 });
    } else {
      hit.count += 1;
    }
  }
  return [...seen.values()];
}

/** Does the point resolve to this element, or to something inside it? The
 *  oracle for a target: a `::before` expander answers as its own element, and a
 *  glyph inside a button answers as the button's child. */
function ownsPoint(el: Element, x: number, y: number): boolean {
  const hit = document.elementFromPoint(x, y);
  return hit !== null && (hit === el || el.contains(hit));
}

/** How far the element's TARGET reaches on one axis, measured outward from its
 *  own centre by hit test, capped at `cap`.
 *
 *  This is why an undersize verdict is not a box read: the app's hit floor lets a
 *  control that must stay visually small grow its TARGET with an absolutely
 *  positioned expander (61-mcp-tools.css), so `.shell-resize` paints 3px and is
 *  grabbable across 24. Reading the box alone reports every one of those as a
 *  violation. */
function targetExtent(el: Element, axis: "x" | "y", cap: number): number | null {
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;

  // NULL, never the box. If the element does not answer at its own centre the
  // target cannot be measured here at all, and returning the painted box instead
  // reports every occluded control as undersize by exactly its own expander.
  //
  // MEASURED on the live app: a `.tool-file-link` inside a CLOSED `<details>`
  // reports a client rect — Chromium hides that content with `content-visibility`
  // on `::details-content` rather than by removing it from layout, so `hasNoBox` is
  // false — while `elementFromPoint` at its centre answers the `<summary>` painted
  // over it. The box fallback turned 12 of those into a 20px-against-24px finding
  // on a chip whose expander reaches 26.
  if (!ownsPoint(el, cx, cy)) {
    return null;
  }
  let lo = 0;
  let hi = 0;
  const limit = cap + 4;
  while (
    lo < limit &&
    ownsPoint(el, axis === "x" ? cx - lo - 1 : cx, axis === "y" ? cy - lo - 1 : cy)
  ) {
    lo += 1;
  }
  while (
    hi < limit &&
    ownsPoint(el, axis === "x" ? cx + hi + 1 : cx, axis === "y" ? cy + hi + 1 : cy)
  ) {
    hi += 1;
  }
  return lo + hi + 1;
}

/** Is this control a target IN A SENTENCE — WCAG 2.5.8's inline exception?
 *
 *  Inline-level AND sitting in a container that carries text of its own, so the
 *  line height constrains the control and the sentence around it carries the reach.
 *  That is the standard's own wording ("in a sentence, or its size is otherwise
 *  constrained by the line-height of non-target text") and the app grants it twice:
 *  `61-mcp-tools.css` zeroes the floor for a prose `a[href]`, and `14-tools.css`
 *  declares it again on `.inline-file-link` because `linkify.ts` emits a `<button>`
 *  that rule's selector cannot reach.
 *
 *  Keying on the TAG instead is what made this walker report 51 undersize findings
 *  against that chip, whose own stylesheet carries the exception with a
 *  measurement: the floor made it 44px tall on a coarse pointer, so every paragraph
 *  line holding one painted 44px. A tag test cannot see that, and the display plus
 *  the surrounding text can.
 *
 *  Requiring the text sibling is what keeps a labelled FIELD in the population the
 *  row rule exists for: an `<input>` beside its label is inline-level with text
 *  around it, so it is floor-exempt by the standard, and it still has to agree with
 *  the controls beside it — which is why this gate governs the FLOOR and the row
 *  rule reads its own signal. */
function inlineInText(el: Element, cs: CSSStyleDeclaration): boolean {
  if (!cs.display.startsWith("inline")) {
    return false;
  }
  const parent = el.parentElement;
  if (parent === null) {
    return false;
  }
  for (const node of [...parent.childNodes]) {
    // 3 is Node.TEXT_NODE, spelled as the number so the serialized walker needs no
    // binding for the `Node` global.
    if (node.nodeType === 3 && (node.textContent ?? "").trim() !== "") {
      return true;
    }
  }
  return false;
}

/** Does this control grow its TARGET with the app's expander idiom — an
 *  absolutely positioned `::before`/`::after` sized off the hit floor?
 *
 *  Read off COMPUTED STYLE rather than hit-tested, deliberately: this answers for
 *  a control scrolled out of view, where a hit test cannot, and the two rows this
 *  exists for are both usually off-screen in a transcript. It detects the
 *  DECLARATION (61-mcp-tools.css: "a control that must stay visually small grows
 *  its TARGET through an absolutely positioned `::after` sized off `--hit-floor`"),
 *  so a decorative absolute pseudo-element that grows nothing reads as an expander
 *  too — which is why what it gates is a REPORTING BUCKET rather than silence. */
function growsTarget(el: Element): boolean {
  for (const pseudo of ["::after", "::before"]) {
    const cs = getComputedStyle(el, pseudo);
    // `"none"` is the only absent value: MEASURED in Chromium 152, a declared
    // `content: ""` computes to the two-character string `""`, so a guard against
    // the empty string here can never fire.
    if (cs.content !== "none" && cs.position === "absolute") {
      return true;
    }
  }
  return false;
}

/** Is the element's centre inside the viewport? A hit test cannot answer for
 *  anything scrolled out, so a target that needs one is reported unmeasurable
 *  rather than judged. */
function centreOnScreen(el: Element): boolean {
  const r = el.getBoundingClientRect();
  const cx = r.left + r.width / 2;
  const cy = r.top + r.height / 2;
  return cx >= 0 && cy >= 0 && cx <= window.innerWidth && cy <= window.innerHeight;
}

// --- reveal ----------------------------------------------------------------

/** Force stylesheet-hidden panels into layout so ONE pass covers them.
 *
 *  Blunt on purpose, and the reason it is owned-tabs-only in every runner: it
 *  unhides by inline `display: revert`, which hands a hidden flex child back its
 *  UA default, so the layout it produces is not the layout that view ships. Read
 *  a finding it surfaces as "this surface has a problem", then re-run the audit
 *  on the view mounted properly before believing a NUMBER.
 *
 *  Several passes, because unhiding a container reveals children its own rule
 *  hides; a fixed point is normally reached in two.
 *
 *  MEASURED, because three of these look like gaps and are not. `display: revert`
 *  DOES unhide a `[hidden]` element, since the important inline declaration wins.
 *  A closed `<details>`'s buried controls already report boxes, because Chromium
 *  hides that content with `content-visibility` on `::details-content` rather
 *  than by removing it from layout. And a control inside a `display: none`
 *  container computes its OWN display rather than `none`, so the container is the
 *  only thing this pass has to touch.
 *
 *  What revert genuinely CANNOT unhide is an element whose UA display is `none`
 *  by TAG. `<datalist>` and a closed `<dialog>` are the two live cases, and the
 *  already-revealed marker is what stops one of those being counted again on
 *  every pass.
 *
 *  So a closed `<dialog>` is skipped to keep `revealed` HONEST — it means
 *  "elements this pass put into layout", and touching one would add an inert
 *  inline style and a count for a box that never appeared. NOT for modality: this
 *  pass never sets the `open` attribute, so it cannot stack a modal, and an
 *  earlier version of this comment claimed it could. `closest` starts at the
 *  element itself, so ONE test covers the dialog and its subtree; a tagName check
 *  beside it was pure redundancy, and an outcome assertion cannot tell two
 *  redundant guards apart. Auditing a dialog's own controls needs it opened,
 *  which is a decision about what this pass REPORTS rather than a bug. */
function reveal(): { revealed: number; passes: number } {
  const SKIP = new Set(["SCRIPT", "STYLE", "TEMPLATE", "LINK", "META", "HEAD", "TITLE"]);
  let revealed = 0;
  let passes = 0;
  for (let pass = 0; pass < 3; pass++) {
    passes = pass + 1;
    let touched = 0;
    for (const el of [...document.body.querySelectorAll<HTMLElement>("*")]) {
      if (SKIP.has(el.tagName)) {
        continue;
      }
      if (el.closest("dialog:not([open])") !== null) {
        continue;
      }
      if (el.dataset["uiqaRevealed"] === "1") {
        continue;
      }
      const cs = getComputedStyle(el);
      const hiddenDisplay = cs.display === "none";
      const hiddenVis = cs.visibility === "hidden" || cs.contentVisibility === "hidden";
      if (!hiddenDisplay && !hiddenVis) {
        continue;
      }
      if (hiddenDisplay) {
        el.style.setProperty("display", "revert", "important");
      }
      if (hiddenVis) {
        el.style.setProperty("visibility", "visible", "important");
        el.style.setProperty("content-visibility", "visible", "important");
      }
      el.dataset["uiqaRevealed"] = "1";
      touched += 1;
    }
    revealed += touched;
    if (touched === 0) {
      break;
    }
  }
  return { revealed, passes };
}

// --- control height --------------------------------------------------------

/** Every form control whose height disagrees with the controls beside it
 *  (vibekit-ui.md "One control height per row"), and every control whose TARGET
 *  is under the tier's hit floor.
 *
 *  A ROW is a flex or grid parent plus a vertical BAND, which is what makes this
 *  a claim about controls a reader sees side by side rather than about every
 *  control on the page. Members are clustered by rect overlap rather than by a
 *  rounded coordinate, so two controls one pixel either side of a bucket
 *  boundary still belong to the same row.
 *
 *  A target IN A SENTENCE is excluded outright (`inlineInText`): that is WCAG
 *  2.5.8's own inline exception, and an inline box constrained by a line height has
 *  no height for a row to agree on either. Reported as `inlineExempt` rather than
 *  dropped silently, because the population is large enough that a reader should
 *  see it was looked at.
 *
 *  A box HEADER is not a control and is invisible here — that is
 *  `height-sweep.mjs`. */
function controlHeight(opts: { tol: number; floor?: number }): unknown {
  const SEL = [
    "button",
    'input:not([type="hidden"])',
    "select",
    "textarea",
    "summary",
    '[role="button"]',
    '[role="switch"]',
    '[role="tab"]',
    '[role="separator"][tabindex]',
    "a[href]",
  ].join(", ");

  const tol = opts.tol;
  let floor = opts.floor ?? 0;
  let floorSource = "--floor";
  if (floor === 0) {
    floor = tokenPx("--hit-floor");
    floorSource = "--hit-floor";
  }
  if (floor === 0) {
    floor = tokenPx("--touch-target");
    floorSource = "--touch-target";
  }
  if (floor === 0) {
    floor = 24;
    floorSource = "default";
  }

  interface Member {
    el: string;
    h: number;
    kind: string;
    declaredH: string;
    minH: string;
    pad: string;
    fs: string;
    node: Element;
  }

  const viewportHidden: (readonly [string, { h: number; el: string }])[] = [];
  const unmeasurable: (readonly [string, { why: string; el: string }])[] = [];
  const undersizeRows: (readonly [string, Omit<Member, "node">])[] = [];
  const expandedRows: (readonly [string, Omit<Member, "node">])[] = [];
  const inlineRows: (readonly [string, { el: string }])[] = [];
  const measured: Member[] = [];

  for (const el of [...document.querySelectorAll(SEL)]) {
    const cs = getComputedStyle(el);

    // WCAG 2.5.8's inline exception governs the FLOOR and not the row: a target in
    // a sentence needs no 24px reach, and a labelled field beside its label still
    // has a visible height that has to match the controls next to it. Two
    // questions, two gates — folding them made the labelled-field case, which is
    // the population the row rule was written for, silently unreportable.
    const floorExempt = inlineInText(el, cs);
    if (floorExempt) {
      inlineRows.push([describeEl(el), { el: describeEl(el) }]);
    }
    if (hasNoBox(el)) {
      viewportHidden.push([describeEl(el), { h: 0, el: describeEl(el) }]);
      continue;
    }
    const distorted = distortedUnder(el);
    if (distorted !== null) {
      unmeasurable.push([
        `${describeEl(el)}|${describeEl(distorted)}`,
        { why: `a transformed ancestor, ${describeEl(distorted)}`, el: describeEl(el) },
      ]);
      continue;
    }
    const r = el.getBoundingClientRect();
    const role = el.getAttribute("role");
    const m: Member = {
      el: describeEl(el),
      h: Math.round(r.height * 100) / 100,
      kind:
        el.tagName === "A" || el.tagName === "DIV" || el.tagName === "SPAN"
          ? `role=${role ?? el.tagName.toLowerCase()}`
          : el.tagName.toLowerCase(),
      declaredH: cs.height,
      minH: cs.minHeight,
      pad: `${lenPx(cs.paddingTop)}+${lenPx(cs.paddingBottom)}`,
      fs: cs.fontSize,
      node: el,
    };
    // IS THIS CONTROL CLAIMING THE ROW'S HEIGHT AT ALL? A control painted under
    // the floor that grows its target with an expander has DECLARED itself
    // visually small, which `vibekit-ui.md` states as its own rule — "a
    // control-height token belongs to a control that OWNS its row, and anything
    // riding inside a row already floored at one measures the ink beside it
    // instead" — so it is not in the one-height-per-row population and comparing
    // it against a sibling reports a settled decision as a defect.
    //
    // Both rows this closes were measured on the live app and both are documented
    // deliberate: a tool header's file badge reads the glyph token so it does not
    // inflate a 36px row, and the turn footer's Rewind leaves the floor because
    // its resting border would weld onto the card's. Neither is silent — each
    // lands in `expanded`, which is a reporting bucket for exactly the reason
    // `growsTarget` reads a declaration rather than a hit test.
    const shortSide = Math.min(r.height, r.width);
    if (shortSide + tol < floor && growsTarget(el)) {
      const { node: _n, ...row } = m;
      expandedRows.push([m.el, row]);
    } else {
      measured.push(m);
    }

    if (floorExempt) {
      continue;
    }

    // The UNDERSIZE half measures the TARGET, not the box, and runs for BOTH
    // populations: a control under the floor may have grown one with an expander,
    // and reading the box calls every one of those a violation — while a control
    // the branch above exempted still has to prove its expander actually reaches.
    // The hit test needs the control on screen.
    if (shortSide + tol >= floor) {
      continue;
    }
    if (!centreOnScreen(el)) {
      unmeasurable.push([
        `${describeEl(el)}|offscreen`,
        {
          why: "its centre is outside the viewport, so no hit test can answer",
          el: describeEl(el),
        },
      ]);
      continue;
    }
    const axis = r.height <= r.width ? "y" : "x";
    const reach = targetExtent(el, axis, floor);
    if (reach === null) {
      unmeasurable.push([
        `${describeEl(el)}|occluded`,
        {
          why: "something else answers at its centre — occluded, or inside a content-visibility:hidden subtree such as a closed <details>",
          el: describeEl(el),
        },
      ]);
      continue;
    }
    if (reach + tol >= floor) {
      continue;
    }
    const { node: _node, ...row } = m;
    undersizeRows.push([
      `${m.el}|${axis}`,
      { ...row, h: Math.round(reach * 100) / 100, kind: axis === "x" ? `${m.kind}:w` : m.kind },
    ]);
  }

  // ROWS: a flex or grid ancestor, then a band inside it.
  const byBox = new Map<Element, Member[]>();
  for (const m of measured) {
    let host: Element | null = m.node.parentElement;
    while (host !== null && host !== document.documentElement) {
      const d = getComputedStyle(host).display;
      if (d === "flex" || d === "inline-flex" || d === "grid" || d === "inline-grid") {
        break;
      }
      host = host.parentElement;
    }
    const key = host ?? m.node.parentElement;
    if (key === null) {
      continue;
    }
    const list = byBox.get(key);
    if (list === undefined) {
      byBox.set(key, [m]);
    } else {
      list.push(m);
    }
  }

  const mismatchRows: (readonly [
    string,
    { spread: number; box: string; members: Omit<Member, "node">[] },
  ])[] = [];
  const passRows: (readonly [string, { members: { el: string; h: number }[] }])[] = [];
  let rows = 0;

  for (const [host, members] of byBox) {
    const sorted = [...members].sort(
      (a, b) => a.node.getBoundingClientRect().top - b.node.getBoundingClientRect().top,
    );
    let band: Member[] = [];
    let bandBottom = -Infinity;
    const flush = (): void => {
      if (band.length === 0) {
        return;
      }
      rows += 1;
      const heights = band.map((m) => m.h);
      const spread = Math.round((Math.max(...heights) - Math.min(...heights)) * 100) / 100;
      const sig = band.map((m) => `${m.el}@${m.h}`).join("|");
      if (band.length > 1 && spread > tol) {
        mismatchRows.push([
          `${describeEl(host)}|${sig}`,
          {
            spread,
            box: describeEl(host),
            members: band.map(({ node: _node, ...rest }) => rest),
          },
        ]);
      } else {
        passRows.push([
          `${describeEl(host)}|${sig}`,
          { members: band.map((m) => ({ el: m.el, h: m.h })) },
        ]);
      }
      band = [];
      bandBottom = -Infinity;
    };
    for (const m of sorted) {
      const r = m.node.getBoundingClientRect();
      if (band.length > 0 && r.top >= bandBottom) {
        flush();
      }
      band.push(m);
      bandBottom = band.length === 1 ? r.bottom : Math.max(bandBottom, r.bottom);
    }
    flush();
  }

  return {
    route: routeOf(),
    viewport: { w: window.innerWidth, h: window.innerHeight },
    floor: Math.round(floor * 100) / 100,
    floorSource,
    pointer: document.documentElement.dataset["pointer"] ?? null,
    pointerCoarse: window.matchMedia("(pointer: coarse)").matches,
    scanned: measured.length,
    rows,
    mismatches: bucket(mismatchRows),
    undersize: bucket(undersizeRows),
    viewportHidden: bucket(viewportHidden),
    unmeasurable: bucket(unmeasurable),
    expanded: bucket(expandedRows),
    inlineExempt: bucket(inlineRows),
    passes: bucket(passRows),
  };
}

// --- radius ----------------------------------------------------------------

/** Every nested rounded corner that does not nest, all four corners of every
 *  radius-bearing element against its nearest radius-bearing ancestor.
 *
 *  THE RULE IS web.md's: a corner is a quarter circle whose centre sits `radius`
 *  in from both edges it joins, so two arcs share a centre only at
 *  `inner = outer - outer-border - inset`, and equal centres additionally
 *  require the inset to be EQUAL on the corner's two edges. An anisotropic inset
 *  is therefore unsatisfiable at any single radius — reported as its own verdict
 *  with no ideal, because the fix is the inset rather than the radius.
 *
 *  Two shapes are outside the rule rather than breaking it, and both are reported
 *  as exempt rather than silently dropped: a stadium or circle (a radius at or
 *  past half the shorter side is a deliberate shape, not a nesting decision), and
 *  a child sitting flush inside a clipping parent, which takes the parent's
 *  corner. A child that paints nothing AT REST is NOT exempt — nearly every icon
 *  button gets a background on hover — so it is judged and flagged as
 *  hover-only. */
function radius(opts: { tol: number }): unknown {
  const tol = opts.tol;
  const CORNERS = [
    ["tl", "borderTopLeftRadius", "top", "left"],
    ["tr", "borderTopRightRadius", "top", "right"],
    ["br", "borderBottomRightRadius", "bottom", "right"],
    ["bl", "borderBottomLeftRadius", "bottom", "left"],
  ] as const;

  /** A corner's radius in px. A percentage resolves against the box, which is
   *  what makes the stadium test below able to see `border-radius: 50%`. */
  const cornerPx = (cs: CSSStyleDeclaration, prop: string, w: number, h: number): number => {
    const raw = cs.getPropertyValue(prop.replace(/[A-Z]/gu, (c) => `-${c.toLowerCase()}`));
    const first = raw.trim().split(/\s+/u)[0] ?? "0px";
    if (first.endsWith("%")) {
      return (Number.parseFloat(first) / 100) * Math.min(w, h);
    }
    return lenPx(first);
  };

  const rounded: Element[] = [];
  for (const el of [...document.body.querySelectorAll("*")]) {
    if (hasNoBox(el)) {
      continue;
    }
    const cs = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    const any = CORNERS.some(([, prop]) => cornerPx(cs, prop, r.width, r.height) > 0);
    if (any) {
      rounded.push(el);
    }
  }

  const findings: (readonly [
    string,
    {
      verdict: string;
      childRadius: number;
      ideal: number | null;
      insetH: number;
      insetV: number;
      parentRadius: number;
      parentBorder: number;
      corners: string;
      childPaints: boolean;
      child: string;
      parent: string;
      note: string;
    },
  ])[] = [];
  const exempt: (readonly [string, { why: string; child: string; parent: string }])[] = [];
  const unmeasurable: (readonly [string, { why: string; child: string }])[] = [];
  let pairs = 0;

  for (const child of rounded) {
    let host: Element | null = child.parentElement;
    let hostCS: CSSStyleDeclaration | null = null;
    while (host !== null && host !== document.documentElement) {
      const cs = getComputedStyle(host);
      const hr = host.getBoundingClientRect();
      if (CORNERS.some(([, prop]) => cornerPx(cs, prop, hr.width, hr.height) > 0)) {
        hostCS = cs;
        break;
      }
      host = host.parentElement;
    }
    if (host === null || hostCS === null) {
      continue;
    }

    const distorted = distortedUnder(child);
    if (distorted !== null) {
      unmeasurable.push([
        `${describeEl(child)}|${describeEl(distorted)}`,
        { why: `a transformed ancestor, ${describeEl(distorted)}`, child: describeEl(child) },
      ]);
      continue;
    }

    const cs = getComputedStyle(child);
    const cr = child.getBoundingClientRect();
    const hr = host.getBoundingClientRect();
    const paints =
      (cs.backgroundColor !== "rgba(0, 0, 0, 0)" && cs.backgroundColor !== "transparent") ||
      cs.backgroundImage !== "none" ||
      (lenPx(cs.borderTopWidth) > 0 && cs.borderTopStyle !== "none");

    // The inset is measured from the parent's BORDER box inward, per edge, which
    // is the perpendicular distance the concentric rule subtracts.
    const borderWidths = {
      top: hostCS.borderTopWidth,
      bottom: hostCS.borderBottomWidth,
      left: hostCS.borderLeftWidth,
      right: hostCS.borderRightWidth,
    };
    const insets = {
      top: cr.top - (hr.top + lenPx(borderWidths.top)),
      bottom: hr.bottom - lenPx(borderWidths.bottom) - cr.bottom,
      left: cr.left - (hr.left + lenPx(borderWidths.left)),
      right: hr.right - lenPx(borderWidths.right) - cr.right,
    };
    const clips = hostCS.overflow === "hidden" || hostCS.overflow === "clip";
    const shortSide = Math.min(cr.width, cr.height);

    const bad: string[] = [];
    let worst: {
      verdict: string;
      childRadius: number;
      ideal: number | null;
      insetH: number;
      insetV: number;
      parentRadius: number;
      parentBorder: number;
      note: string;
    } | null = null;
    let exemptWhy: string | null = null;

    for (const [name, prop, vEdge, hEdge] of CORNERS) {
      const childR = cornerPx(cs, prop, cr.width, cr.height);
      const parentR = cornerPx(hostCS, prop, hr.width, hr.height);
      if (parentR <= 0) {
        continue;
      }
      if (childR >= shortSide / 2 - tol && childR > 0) {
        exemptWhy = "stadium or circle (a shape, not a nesting)";
        continue;
      }
      const insetV = Math.round(insets[vEdge] * 100) / 100;
      const insetH = Math.round(insets[hEdge] * 100) / 100;
      if (Math.abs(insetV) <= tol && Math.abs(insetH) <= tol && clips) {
        exemptWhy = "flush inside a clipping parent (takes its corner)";
        continue;
      }

      // A corner joins TWO edges, so the concentric rule subtracts a distance
      // per edge — the inset PLUS that edge's own parent border. Reading one
      // border for both is wrong wherever the parent's widths differ, which is
      // exactly the shape a one-sided rule (a leading accent rail) produces.
      const borderV = lenPx(borderWidths[vEdge]);
      const borderH = lenPx(borderWidths[hEdge]);
      const gapV = Math.round((insetV + borderV) * 100) / 100;
      const gapH = Math.round((insetH + borderH) * 100) / 100;

      // IS THIS CORNER A NESTING AT ALL? Only where the child's corner lies in
      // the parent's own arc region: no further in than where that arc ENDS
      // (`parentRadius` from each edge, past which the child sits against a
      // straight run with no arc to share a centre with), and not OUTSIDE the
      // parent's border box at all (a negative inset — an overflowing child is
      // not nested in that corner either; `.pill-expand-content` opens upward and
      // is the live case).
      //
      // Judging every rounded descendant is what made this walker's first live run
      // 59 findings of which one was real: a close button at a header's leading
      // edge reports a 762px inset on its trailing corners, and a card's own
      // header reports the card's whole height on its bottom pair, so every small
      // child and every full-width band came back "anisotropic" for being exactly
      // where it belongs. With the gate: 37 corners judged instead of 2256.
      //
      // Not an exemption and not a finding — the two corners simply are not a
      // pair, so they are skipped silently and `pairs` counts what was judged.
      const nests = (gap: number): boolean => gap >= -tol && gap <= parentR + tol;
      if (!nests(gapV) || !nests(gapH)) {
        continue;
      }
      pairs += 1;

      if (Math.abs(gapV - gapH) > tol) {
        bad.push(name);
        worst ??= {
          verdict: "anisotropic",
          childRadius: Math.round(childR * 100) / 100,
          ideal: null,
          insetH,
          insetV,
          parentRadius: Math.round(parentR * 100) / 100,
          parentBorder: borderV,
          note: `unsatisfiable at any radius: the two edges of this corner are ${gapV} and ${gapH} in — fix the inset or the border, not the radius`,
        };
        continue;
      }
      const ideal = Math.max(0, Math.round((parentR - gapV) * 100) / 100);
      if (Math.abs(childR - ideal) <= tol) {
        continue;
      }
      bad.push(name);
      worst ??= {
        verdict: childR > ideal ? "too-round" : "too-square",
        childRadius: Math.round(childR * 100) / 100,
        ideal,
        insetH,
        insetV,
        parentRadius: Math.round(parentR * 100) / 100,
        parentBorder: borderV,
        note: "",
      };
    }

    if (bad.length > 0 && worst !== null) {
      findings.push([
        `${describeEl(child)}|${describeEl(host)}|${worst.verdict}|${bad.join(",")}`,
        {
          ...worst,
          corners: bad.join(","),
          childPaints: paints,
          child: describeEl(child),
          parent: describeEl(host),
        },
      ]);
    } else if (exemptWhy !== null) {
      exempt.push([
        `${describeEl(child)}|${describeEl(host)}|${exemptWhy}`,
        { why: exemptWhy, child: describeEl(child), parent: describeEl(host) },
      ]);
    }
  }

  return {
    route: routeOf(),
    rounded: rounded.length,
    pairs,
    findings: bucket(findings),
    unmeasurable: bucket(unmeasurable),
    exempt: bucket(exempt),
  };
}

// --- colour, for contrast ---------------------------------------------------

/** A CSS colour as sRGB bytes plus alpha, parsed by PAINTING it.
 *
 *  The canvas is the parser because `getComputedStyle` hands back the AUTHORED
 *  form: measured in Chromium 152, an `oklch()` colour computes to `oklch(...)`
 *  and a `color-mix()` to `oklab(...)`, and `fillStyle` round-trips both unchanged
 *  rather than normalising them. A 1x1 `getImageData` read answers exact sRGB
 *  bytes for every syntax the engine accepts, alpha included (0.5 -> 128,
 *  0.4 -> 102), which is 8-bit precision — well inside the two decimals a contrast
 *  ratio is quoted to.
 *
 *  The input must be a COMPUTED value or a `CSS.supports`-validated string: an
 *  invalid assignment to `fillStyle` is silently ignored, so garbage would read as
 *  whatever was painted last. */
function parseColor(css: string): { r: number; g: number; b: number; a: number } {
  const memo = parseColor as unknown as { cx?: CanvasRenderingContext2D };
  if (memo.cx === undefined) {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const cx = canvas.getContext("2d", { willReadFrequently: true });
    if (cx === null) {
      return { r: 0, g: 0, b: 0, a: 1 };
    }
    memo.cx = cx;
  }
  const cx = memo.cx;
  cx.clearRect(0, 0, 1, 1);
  cx.fillStyle = css;
  cx.fillRect(0, 0, 1, 1);
  const d = cx.getImageData(0, 0, 1, 1).data;
  return { r: d[0] ?? 0, g: d[1] ?? 0, b: d[2] ?? 0, a: (d[3] ?? 0) / 255 };
}

/** `#rrggbb`, for a report a reader compares by eye against a token table. */
function hexOf(c: { r: number; g: number; b: number }): string {
  const p = (n: number): string =>
    Math.max(0, Math.min(255, Math.round(n)))
      .toString(16)
      .padStart(2, "0");
  return `#${p(c.r)}${p(c.g)}${p(c.b)}`;
}

/** Source-over: `fg` at its own alpha onto an opaque `bg`. */
function over(
  fg: { r: number; g: number; b: number; a: number },
  bg: { r: number; g: number; b: number; a: number },
): { r: number; g: number; b: number; a: number } {
  const a = fg.a;
  return {
    r: fg.r * a + bg.r * (1 - a),
    g: fg.g * a + bg.g * (1 - a),
    b: fg.b * a + bg.b * (1 - a),
    a: 1,
  };
}

/** WCAG 2.x relative luminance, and the ratio over it. Written out rather than
 *  approximated because the whole walker exists to produce a number someone acts
 *  on. */
function contrastRatio(
  x: { r: number; g: number; b: number },
  y: { r: number; g: number; b: number },
): number {
  const lum = (c: { r: number; g: number; b: number }): number => {
    const ch = (v: number): number => {
      const s = v / 255;
      return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
    };
    return 0.2126 * ch(c.r) + 0.7152 * ch(c.g) + 0.0722 * ch(c.b);
  };
  const a = lum(x);
  const b = lum(y);
  const hi = Math.max(a, b);
  const lo = Math.min(a, b);
  return (hi + 0.05) / (lo + 0.05);
}

/** Every `opacity` from this element to the root, multiplied.
 *
 *  An ancestor's opacity dims the element AND its own background, so this is
 *  applied to the text's alpha and, separately, to each background layer's — which
 *  is what keeps a dimmed box's text from being measured against an undimmed
 *  surface. */
function foldedOpacity(el: Element): number {
  let out = 1;
  let cur: Element | null = el;
  while (cur !== null) {
    const o = Number.parseFloat(getComputedStyle(cur).opacity);
    if (Number.isFinite(o)) {
      out *= o;
    }
    cur = cur.parentElement;
  }
  return out;
}

/** Which ancestors (this element included) are dimming it, outermost last. */
function dimmedBy(el: Element): string[] {
  const out: string[] = [];
  let cur: Element | null = el;
  while (cur !== null) {
    const o = Number.parseFloat(getComputedStyle(cur).opacity);
    if (Number.isFinite(o) && o < 1) {
      out.push(`${describeEl(cur)}@${Math.round(o * 100) / 100}`);
    }
    cur = cur.parentElement;
  }
  return out;
}

/** The surface this element's text actually paints on: every background layer from
 *  here outward, each dimmed by its own opacity chain, composited onto the UA's
 *  white canvas.
 *
 *  A `background-image` REFUSES rather than guesses — a gradient or a photo has no
 *  single colour to be measured against, so the element is reported unresolved and
 *  a reader composites it by hand or probes the pixels. */
function surfaceUnder(el: Element): {
  color: { r: number; g: number; b: number; a: number } | null;
  from: string;
  imageFrom: string | null;
} {
  const layers: { c: { r: number; g: number; b: number; a: number }; who: string }[] = [];
  let cur: Element | null = el;
  while (cur !== null) {
    const cs = getComputedStyle(cur);
    if (cs.backgroundImage !== "none") {
      return { color: null, from: describeEl(cur), imageFrom: describeEl(cur) };
    }
    const c = parseColor(cs.backgroundColor);
    if (c.a > 0) {
      const dim = foldedOpacity(cur);
      layers.push({ c: { ...c, a: c.a * dim }, who: describeEl(cur) });
      if (c.a >= 1 && dim >= 1) {
        break;
      }
    }
    cur = cur.parentElement;
  }
  // The UA paints white under everything, so an all-transparent chain is white
  // rather than unknown.
  let out = { r: 255, g: 255, b: 255, a: 1 };
  for (let i = layers.length - 1; i >= 0; i -= 1) {
    const layer = layers[i];
    if (layer !== undefined) {
      out = over(layer.c, out);
    }
  }
  return {
    color: out,
    from: layers.length === 0 ? "the UA canvas" : layers.map((l) => l.who).join(" < "),
    imageFrom: null,
  };
}

/** A custom property's value at this element, when it is a colour.
 *
 *  `CSS.supports` is the validator rather than a parse attempt, because
 *  `parseColor` cannot report failure — an invalid `fillStyle` assignment is
 *  ignored, so an unparseable token would read as the last colour painted. */
function colorTokenAt(el: Element, name: string): string | null {
  const raw = getComputedStyle(el).getPropertyValue(name).trim();
  if (raw === "") {
    return null;
  }
  return CSS.supports("color", raw) ? raw : null;
}

// --- contrast ---------------------------------------------------------------

/** Every text-painting element against the surface it ACTUALLY paints on, with
 *  every ancestor opacity folded in, floored at WCAG 1.4.3.
 *
 *  WHY THIS EXISTS BESIDE THE STATIC GATES. `vibekit/scripts/css-contrast.py`
 *  audits the TOKEN GRAPH — a declared ink against a declared surface — and that is
 *  the right instrument for a palette. It cannot see three things this can: a
 *  surface that is several semi-transparent layers composited (`--c-hover` washes
 *  over a rung over a card), an ancestor `opacity` dimming ink and box together,
 *  and the REAL font size and weight deciding which floor applies. Those are where
 *  1.4.3 failures actually hide.
 *
 *  TEXT NODES ONLY. An `svg` glyph painting `currentColor` is never scanned, since
 *  1.4.11's 3:1 for a graphical object is a different check with a different floor.
 *  A CHARACTER used as a glyph — a bare check mark in a status badge — IS scanned,
 *  at 4.5:1 where 3:1 would be the honest floor, so read those by eye.
 *
 *  It reports rather than rules on two populations it cannot judge: text over a
 *  background-image (`unresolved`) and text a `mix-blend-mode` or `filter` recolours
 *  at paint time, which this model does not attempt at all. */
function contrast(opts: { inkToken?: string | null }): unknown {
  const SKIP = new Set([
    "SCRIPT",
    "STYLE",
    "TEMPLATE",
    "LINK",
    "META",
    "HEAD",
    "TITLE",
    "NOSCRIPT",
    "OPTION",
  ]);

  // --- the theme-settle check ---
  //
  // A custom property INHERITS, so every element resolves the root's value unless
  // something between them re-declares it. So sampling the ink token at deep leaves
  // is exactly the "a descendant still resolves the previous theme" test: an inner
  // `data-theme`, or a subtree the flip has not reached, shows up as a different
  // value. A `mixed` verdict means the numbers below describe two palettes at once.
  const root = document.documentElement;
  const CANDIDATES = [
    "--c-text-primary",
    "--c-text-secondary",
    "--c-fg",
    "--fg",
    "--color-text",
    "--text",
  ];
  let inkToken: string | null = null;
  if (opts.inkToken === null) {
    inkToken = null;
  } else if (typeof opts.inkToken === "string") {
    inkToken = colorTokenAt(root, opts.inkToken) === null ? null : opts.inkToken;
  } else {
    for (const name of CANDIDATES) {
      if (colorTokenAt(root, name) !== null) {
        inkToken = name;
        break;
      }
    }
  }

  const leaves: Element[] = [];
  for (const el of [...document.body.querySelectorAll("*")]) {
    if (el.children.length === 0 && !hasNoBox(el) && !SKIP.has(el.tagName)) {
      leaves.push(el);
      if (leaves.length >= 40) {
        break;
      }
    }
  }
  let palette = "unavailable";
  let rootInk: string | null = null;
  let leafInk: string | null = null;
  if (inkToken !== null) {
    const rootRaw = colorTokenAt(root, inkToken);
    rootInk = rootRaw === null ? null : hexOf(parseColor(rootRaw));
    palette = "consistent";
    for (const leaf of leaves) {
      const raw = colorTokenAt(leaf, inkToken);
      const hex = raw === null ? null : hexOf(parseColor(raw));
      leafInk ??= hex;
      if (hex !== null && hex !== rootInk) {
        leafInk = hex;
        palette = "mixed";
        break;
      }
    }
    leafInk ??= rootInk;
  }

  interface Finding {
    ratio: number;
    floor: number;
    size: number;
    painted: string;
    bg: string;
    opacity: number;
    opacityFrom: string[];
    sel: string;
    bgFrom: string;
  }
  const findings: (readonly [string, Finding])[] = [];
  const dimmed: (readonly [
    string,
    { ratio: number; floor: number; opacityFrom: string[]; sel: string },
  ])[] = [];
  const unresolved: (readonly [string, { sel: string; bgFrom: string }])[] = [];
  const passes: (readonly [
    string,
    { ratio: number; floor: number; painted: string; bg: string; sel: string },
  ])[] = [];
  const inactive: (readonly [string, { sel: string }])[] = [];
  const backdrops = new Set<string>();
  let scanned = 0;

  for (const el of [...document.body.querySelectorAll("*")]) {
    if (SKIP.has(el.tagName)) {
      continue;
    }
    // A DIRECT text child, so a container is not credited with its children's text
    // and one string is measured once.
    let hasText = false;
    for (const node of [...el.childNodes]) {
      if (node.nodeType === 3 && (node.textContent ?? "").trim() !== "") {
        hasText = true;
        break;
      }
    }
    if (!hasText || hasNoBox(el)) {
      continue;
    }
    const cs = getComputedStyle(el);
    if (cs.visibility !== "visible" || cs.contentVisibility === "hidden") {
      continue;
    }
    // NOT PAINTED, so its contrast is a fact about nothing a reader sees — the same
    // call the screen-reader-only skip below makes. A folded zero is the ordinary
    // state of a collapsed disclosure body and an unexpanded pill card, so without
    // this the report is mostly 1:1 rows for text nobody can look at: measured on
    // the live app, 10 of 15 violations, one of them 82 elements deep in a closed
    // tool region. `--reveal` is how those surfaces get audited.
    const paintedAlpha = foldedOpacity(el);
    if (paintedAlpha <= 0.001) {
      continue;
    }
    // WCAG 1.4.3 exempts an INACTIVE component outright ("text that is part of an
    // inactive user interface component ... has no contrast requirement"), and this
    // app dims a disabled control to 0.4, so every disabled label would otherwise
    // read as a failure. Counted rather than dropped, because "is this really
    // disabled" is a judgement a reader may want to check.
    if (el.closest(":disabled, [aria-disabled='true']") !== null) {
      inactive.push([describeEl(el), { sel: describeEl(el) }]);
      continue;
    }
    const r = el.getBoundingClientRect();
    // A screen-reader-only string is clipped to about a pixel: painted, and not
    // readable, so its contrast is not a fact about anything a reader sees.
    if (r.width < 2 || r.height < 2) {
      continue;
    }

    const sel = describeEl(el);
    const surface = surfaceUnder(el);
    if (surface.color === null) {
      unresolved.push([sel, { sel, bgFrom: surface.imageFrom ?? surface.from }]);
      continue;
    }
    scanned += 1;

    const dim = paintedAlpha;
    const inkRaw = parseColor(cs.color);
    const ink = over({ ...inkRaw, a: inkRaw.a * dim }, surface.color);
    const ratio = Math.round(contrastRatio(ink, surface.color) * 100) / 100;

    const size = Math.round(Number.parseFloat(cs.fontSize) * 100) / 100;
    const weight = Number.parseFloat(cs.fontWeight);
    // WCAG 1.4.3's large-text floor: 18.66px bold (14pt) or 24px (18pt).
    const large = size >= 24 || (Number.isFinite(weight) && weight >= 700 && size >= 18.66);
    const floor = large ? 3 : 4.5;

    const painted = hexOf(ink);
    const bg = hexOf(surface.color);
    backdrops.add(bg);
    const opacity = Math.round(dim * 100) / 100;
    const opacityFrom = dim < 1 ? dimmedBy(el) : [];

    if (ratio + 0.005 < floor) {
      findings.push([
        `${sel}|${painted}|${bg}|${floor}`,
        {
          ratio,
          floor,
          size,
          painted,
          bg,
          opacity,
          opacityFrom,
          sel,
          bgFrom: surface.from,
        },
      ]);
    } else if (dim < 1) {
      // Passing, but under an opacity NO static gate can see — listed so a reader
      // knows the dimming was accounted for rather than missed. A dimmed FAILURE is
      // in `findings` above carrying the same opacity fields, so the two lists never
      // describe one element twice.
      dimmed.push([`${sel}|${painted}|${bg}`, { ratio, floor, opacityFrom, sel }]);
    } else {
      passes.push([`${sel}|${painted}|${bg}`, { ratio, floor, painted, bg, sel }]);
    }
  }

  return {
    route: routeOf(),
    theme: root.dataset["theme"] ?? "unset",
    palette,
    inkToken,
    rootInk,
    leafInk,
    scanned,
    findings: bucket(findings),
    dimmed: bucket(dimmed),
    unresolved: bucket(unresolved),
    inactive: bucket(inactive),
    backdrops: [...backdrops].sort(),
    passes: bucket(passes),
  };
}

// --- the injection contract ------------------------------------------------

/** The helpers `pageSource` emits beside a walker. The KEY is the emitted name,
 *  so it is what a walker body calls; the binding is by string and no type
 *  checker can see it, so renaming a key means renaming its call sites. */
const HELPERS = {
  describeEl,
  lenPx,
  routeOf,
  tokenPx,
  hasNoBox,
  distortedUnder,
  bucket,
  ownsPoint,
  targetExtent,
  inlineInText,
  growsTarget,
  centreOnScreen,
  parseColor,
  hexOf,
  over,
  contrastRatio,
  foldedOpacity,
  dimmedBy,
  surfaceUnder,
  colorTokenAt,
} as const;

/** Every walker a runner may ask for, by the name it passes. */
export const WALKERS: Readonly<Record<string, Walker>> = {
  reveal: reveal,
  "control-height": controlHeight,
  radius: radius,
  contrast: contrast,
};

/** One page-side expression: the helpers, then the walker applied to its options.
 *
 *  An IIFE rather than bare statements because `Runtime.evaluate` returns the
 *  value of an EXPRESSION, and the helpers have to share one scope with the
 *  walker for its calls to resolve. */
export function pageSource(name: string, opts: Readonly<Record<string, unknown>> = {}): string {
  const fn = WALKERS[name];
  if (fn === undefined) {
    throw new Error(`unknown walker "${name}" (known: ${Object.keys(WALKERS).join(", ")})`);
  }
  const prelude = Object.entries(HELPERS)
    .map(([alias, helper]) => `const ${alias} = ${helper.toString()};`)
    .join("\n");
  return `(() => {\n${prelude}\nreturn (${fn.toString()})(${JSON.stringify(opts)});\n})()`;
}
