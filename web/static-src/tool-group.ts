// ---------------------------------------------------------------------------
// Tool group: collapsible container that wraps consecutive tool calls.
// Auto-collapses once a run of ≥3 completed calls is done, regardless of
// whether the calls share a kind. The header shows a per-kind summary
// (e.g. "Read 5 files: a.ts, b.go, + 3 more") or a mixed breakdown for
// heterogeneous groups ("7 operations: 4 reads, 2 edits, 1 search").
//
// User-initiated clicks disable auto-collapse (the group becomes user-
// controlled) so the UI doesn't fight against the reader.
// ---------------------------------------------------------------------------

import { setUserScrolledUp } from "./scroll.js";
import type { ToolKind } from "./tool-schema.js";
import { registerCleanup } from "./actions/index.js";

class ToolGroupTracker {
  private currentGroup: HTMLDivElement | null = null;
  private lastWasToolCall = false;
  private inProgressElements = new Set<HTMLElement>();
  private tickTimer: ReturnType<typeof setInterval> | null = null;

  breakGroup(): void {
    this.lastWasToolCall = false;
    this.currentGroup = null;
  }

  getOrCreateGroup(mount: (el: HTMLElement) => void): HTMLDivElement {
    if (!this.lastWasToolCall || this.currentGroup === null) {
      const group = document.createElement("div");
      group.className = "tool-group";
      const header = document.createElement("div");
      header.className = "tool-group-header";
      header.setAttribute("role", "button");
      header.setAttribute("tabindex", "0");
      header.setAttribute("aria-expanded", "true");
      header.innerHTML = '<span class="tool-group-count"></span>';
      header.addEventListener("click", () => onHeaderClick(group, header));
      header.addEventListener("keydown", (e: KeyboardEvent) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onHeaderClick(group, header);
        }
      });
      group.appendChild(header);
      mount(group);
      this.currentGroup = group;
    }
    this.lastWasToolCall = true;
    queueHeaderUpdate(this.currentGroup);
    return this.currentGroup;
  }

  trackInProgress(el: HTMLElement): void {
    this.inProgressElements.add(el);
    this.startTicker();
  }

  untrackInProgress(el: HTMLElement): void {
    this.inProgressElements.delete(el);
    if (this.inProgressElements.size === 0) this.stopTicker();
  }

  private startTicker(): void {
    if (this.tickTimer !== null) return;
    this.tickTimer = setInterval(() => {
      const now = Date.now();
      for (const el of this.inProgressElements) {
        const start = el.dataset["startMs"];
        if (start === undefined) { this.inProgressElements.delete(el); continue; }
        const ms = now - parseInt(start, 10);
        if (ms < 2000) continue;
        const dur = el.querySelector(".tool-duration");
        if (dur !== null) dur.textContent = formatDuration(ms);
      }
      if (this.inProgressElements.size === 0) this.stopTicker();
    }, 1000);
  }

  /** @internal Used by registerCleanup. */
  stopTicker(): void {
    if (this.tickTimer !== null) {
      clearInterval(this.tickTimer);
      this.tickTimer = null;
    }
  }
}

const tracker = new ToolGroupTracker();
registerCleanup(() => { tracker.stopTicker(); });

// --- Delegate exports ---

export function breakToolGroup(): void { tracker.breakGroup(); }

export function getOrCreateToolGroup(mount: (el: HTMLElement) => void): HTMLDivElement {
  return tracker.getOrCreateGroup(mount);
}

export function trackInProgress(el: HTMLElement): void { tracker.trackInProgress(el); }
export function untrackInProgress(el: HTMLElement): void { tracker.untrackInProgress(el); }

// --- Header update ---

function queueHeaderUpdate(group: HTMLDivElement): void {
  queueMicrotask(() => updateHeader(group));
}

function updateHeader(group: HTMLElement): void {
  const header = group.querySelector(".tool-group-header .tool-group-count");
  if (header === null) return;
  const calls = [...group.querySelectorAll(":scope > .tool-call")] as HTMLElement[];
  const collapsed = group.classList.contains("tool-group-collapsed")
    || group.classList.contains("tool-group-auto-collapsed");
  const summary = summarize(calls);
  header.textContent = collapsed ? `${summary} (collapsed)` : summary;
}

// --- Summarizers (pure, no state dependency) ---

export interface CallInfo {
  kind: ToolKind;
  title: string;
  filename: string;
  mcpServer: string;
}

export function summarize(calls: HTMLElement[]): string {
  const n = calls.length;
  if (n === 0) return "0 tool calls";
  const infos: CallInfo[] = calls.map((c) => ({
    kind: (c.dataset["kind"] ?? "other") as ToolKind,
    title: c.dataset["title"] ?? "",
    filename: c.dataset["filename"] ?? "",
    mcpServer: c.dataset["mcpServer"] ?? "",
  }));

  const firstKind = infos[0]!.kind;
  const allSame = infos.every((i) => i.kind === firstKind);
  if (allSame) return summarizeSameKind(firstKind, infos);
  return summarizeMixed(infos);
}

const TOOL_KIND_LABELS: Readonly<Record<ToolKind, { verb: string; noun: string; samplesFrom: "files" | "titles" }>> = {
  read:        { verb: "Read",     noun: "file",             samplesFrom: "files" },
  edit:        { verb: "Edited",   noun: "file",             samplesFrom: "files" },
  write:       { verb: "Wrote",    noun: "file",             samplesFrom: "files" },
  delete:      { verb: "Deleted",  noun: "file",             samplesFrom: "files" },
  move:        { verb: "Moved",    noun: "file",             samplesFrom: "files" },
  search:      { verb: "Searched", noun: "search",           samplesFrom: "titles" },
  execute:     { verb: "Ran",      noun: "command",          samplesFrom: "titles" },
  shell:       { verb: "Ran",      noun: "shell command",    samplesFrom: "titles" },
  hook:        { verb: "Ran",      noun: "hook",             samplesFrom: "titles" },
  fetch:       { verb: "Fetched",  noun: "URL",              samplesFrom: "titles" },
  think:       { verb: "",         noun: "thinking step",    samplesFrom: "titles" },
  switch_mode: { verb: "",         noun: "mode switch",      samplesFrom: "titles" },
  mcp:         { verb: "",         noun: "integration call", samplesFrom: "titles" },
  browser:     { verb: "Browsed",  noun: "page",             samplesFrom: "titles" },
  command:     { verb: "Ran",      noun: "command",          samplesFrom: "titles" },
  other:       { verb: "Ran",      noun: "call",             samplesFrom: "titles" },
};

export function summarizeSameKind(kind: ToolKind, infos: CallInfo[]): string {
  const n = infos.length;
  if (kind === "mcp") return summarizeMCP(infos);

  const label = TOOL_KIND_LABELS[kind] ?? TOOL_KIND_LABELS.other;
  if (label.verb === "") {
    const plural = n === 1 ? label.noun : kindNoun(kind, n);
    return `${String(n)} ${plural}`;
  }
  const samples = label.samplesFrom === "files"
    ? infos.map((i) => i.filename).filter((s) => s !== "")
    : infos.map((i) => i.title).filter((s) => s !== "");
  return labelWithSamples(n, label.noun, label.verb, samples);
}

export function summarizeMCP(infos: CallInfo[]): string {
  const n = infos.length;
  const servers = new Set(infos.map((i) => i.mcpServer).filter((s) => s !== ""));
  if (servers.size === 1) {
    const server = infos[0]!.mcpServer;
    const titles = dedup(infos.map((i) => i.title).filter((s) => s !== ""));
    const head = `Called ${String(n)} ${server} tool${n === 1 ? "" : "s"}`;
    if (titles.length === 0) return head;
    const shown = titles.slice(0, 2);
    const more = titles.length - shown.length;
    const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
    return `${head}: ${tail}`;
  }
  const counts = new Map<string, number>();
  for (const i of infos) counts.set(i.mcpServer, (counts.get(i.mcpServer) ?? 0) + 1);
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([srv, c]) => `${String(c)} ${srv}`);
  return `${String(n)} integration call${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

export function labelWithSamples(n: number, noun: string, verb: string, samples: string[]): string {
  const pluralNoun = n === 1 ? noun : `${noun}s`;
  const head = `${verb} ${String(n)} ${pluralNoun}`;
  const uniq = dedup(samples);
  if (uniq.length === 0) return head;
  const shown = uniq.slice(0, 2);
  const more = uniq.length - shown.length;
  const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
  return `${head}: ${tail}`;
}

export function summarizeMixed(infos: CallInfo[]): string {
  const n = infos.length;
  const counts = new Map<string, number>();
  for (const i of infos) counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1);
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([k, c]) => `${String(c)} ${kindNoun(k, c)}`);
  return `${String(n)} operation${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

export function kindNoun(kind: string, count: number): string {
  const single: Record<string, string> = {
    read: "read", edit: "edit", write: "write",
    delete: "delete", move: "move",
    search: "search", execute: "command",
    fetch: "fetch", think: "thinking step",
    switch_mode: "mode switch",
    mcp: "integration call",
  };
  const noun = single[kind] ?? "call";
  if (count === 1) return noun;
  if (noun.endsWith("s")) return noun;
  if (noun.endsWith("x") || noun.endsWith("h")) return noun + "es";
  return noun + "s";
}

function dedup(arr: string[]): string[] {
  return [...new Set(arr)];
}

// --- Collapse / expand ---

function onHeaderClick(group: HTMLDivElement, _header: HTMLDivElement): void {
  group.classList.add("tool-group-user-toggled");
  const wasAuto = group.classList.contains("tool-group-auto-collapsed");
  if (wasAuto) group.classList.remove("tool-group-auto-collapsed");
  group.classList.toggle("tool-group-collapsed");
  const collapsedNow = group.classList.contains("tool-group-collapsed");
  _header.setAttribute("aria-expanded", collapsedNow ? "false" : "true");
  updateHeader(group);
  if (!collapsedNow || wasAuto) setUserScrolledUp(true);
}

export function maybeCollapseGroup(el: HTMLElement): void {
  const group = el.closest(".tool-group") as HTMLElement | null;
  if (group === null) return;
  if (group.classList.contains("tool-group-auto-collapsed")) return;
  if (group.classList.contains("tool-group-user-toggled")) return;
  const calls = group.querySelectorAll(":scope > .tool-call");
  if (calls.length < 3) return;
  for (const c of calls) {
    if ((c as HTMLElement).dataset["startMs"] !== undefined) return;
  }
  group.classList.add("tool-group-auto-collapsed");
  const header = group.querySelector(".tool-group-header") as HTMLElement | null;
  header?.setAttribute("aria-expanded", "false");
  updateHeader(group);
}

export function formatDuration(ms: number): string {
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}m${s % 60}s`;
}
