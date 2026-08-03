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
import { escText } from "./strings.js";
import { ansiToHtml } from "./ansi.js";
import { fileIcon, toolIcon, ICON_CHEVRON_DOWN, ICON_CHEVRON_UP } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { openFile, openFileDiff } from "./editor-openers.js";
import { lineDiff, truncateChanged, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import {
  renderInfoFor,
  formatMCPToolName,
  toolTier,
  isToolActive,
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
  const tier = toolTier(info.kind);
  const rawTitle = opts.title.startsWith("Running: ") ? opts.title.slice(9) : opts.title;
  const displayTitle = info.mcp !== null ? formatMCPToolName(info.mcp.tool) : rawTitle;

  const node = el("div", { className: `tool-call tool-tier-${tier}` }) as HTMLDivElement;
  node.dataset["kind"] = info.kind;
  node.dataset["title"] = displayTitle;
  node.dataset["tier"] = tier;
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

  node.appendChild(buildHeader(opts, displayTitle, info));

  // Medium tier: add a subtitle row with the first meaningful input param.
  if (tier === "medium") {
    const subtitle = extractSubtitle(opts.input);
    if (subtitle !== "") {
      const sub = el("div", { className: "tool-subtitle" }, subtitle);
      node.appendChild(sub);
    }
  }

  // Complex tier: add a scrollable output box.
  // Simple + medium: add a hidden details block (toggle on click).
  if (tier === "complex") {
    const outputBox = el("div", { className: "tool-output-box" }) as HTMLDivElement;
    node.appendChild(outputBox);
    if (opts.output !== undefined && opts.output !== "") {
      appendToOutputBox(outputBox, opts.output);
    }
  } else {
    node.insertAdjacentHTML("beforeend", buildDetails(opts));
    if (opts.output !== undefined && opts.output !== "") {
      appendOutput(node, opts.output);
    }
  }

  wireFileLink(node, info.filePath);
  if (tier !== "simple") {
    wireToggle(node);
  }

  if (opts.diffs !== undefined && opts.diffs.length > 0) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const d = opts.diffs[0]!;
    insertDiffPreview(node, d.path, {
      oldText: d.old_text ?? "",
      newText: d.new_text,
    });
  } else if (info.writesFile && info.diffSources !== null) {
    insertDiffPreview(node, info.filePath, info.diffSources);
  }
  return node;
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

/** Append text to a complex-tier scrollable output box. Auto-scrolls
 *  to the bottom so the user sees the latest output. */
function appendToOutputBox(box: HTMLDivElement, text: string): void {
  const pre = box.querySelector("pre");
  if (pre !== null) {
    pre.textContent += text;
  } else {
    const newPre = el("pre", null, text);
    box.appendChild(newPre);
  }
  box.scrollTop = box.scrollHeight;
}

// --- HTML fragments ---

function buildHeader(
  opts: BuildToolCardOpts,
  displayTitle: string,
  info: ToolRenderInfo,
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
    const btn = el(
      "button",
      { className: "tool-file-link", "data-path": info.filePath, title: info.filePath },
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

  const status = el("span", { className: `tool-status ${opts.status}` }, opts.status);
  header.appendChild(status);

  const toggle = el(
    "button",
    {
      className: "tool-toggle",
      "aria-expanded": "false",
      "aria-label": "Toggle tool details",
    },
    iconEl(ICON_CHEVRON_DOWN),
  );
  header.appendChild(toggle);

  return header;
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

function wireFileLink(el: HTMLElement, filePath: string): void {
  if (filePath === "") {
    return;
  }
  el.querySelector(".tool-file-link")?.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    openFile(filePath);
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

function appendOutput(node: HTMLElement, output: string): void {
  const out = node.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const pre = el("pre");
  pre.innerHTML = ansiToHtml(output);
  out.appendChild(pre);
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

  const stats = el("div", { className: "tool-diff-stats" });
  stats.innerHTML =
    `<span class="diff-add-count">+${String(s.adds)}</span>` +
    `<span class="diff-del-count">-${String(s.dels)}</span>`;
  const viewBtn = el("button", { className: "tool-diff-view-btn" }, "View diff");
  viewBtn.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    openFileDiff(filePath, src.oldText, src.newText, { oldLabel: "before", newLabel: "after" });
  });
  stats.appendChild(viewBtn);
  wrap.appendChild(stats);

  const trimmed = truncateChanged(diff, 3);
  const mini = renderDiffPane(trimmed.lines, { lineNumbers: false, syncScroll: false });
  mini.classList.add("tool-diff-mini");
  wrap.appendChild(mini);

  if (trimmed.more > 0) {
    const more = el(
      "div",
      { className: "tool-diff-more" },
      `+${String(trimmed.more)} more change${trimmed.more === 1 ? "" : "s"}`,
    );
    wrap.appendChild(more);
  }

  // The third §3.4 case: a card GROWS when its diff preview lands on the update
  // path, which pushes everything below it — including the reader's position —
  // down. Content-growth class, same helper. Immediate, like reasoning's seal
  // and unlike tool-group's animated collapse.
  preserveReadingPosition(() => {
    node.insertBefore(wrap, node.querySelector(".tool-details"));
  }, "content-growth");
}
