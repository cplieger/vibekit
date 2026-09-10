// The focus ring on a control its clipping card does not clear, and the clip
// every one of those cards applies to it.
//
// `40-a11y.css` floors every focusable at `outline: var(--focus-ring)` with
// `--focus-offset` (+1px), so the 2px band sits 1..3px OUTSIDE the border box —
// 3px of REACH. Seven of this app's boxes declare `overflow: hidden` with no padding
// of their own, and between them they hold NINE controls the clip reaches: the
// criterion is that measured reach against the distance from the control's border box
// to the clip box, which is wider than being flush. Seven members are flush on the
// edges they lose, a code block's action button sits 2px in and loses 1px of its top
// band, and an account's repo-list `summary` is flush on both inline edges. Where the
// control is the WHOLE card (a delegate as born, a run step) the clip takes all four
// edges at once, which fails WCAG 2.4.7 rather than degrading 2.4.11.
//
// FOUR KINDS OF CLAIM, and none is sufficient alone.
//
// GEOMETRIC, against the real assembled cascade (`mountAppCSS`, `css/MANIFEST`
// order) with `:focus-visible` armed by a REAL `userEvent.tab()` — a programmatic
// `focus()` does not set the flag, and a synthetic event drives no style recalc.
// The instrument is the outline box (`rect ± (outline-width + outline-offset)`)
// against the clipper's PADDING box, which is where `overflow` clips. It reports
// numbers per edge rather than a boolean, so a failure says how far.
//
// SOURCE, because computed style cannot answer it: that ONE rule serves every
// member, that its body is the same body for all of them, and that it declares the
// OFFSET only — the ring's width and colour keep their single owner in the floor.
//
// CLEARANCE, the other half of moving the ring inward: the band now lands inside the
// border box, so each member's own padding has to keep it off that member's content,
// on all four edges rather than the two the ring reaches first.
//
// EXCLUSIONS, so the rule cannot have widened and cannot need to. There are two ways
// a focusable inside a clipper stays out, and both are pinned as a number: its measured
// inset CLEARS the reach, or it declares the inset offset itself so the reach is 0
// (`.turn-file-row`, whose clipper it is flush against). Plus a control outside every
// card keeping the floor's outside ring.
//
// One trap for anyone extending this: `.subagent-block`, `.run-card` and
// `.tool-group` mount with `animation: vk-slide-up`, a 6px translate. It cancels
// out of an overflow measurement (the clipper and its child move together) and not
// out of an absolute one, so the fixtures disable it.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { userEvent } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const a11y = loadCSS("40-a11y.css");

/** Every focusable the inset offset is declared for, as `40-a11y.css` spells it. */
const MEMBERS = [
  "a.subagent-header",
  ".subagent-container.has-disclosure > .subagent-header",
  ".run-head",
  "a.run-step-head",
  ".tool-group-header",
  ".code-act-btn",
  ".git-repo-section-header",
  ".git-repo-section-header-toggle",
  ".forge-account-repos-summary",
] as const;

let style: HTMLStyleElement;
let host: HTMLElement;
/** Where every Tab starts, so focus arrives by keyboard rather than by script. */
let sentinel: HTMLButtonElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
  host = document.createElement("div");
  // A definite width, and at the top of the page: `content-visibility: auto` on two
  // of these cards skips its contents while off-screen, and a skipped subtree
  // reports no boxes at all.
  host.style.cssText = "position:fixed;top:0;left:0;width:774px;";
  sentinel = document.createElement("button");
  sentinel.type = "button";
  sentinel.textContent = "start";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

function node<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  attrs: Record<string, string> = {},
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  n.className = className;
  for (const [name, value] of Object.entries(attrs)) {
    n.setAttribute(name, value);
  }
  return n;
}

/** A labelled span. The text goes on the span rather than on the head, because
 *  `head.textContent = …` after an `append` REPLACES the children — and the
 *  clearance case below measures the head's first CHILD, so a head whose spans were
 *  wiped would have nothing to clear. */
function span(className: string, label: string): HTMLSpanElement {
  const n = node("span", className);
  n.textContent = label;
  return n;
}

/** A glyph the size `iconEl` produces, so a member whose only child is its icon has
 *  a real box for the clearance case to measure. */
function glyph(): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  return svg;
}

/** A card in the styled host, with the sentinel ahead of it and the entry
 *  animation off. */
function mount<T extends HTMLElement>(clipper: T): T {
  clipper.style.animation = "none";
  host.replaceChildren(sentinel, clipper);
  return clipper;
}

/** Tab from the sentinel until focus lands on `target`. A real Tab is what arms
 *  `:focus-visible`; the loop is what lets a case name a control that is not its
 *  card's first focusable. */
async function focusByTab(target: HTMLElement): Promise<void> {
  sentinel.focus();
  for (let i = 0; i < 12; i++) {
    await userEvent.tab();
    if (document.activeElement === target) {
      return;
    }
  }
  throw new Error(`Tab never reached .${target.className}`);
}

interface Edges {
  top: number;
  bottom: number;
  left: number;
  right: number;
}

const px = (v: string): number => Number.parseFloat(v) || 0;
const round = (v: number): number => Math.round(v * 100) / 100;

/** The box `overflow: hidden` clips at: the clipper's PADDING box, which is its
 *  border box less its own border. */
function clipBox(clipper: HTMLElement): Edges {
  const r = clipper.getBoundingClientRect();
  const cs = getComputedStyle(clipper);
  return {
    top: r.top + px(cs.borderTopWidth),
    bottom: r.bottom - px(cs.borderBottomWidth),
    left: r.left + px(cs.borderLeftWidth),
    right: r.right - px(cs.borderRightWidth),
  };
}

/** How far the focus ring's OUTER edge falls outside the clip box, per edge, in
 *  CSS pixels. Positive is clipped; zero or negative is painted. */
function ringOverflow(target: HTMLElement, clipper: HTMLElement): Edges {
  const cs = getComputedStyle(target);
  const reach = px(cs.outlineWidth) + px(cs.outlineOffset);
  const t = target.getBoundingClientRect();
  const c = clipBox(clipper);
  return {
    top: round(c.top - (t.top - reach)),
    bottom: round(t.bottom + reach - c.bottom),
    left: round(c.left - (t.left - reach)),
    right: round(t.right + reach - c.right),
  };
}

/** How far the target's own border box sits inside the clip box, per edge. Zero is
 *  flush; anything under the ring's reach is clipped by that much. */
function inset(target: Element, clipper: HTMLElement): Edges {
  const t = target.getBoundingClientRect();
  const c = clipBox(clipper);
  return {
    top: round(t.top - c.top),
    bottom: round(c.bottom - t.bottom),
    left: round(t.left - c.left),
    right: round(c.right - t.right),
  };
}

const EDGES = ["top", "bottom", "left", "right"] as const;

/** How far the band's outer edge sits outside a control's border box: its
 *  `outline-offset` plus the ring's own width. Read off the control, so a control on
 *  the floor answers 3 and a member answers 0. */
function reachOf(target: HTMLElement): number {
  const cs = getComputedStyle(target);
  return px(cs.outlineWidth) + px(cs.outlineOffset);
}

/** The floor's reach in CSS pixels, and the criterion every inset below is judged
 *  against. A premise case reads it off a real focused control rather than trusting
 *  this constant, which is what a member cannot answer for itself — its own offset is
 *  the inset token, so its reach is 0. */
const FLOOR_REACH = 3;

/** Every edge of the ring is painted inside the clip box. Asserted per edge so a
 *  failure names the edge and its overflow in pixels. */
function expectRingInside(name: string, target: HTMLElement, clipper: HTMLElement): void {
  const over = ringOverflow(target, clipper);
  for (const edge of EDGES) {
    expect(
      over[edge],
      `${name}: ring overflows the clip box at ${edge} by ${over[edge]}px`,
    ).toBeLessThanOrEqual(0.5);
  }
}

/** A focusable the rule leaves on the floor's outside ring, with the clearance that
 *  earns it: the reach against the tightest edge of its measured inset. The number is
 *  what makes the exclusion a fact — an inner padding that thins to under the reach
 *  fails here instead of quietly clipping a ring. */
function expectClears(name: string, target: HTMLElement, clipper: HTMLElement): void {
  expect(getComputedStyle(target).outlineOffset, `${name}: not on the floor's offset`).toBe("1px");
  const reach = reachOf(target);
  const gap = inset(target, clipper);
  for (const edge of EDGES) {
    expect(
      gap[edge],
      `${name}: only ${gap[edge]}px inside the clip box at ${edge}, against ${reach}px of reach`,
    ).toBeGreaterThanOrEqual(reach);
  }
  expectRingInside(name, target, clipper);
}

// ---------------------------------------------------------------------------
// Fixtures, mirroring the builders rather than driving them: the subject is the
// stylesheet, and importing the builders drags the store and the tab projection in
// behind them for facts they do not decide.
// ---------------------------------------------------------------------------

/** `.subagent-block` as `buildSubagentCard` assembles a LEAF: the anchor head, the
 *  tail, the foot. As BORN both of the latter are empty, so the tail measures 0 and
 *  the foot is `display: none` — the head is the whole card. */
function subagentCard(opts: { foot: boolean }): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("a", "subagent-header", { href: "/chat/c/subagent/u-1" });
  head.append(
    node("span", "subagent-icon tool-icon"),
    node("span", "subagent-name"),
    node("span", "subagent-head-chevron", { "aria-hidden": "true" }),
  );
  const tail = node("div", "subagent-tail", { "aria-hidden": "true" });
  const foot = node("div", "subagent-foot");
  if (opts.foot) {
    const footer = node("div", "turn-footer subagent-footer", { role: "note" });
    const summary = node("button", "turn-ledger-summary", { type: "button" });
    summary.append(node("span", "turn-ledger-glyph"), node("span", "turn-ledger-text"));
    summary.textContent = "2 files +12 \u22124";
    footer.appendChild(summary);
    foot.appendChild(footer);
  }
  const clipper = node("div", "subagent-block");
  clipper.append(head, tail, foot);
  return { clipper: mount(clipper), head };
}

/** The CONTAINER shape: the same block, but the head is the disclosure's own
 *  `role="button"` and the body holds its stages. */
function subagentContainer(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "subagent-header", { role: "button", tabindex: "0" });
  head.append(node("span", "subagent-icon tool-icon"), node("span", "subagent-name"));
  const body = node("div", "subagent-body");
  body.appendChild(node("div", "subagent-block")).textContent = "a stage card";
  const clipper = node("div", "subagent-block subagent-container has-disclosure");
  clipper.append(head, body, node("div", "subagent-foot"));
  return { clipper: mount(clipper), head };
}

/** `.run-card` as `run-card.ts` builds its head, plus one step so the head's bottom
 *  edge is not the card's. */
function runCard(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "run-head", { role: "button", tabindex: "0" });
  head.append(span("run-name", "recipe"), node("span", "run-head-meta"));
  const body = node("div", "run-body");
  body.appendChild(node("div", "run-step")).textContent = "a step row";
  const clipper = node("div", "run-card");
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** The same card with its foot, whose link clears the clip box by a single pixel —
 *  one of the two tightest clearances in this population. */
function runCardWithFoot(): { clipper: HTMLElement; open: HTMLElement } {
  const head = node("div", "run-head", { role: "button", tabindex: "0" });
  head.append(span("run-name", "recipe"), node("span", "run-head-meta"));
  const body = node("div", "run-body");
  body.appendChild(node("div", "run-step")).textContent = "a step row";
  const foot = node("div", "run-foot");
  const open = node("a", "run-open", { href: "/run/wf-1" });
  open.textContent = "Open run";
  foot.append(span("run-ledger", "3 steps"), open);
  const clipper = node("div", "run-card");
  clipper.append(head, body, foot);
  return { clipper: mount(clipper), open };
}

/** A step ROW: its head is a door, and it is the row's only child, so all four of
 *  the row's edges are the head's. */
function runStep(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("a", "run-step-head", { href: "/run/wf-1#node=step" });
  head.append(node("span", "run-step-glyph"), span("run-step-name", "build"));
  const clipper = node("div", "run-step");
  clipper.appendChild(head);
  return { clipper: mount(clipper), head };
}

function toolGroup(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "tool-group-header", {
    role: "button",
    tabindex: "0",
    "aria-expanded": "true",
  });
  head.append(node("span", "tool-icon"), span("tool-group-count", "Ran 3 commands"));
  const body = node("div", "tool-group-body");
  body.appendChild(node("div", "tool-call")).textContent = "a member card";
  const clipper = node("div", "tool-group");
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** A code block as `code-blocks.ts` wraps one: `.code-wrap` draws the border, the
 *  radius and the clip, and `.code-head` is a title bar whose 2px of block padding is
 *  the ONLY thing between an action button and that clip. The head is the reason this
 *  member is not flush and is clipped anyway. */
function codeBlock(): { clipper: HTMLElement; copy: HTMLElement; run: HTMLElement } {
  const copy = node("button", "code-act-btn", { type: "button", "aria-label": "Copy" });
  copy.appendChild(glyph());
  const run = node("button", "code-act-btn", { type: "button", "aria-label": "Type in shell" });
  run.appendChild(glyph());
  const actions = node("div", "code-actions");
  actions.append(copy, run);
  const head = node("div", "code-head");
  head.append(span("code-lang", "bash"), actions);
  const pre = document.createElement("pre");
  pre.appendChild(document.createElement("code")).textContent = "echo one\necho two\n";
  const clipper = node("div", "code-wrap", { "data-code-state": "final" });
  clipper.append(head, pre);
  return { clipper: mount(clipper), copy, run };
}

/** The Changes tab's shape: `.git-repo-section-header` IS the button, with the
 *  chevron prepended ahead of the name. */
function gitRepoSection(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("button", "git-repo-section-header", { type: "button" });
  head.append(
    node("span", "disclosure-chevron git-repo-section-chevron", { "aria-hidden": "true" }),
    span("git-repo-section-name", "vibekit"),
  );
  const body = node("div", "git-file-list");
  body.textContent = "one changed file";
  const clipper = node("section", "git-repo-section", { "data-repo": "vibekit" });
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** The PRs tab's shape, and the reason `.git-repo-section` holds TWO focusables:
 *  the same header class becomes a `padding: 0` ROW (a div, so it cannot be the
 *  button — the + New PR button would nest inside one), hosting the toggle that
 *  discloses the section. The toggle carries the header's padding itself, which
 *  insets its text and not its box, so it is flush with the clip box on the top and
 *  the left while the button beside it holds the right edge. */
function gitRepoSectionPRs(): {
  clipper: HTMLElement;
  head: HTMLElement;
  newBtn: HTMLElement;
} {
  const row = node("div", "git-repo-section-header git-repo-section-header-row");
  const head = node("button", "git-repo-section-header-toggle", { type: "button" });
  head.append(
    node("span", "disclosure-chevron git-repo-section-chevron", { "aria-hidden": "true" }),
    node("span", "git-repo-section-forge-icon git-repo-section-forge-github", {
      "aria-hidden": "true",
    }),
    span("git-repo-section-name", "cplieger/vibekit"),
    span("git-repo-section-meta", "2 open"),
  );
  const newBtn = node("button", "btn-small btn-primary", { type: "button" });
  newBtn.textContent = "+ New PR";
  row.append(head, newBtn);
  const body = node("div", "git-repo-section-body");
  body.appendChild(node("div", "git-repo-section-body-inner")).textContent = "a pr row";
  const clipper = node("section", "git-repo-section", { "data-repo": "cplieger/vibekit" });
  clipper.append(row, body);
  return { clipper: mount(clipper), head, newBtn };
}

/** One configured forge account, as `forge-auth.ts` paints its row: the identity and
 *  its actions, then the repo-list `details`. That `details` carries no padding and no
 *  inline border, so its `summary` — a focusable by construction — spans the clip box
 *  edge to edge, and is the card's last box while it is closed.
 *
 *  The `Manage` link is built and deliberately not returned: it is what puts `Sign out`
 *  at the row's trailing edge, and it carries no geometry of its own — same flex row,
 *  same control height, looser on the inline axis — so `Sign out`'s pinned 8px IS its
 *  clearance and a case of its own could only fail when that one does. */
function forgeAccountRow(): {
  clipper: HTMLElement;
  details: HTMLDetailsElement;
  summary: HTMLElement;
  signOut: HTMLElement;
  cloneAll: HTMLElement;
  repoBtn: HTMLElement;
} {
  const manage = node("a", "btn-small forge-account-manage", { href: "https://example.test/" });
  manage.append(span("", "Manage"), glyph());
  const signOut = node("button", "btn-small btn-danger", { type: "button" });
  signOut.textContent = "Sign out";
  const actions = node("div", "forge-account-actions");
  actions.append(manage, signOut);
  const identity = node("div", "forge-account-identity");
  identity.append(
    span("forge-account-primary", "someone@example.test"),
    span("forge-account-meta", "@someone \u00b7 github.com"),
  );
  const top = node("div", "forge-account-row-top");
  top.append(identity, actions);

  const cloneAll = node("button", "btn-small forge-account-repos-clone-all", { type: "button" });
  cloneAll.textContent = "Clone all";
  const summary = node("summary", "forge-account-repos-summary");
  summary.append(
    node("span", "disclosure-chevron forge-account-repos-chevron", { "aria-hidden": "true" }),
    node("span", "forge-account-repos-icon", { "aria-hidden": "true" }),
    span("forge-account-repos-label", "3 repos, 1 cloned locally"),
    cloneAll,
  );
  const repoBtn = node("button", "btn-small", { type: "button" });
  repoBtn.textContent = "Clone";
  const repoRow = node("li", "forge-account-repo-row");
  repoRow.append(
    node("span", "forge-account-repo-state"),
    span("forge-account-repo-name", "owner/repo"),
    repoBtn,
  );
  const list = node("ul", "forge-account-repos-list");
  list.appendChild(repoRow);
  const details = node("details", "forge-account-repos", {
    "data-account-id": "github:github.com",
  }) as HTMLDetailsElement;
  details.append(summary, list);

  const clipper = node("li", "forge-account-row", { "data-id": "github:github.com" });
  clipper.append(top, details);
  const outer = node("ul", "forge-account-list");
  outer.appendChild(clipper);
  mount(outer);
  return { clipper, details, summary, signOut, cloneAll, repoBtn };
}

/** The Changes tab's changed-file list: a clipper of its own, whose rows give their
 *  controls the padding the list has none of. */
function gitFileList(): { clipper: HTMLElement; path: HTMLElement; discard: HTMLElement } {
  const path = node("button", "git-file-path", { type: "button" });
  path.textContent = "static-src/app.ts";
  const stage = node("button", "btn-small", { type: "button" });
  stage.textContent = "Stage";
  const discard = node("button", "btn-small btn-danger", { type: "button" });
  discard.textContent = "Discard";
  const rowActions = node("span", "git-file-actions");
  rowActions.append(stage, discard);
  const top = node("div", "git-file-row-top");
  top.append(span("git-file-status git-st-m", "M"), path, rowActions);
  const row = node("li", "git-file-row");
  row.appendChild(top);
  const clipper = node("ul", "git-file-list", { "aria-label": "Changed" });
  clipper.appendChild(row);
  const inner = node("div", "git-repo-section-body-inner");
  inner.appendChild(clipper);
  const body = node("div", "git-repo-section-body uip-disclosure-region");
  body.appendChild(inner);
  const section = node("section", "git-repo-section", { "data-repo": "vibekit" });
  section.appendChild(body);
  mount(section);
  return { clipper, path, discard };
}

/** A diff pane's chrome, whose whitespace checkbox is its only focusable. ONE shape:
 *  the checkbox lives in `.diff-pane-toolbar` whether or not the pane carries column
 *  labels, so its clearance is that row's own `padding-block` and a label row below it
 *  cannot move the number. */
function diffPane(opts: { labelled: boolean }): { clipper: HTMLElement; toggle: HTMLElement } {
  const toggle = node("input", "", { type: "checkbox" });
  const label = node("label", "diff-pane-ws-toggle");
  label.append(toggle, span("", "Ignore whitespace"));
  const toolbar = node("div", "diff-pane-toolbar");
  toolbar.appendChild(label);
  const clipper = node("div", "diff-pane");
  clipper.appendChild(toolbar);
  if (opts.labelled) {
    const header = node("div", "diff-pane-header");
    header.append(
      span("diff-pane-label diff-pane-label-old", "HEAD"),
      span("diff-pane-label diff-pane-label-new", "working tree"),
    );
    clipper.appendChild(header);
  }
  const body = node("div", "diff-pane-body");
  body.textContent = "diff rows";
  clipper.appendChild(body);
  return { clipper: mount(clipper), toggle };
}

/** A tool card as `tool-card.ts` builds one with a disclosure: `.tool-summary` is the
 *  positioned box and the chevron button is OUT OF FLOW inside it, so no padding
 *  declaration produces that button's clearance. Title-only, and that no longer matters
 *  to the block clearance: the chevron is centred on `.tool-header` rather than on the
 *  whole summary, so a subtitle below it leaves the number where it is. */
function toolCard(): { clipper: HTMLElement; disclosure: HTMLElement } {
  const disclosure = node("button", "tool-disclosure", {
    "aria-expanded": "false",
    "aria-label": "Toggle tool details",
  });
  disclosure.appendChild(node("span", "disclosure-chevron", { "aria-hidden": "true" }));
  const header = node("div", "tool-header");
  header.append(node("span", "tool-icon"), span("tool-title", "Run Command"), disclosure);
  const summary = node("div", "tool-summary has-disclosure");
  summary.appendChild(header);
  const details = node("div", "tool-details");
  details.appendChild(node("div", "tool-output")).textContent = "one line of output";
  const clipper = node("div", "tool-call", { "data-kind": "execute", "data-depth1": "output" });
  clipper.append(summary, details);
  return { clipper: mount(clipper), disclosure };
}

/** One configured MCP server, as `mcp-ui.ts` paints its row. The LIST is the clipper
 *  and carries no padding of its own — its 1px gap is the row separator — so every
 *  control in it takes its inset from `.mcp-row`. */
function mcpRow(): { clipper: HTMLElement; del: HTMLElement } {
  const edit = node("button", "btn-small", { type: "button" });
  edit.textContent = "Edit";
  const del = node("button", "btn-small btn-danger", { type: "button" });
  del.textContent = "Delete";
  const actions = node("div", "mcp-row-actions");
  actions.append(edit, del);
  const check = node("input", "", { type: "checkbox" });
  const toggle = node("label", "toggle mcp-toggle");
  toggle.append(check, node("span", "toggle-slider"));
  const nameLine = node("div", "mcp-row-name-line");
  nameLine.append(node("span", "mcp-dot", { role: "img" }), span("mcp-row-name", "github"));
  const meta = node("div", "mcp-row-meta");
  meta.appendChild(span("mcp-row-meta-text", "stdio"));
  const body = node("div", "mcp-row-body");
  body.append(nameLine, meta);
  const row = node("div", "mcp-row", { "data-server-id": "github" });
  row.append(toggle, body, actions);
  const clipper = node("div", "mcp-server-list");
  clipper.appendChild(row);
  return { clipper: mount(clipper), del };
}

/** A turn footer with its file list OPEN, which is the only state that list has a box
 *  in: closed it is `display: none`. The list is a clipper of its own — `overflow:
 *  clip` rather than the `hidden` every card above uses — over a row that spans it.
 *  The transition is off because `@starting-style` animates the list's block-size up
 *  from 0 on the frame it first renders open, so a box measured during it is partial. */
function turnLedgerFiles(): { clipper: HTMLElement; row: HTMLElement } {
  const row = node("button", "turn-file-row", { type: "button" });
  row.append(span("turn-file-path", "static-src/app.ts"), node("span", "turn-file-delta"));
  const item = node("li", "turn-ledger-file");
  item.appendChild(row);
  const list = node("ul", "turn-ledger-files");
  list.style.transition = "none";
  list.appendChild(item);
  const summary = node("button", "turn-ledger-summary", { type: "button" });
  summary.append(node("span", "turn-ledger-glyph"), span("turn-ledger-text", "1 file +3 \u22121"));
  const footer = node("div", "turn-footer", { role: "note", "data-files": "open" });
  footer.append(summary, node("time", "turn-elapsed"), list);
  mount(footer);
  return { clipper: list, row };
}

/** The model pill's card, which gives its own padding to the scroller so the effort
 *  section can bleed to both edges — so both of its control kinds take their inset
 *  from an inner element. `is-open` because the resting state is `scale(0.4)`, and a
 *  transformed box reports scaled boxes against unscaled computed borders. */
function modelCard(): { clipper: HTMLElement; item: HTMLElement; tier: HTMLElement } {
  const item = node("button", "pill-model-item", { type: "button", role: "option" });
  item.append(span("", "a-model"), span("pill-model-meta", "1x"));
  const scroll = node("div", "pill-model-scroll");
  scroll.appendChild(item);
  const row = node("div", "effort-row");
  // The caption names the dimension AND the live tier, which is what lets the knob
  // below carry no text at all.
  const caption = span("effort-label", "Effort: ");
  caption.appendChild(span("effort-value", "high"));
  row.appendChild(caption);
  const track = node("div", "effort-track", { "data-tiers": "3" });
  for (const level of ["low", "medium", "high"]) {
    track.appendChild(node("span", "effort-tick", { "data-level": level }));
  }
  // The knob is the section's only tab stop, so it is the control this card's
  // clipper has to clear.
  const tier = node("div", "effort-knob", {
    role: "slider",
    tabindex: "0",
    "aria-label": "Reasoning effort",
    "data-level": "high",
  });
  track.appendChild(tier);
  row.appendChild(track);
  const clipper = node("span", "pill-expand-content pill-model-list is-open");
  clipper.append(scroll, row);
  const slot = node("span", "pill-slot");
  slot.appendChild(clipper);
  mount(slot);
  return { clipper, item, tier };
}

// ---------------------------------------------------------------------------

describe("the premise: a real Tab arms the floor's ring", () => {
  it("puts a 2px solid outline on the head of a delegate's card", async () => {
    const { head } = subagentCard({ foot: false });
    await focusByTab(head);
    expect(head.matches(":focus-visible")).toBe(true);
    const cs = getComputedStyle(head);
    expect(cs.outlineStyle).toBe("solid");
    expect(cs.outlineWidth).toBe("2px");
  });

  it("reaches 3px outside the border box, which is the criterion itself", async () => {
    // What every inset below is judged against, read off a control the rule does not
    // name rather than restated: `--focus-offset` plus the ring's own width. A token
    // change moves the criterion with it and fails here.
    const { newBtn } = gitRepoSectionPRs();
    await focusByTab(newBtn);
    expect(reachOf(newBtn)).toBe(FLOOR_REACH);
  });
});

describe("the premise: every one of these cards clips at its padding box", () => {
  // The fact behind the rule's own comment. Removing the clip is NOT the remedy for
  // the first two: `content-visibility: auto` applies paint containment on its own
  // and clips to the same edge, so `overflow: visible` alone changes nothing. For
  // the others the clip is what rounds the corners under a flush child's hover
  // fill, which is the same argument `31-exec-view.css` records for its group box.
  // One fixture at a time, because `mount` replaces the host's children: a
  // computed style read off a detached element answers the empty string, which is
  // not a value any assertion here means.
  it("with paint containment as well, on the two card ROOTS", () => {
    for (const build of [() => subagentCard({ foot: false }), runCard]) {
      const { clipper } = build();
      const cs = getComputedStyle(clipper);
      expect(cs.overflow, clipper.className).toBe("hidden");
      expect(cs.contentVisibility, clipper.className).toBe("auto");
    }
  });

  it("by overflow alone, on the five whose corners the clip rounds", () => {
    for (const build of [runStep, toolGroup, codeBlock, gitRepoSection, forgeAccountRow]) {
      const { clipper } = build();
      expect(getComputedStyle(clipper).overflow, clipper.className).toBe("hidden");
    }
  });
});

describe("the ring is painted inside the card that clips it", () => {
  it("on a delegate's card as BORN, where every edge is the card's", async () => {
    // The worst of the states, and the ordinary one: an empty tail measures 0 and an
    // empty foot is `display: none`, so the head IS the card and the clip reaches all
    // four edges at once. Every delegate is here the moment it starts, and Tab reaches
    // it.
    const { clipper, head } = subagentCard({ foot: false });
    await focusByTab(head);
    const flush = inset(head, clipper);
    for (const edge of EDGES) {
      expect(flush[edge], `born card: ${edge} edge is not flush (${flush[edge]}px)`).toBeLessThan(
        0.5,
      );
    }
    expectRingInside("born card", head, clipper);
  });

  it("on a delegate's card with a foot, flush on three edges", async () => {
    const { clipper, head } = subagentCard({ foot: true });
    await focusByTab(head);
    const flush = inset(head, clipper);
    expect(flush.top).toBeLessThan(0.5);
    expect(flush.left).toBeLessThan(0.5);
    expect(flush.right).toBeLessThan(0.5);
    expect(flush.bottom).toBeGreaterThan(0.5);
    expectRingInside("leaf head", head, clipper);
  });

  it("on a pipeline CONTAINER's head, the disclosure shape on that same class", async () => {
    // One class, two focusable shapes: the leaf's head is an anchor to the delegate's
    // page and this one is the disclosure's own `role="button"` + `tabindex="0"`, both
    // on a `.subagent-block`. The rule names each spelling, so a card that swaps
    // between them keeps the ring either way.
    const { clipper, head } = subagentContainer();
    await focusByTab(head);
    expectRingInside("container head", head, clipper);
  });

  it("on a run card's head", async () => {
    const { clipper, head } = runCard();
    await focusByTab(head);
    expectRingInside("run head", head, clipper);
  });

  it("on a run step's head, where every edge is the row's", async () => {
    const { clipper, head } = runStep();
    await focusByTab(head);
    const flush = inset(head, clipper);
    for (const edge of EDGES) {
      expect(flush[edge], `run step: ${edge} edge is not flush (${flush[edge]}px)`).toBeLessThan(
        0.5,
      );
    }
    expectRingInside("run step head", head, clipper);
  });

  it("on a tool group's header", async () => {
    const { clipper, head } = toolGroup();
    await focusByTab(head);
    expectRingInside("tool group header", head, clipper);
  });

  it("on a code block's action buttons, which are 2px inside rather than flush", async () => {
    // The MILDEST member and the one the reach is the whole criterion for: `.code-head`
    // supplies 2px of block padding against 3px of reach, so 1px of the top band is
    // lost while the other three edges are clear. A criterion keyed on flushness, or on
    // the mere existence of an inner element's padding, does not see this.
    const { clipper, copy, run } = codeBlock();
    await focusByTab(copy);
    const gap = inset(copy, clipper);
    expect(gap.top, `code copy button: block-start inset against the floor's reach`).toBe(2);
    expect(gap.top).toBeLessThan(FLOOR_REACH);
    expect(gap.right, "the other three edges are clear").toBeGreaterThanOrEqual(FLOOR_REACH);
    expectRingInside("code copy button", copy, clipper);
    await focusByTab(run);
    expectRingInside("code run button", run, clipper);
  });

  it("on a git repo section's header", async () => {
    const { clipper, head } = gitRepoSection();
    await focusByTab(head);
    expectRingInside("git section header", head, clipper);
  });

  it("on the PRs tab's toggle, the second focusable in that same card", async () => {
    // One clipper, two focusable header shapes: the class the rule already named is
    // a `padding: 0` row here, and the toggle nested inside it is what a reader tabs
    // to. Flush on the top and the left only, so this is the milder class — two
    // edges of ring rather than four — and the same defect on the same surface.
    const { clipper, head } = gitRepoSectionPRs();
    await focusByTab(head);
    const flush = inset(head, clipper);
    expect(flush.top).toBeLessThan(0.5);
    expect(flush.left).toBeLessThan(0.5);
    expect(flush.bottom).toBeGreaterThan(0.5);
    expect(flush.right).toBeGreaterThan(0.5);
    expectRingInside("prs section toggle", head, clipper);
  });

  it("on an account's repo-list summary, flush on both inline edges in either state", async () => {
    // A `<summary>` is focusable by construction and the floor's first arm names it,
    // and this one is a full-bleed child of a `padding: 0` `details` inside a
    // `padding: 0` clipping card — so its padding insets its text and not its box,
    // exactly like the PRs toggle. Closed it is also the card's last box, which takes
    // the block-end edge as well.
    const { clipper, details, summary } = forgeAccountRow();
    await focusByTab(summary);
    const closed = inset(summary, clipper);
    expect(closed.left).toBeLessThan(0.5);
    expect(closed.right).toBeLessThan(0.5);
    expect(closed.bottom, "closed: a following box would take the block-end edge").toBeLessThan(
      0.5,
    );
    expectRingInside("account repos summary (closed)", summary, clipper);

    details.open = true;
    await focusByTab(summary);
    const open = inset(summary, clipper);
    expect(open.left).toBeLessThan(0.5);
    expect(open.right).toBeLessThan(0.5);
    expect(open.bottom).toBeGreaterThan(0.5);
    expectRingInside("account repos summary (open)", summary, clipper);
  });
});

describe("the inset band clears the content it now sits over", () => {
  // The other half of moving the ring inward: the band lands in [0, |offset|] INSIDE
  // the border box, so a member whose padding is thinner than the offset would paint
  // it over its own content. Nothing else states that.
  it("on every member, on all four edges", async () => {
    // All four rather than the two the band reaches first: it lands the same depth in
    // on every edge, so a member spelling `padding-block: 8px 0` would pass a
    // block-start-only guard and paint over its own last child. The first child
    // answers for block-start and inline-start, the last for block-end and inline-end,
    // which needs no symmetry premise about the padding.
    const builders: [string, () => HTMLElement][] = [
      ["leaf head", () => subagentCard({ foot: false }).head],
      ["container head", () => subagentContainer().head],
      ["run head", () => runCard().head],
      ["run step head", () => runStep().head],
      ["tool group header", () => toolGroup().head],
      ["code action button", () => codeBlock().copy],
      ["git section header", () => gitRepoSection().head],
      ["prs section toggle", () => gitRepoSectionPRs().head],
      ["account repos summary", () => forgeAccountRow().summary],
    ];
    for (const [name, build] of builders) {
      const head = build();
      await focusByTab(head);
      const cs = getComputedStyle(head);
      // A negative offset puts the outline's own edge that far inside the border box
      // and paints outward from it, so the band's depth IS the offset's magnitude.
      const depth = -px(cs.outlineOffset);
      expect(depth, `${name}: offset is not the inset token`).toBeGreaterThan(0);
      const first = head.firstElementChild;
      const last = head.lastElementChild;
      if (first === null || last === null) {
        throw new Error(`${name}: the fixture built no child to clear`);
      }
      const near = inset(first, head);
      const far = inset(last, head);
      expect(
        near.top,
        `${name}: the band reaches its first child at block-start`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        near.left,
        `${name}: the band reaches its first child at inline-start`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        far.bottom,
        `${name}: the band reaches its last child at block-end`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        far.right,
        `${name}: the band reaches its last child at inline-end`,
      ).toBeGreaterThanOrEqual(depth);
    }
  });
});

describe("the exclusions: a focusable whose clipper clears the reach", () => {
  it("leaves the card's own FOOT button on the floor's outside ring", async () => {
    // `.subagent-foot > .subagent-footer` insets its controls by
    // `padding-inline: var(--sp-2)` and `.turn-footer` by `padding-block: var(--sp-1)`,
    // so the band clears the clip box on every edge.
    const { clipper } = subagentCard({ foot: true });
    const summary = clipper.querySelector<HTMLElement>(".turn-ledger-summary");
    if (summary === null) {
      throw new Error("the fixture built no ledger button");
    }
    await focusByTab(summary);
    expectClears("ledger button", summary, clipper);
    // Outside its OWN border box, which is what the floor means and what the nine
    // members give up.
    const own = ringOverflow(summary, summary);
    expect(own.top).toBeGreaterThan(0);
    expect(own.left).toBeGreaterThan(0);
  });

  it("pins `.run-open`, which clears the run card by one pixel", async () => {
    // One of the two tightest clearances here, an account's repo row being the other:
    // the foot's 4px of `padding-block` against 3px of reach, so a foot that ever loses
    // it joins the rule and this is where that shows up.
    const card = runCardWithFoot();
    await focusByTab(card.open);
    expect(inset(card.open, card.clipper).bottom).toBe(reachOf(card.open) + 1);
    expectClears("run foot link", card.open, card.clipper);
  });

  it("pins the + New PR button, which shares a clipper with a member", async () => {
    const { clipper, newBtn } = gitRepoSectionPRs();
    await focusByTab(newBtn);
    expectClears("+ New PR", newBtn, clipper);
  });

  it("pins the account row's own controls, which the summary beside them does not", async () => {
    // One clipper, two verdicts, decided per control rather than per card:
    // `.forge-account-row-top` and `.forge-account-repo-row` inset their controls with
    // real padding (8px and 4px), while the `summary` between them carries padding that
    // insets only its text.
    const { clipper, details, signOut, cloneAll, repoBtn } = forgeAccountRow();
    await focusByTab(signOut);
    expect(inset(signOut, clipper).top).toBe(8);
    expectClears("account Sign out", signOut, clipper);
    await focusByTab(cloneAll);
    expect(inset(cloneAll, clipper).bottom).toBe(8);
    expectClears("account Clone all", cloneAll, clipper);
    details.open = true;
    await focusByTab(repoBtn);
    expect(inset(repoBtn, clipper).bottom).toBe(reachOf(repoBtn) + 1);
    expectClears("account repo Clone", repoBtn, clipper);
  });

  it("pins the changed-file list's controls", async () => {
    const { clipper, path, discard } = gitFileList();
    await focusByTab(path);
    expect(inset(path, clipper).top).toBe(14);
    expectClears("git file path", path, clipper);
    await focusByTab(discard);
    expect(inset(discard, clipper).top).toBe(8);
    expectClears("git file Discard", discard, clipper);
  });

  it("pins the diff pane's whitespace checkbox against its toolbar's padding", async () => {
    // The checkbox is the tallest thing in that row, so the clearance is
    // `.diff-pane-toolbar`'s `padding-block` exactly: 4px against 3px of reach.
    const { clipper, toggle } = diffPane({ labelled: false });
    await focusByTab(toggle);
    expect(inset(toggle, clipper).top, "the toolbar's own padding-block").toBe(4);
    expectClears("whitespace checkbox", toggle, clipper);
  });

  it("keeps that number when the pane also carries column labels", async () => {
    // The label row is a SIBLING of the toolbar rather than the line the checkbox
    // shares, which is what makes the two shapes one number instead of two — and is
    // also what keeps each caption over its own column.
    const { clipper, toggle } = diffPane({ labelled: true });
    await focusByTab(toggle);
    expect(inset(toggle, clipper).top).toBe(4);
    expectClears("whitespace checkbox (labelled pane)", toggle, clipper);
  });

  it("pins the tool card's disclosure, whose clearance is no padding at all", async () => {
    // The one exclusion here that no padding declaration produces: the chevron is
    // `position: absolute` inside `.tool-header`, so its inline clearance is a declared
    // `inset-inline-start` and its block clearance is half the header's spare room —
    // which moves with that row's HEIGHT (a `min-height`, a font-size) rather than with
    // anything a reader would check as padding.
    //
    // LEADING since 2026-09 (chevron.ts: a disclosure chevron leads and rotates, and
    // only a navigating one trails), so this reads the START inset where it used to
    // read the END one.
    const { clipper, disclosure } = toolCard();
    await focusByTab(disclosure);
    const gap = inset(disclosure, clipper);
    expect(gap.left, "a declared inset-inline-start").toBe(12);
    expect(gap.top, "half the header's spare block room").toBe(6);
    expectClears("tool card disclosure", disclosure, clipper);
  });

  it("pins the MCP row's action buttons", async () => {
    // The list carries no padding, so `.mcp-row`'s own is the whole clearance on the
    // block axis and the trailing gutter holds the inline-end edge.
    const { clipper, del } = mcpRow();
    await focusByTab(del);
    const gap = inset(del, clipper);
    expect(gap.top).toBe(8);
    expect(gap.bottom).toBe(8);
    expectClears("mcp row Delete", del, clipper);
  });

  it("pins the model card's rows and tiers", async () => {
    // The card gives its padding to `.pill-model-scroll` so the effort section can
    // bleed to both edges, and both are still 8px in from the clip box.
    const { clipper, item, tier } = modelCard();
    await focusByTab(item);
    expect(inset(item, clipper).top).toBe(8);
    expectClears("model row", item, clipper);
    await focusByTab(tier);
    // 8, the row's own --sp-2, with nothing between: the knob is centred in a rail
    // LINE reserved at exactly its own height, and that line carries no border — the
    // hairline that used to sit between them moved onto the thin rail inside it, and
    // read 9 here while it was the track's.
    expect(inset(tier, clipper).bottom).toBe(8);
    expectClears("effort tier", tier, clipper);
  });

  it("leaves a control outside every card alone", async () => {
    // A plain button, and NOT the obvious candidate: a top-level `.ev-row` looks like
    // the unclipped twin of this rule's members and cannot serve as one, because
    // `exec-view/tree.ts` builds every row with `tabindex="-1"` and nothing installs
    // roving focus over them — so the floor's `[tabindex]:not([tabindex="-1"])` arm
    // never matches, a real Tab cannot reach the row, and there is no ring to measure.
    const plain = node("button", "btn", { type: "button" });
    plain.textContent = "unclipped";
    const bare = node("div", "focus-ring-clip-bare-host");
    bare.appendChild(plain);
    mount(bare);
    await focusByTab(plain);
    expect(getComputedStyle(plain).outlineOffset).toBe("1px");
    const own = ringOverflow(plain, plain);
    for (const edge of EDGES) {
      expect(own[edge], `unclipped control: ring is not outside at ${edge}`).toBeGreaterThan(0);
    }
  });
});

describe("the exclusion that is not a clearance: a row already on the inset token", () => {
  it("keeps `.turn-file-row`'s band inside a list it is flush against", async () => {
    // `.turn-ledger-files` clips over a row that spans it, so on the floor's reach that
    // row would lose its whole band — the four-edge case the members are here for. It
    // stays out of the one rule because `29-turns.css` declares the same inset offset
    // for it, taking its reach to 0, and that is what this pins: there is no clearance
    // to measure, so the number is the reach itself.
    const { clipper, row } = turnLedgerFiles();
    await focusByTab(row);
    expect(getComputedStyle(clipper).overflow, "the file list clips").toBe("clip");
    const flush = inset(row, clipper);
    for (const edge of EDGES) {
      expect(
        flush[edge],
        `turn file row: ${edge} edge is not flush (${flush[edge]}px)`,
      ).toBeLessThan(0.5);
    }
    expect(reachOf(row), "the row's own offset cancels the ring's reach").toBe(0);
    expectRingInside("turn file row", row, clipper);
  });
});

describe("read as source: one rule, one owner for the ring", () => {
  it("declares the inset offset once for every member", () => {
    // `ruleContaining` requires exactly one rule per selector, so a member that
    // gained a second home fails there. The bodies being ONE body is what makes it a
    // rule rather than nine that can drift.
    const bodies = new Set(
      MEMBERS.map((m) => ruleContaining(a11y, `${m}:focus-visible`, "top").body),
    );
    expect([...bodies]).toHaveLength(1);
  });

  it("declares the OFFSET and nothing else, so the floor keeps the ring", () => {
    const rule = ruleContaining(a11y, "a.subagent-header:focus-visible", "top");
    expect(rule.body).toMatch(/outline-offset:\s*var\(--focus-offset-inset\)/u);
    // A second spelling of the ring is what `css-tokens.node.test.ts` exists to
    // catch; restating it here would be one more place for the width and the colour
    // to disagree with the floor. The leading alternation is what covers a declaration
    // opening the body: requiring a character before `outline` lets that one through,
    // and only prettier's formatting makes it unreachable today.
    expect(rule.body).not.toMatch(/(^|[^-])outline\s*:/u);
  });
});
