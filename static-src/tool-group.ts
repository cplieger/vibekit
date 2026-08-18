// ---------------------------------------------------------------------------
// Tool group: collapsible container that wraps consecutive tool calls.
// Auto-collapses once a run of ≥3 completed calls is done, regardless of
// whether the calls share a kind. The header shows a per-kind summary
// (e.g. "Read 5 files: a.ts, b.go, + 3 more") or a mixed breakdown for
// heterogeneous groups ("7 operations: 4 reads, 2 edits, 1 search").
//
// User-initiated clicks disable auto-collapse (the group becomes user-
// controlled) so the UI doesn't fight against the reader.
//
// FAILURE IS NOT NOISE, and that is the axis the grouping rules turn on.
// Collapsing exists to hide items that are individually uninteresting; a failed
// call is the opposite. So: a group holding a failure never auto-collapses, one
// that fails while already collapsed re-opens itself, the header's glyph is
// tinted to the WORST status inside it (one red member makes a red header, so the
// reader can act on a closed group without opening it), and the summary NAMES
// the failure rather than averaging it away — `Ran 12 commands · 1 failed`.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import type { ToolKind } from "./tool-schema.js";
import { registerCleanup } from "./actions/index.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";

/** CSS class names for tool-group collapse state machine. */
const CLS_COLLAPSED = "tool-group-collapsed";
const CLS_AUTO_COLLAPSED = "tool-group-auto-collapsed";
const CLS_USER_TOGGLED = "tool-group-user-toggled";

// Per-group disclosure controllers for the .tool-group-body region. The
// collapse STATE MACHINE (user latch, auto-collapse, the CLS_* classes) stays
// vibekit's; the region-only disclosure (trigger: null) supplies the animated
// height 0↔auto plus aria-hidden + inert on the collapsed card region — which
// the old display:none class flip provided only partially.
const groupCtls = new WeakMap<HTMLElement, DisclosureController>();

/** The card container inside a group shell. Cards are appended HERE, not to
 *  the group root, so the collapse region excludes the always-visible header. */
export function groupBody(group: HTMLElement): HTMLElement {
  return group.querySelector<HTMLElement>(":scope > .tool-group-body") ?? group;
}

class ToolGroupTracker {
  private inProgressElements = new Set<HTMLElement>();
  private tickTimer: ReturnType<typeof setInterval> | null = null;

  trackInProgress(node: HTMLElement): void {
    this.inProgressElements.add(node);
    this.startTicker();
  }

  untrackInProgress(node: HTMLElement): void {
    this.inProgressElements.delete(node);
    if (this.inProgressElements.size === 0) {
      this.stopTicker();
    }
  }

  private startTicker(): void {
    if (this.tickTimer !== null) {
      return;
    }
    this.tickTimer = setInterval(() => {
      const now = Date.now();
      for (const node of this.inProgressElements) {
        const start = node.dataset["startMs"];
        if (start === undefined) {
          this.inProgressElements.delete(node);
          continue;
        }
        const ms = now - parseInt(start, 10);
        if (ms < 2000) {
          continue;
        }
        const dur = node.querySelector(".tool-duration");
        if (dur !== null) {
          dur.textContent = formatDuration(ms);
        }
      }
      if (this.inProgressElements.size === 0) {
        this.stopTicker();
      }
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
registerCleanup(() => {
  tracker.stopTicker();
});

// --- Delegate exports ---

export function trackInProgress(node: HTMLElement): void {
  tracker.trackInProgress(node);
}
export function untrackInProgress(node: HTMLElement): void {
  tracker.untrackInProgress(node);
}

// --- Header update ---

/** Build a `.tool-group` shell: header (role=button, tabindex, aria-expanded)
 *  + keyboard (Enter/Space) + click collapse toggle. The caller appends the
 *  tool-call children and owns per-container grouping — the block dispatcher
 *  groups per render container (including nested subagent bodies), so there is
 *  no single global "current group" anymore. */
export function buildToolGroupShell(): HTMLDivElement {
  const group = el("div", { className: "tool-group" }) as HTMLDivElement;
  const header = el(
    "div",
    { className: "tool-group-header", role: "button", tabindex: "0", "aria-expanded": "true" },
    // The shared disclosure chevron, replacing a `content: "▸ "` that appeared
    // ONLY when the group was collapsed — so an expanded group advertised
    // nothing and the affordance had to be discovered. It is present in both
    // states now and rotates, like every other disclosure in the app.
    chevronEl(),
    // Same glyph slot as a member card, tinted by refreshGroupHeader to the
    // worst status inside. One vocabulary, learned once.
    el("span", { className: "tool-group-icon tool-icon" }),
    el("span", { className: "tool-group-count" }),
  ) as HTMLDivElement;
  header.addEventListener("click", () => {
    onHeaderClick(group, header);
  });
  header.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onHeaderClick(group, header);
    }
  });
  group.appendChild(header);
  const body = el("div", { className: "tool-group-body" });
  group.appendChild(body);
  groupCtls.set(group, createDisclosure(null, body, { open: true }));
  return group;
}

/** Recompute the group header summary text. Called after each card append,
 *  on every tool status flip, and on collapse toggle. */
export function refreshGroupHeader(group: HTMLElement): void {
  const header = group.querySelector(".tool-group-header .tool-group-count");
  if (header === null) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  const collapsed =
    group.classList.contains(CLS_COLLAPSED) || group.classList.contains(CLS_AUTO_COLLAPSED);
  const failures = countFailures(calls);
  // The summary states the aggregate FACT and names any failure in it. It never
  // counts cards: "Read 5 files" is right, "5 tool calls" is a bug.
  const summary = summarize(calls) + (failures > 0 ? ` \u00b7 ${String(failures)} failed` : "");
  header.textContent = collapsed ? `${summary} (collapsed)` : summary;
  paintGroupOutcome(group, calls, failures);
}

/** How many settled members of a group failed. */
function countFailures(calls: HTMLElement[]): number {
  return calls.filter((c) => c.dataset["outcome"] === "fail").length;
}

/** Tint the group's glyph to the worst status inside it, with the same
 *  check/cross shape a member card carries. Reads the members' own
 *  `data-outcome`, so there is one source for the state. */
function paintGroupOutcome(group: HTMLElement, calls: HTMLElement[], failures: number): void {
  const icon = group.querySelector<HTMLElement>(".tool-group-icon");
  if (icon === null) {
    return;
  }
  const running = calls.some((c) => c.dataset["outcome"] === "running");
  const state = failures > 0 ? "fail" : running ? "running" : "ok";
  group.dataset["outcome"] = state;
  icon.classList.remove("is-ok", "is-fail", "is-running");
  icon.classList.add(`is-${state}`);
  icon.textContent = state === "fail" ? "\u2717" : state === "ok" ? "\u2713" : "";
  icon.setAttribute("aria-hidden", "true");
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
  if (n === 0) {
    return "0 tool calls";
  }
  const infos: CallInfo[] = calls.map((c) => ({
    kind: (c.dataset["kind"] ?? "other") as ToolKind,
    title: c.dataset["title"] ?? "",
    filename: c.dataset["filename"] ?? "",
    mcpServer: c.dataset["mcpServer"] ?? "",
  }));

  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const firstKind = infos[0]!.kind;
  const allSame = infos.every((i) => i.kind === firstKind);
  if (allSame) {
    return summarizeSameKind(firstKind, infos);
  }
  return summarizeMixed(infos);
}

const TOOL_KIND_LABELS: Readonly<
  Record<ToolKind, { verb: string; noun: string; samplesFrom: "files" | "titles" }>
> = {
  read: { verb: "Read", noun: "file", samplesFrom: "files" },
  edit: { verb: "Edited", noun: "file", samplesFrom: "files" },
  write: { verb: "Wrote", noun: "file", samplesFrom: "files" },
  delete: { verb: "Deleted", noun: "file", samplesFrom: "files" },
  move: { verb: "Moved", noun: "file", samplesFrom: "files" },
  search: { verb: "Searched", noun: "search", samplesFrom: "titles" },
  execute: { verb: "Ran", noun: "command", samplesFrom: "titles" },
  shell: { verb: "Ran", noun: "shell command", samplesFrom: "titles" },
  hook: { verb: "Ran", noun: "hook", samplesFrom: "titles" },
  fetch: { verb: "Fetched", noun: "URL", samplesFrom: "titles" },
  think: { verb: "", noun: "thinking step", samplesFrom: "titles" },
  switch_mode: { verb: "", noun: "mode switch", samplesFrom: "titles" },
  mcp: { verb: "", noun: "integration call", samplesFrom: "titles" },
  browser: { verb: "Browsed", noun: "page", samplesFrom: "titles" },
  command: { verb: "Ran", noun: "command", samplesFrom: "titles" },
  other: { verb: "Ran", noun: "call", samplesFrom: "titles" },
};

export function summarizeSameKind(kind: ToolKind, infos: CallInfo[]): string {
  const n = infos.length;
  if (kind === "mcp") {
    return summarizeMCP(infos);
  }

  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  const label = TOOL_KIND_LABELS[kind] ?? TOOL_KIND_LABELS.other;
  if (label.verb === "") {
    const plural = n === 1 ? label.noun : kindNoun(kind, n);
    return `${String(n)} ${plural}`;
  }
  const samples =
    label.samplesFrom === "files"
      ? infos.map((i) => i.filename).filter((s) => s !== "")
      : infos.map((i) => i.title).filter((s) => s !== "");
  return labelWithSamples(n, label.noun, label.verb, samples);
}

export function summarizeMCP(infos: CallInfo[]): string {
  const n = infos.length;
  const servers = new Set(infos.map((i) => i.mcpServer).filter((s) => s !== ""));
  if (servers.size === 1) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const server = infos[0]!.mcpServer;
    const titles = dedup(infos.map((i) => i.title).filter((s) => s !== ""));
    const head = `Called ${String(n)} ${server} tool${n === 1 ? "" : "s"}`;
    if (titles.length === 0) {
      return head;
    }
    const shown = titles.slice(0, 2);
    const more = titles.length - shown.length;
    const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
    return `${head}: ${tail}`;
  }
  const counts = new Map<string, number>();
  for (const i of infos) {
    counts.set(i.mcpServer, (counts.get(i.mcpServer) ?? 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([srv, c]) => `${String(c)} ${srv}`);
  return `${String(n)} integration call${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

export function labelWithSamples(n: number, noun: string, verb: string, samples: string[]): string {
  const pluralNoun = n === 1 ? noun : `${noun}s`;
  const head = `${verb} ${String(n)} ${pluralNoun}`;
  const uniq = dedup(samples);
  if (uniq.length === 0) {
    return head;
  }
  const shown = uniq.slice(0, 2);
  const more = uniq.length - shown.length;
  const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
  return `${head}: ${tail}`;
}

export function summarizeMixed(infos: CallInfo[]): string {
  const n = infos.length;
  const counts = new Map<string, number>();
  for (const i of infos) {
    counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([k, c]) => `${String(c)} ${kindNoun(k, c)}`);
  return `${String(n)} operation${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

export function kindNoun(kind: string, count: number): string {
  const single: Record<string, string> = {
    read: "read",
    edit: "edit",
    write: "write",
    delete: "delete",
    move: "move",
    search: "search",
    execute: "command",
    fetch: "fetch",
    think: "thinking step",
    switch_mode: "mode switch",
    mcp: "integration call",
  };
  const noun = single[kind] ?? "call";
  if (count === 1) {
    return noun;
  }
  if (noun.endsWith("s")) {
    return noun;
  }
  if (noun.endsWith("x") || noun.endsWith("h")) {
    return noun + "es";
  }
  return noun + "s";
}

function dedup(arr: string[]): string[] {
  return [...new Set(arr)];
}

// --- Collapse / expand ---

function onHeaderClick(group: HTMLDivElement, _header: HTMLDivElement): void {
  group.classList.add(CLS_USER_TOGGLED);
  const wasAuto = group.classList.contains(CLS_AUTO_COLLAPSED);
  if (wasAuto) {
    group.classList.remove(CLS_AUTO_COLLAPSED);
  }
  group.classList.toggle(CLS_COLLAPSED);
  const collapsedNow = group.classList.contains(CLS_COLLAPSED);
  // Drive the body region's disclosure to match the class-derived state
  // (preserving the existing model, where a click on an AUTO-collapsed group
  // converts it to a USER collapse and it stays closed).
  const ctl = groupCtls.get(group);
  if (collapsedNow) {
    ctl?.close();
  } else {
    ctl?.open();
  }
  _header.setAttribute("aria-expanded", collapsedNow ? "false" : "true");
  refreshGroupHeader(group);
  if (!collapsedNow || wasAuto) {
    setUserScrolledUp(true);
  }
}

export function maybeCollapseGroup(node: HTMLElement): void {
  const group = node.closest<HTMLElement>(".tool-group");
  if (group === null) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];

  // A failure inside the group defeats collapse in BOTH directions: it blocks an
  // auto-collapse, and it re-opens a group that already auto-collapsed before
  // the failing member settled. Without the second half a failure inside a run
  // of twelve is invisible — the group closed while everything still looked fine.
  if (countFailures(calls) > 0) {
    if (
      group.classList.contains(CLS_AUTO_COLLAPSED) &&
      !group.classList.contains(CLS_USER_TOGGLED)
    ) {
      preserveReadingPosition(() => {
        group.classList.remove(CLS_AUTO_COLLAPSED);
        groupCtls.get(group)?.open();
        group
          .querySelector<HTMLElement>(".tool-group-header")
          ?.setAttribute("aria-expanded", "true");
        refreshGroupHeader(group);
      }, "content-growth");
    }
    return;
  }

  if (group.classList.contains(CLS_AUTO_COLLAPSED)) {
    return;
  }
  if (group.classList.contains(CLS_USER_TOGGLED)) {
    return;
  }
  if (calls.length < 3) {
    return;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return;
    }
  }
  // An AUTO collapse removes height ABOVE the reader, so it is compensated.
  // (The user-toggle path below enters Reading instead — that is a different
  // intent: the reader just acted, so nothing should re-pin.) This is the one
  // ANIMATED height change of the three §3.4 names, via createDisclosure.
  preserveReadingPosition(() => {
    group.classList.add(CLS_AUTO_COLLAPSED);
    groupCtls.get(group)?.close();
    const header = group.querySelector<HTMLElement>(".tool-group-header");
    header?.setAttribute("aria-expanded", "false");
    refreshGroupHeader(group);
  }, "content-growth");
}

export function formatDuration(ms: number): string {
  if (ms < 60_000) {
    return `${(ms / 1000).toFixed(1)}s`;
  }
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}m${s % 60}s`;
}
