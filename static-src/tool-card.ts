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
import { escText, windowOutput, windowSpans, humanName } from "./strings.js";
import { renderOutput } from "./output-render.js";
import { linkifyPaths } from "./linkify.js";
import { fileIcon, toolIcon, outcomeIcon } from "./icons.js";
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
  const withToggle = hasDepth1(info.kind);
  const rawTitle = opts.title.startsWith("Running: ") ? opts.title.slice(9) : opts.title;
  // A disclose_context call names its DOCUMENT, not the tool that fetched it:
  // the activation is the moment a skill's body enters the prompt, and "which
  // skill" is the only fact a reader wants from the row.
  const displayTitle =
    info.disclosed !== null
      ? disclosedClaim(info.disclosed)
      : info.mcp !== null
        ? formatMCPToolName(info.mcp.tool)
        : humanName(rawTitle);

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

  const summary = el("div", {
    className: withToggle ? "tool-summary has-disclosure" : "tool-summary",
  });
  summary.appendChild(buildHeader(opts, displayTitle, info, withToggle));
  node.appendChild(summary);
  applyOutcome(node, opts.status, displayTitle, info);

  // A claim-only kind gets no details region and no toggle. A `search` or
  // `fetch` keeps the subtitle row, which is its claim's second fact. It lives
  // inside the same summary as the title so the whole visible box is one
  // disclosure target and one hover surface.
  if (depth1 === "search" || depth1 === "fetch" || depth1 === "generic") {
    const subtitle = extractSubtitle(opts.input);
    if (subtitle !== "") {
      summary.appendChild(el("div", { className: "tool-subtitle" }, subtitle));
    }
  }

  if (depth1 === "move") {
    const row = moveRow(opts.input);
    if (row !== null) {
      summary.appendChild(row);
    }
  }

  if (withToggle) {
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
  const header = el("div", {
    className: "tool-header",
    title: displayTitle,
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

  // No status WORD, and no second mark either. The row carries ONE mark — the
  // glyph above (see applyOutcome) — and a finished card printing the literal
  // enum value `completed` was the claim "Tool call completed", which says
  // nothing the row does not already say.

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

/** What a `.tool-icon` slot holds: the glyph it was BUILT with, and the state
 *  currently painted into it. Keyed on the element so `applyOutcome` needs no
 *  signature change and no caller has to supply the identity glyph twice. */
interface IconMark {
  identity: Element | null;
  painted: OutcomeState | null;
}
const iconMarks = new WeakMap<HTMLElement, IconMark>();

/** Paint a row's ONE outcome mark, and give the row an accessible name.
 *
 *  ONE MARK PER ROW, and its SHAPE is what changes for a non-success state.
 *  `ok` and `running` keep the row's own identity glyph (per-kind for a tool
 *  card, `ICON_TAB_RUN` for a History row) and only its tint moves; `fail`,
 *  `warn` and `denied` REPLACE that glyph with a general road-sign silhouette
 *  from `icons.ts` (`outcomeIcon`), red for a failure and yellow for the two
 *  stops, each a distinct shape. So hue stays a channel and is never the only
 *  one, and WCAG 1.4.1 is satisfied by the shape swap rather than by the second
 *  mark this replaced (a 7px character badge composited on the glyph's corner,
 *  which said the same thing twice). The status word is still not visible text:
 *  the accessible name carries it ("Edited auth.go, succeeded").
 *
 *  THE IDENTITY GLYPH IS CAPTURED, NOT RECOMPUTED. Callers mount it before the
 *  first call — this builder, the update path (`messages-tools.ts`) reusing what
 *  the builder mounted, and `history.ts`, whose glyph is `ICON_TAB_RUN` and not
 *  a `toolIcon` at all — so re-deriving it here would repaint a run row with a
 *  tool glyph. `dataset.title` cannot stand in for the raw title either: it holds
 *  the DISPLAY title, while `toolIcon` keys its overrides on the raw one. The
 *  contract is therefore that the identity glyph is in the slot before the first
 *  call. A slot with none keeps whatever it has for `ok`/`running`, because this
 *  function does not own content it never wrote — but a silhouette it wrote
 *  itself IS its own, so a return to `ok` clears that instead of leaving a red
 *  triangle standing under an `is-ok` class.
 *
 *  Repeated calls are idempotent: the mark is written with `replaceChildren` and
 *  only when the state has actually changed, so two SVGs in one `.tool-icon` is
 *  unrepresentable. The slot is `aria-hidden` — the mark is decorative, because
 *  the word is in the name.
 *
 *  TWO SITES PAINT AN OUTCOME WITHOUT CALLING THIS, and both are deliberate:
 *  `tool-group.ts` paintGroupOutcome, whose slot has no identity glyph to keep,
 *  and `fundamentals/subagent-block.ts` applyIcon, which owns its own identity
 *  glyph and spinner. What they share with this function is the GLYPH SET and its
 *  resolver in `icons.ts`, which is the thing that makes a half-migrated
 *  vocabulary unrepresentable — saying "one writer" is what previously hid the
 *  fact that a copy existed at all.
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
    icon.setAttribute("aria-hidden", "true");
    let mark = iconMarks.get(icon);
    if (mark === undefined) {
      const built = icon.firstElementChild;
      mark = {
        identity: built === null ? null : (built.cloneNode(true) as Element),
        painted: null,
      };
      iconMarks.set(icon, mark);
    }
    if (mark.painted !== state) {
      // The identity glyph for a success or a live call; the shared silhouette
      // otherwise. `replaceChildren` plus the guard is what makes a repeat paint
      // a no-op instead of a second SVG.
      const keepIdentity = state === "ok" || state === "running";
      const wanted = keepIdentity
        ? (mark.identity?.cloneNode(true) ?? null)
        : iconEl(outcomeIcon(state));
      // `painted` is recorded only when the slot is actually written, so it
      // always describes what the slot HOLDS.
      if (wanted !== null) {
        icon.replaceChildren(wanted);
        mark.painted = state;
      } else if (mark.painted !== null) {
        // No identity glyph to restore, and the slot is holding a silhouette
        // THIS function put there. It owns that content, so a return to
        // `ok`/`running` clears it rather than leaving a red triangle under an
        // `is-ok` class. A slot this function has never written is left alone.
        icon.replaceChildren();
        mark.painted = state;
      }
    }
  }
  const subject = info.fileBasename !== "" ? `${displayTitle} ${info.fileBasename}` : displayTitle;
  nameTarget.setAttribute("aria-label", `${subject}, ${outcomeWord(state)}`);
}

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
  const summary = el.querySelector<HTMLElement>(".tool-summary");
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
  // The whole visible summary activates that chevron: title row, subtitle or
  // move row, and the blank padding between them. Wired HERE rather than in
  // buildHeader, which is what keeps a claim-only card inert: no toggle means
  // no `.tool-details`, an early return above, and a summary that never becomes
  // clickable. Nested controls keep their own click through wireRowToggle.
  if (summary !== null) {
    wireRowToggle(summary, toggle);
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
