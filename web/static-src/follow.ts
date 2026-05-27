// ---------------------------------------------------------------------------
// Follow-along: a read-only viewer that tracks the agent's file operations
// in real time. When enabled, a singleton "Follow" tab opens and updates
// in-place as tool calls report locations.
//
// Toggle: #follow-along-btn in the prompt bar.
// Pause: button inside the follow header; freezes the view. Play resumes
//        from wherever the agent is now. Manual pause is sticky across
//        tab switches; auto-pause (tab lost focus) resumes on return.
// Open: button to open the current file in a proper editor tab.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { onSSE } from "./bus.js";
import {
  openTab,
  closeTab,
  hasTab,
  activateTab,
  getActiveTabId,
  setOnActivate,
  TAB_VIEWS,
} from "./tabs.js";
import { apiGet } from "./api-client.js";
import { escText } from "./strings.js";
import { highlight } from "./highlight.js";
import { openFile } from "./editor-openers.js";
import { getActiveId, activeVersion } from "./store.js";
import { effect } from "./signals.js";
import { profileFor } from "./tool-schema.js";
import type { ToolLocation } from "./types.js";
import { registerCleanup } from "./actions/index.js";

const TAB_ID = "__follow__";

// ---------------------------------------------------------------------------
// FollowController: owns all follow-along state as instance fields.
// ---------------------------------------------------------------------------

class FollowController {
  private enabled = false;
  private paused = false;
  private manualPause = false;
  private currentPath = "";
  private currentLine = 0;
  private pendingPath = "";
  private pendingLine = 0;

  private loadGeneration = 0;
  private loadController: AbortController | null = null;

  private readonly hasToolCall = new Set<string>();

  // Virtualisation state
  private lines: string[] = [];
  private filePath = "";
  private lineHeight = 20; // px, updated on first render
  private bufferLines = 20;
  private scrollHandler: (() => void) | null = null;
  private rowPool: HTMLDivElement[] = [];
  private poolSize = 0;

  init(): void {
    $.followAlongBtn.addEventListener("click", () => {
      this.toggle();
    });
    setOnActivate((id) => {
      this.onTabChanged(id);
    });

    onSSE("tool_call", (chatID, p) => {
      this.markToolCall(chatID);
      this.handleLocations(p.tool_call.locations, p.tool_call.kind);
    });
    onSSE("tool_call_update", (chatID, p) => {
      this.markToolCall(chatID);
      this.handleLocations(p.tool_call.locations, p.tool_call.kind);
    });
    onSSE("turn_ended", (chatID) => {
      if (this.enabled && getActiveTabId() === TAB_ID) {
        activateTab(chatID);
      }
    });
    onSSE("chat_deleted", (chatID) => {
      this.hasToolCall.delete(chatID);
    });
    effect(() => {
      void activeVersion.value;
      this.syncEnabled();
    });
    this.syncEnabled();
  }

  /** Abort any in-flight follow-along file load. Wired to global cleanup. */
  cancelLoad(): void {
    this.loadController?.abort();
    this.loadController = null;
  }

  showFollowView(): void {
    if (this.enabled) {
      activateTab(TAB_ID);
      return;
    }
    this.toggle();
  }

  onFollowTabClosed(): void {
    this.loadController?.abort();
    this.loadController = null;
    this.enabled = false;
    this.paused = false;
    this.manualPause = false;
    $.followAlongBtn.setAttribute("aria-pressed", "false");
    this.currentPath = "";
    this.currentLine = 0;
    this.pendingPath = "";
    this.pendingLine = 0;
    this.lines = [];
    this.rowPool = [];
    this.poolSize = 0;
  }

  // --- Internal ---

  private markToolCall(chatID: string): void {
    if (this.hasToolCall.has(chatID)) {
      return;
    }
    this.hasToolCall.add(chatID);
    if (getActiveId() === chatID) {
      this.syncEnabled();
    }
  }

  private syncEnabled(): void {
    const active = getActiveId();
    const ready = active !== "" && this.hasToolCall.has(active);
    $.followAlongBtn.disabled = !ready;
    if (!ready && this.enabled) {
      this.toggle();
    }
  }

  private onTabChanged(activeId: string): void {
    if (!this.enabled) {
      return;
    }
    if (activeId === TAB_ID) {
      if (this.paused && !this.manualPause) {
        this.setPaused(false);
        if (this.pendingPath !== "") {
          const path = this.pendingPath;
          const line = this.pendingLine;
          this.pendingPath = "";
          this.pendingLine = 0;
          this.applyLocation(path, line);
        }
      }
    } else {
      if (!this.paused) {
        this.setPaused(true);
      }
    }
  }

  private toggle(): void {
    this.enabled = !this.enabled;
    $.followAlongBtn.setAttribute("aria-pressed", String(this.enabled));
    if (this.enabled) {
      this.paused = false;
      this.manualPause = false;
      this.openFollowTab();
    } else {
      this.closeFollowTab();
    }
  }

  private openFollowTab(): void {
    if (!hasTab(TAB_ID)) {
      openTab({
        id: TAB_ID,
        name: "Follow",
        kind: "follow",
        view: TAB_VIEWS.follow,
        route: { kind: "follow" },
        onClose: () => {
          this.onFollowTabClosed();
        },
      });
    }
    activateTab(TAB_ID);
    this.renderWaiting();
  }

  private closeFollowTab(): void {
    if (hasTab(TAB_ID)) {
      closeTab(TAB_ID);
    }
    this.loadController?.abort();
    this.loadController = null;
    this.currentPath = "";
    this.currentLine = 0;
    this.pendingPath = "";
    this.pendingLine = 0;
    this.lines = [];
    this.rowPool = [];
    this.poolSize = 0;
  }

  private handleLocations(locations: ToolLocation[] | undefined, kind?: string): void {
    if (!this.enabled || locations === undefined || locations.length === 0) {
      return;
    }
    const loc = locations[0]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const isWrite = profileFor("", kind ?? "").writesFile;

    if (this.paused) {
      this.pendingPath = loc.path;
      this.pendingLine = loc.line ?? 0;
      return;
    }

    this.applyLocation(loc.path, loc.line ?? 0, isWrite);
  }

  private applyLocation(path: string, line: number, forceReload = false): void {
    if (!forceReload && path === this.currentPath && line === this.currentLine) {
      return;
    }

    const pathChanged = path !== this.currentPath || forceReload;
    this.currentPath = path;
    this.currentLine = line;

    if (!hasTab(TAB_ID)) {
      this.openFollowTab();
    }
    // Note: no need to activate the tab here — activateTab() returns
    // early when the tab is already active, and we only want to
    // surface the follow tab if it isn't already shown (handled by
    // openFollowTab).

    this.updateTabName(basename(this.currentPath));

    if (pathChanged) {
      void this.loadFile(this.currentPath, this.currentLine);
    } else {
      this.scrollToCurrentLine();
    }
  }

  private setPaused(value: boolean): void {
    this.paused = value;
    const btn = getView().querySelector(".follow-pause-btn");
    if (btn !== null) {
      btn.setAttribute("aria-pressed", String(this.paused));
      btn.title = this.paused ? "Resume following" : "Pause following";
    }
  }

  private togglePause(): void {
    if (this.paused) {
      this.manualPause = false;
      this.setPaused(false);
      if (this.pendingPath !== "") {
        const path = this.pendingPath;
        const line = this.pendingLine;
        this.pendingPath = "";
        this.pendingLine = 0;
        this.applyLocation(path, line);
      }
    } else {
      this.manualPause = true;
      this.setPaused(true);
    }
  }

  private openCurrentInEditor(): void {
    if (this.currentPath === "") {
      return;
    }
    openFile(this.currentPath, this.currentLine > 0 ? this.currentLine : undefined);
  }

  private updateTabName(name: string): void {
    const tabEl = document.querySelector(`[data-tab-id="${TAB_ID}"] .tab-name`);
    if (tabEl !== null) {
      tabEl.textContent = name;
    }
  }

  // --- Rendering ---

  private renderWaiting(): void {
    getView().innerHTML =
      `<div class="follow-waiting">` +
      `<span class="follow-waiting-text">Waiting for agent activity...</span></div>`;
  }

  private renderHeader(path: string): string {
    return (
      `<div class="follow-header">` +
      `<span class="follow-path" title="${escText(path)}">${escText(path)}</span>` +
      `<button class="follow-open-btn" type="button" ` +
      `title="Open in editor" aria-label="Open in editor">` +
      `<svg width="14" height="14" viewBox="0 0 24 24" fill="none" ` +
      `stroke="currentColor" stroke-width="2" stroke-linecap="round" ` +
      `stroke-linejoin="round"><path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 ` +
      `01-2-2V8a2 2 0 012-2h6"/><polyline points="15 3 21 3 21 9"/>` +
      `<line x1="10" y1="14" x2="21" y2="3"/></svg></button>` +
      `<button class="follow-pause-btn" type="button" ` +
      `title="${this.paused ? "Resume following" : "Pause following"}" ` +
      `aria-label="Toggle pause" aria-pressed="${String(this.paused)}">` +
      `<svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor" ` +
      `stroke="none"><rect class="follow-pause-icon" x="6" y="4" width="4" ` +
      `height="16" rx="1"/><rect class="follow-pause-icon" x="14" y="4" ` +
      `width="4" height="16" rx="1"/>` +
      `<polygon class="follow-play-icon" points="6,4 20,12 6,20"/>` +
      `</svg></button></div>`
    );
  }

  private async loadFile(path: string, line: number): Promise<void> {
    const view = getView();
    const gen = ++this.loadGeneration;
    this.loadController?.abort();
    this.loadController = new AbortController();
    const { signal } = this.loadController;

    view.innerHTML =
      this.renderHeader(path) +
      `<div class="follow-code"><div class="follow-skeleton">` +
      `<div class="skeleton skeleton-follow-line" style="width:60%"></div>` +
      `<div class="skeleton skeleton-follow-line" style="width:80%"></div>` +
      `<div class="skeleton skeleton-follow-line" style="width:45%"></div>` +
      `<div class="skeleton skeleton-follow-line" style="width:70%"></div>` +
      `<div class="skeleton skeleton-follow-line" style="width:55%"></div>` +
      `</div></div>`;
    this.wireHeaderButtons(view);

    const resp = await apiGet<{ content?: string; error?: string }>(
      `/api/file?path=${encodeURIComponent(path)}`,
      signal,
    );
    if (gen !== this.loadGeneration || this.currentPath !== path) {
      return;
    }

    if (resp === null || resp.error !== undefined) {
      const msg = resp?.error ?? "File not available";
      view.querySelector(".follow-code")!.innerHTML =
        // eslint-disable-line @typescript-eslint/no-non-null-assertion
        `<div class="follow-error">${escText(msg)}<br><code>${escText(path)}</code></div>`;
      return;
    }

    const content = resp.content ?? "";
    this.lines = content.split("\n");
    this.filePath = path;

    const codeWrap = view.querySelector(".follow-code");
    if (codeWrap === null) {
      return;
    }

    // For small files (≤200 lines), render all at once (no virtualisation overhead).
    if (this.lines.length <= 200) {
      const gutterWidth = String(this.lines.length).length;
      let html = "";
      for (let i = 0; i < this.lines.length; i++) {
        const lineNum = i + 1;
        const active = lineNum === line ? " follow-active-line" : "";
        const gutter = String(lineNum).padStart(gutterWidth, " ");
        const highlighted = highlight(this.lines[i] ?? "", basename(path));
        html +=
          `<div class="follow-line${active}" data-line="${String(lineNum)}">` +
          `<span class="follow-gutter">${gutter}</span>` +
          `<span class="follow-text">${highlighted}</span></div>`;
      }
      codeWrap.innerHTML = `<pre class="follow-pre"><code>${html}</code></pre>`;
      scrollToLine(line);
      return;
    }

    // Virtualised rendering for large files.
    codeWrap.innerHTML = `<pre class="follow-pre follow-virtual"><code></code></pre>`;
    const pre = codeWrap.querySelector<HTMLPreElement>(".follow-pre")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion

    // Reset pool for new file
    this.rowPool = [];
    this.poolSize = 0;

    // Measure line height from a probe element.
    const probe = document.createElement("div");
    probe.className = "follow-line";
    probe.innerHTML = `<span class="follow-gutter">1</span><span class="follow-text">x</span>`;
    pre.querySelector("code")!.appendChild(probe); // eslint-disable-line @typescript-eslint/no-non-null-assertion
    this.lineHeight = probe.getBoundingClientRect().height || 20;
    probe.remove();

    // Attach scroll handler for virtual rendering.
    // NOTE: No need to remove old handler — pre is freshly created each loadFile call.
    this.scrollHandler = () => {
      this.renderWindow(pre);
    };
    pre.style.overflow = "auto";
    pre.addEventListener("scroll", this.scrollHandler);

    this.renderWindow(pre);
    this.scrollToLineVirtual(pre, line);
  }

  private renderWindow(pre: HTMLPreElement): void {
    const win = computeVirtualWindow(
      this.lines.length,
      this.lineHeight,
      pre.scrollTop,
      pre.clientHeight,
      this.bufferLines,
    );
    const startLine = win.startLine;
    const endLine = win.endLine;
    const needed = endLine - startLine;

    const code = pre.querySelector("code")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const gutterWidth = String(this.lines.length).length;
    const fname = basename(this.filePath);

    // Resize pool if needed
    while (this.rowPool.length < needed) {
      const row = document.createElement("div");
      row.className = "follow-line";
      const gutter = document.createElement("span");
      gutter.className = "follow-gutter";
      const text = document.createElement("span");
      text.className = "follow-text";
      row.append(gutter, text);
      code.appendChild(row);
      this.rowPool.push(row);
    }
    // Hide excess pool elements
    for (let i = needed; i < this.poolSize; i++) {
      this.rowPool[i]!.style.display = "none"; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    }
    this.poolSize = needed;

    // Update pool elements in place
    for (let i = 0; i < needed; i++) {
      const lineIdx = startLine + i;
      const lineNum = lineIdx + 1;
      const row = this.rowPool[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
      row.style.display = "";
      row.dataset["line"] = String(lineNum);
      const isActive = lineNum === this.currentLine;
      row.classList.toggle("follow-active-line", isActive);
      (row.children[0] as HTMLElement).textContent = String(lineNum).padStart(gutterWidth, " ");
      (row.children[1] as HTMLElement).innerHTML = highlight(this.lines[lineIdx] ?? "", fname);
    }

    code.style.paddingTop = `${win.paddingTopPx}px`;
    code.style.paddingBottom = `${win.paddingBottomPx}px`;
  }

  private scrollToLineVirtual(pre: HTMLPreElement, line: number): void {
    if (line <= 0) {
      return;
    }
    pre.scrollTop = computeScrollTarget(line, this.lineHeight, pre.clientHeight);
    // Re-render after scroll position change.
    this.renderWindow(pre);
  }

  private scrollToCurrentLine(): void {
    const view = getView();
    const pre = view.querySelector(".follow-virtual");
    if (pre !== null) {
      this.scrollToLineVirtual(pre, this.currentLine);
    } else {
      scrollToLine(this.currentLine);
    }
  }

  private wireHeaderButtons(view: HTMLElement): void {
    view.querySelector(".follow-pause-btn")?.addEventListener("click", () => {
      this.togglePause();
    });
    view.querySelector(".follow-open-btn")?.addEventListener("click", () => {
      this.openCurrentInEditor();
    });
  }
}

// ---------------------------------------------------------------------------
// Pure geometry helpers (stateless, unit-testable without DOM)
// ---------------------------------------------------------------------------

export interface VirtualWindow {
  startLine: number;
  endLine: number;
  paddingTopPx: number;
  paddingBottomPx: number;
}

export function computeVirtualWindow(
  totalLines: number,
  lineHeight: number,
  scrollTop: number,
  viewportHeight: number,
  bufferLines: number,
): VirtualWindow {
  const startLine = Math.max(0, Math.floor(scrollTop / lineHeight) - bufferLines);
  const visibleCount = Math.ceil(viewportHeight / lineHeight) + bufferLines * 2;
  const endLine = Math.min(totalLines, startLine + visibleCount);
  const totalHeight = totalLines * lineHeight;
  return {
    startLine,
    endLine,
    paddingTopPx: startLine * lineHeight,
    paddingBottomPx: Math.max(0, totalHeight - endLine * lineHeight),
  };
}

export function computeScrollTarget(
  line: number,
  lineHeight: number,
  viewportHeight: number,
): number {
  const targetTop = (line - 1) * lineHeight;
  return Math.max(0, targetTop - viewportHeight / 2);
}

// ---------------------------------------------------------------------------
// Helpers (stateless)
// ---------------------------------------------------------------------------

function basename(path: string): string {
  const i = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return i >= 0 ? path.slice(i + 1) : path;
}

function getView(): HTMLDivElement {
  return document.getElementById("follow-view") as HTMLDivElement;
}

function scrollToLine(line: number): void {
  if (line <= 0) {
    return;
  }
  const view = getView();
  view.querySelector(".follow-active-line")?.classList.remove("follow-active-line");
  const target = view.querySelector(`[data-line="${String(line)}"]`);
  if (target !== null) {
    target.classList.add("follow-active-line");
    target.scrollIntoView({ block: "center", behavior: "smooth" });
  }
}

// ---------------------------------------------------------------------------
// Module-level singleton + delegate exports for backward compat.
// ---------------------------------------------------------------------------

const instance = new FollowController();
registerCleanup(() => {
  instance.cancelLoad();
});

export function initFollowAlong(): void {
  instance.init();
}

export function showFollowView(): void {
  instance.showFollowView();
}

export function onFollowTabClosed(): void {
  instance.onFollowTabClosed();
}
