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
import { openFile, openFileDiff } from "./editor-openers.js";
import { lineDiff, truncateChanged, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp } from "./scroll.js";
import {
  renderInfoFor,
  formatMCPToolName,
  toolTier,
  isToolActive,
  type ToolRenderInfo,
} from "./tool-schema.js";
import { trackInProgress } from "./tool-group.js";

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

  const el = document.createElement("div");
  el.className = `tool-call tool-tier-${tier}`;
  el.dataset["kind"] = info.kind;
  el.dataset["title"] = displayTitle;
  el.dataset["tier"] = tier;
  el.dataset["toolId"] = opts.id;
  if (info.mcp !== null) {
    el.dataset["mcpServer"] = info.mcp.server;
  }
  if (info.filePath !== "") {
    el.dataset["filename"] = info.fileBasename;
    el.dataset["filePath"] = info.filePath;
  }
  if (opts.live && isToolActive(opts.status)) {
    el.dataset["startMs"] = String(Date.now());
    trackInProgress(el);
  }

  el.appendChild(buildHeader(opts, displayTitle, info));

  // Medium tier: add a subtitle row with the first meaningful input param.
  if (tier === "medium") {
    const subtitle = extractSubtitle(opts.input);
    if (subtitle !== "") {
      const sub = document.createElement("div");
      sub.className = "tool-subtitle";
      sub.textContent = subtitle;
      el.appendChild(sub);
    }
  }

  // Complex tier: add a scrollable output box.
  // Simple + medium: add a hidden details block (toggle on click).
  if (tier === "complex") {
    const outputBox = document.createElement("div");
    outputBox.className = "tool-output-box";
    el.appendChild(outputBox);
    if (opts.output !== undefined && opts.output !== "") {
      appendToOutputBox(outputBox, opts.output);
    }
  } else {
    el.innerHTML += buildDetails(opts);
    if (opts.output !== undefined && opts.output !== "") {
      appendOutput(el, opts.output);
    }
  }

  wireFileLink(el, info.filePath);
  if (tier !== "simple") {
    wireToggle(el);
  }

  if (opts.diffs !== undefined && opts.diffs.length > 0) {
    const d = opts.diffs[0]!;
    insertDiffPreview(el, d.path, {
      oldText: d.old_text ?? "",
      newText: d.new_text,
    });
  } else if (info.writesFile && info.diffSources !== null) {
    insertDiffPreview(el, info.filePath, info.diffSources);
  }
  return el;
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
    const newPre = document.createElement("pre");
    newPre.textContent = text;
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
  const header = document.createElement("div");
  header.className = "tool-header";
  header.title = opts.title;

  const iconSpan = document.createElement("span");
  iconSpan.className = "tool-icon";
  iconSpan.innerHTML = toolIcon(info.kind, opts.title);
  header.appendChild(iconSpan);

  const titleSpan = document.createElement("span");
  titleSpan.className = "tool-title";
  titleSpan.textContent = displayTitle;
  header.appendChild(titleSpan);

  if (info.mcp !== null) {
    const badge = document.createElement("span");
    badge.className = "tool-mcp-badge";
    badge.title = `From the ${info.mcp.server} MCP integration`;
    badge.style.setProperty("--mcp-hue", String(mcpHue(info.mcp.server)));
    badge.textContent = info.mcp.server;
    header.appendChild(badge);
  }

  if (info.filePath !== "") {
    const btn = document.createElement("button");
    btn.className = "tool-file-link";
    btn.dataset["path"] = info.filePath;
    btn.title = info.filePath;
    const fIcon = document.createElement("span");
    fIcon.className = "tool-file-icon";
    fIcon.innerHTML = fileIcon(info.fileBasename, false);
    btn.appendChild(fIcon);
    const fName = document.createElement("span");
    fName.className = "tool-file-name";
    fName.textContent = info.fileBasename;
    btn.appendChild(fName);
    header.appendChild(btn);
  }

  if (opts.live && isToolActive(opts.status)) {
    const spinner = document.createElement("span");
    spinner.className = "tool-spinner";
    header.appendChild(spinner);
  }

  if (opts.live) {
    const duration = document.createElement("span");
    duration.className = "tool-duration";
    header.appendChild(duration);
  }

  const status = document.createElement("span");
  status.className = `tool-status ${opts.status}`;
  status.textContent = opts.status;
  header.appendChild(status);

  const toggle = document.createElement("button");
  toggle.className = "tool-toggle";
  toggle.setAttribute("aria-expanded", "false");
  toggle.setAttribute("aria-label", "Toggle tool details");
  toggle.innerHTML = ICON_CHEVRON_DOWN;
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
  return `<div class="tool-details collapsed">${inputBlock}<div class="tool-output"></div></div>`;
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

function wireToggle(el: HTMLElement): void {
  el.querySelector(".tool-toggle")?.addEventListener("click", () => {
    const d = el.querySelector(".tool-details")!;
    const b = el.querySelector(".tool-toggle")!;
    if (d.classList.contains("collapsed")) {
      d.classList.remove("collapsed");
      b.innerHTML = ICON_CHEVRON_UP;
      b.setAttribute("aria-expanded", "true");
    } else {
      d.classList.add("collapsed");
      b.innerHTML = ICON_CHEVRON_DOWN;
      b.setAttribute("aria-expanded", "false");
      setUserScrolledUp(true);
    }
  });
}

function appendOutput(el: HTMLElement, output: string): void {
  const out = el.querySelector(".tool-output");
  if (out === null) {
    return;
  }
  const pre = document.createElement("pre");
  pre.innerHTML = ansiToHtml(output);
  out.appendChild(pre);
}

// --- Inline diff preview for file-writing tools ---

export function insertDiffPreview(
  el: HTMLDivElement,
  filePath: string,
  src: { oldText: string; newText: string },
): void {
  const diff = lineDiff(src.oldText, src.newText);
  const s = diffStats(diff);
  if (s.adds === 0 && s.dels === 0) {
    return;
  }

  const wrap = document.createElement("div");
  wrap.className = "tool-diff-preview";

  const stats = document.createElement("div");
  stats.className = "tool-diff-stats";
  stats.innerHTML =
    `<span class="diff-add-count">+${String(s.adds)}</span>` +
    `<span class="diff-del-count">-${String(s.dels)}</span>`;
  const viewBtn = document.createElement("button");
  viewBtn.className = "tool-diff-view-btn";
  viewBtn.textContent = "View diff";
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
    const more = document.createElement("div");
    more.className = "tool-diff-more";
    more.textContent = `+${String(trimmed.more)} more change${trimmed.more === 1 ? "" : "s"}`;
    wrap.appendChild(more);
  }

  el.insertBefore(wrap, el.querySelector(".tool-details"));
}
