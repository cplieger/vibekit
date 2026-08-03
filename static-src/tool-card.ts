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

import type { ToolStatus, ToolLocation, ToolDiff } from "./types.js";
import { escText, windowOutput } from "./strings.js";
import { ansiToHtml } from "./ansi.js";
import { linkifyPaths } from "./linkify.js";
import { fileIcon, toolIcon, ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { openFileDiff } from "./editor-openers.js";
import { openChange, openAtLine } from "./navigate.js";
import { lineDiff, windowHunks, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import {
  renderInfoFor,
  formatMCPToolName,
  toolDepth1,
  hasDepth1,
  isToolActive,
  isToolDone,
  type ToolRenderInfo,
} from "./tool-schema.js";
import { trackInProgress } from "./tool-group.js";
import { el } from "@cplieger/reactive";

export interface BuildToolCardOpts {
  id: string;
  title: string;
  kind: string;
  status: ToolStatus;
  input?: Record<string, unknown>;
  output?: string;
  locations?: ToolLocation[];
  diffs?: ToolDiff[];
  /** Live mode: show spinner + start timestamp + show-raw-input block +
   *  expand-on-fail. Replay mode: omit those since the call has settled. */
  live: boolean;
}

/** Build a tool-call element. Does not append it to the DOM. */
export function buildToolCard(opts: BuildToolCardOpts): HTMLDivElement {
  const info = renderInfoFor(opts.title, opts.kind, opts.input);
  const depth1 = toolDepth1(info.kind);
  const rawTitle = opts.title.startsWith("Running: ") ? opts.title.slice(9) : opts.title;
  const displayTitle = info.mcp !== null ? formatMCPToolName(info.mcp.tool) : rawTitle;

  const node = el("div", { className: `tool-call tool-depth1-${depth1}` }) as HTMLDivElement;
  node.dataset["kind"] = info.kind;
  node.dataset["title"] = displayTitle;
  node.dataset["depth1"] = depth1;
  node.dataset["toolId"] = opts.id;
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
      appendOutput(node, opts.output, depth1 === "output");
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
  const header = el("div", { className: "tool-header", title: opts.title }) as HTMLDivElement;

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
          className: "tool-toggle",
          "aria-expanded": "false",
          "aria-label": "Toggle tool details",
        },
        iconEl(ICON_CHEVRON_DOWN),
      ),
    );
  }

  return header;
}

/** Paint a card's outcome onto its glyph, and give the card an accessible name.
 *
 *  ONE writer for the whole vocabulary, which is the point: the outcome used to
 *  be written in four places (here, the update path, the subagent block, and the
 *  group header) and each spelled it slightly differently.
 *
 *  Tint alone would fail WCAG 1.4.1 — a green and a red glyph of identical shape
 *  are one channel — so a settled card also carries a SHAPE: a small check or
 *  cross composited on the glyph. The status word is not restored as visible
 *  text; the accessible name carries it instead ("Edited auth.go, succeeded"),
 *  and a programmatic name is not visible text. */
export function applyOutcome(
  node: HTMLElement,
  status: ToolStatus,
  displayTitle: string,
  info: ToolRenderInfo,
): void {
  const icon = node.querySelector<HTMLElement>(".tool-icon");
  const state = isToolDone(status) ? (status === "failed" ? "fail" : "ok") : "running";
  node.dataset["outcome"] = state;
  if (icon !== null) {
    icon.classList.remove("is-ok", "is-fail", "is-running");
    icon.classList.add(`is-${state}`);
    // The shape half. Rebuilt rather than toggled so a re-render cannot stack
    // two badges on one glyph.
    icon.querySelector(".tool-outcome-badge")?.remove();
    if (state !== "running") {
      icon.appendChild(
        el(
          "span",
          { className: "tool-outcome-badge", "aria-hidden": "true" },
          state === "ok" ? "\u2713" : "\u2717",
        ),
      );
    }
  }
  const subject = info.fileBasename !== "" ? `${displayTitle} ${info.fileBasename}` : displayTitle;
  node.setAttribute("aria-label", `${subject}, ${outcomeWord(state)}`);
}

/** The word the accessible name uses. Deliberately not the wire enum: "pending"
 *  and "in_progress" both mean the same thing to a listener. */
function outcomeWord(state: string): string {
  switch (state) {
    case "ok":
      return "succeeded";
    case "fail":
      return "failed";
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
  return `<div class="tool-details">${inputBlock}<div class="tool-output"></div></div>`;
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
  const toggle = el.querySelector<HTMLElement>(".tool-toggle");
  const details = el.querySelector<HTMLElement>(".tool-details");
  if (toggle === null || details === null) {
    return;
  }
  // The disclosure primitive owns aria-expanded/aria-controls, activation,
  // and the animated height 0↔auto with aria-hidden + inert on the collapsed
  // region (which the old class flip never set — collapsed details stayed in
  // the accessibility tree). The chevron swap and the scroll-freeze on a
  // user collapse stay vibekit's, via onToggle.
  const ctl = createDisclosure(toggle, details, {
    open: false,
    onToggle: (open, source) => {
      toggle.replaceChildren(iconEl(open ? ICON_CHEVRON_UP : ICON_CHEVRON_DOWN));
      if (!open && source === "user") {
        setUserScrolledUp(true);
      }
    },
  });
  detailCtls.set(el, ctl);
}

/** Force-open a card's details (e.g. when the tool fails so the error output
 *  is visible without a click). The chevron follows via the controller's
 *  onToggle; a card without wired details is a no-op. */
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
function appendOutput(node: HTMLElement, output: string, windowed: boolean): void {
  const out = node.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const pre = el("pre");
  if (!windowed) {
    pre.innerHTML = ansiToHtml(output);
    // A search tool's output IS its result list — `path:line: match` per hit —
    // so linkifying it is the search-hit seam. It ran on prose and turn headers
    // but never on tool output, which is where the hits actually are.
    linkifyPaths(pre, { insidePre: true });
    out.appendChild(pre);
    return;
  }
  const win = windowOutput(output);
  pre.innerHTML = ansiToHtml(win.text);
  linkifyPaths(pre, { insidePre: true });
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
    pre.innerHTML = ansiToHtml(output);
    linkifyPaths(pre, { insidePre: true });
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
