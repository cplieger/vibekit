// ---------------------------------------------------------------------------
// Tool-card rendering: one builder used by both the live `addToolCall`
// flow and scroll-back replay. Replaces the two drifting code paths that
// each accreted their own decoration logic.
//
// The builder produces a ready-to-mount `.tool-call` element with all
// dataset fields set, all click handlers wired, and (for file-writing
// tools) the inline diff preview inserted. Callers only need to append
// it into a tool group or equivalent container.
// ---------------------------------------------------------------------------

import type { ToolStatus, TextSpan } from "./types.js";
import type { BuildToolCardOpts } from "./tool-card-opts.js";
import { escText, windowOutput, windowSpans } from "./strings.js";
import { renderOutput } from "./output-render.js";
import { linkifyPaths } from "./linkify.js";
import { fileIcon, toolIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { chevronEl } from "./chevron.js";
import { openFileDiff } from "./editor-openers.js";
import { openChange, openAtLine } from "./navigate.js";
import { lineDiff, windowHunks, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import { wireRowToggle } from "./disclosure-row.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import {
  renderInfoFor,
  disclosedClaim,
  formatMCPToolName,
  toolDepth1,
  hasDepth1,
  isToolActive,
  isToolDone,
  type ToolRenderInfo,
} from "./tool-schema.js";
import type { ToolDenial } from "./types.js";
import { trackInProgress } from "./tool-group.js";
import { el } from "@cplieger/reactive";

/** Build a tool-call element. Does not append it to the DOM. */
export function buildToolCard(opts: BuildToolCardOpts): HTMLDivElement {
  const info = renderInfoFor(opts.title, opts.kind, opts.input, {
    disclosed: opts.disclosed,
    denial: opts.denial,
  });
  const depth1 = toolDepth1(info.kind);
  const rawTitle = opts.title.startsWith("Running: ") ? opts.title.slice(9) : opts.title;
  // A disclose_context call names its DOCUMENT, not the tool that fetched it:
  // the activation is the moment a skill's body enters the prompt, and "which
  // skill" is the only fact a reader wants from the row.
  const displayTitle =
    info.disclosed !== null
      ? disclosedClaim(info.disclosed)
      : info.mcp !== null
        ? formatMCPToolName(info.mcp.tool)
        : rawTitle;

  const node = el("div", { className: `tool-call tool-depth1-${depth1}` }) as HTMLDivElement;
  node.dataset["kind"] = info.kind;
  node.dataset["title"] = displayTitle;
  node.dataset["depth1"] = depth1;
  node.dataset["toolId"] = opts.id;
  if (info.disclosed !== null) {
    node.dataset["disclosed"] = info.disclosed.type;
  }
  if (info.denial !== null) {
    // Read back by applyOutcome on the update path, which only has the DOM.
    node.dataset["denied"] = "1";
  }
  if (info.mcp !== null) {
    node.dataset["mcpServer"] = info.mcp.server;
  }
  if (info.filePath !== "") {
    node.dataset["filename"] = info.fileBasename;
    node.dataset["filePath"] = info.filePath;
  }
  if (opts.live && isToolActive(opts.status)) {
    node.dataset["startMs"] = String(Date.now());
    trackInProgress(node);
  }

  node.appendChild(buildHeader(opts, displayTitle, info, hasDepth1(info.kind)));
  applyOutcome(node, opts.status, displayTitle, info);

  // A claim-only kind gets no details region and no toggle. A `search` or
  // `fetch` keeps the subtitle row, which is its claim's second fact.
  if (depth1 === "search" || depth1 === "fetch" || depth1 === "generic") {
    const subtitle = extractSubtitle(opts.input);
    if (subtitle !== "") {
      node.appendChild(el("div", { className: "tool-subtitle" }, subtitle));
    }
  }

  if (depth1 === "move") {
    const row = moveRow(opts.input);
    if (row !== null) {
      node.appendChild(row);
    }
  }

  if (hasDepth1(info.kind)) {
    node.insertAdjacentHTML("beforeend", buildDetails(opts));
    if (opts.output !== undefined && opts.output !== "") {
      appendOutput(node, opts.output, opts.outputSpans ?? [], depth1 === "output");
    }
    wireToggle(node);
  }

  wireFileLink(node, info.filePath, depth1 === "diff");

  // An edit's diff lives BEHIND the disclosure, not in the resting state. It
  // used to be inserted unconditionally, which made every edit card's depth 1
  // its resting state — and turned a merged multi-edit group into a wall of
  // hunks by default, the opposite of what progressive collapse buys.
  if (depth1 === "diff") {
    if (opts.diffs !== undefined && opts.diffs.length > 0) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const d = opts.diffs[0]!;
      insertDiffPreview(node, d.path, { oldText: d.old_text ?? "", newText: d.new_text });
    } else if (info.writesFile && info.diffSources !== null) {
      insertDiffPreview(node, info.filePath, info.diffSources);
    }
  }
  return node;
}

/** Two facts a move's claim line cannot carry. */
function moveRow(input: Record<string, unknown> | undefined): HTMLElement | null {
  const from = typeof input?.["sourcePath"] === "string" ? input["sourcePath"] : "";
  const to = typeof input?.["destinationPath"] === "string" ? input["destinationPath"] : "";
  if (from === "" || to === "") {
    return null;
  }
  return el(
    "div",
    { className: "tool-move-row" },
    el("span", { className: "tool-move-from" }, from),
    el("span", { className: "tool-move-arrow", "aria-hidden": "true" }, "\u2192"),
    el("span", { className: "tool-move-to" }, to),
  );
}

/** Extract a one-line subtitle from tool input for medium-tier cards. */
export function extractSubtitle(input: Record<string, unknown> | undefined): string {
  if (input === undefined) {
    return "";
  }
  // Try common input fields in priority order.
  for (const key of ["query", "pattern", "command", "url", "path", "explanation"]) {
    const val = input[key];
    if (typeof val === "string" && val !== "") {
      return val.length > 120 ? val.slice(0, 117) + "\u2026" : val;
    }
  }
  return "";
}

// --- HTML fragments ---

function buildHeader(
  opts: BuildToolCardOpts,
  displayTitle: string,
  info: ToolRenderInfo,
  withToggle: boolean,
): HTMLDivElement {
  // `.has-disclosure` is the CSS hook for the clickable affordance
  // (14-tools.css); stamped with the toggle so the two cannot drift.
  const header = el("div", {
    className: withToggle ? "tool-header has-disclosure" : "tool-header",
    title: opts.title,
  }) as HTMLDivElement;

  const iconSpan = el("span", { className: "tool-icon" }, iconEl(toolIcon(info.kind, opts.title)));
  header.appendChild(iconSpan);

  const titleSpan = el("span", { className: "tool-title" }, displayTitle);
  header.appendChild(titleSpan);

  if (info.mcp !== null) {
    const badge = el(
      "span",
      { className: "tool-mcp-badge", title: `From the ${info.mcp.server} MCP integration` },
      info.mcp.server,
    );
    badge.style.setProperty("--mcp-hue", String(mcpHue(info.mcp.server)));
    header.appendChild(badge);
  }

  if (info.filePath !== "") {
    // The filename IS the link to the change. There used to be a second
    // "View diff" button beside the stats; depth 2 is a click on the SUBJECT,
    // and a generic button next to it was a second affordance for one intent.
    const btn = el(
      "button",
      {
        className: "tool-file-link",
        "data-path": info.filePath,
        title: info.filePath,
        "data-tooltip": "Open the diff",
      },
      el("span", { className: "tool-file-icon" }, iconEl(fileIcon(info.fileBasename, false))),
      el("span", { className: "tool-file-name" }, info.fileBasename),
    );
    header.appendChild(btn);
  }

  if (opts.live && isToolActive(opts.status)) {
    const spinner = el("span", { className: "tool-spinner" });
    header.appendChild(spinner);
  }

  if (opts.live) {
    const duration = el("span", { className: "tool-duration" });
    header.appendChild(duration);
  }

  // No status WORD. Outcome rides the per-kind glyph (see applyOutcome): a
  // finished card printing the literal enum value `completed` was the claim
  // "Tool call completed", which says nothing the row does not already say.

  // A claim-only card has nothing to reveal, so it gets no toggle AT ALL rather
  // than a hidden one: a `display: none` button is still a button in the
  // accessibility tree, announcing a control that would open an empty region.
  if (withToggle) {
    header.appendChild(
      el(
        "button",
        {
          className: "tool-disclosure",
          "aria-expanded": "false",
          "aria-label": "Toggle tool details",
        },
        chevronEl(),
      ),
    );
  }

  return header;
}

/** What a caller may state. `ToolStatus` is the tool wire enum; `aborted` is the
 *  one run-level status with no tool counterpart, admitted because the History
 *  page states a run's verdict through this same writer and a stopped run is
 *  neither a success nor a failure of the work. */
type OutcomeStatus = ToolStatus | "aborted";

/** The verdicts the vocabulary paints. Not the wire enums: `pending` and
 *  `in_progress` are one thing to a reader, and a refusal is its own state. */
type OutcomeState = "ok" | "fail" | "warn" | "denied" | "running";

/** Paint a card's outcome onto its glyph, and give the card an accessible name.
 *
 *  ONE writer for the CARD vocabulary, and two sites deliberately outside it, so
 *  do not read the count as three. Callers: this builder, the tool-card update
 *  path (`messages-tools.ts`), and a History run row (`history.ts`). The two that
 *  paint an outcome without coming through here are `tool-group.ts`
 *  paintGroupOutcome, which writes a group header's verdict as text and needs no
 *  badge, and `fundamentals/subagent-block.ts` applyIcon, which hand-rolls both
 *  the tint classes and a `.tool-outcome-badge` of its own. That second one is
 *  what the "one writer" claim this comment used to make was hiding, and the copy
 *  cost it both channels until 2026-08-26: its badge host lacked the `.tool-icon`
 *  class, so the mark had no containing block (it painted ~190px away at the
 *  header's edge) and the `is-*` classes matched no rule. Route a new site through
 *  this function rather than copying a block, or it inherits that class of defect.
 *
 *  Tint alone would fail WCAG 1.4.1 — a green and a red glyph of identical shape
 *  are one channel — so a settled card also carries a SHAPE: a small check or
 *  cross composited on the glyph. The status word is not restored as visible
 *  text; the accessible name carries it instead ("Edited auth.go, succeeded"),
 *  and a programmatic name is not visible text.
 *
 *  `nameTarget` is the element the name lands on, defaulting to the glyph's own
 *  host because on a tool card they are one element. A History row separates
 *  them: the glyph is a row column while the control is the row's open button,
 *  and a name on the plain row would reach nobody. */
export function applyOutcome(
  node: HTMLElement,
  status: OutcomeStatus,
  displayTitle: string,
  info: ToolRenderInfo,
  nameTarget: HTMLElement = node,
): void {
  const icon = node.querySelector<HTMLElement>(".tool-icon");
  // A policy refusal is its OWN state, not a failure. The command was never run,
  // so "failed" would send the reader to debug a tool that behaved correctly;
  // what they need is the rule. Read from the dataset as well as the info so the
  // update path (which only has the DOM) reaches the same verdict.
  const denied = info.denial !== null || node.dataset["denied"] === "1";
  const state: OutcomeState = denied
    ? "denied"
    : status === "aborted"
      ? "warn"
      : isToolDone(status)
        ? status === "failed"
          ? "fail"
          : "ok"
        : "running";
  node.dataset["outcome"] = state;
  if (icon !== null) {
    icon.classList.remove("is-ok", "is-fail", "is-warn", "is-running", "is-denied");
    icon.classList.add(`is-${state}`);
    // The shape half. Rebuilt rather than toggled so a re-render cannot stack
    // two badges on one glyph.
    icon.querySelector(".tool-outcome-badge")?.remove();
    if (state !== "running") {
      icon.appendChild(
        el(
          "span",
          { className: "tool-outcome-badge", "aria-hidden": "true" },
          OUTCOME_BADGE[state],
        ),
      );
    }
  }
  const subject = info.fileBasename !== "" ? `${displayTitle} ${info.fileBasename}` : displayTitle;
  nameTarget.setAttribute("aria-label", `${subject}, ${outcomeWord(state)}`);
}

/** The shape half of the vocabulary, one glyph per settled state.
 *
 *  A shield for a refusal and a triangle for a stop are distinct in SHAPE, not
 *  only in tint, for the same WCAG 1.4.1 reason the check and cross are shapes —
 *  which matters twice over here, because both are amber. */
const OUTCOME_BADGE: Readonly<Record<Exclude<OutcomeState, "running">, string>> = {
  ok: "\u2713",
  fail: "\u2717",
  warn: "\u26A0",
  denied: "\u26D4",
};

/** The word the accessible name uses. Deliberately not the wire enum: "pending"
 *  and "in_progress" both mean the same thing to a listener. */
function outcomeWord(state: OutcomeState): string {
  switch (state) {
    case "ok":
      return "succeeded";
    case "fail":
      return "failed";
    case "warn":
      return "aborted";
    case "denied":
      return "blocked by security policy";
    default:
      return "running";
  }
}

// mcpHue derives a stable integer in [0,360) from the server name so
// per-server badges get consistent colours across renders without a
// lookup table. Simple FNV-ish fold — collisions are visual (two
// different server names could share a hue) but acceptable at the
// badge size and count a single vibekit user would configure.
export function mcpHue(server: string): number {
  let h = 2166136261 >>> 0;
  for (let i = 0; i < server.length; i++) {
    h ^= server.charCodeAt(i);
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h % 360;
}

function buildDetails(opts: BuildToolCardOpts): string {
  const inputBlock =
    opts.live && opts.input !== undefined
      ? `<pre class="tool-input">${escText(JSON.stringify(opts.input, null, 2))}</pre>`
      : "";
  // No "collapsed" class: the disclosure controller wired in wireToggle owns
  // the collapse state (inline height + aria-hidden/inert on the region).
  return `<div class="tool-details">${denialBlock(opts.denial)}${inputBlock}<div class="tool-output"></div></div>`;
}

/** The rule that refused the call, and where it lives.
 *
 *  This is the whole point of surfacing a denial separately: the user owns the
 *  policy, so a refusal that names its rule and its file is one step from
 *  changing it. Without this the card says "blocked" and the reader has to go
 *  hunt the policy for a rule that may not even be the one that fired. */
function denialBlock(d: ToolDenial | undefined): string {
  if (d === undefined) {
    return "";
  }
  const rows: string[] = [
    `<div class="tool-denial-row"><span>Capability</span><code>${escText(d.capability)}</code></div>`,
  ];
  if (d.resource !== "") {
    rows.push(
      `<div class="tool-denial-row"><span>Resource</span><code>${escText(d.resource)}</code></div>`,
    );
  }
  if (d.rule !== undefined) {
    const patterns = [
      ...(d.rule.match ?? []).map((m) => escText(m)),
      ...(d.rule.exclude ?? []).map((m) => `!${escText(m)}`),
    ].join(", ");
    rows.push(
      `<div class="tool-denial-row"><span>Rule</span><code>${escText(d.rule.effect)} ${escText(d.rule.capability)}${patterns === "" ? "" : ` (${patterns})`}</code></div>`,
    );
  }
  if (d.source !== "") {
    rows.push(
      `<div class="tool-denial-row"><span>From</span><code>${escText(d.scope)}: ${escText(d.source)}</code></div>`,
    );
  }
  return `<div class="tool-denial">${rows.join("")}</div>`;
}

// --- Wiring ---

/** The filename opens the CHANGE on a card that made one, and the FILE on a card
 *  that only read it — a read card's filename has no diff to show.
 *
 *  A change opens vs HEAD rather than from the card's own before/after pair,
 *  which is the honest source: the write has already landed, so the working tree
 *  IS the after state and git holds the before. (The card's own pair is what the
 *  `+N -M` link uses, for the narrower "what did THIS call do".) */
function wireFileLink(el: HTMLElement, filePath: string, isChange: boolean): void {
  if (filePath === "") {
    return;
  }
  el.querySelector(".tool-file-link")?.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    if (isChange) {
      openChange(filePath);
    } else {
      openAtLine(filePath);
    }
  });
}

// Per-card details disclosure controllers, for external expansion
// (messages-tools.ts force-opens the details when a tool fails).
const detailCtls = new WeakMap<HTMLElement, DisclosureController>();

function wireToggle(el: HTMLElement): void {
  const toggle = el.querySelector<HTMLElement>(".tool-disclosure");
  const details = el.querySelector<HTMLElement>(".tool-details");
  if (toggle === null || details === null) {
    return;
  }
  const header = el.querySelector<HTMLElement>(".tool-header");
  // The disclosure primitive owns aria-expanded/aria-controls, activation,
  // and the animated height 0↔auto with aria-hidden + inert on the collapsed
  // region (which the old class flip never set — collapsed details stayed in
  // the accessibility tree). Only the scroll-freeze on a user collapse stays
  // vibekit's, via onToggle.
  //
  // The chevron is NOT swapped here any more. Direction is CSS's, keyed off the
  // `aria-expanded` this controller already writes (`.disclosure-chevron` in
  // 10-shell-app.css, flipped in 14-tools.css) — so the glyph animates into its
  // new direction instead of being replaced mid-transition, and one convention
  // covers all eight disclosures in the app rather than this one.
  const ctl = createDisclosure(toggle, details, {
    open: false,
    onToggle: (open, source) => {
      if (!open && source === "user") {
        setUserScrolledUp(true);
      }
    },
  });
  // The whole row activates that chevron, so the card matches the tool group it
  // sits inside instead of asking for a 24x24 target at the far end of a 775px
  // header. Wired HERE rather than in buildHeader, which is what keeps a
  // claim-only card inert: no toggle means no `.tool-details`, an early return
  // above, and a header that never becomes clickable. `.tool-file-link` already
  // stops propagation (wireFileLink), and would be skipped as a `<button>`
  // anyway.
  if (header !== null) {
    wireRowToggle(header, toggle);
  }
  detailCtls.set(el, ctl);
}

/** Force-open a card's details (e.g. when the tool fails so the error output
 *  is visible without a click). The chevron follows from the `aria-expanded`
 *  the controller writes; a card without wired details is a no-op. */
export function expandToolDetails(card: HTMLElement): void {
  detailCtls.get(card)?.open();
}

/** Fill a card's output region. When `windowed`, depth 1 shows the first and
 *  last N lines and a control reveals the rest IN PLACE — the only depth 2 in
 *  the ladder that does not leave the transcript.
 *
 *  It deliberately does NOT route to the shell panel. That is one global LIVE
 *  server-side PTY whose only host controls are send and reset; writing a
 *  finished command's historical bytes into it would present them as part of the
 *  current stream, where the next server frame can interleave or erase them, and
 *  would corrupt a surface the user may be using for something else. */
function appendOutput(
  node: HTMLElement,
  output: string,
  spans: readonly TextSpan[],
  windowed: boolean,
): void {
  const out = node.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const pre = el("pre");
  const paint = (text: string, s: readonly TextSpan[]): void => {
    renderOutput(pre, text, s);
    // A search tool's output IS its result list — `path:line: match` per hit —
    // so linkifying it is the search-hit seam. It ran on prose and turn headers
    // but never on tool output, which is where the hits actually are.
    linkifyPaths(pre, { insidePre: true });
  };
  if (!windowed) {
    paint(output, spans);
    out.appendChild(pre);
    return;
  }
  const win = windowOutput(output);
  paint(win.text, windowSpans(spans, win.kept));
  out.appendChild(pre);
  if (win.elided === 0) {
    return;
  }
  const reveal = el(
    "button",
    { type: "button", className: "tool-output-reveal" },
    `Show ${String(win.elided)} more line${win.elided === 1 ? "" : "s"}`,
  );
  reveal.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    paint(output, spans);
    reveal.remove();
  });
  out.appendChild(reveal);
}

// --- Inline diff preview for file-writing tools ---

export function insertDiffPreview(
  node: HTMLDivElement,
  filePath: string,
  src: { oldText: string; newText: string },
): void {
  const diff = lineDiff(src.oldText, src.newText);
  const s = diffStats(diff);
  if (s.adds === 0 && s.dels === 0) {
    return;
  }

  const wrap = el("div", { className: "tool-diff-preview" });

  // `+N -M` is a link to the same diff, scrolled to the first hunk. Numbers
  // answer "how much" where the glyph's colour only answers "whether", so they
  // stay on the claim line and become the second entry point to depth 2.
  const statBtn = el(
    "button",
    { type: "button", className: "tool-diff-stats", "data-tooltip": "Open the diff" },
    el("span", { className: "diff-add-count" }, `+${String(s.adds)}`),
    el("span", { className: "diff-del-count" }, `-${String(s.dels)}`),
  );
  statBtn.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    openFileDiff(filePath, src.oldText, src.newText, { oldLabel: "before", newLabel: "after" });
  });
  wrap.appendChild(statBtn);

  // Unified, whole hunks, line numbers ON. Line numbers are what let a reader
  // carry their place across the click into the real document; without them the
  // peek is a fragment with no address.
  const win = windowHunks(diff, { maxRows: 24, context: 2 });
  const mini = renderDiffPane(win.lines, {
    unified: true,
    lineNumbers: true,
    syncScroll: false,
    lang: filePath,
  });
  mini.classList.add("tool-diff-mini");
  wrap.appendChild(mini);

  if (win.hunksOmitted > 0) {
    wrap.appendChild(
      el(
        "div",
        { className: "tool-diff-more" },
        `+${String(win.hunksOmitted)} more hunk${win.hunksOmitted === 1 ? "" : "s"}`,
      ),
    );
  }

  // The third §3.4 case: a card GROWS when its diff preview lands on the update
  // path, which pushes everything below it — including the reader's position —
  // down. Content-growth class, same helper. Immediate, like reasoning's seal
  // and unlike tool-group's animated collapse.
  preserveReadingPosition(() => {
    node.insertBefore(wrap, node.querySelector(".tool-details"));
  }, "content-growth");
}
