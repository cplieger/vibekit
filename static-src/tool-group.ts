// ---------------------------------------------------------------------------
// Tool group: collapsible container that wraps consecutive tool calls.
//
// THE BOX ONLY EXISTS FROM THE SECOND CONSECUTIVE CALL. A lone tool call is not
// a group of one: a header summarising "Read 1 file: a.ts" over a single card
// that already says exactly that is two renderings of one fact, wrapped in a
// disclosure with nothing to hide. So a one-member shell carries CLS_BARE, and
// `14-tools.css` answers it by dropping all three things that make a member a ROW
// rather than a card — the header, the first row's separator hairline, and the
// tighter `padding-block`. What is left is PIXEL-IDENTICAL to a standalone
// `.tool-call`, because `.tool-group` and `.tool-call` already declare the same
// four chrome properties (background, border, radius, overflow) from the same
// tokens: measured 42px against 42px in headless Chromium with the real card
// builder, and a screenshot of each came out BYTE-IDENTICAL. The padding half is
// the one a synthetic fixture hides — a hand-rolled card measures under both
// floors, so both states report the floor and the 6px gap is invisible.
//
// The shell is mounted on the FIRST card and gains its chrome by class when the
// second lands, rather than the first card being mounted bare and re-parented
// later. Re-seating a node restarts its CSS animations and blurs focus inside it,
// and `.tool-call` carries `vk-slide-up` — so promoting a card mid-stream would
// replay the entry slide on a card the reader is already looking at, and would
// drop focus if they had opened its output or diff. The cost of the cheaper path
// is stated plainly: the selector `.tool-group` stops meaning "two or more", so
// anything reading it (a CSS rule, a test, an audit script) must consult
// `groupIsBare`. The bare header is `display: none`, which removes it from the
// accessibility tree AND from tab order, so there is no phantom `aria-expanded`
// and no dead tab stop. The second cost, also stated: the first card SHRINKS 6px
// when the second lands, in the same frame as the header appearing — one reflow
// inside a change that is already adding 37px of header.
//
// A BARE GROUP NEVER COLLAPSES, in either direction: its body region IS the lone
// card, so closing the disclosure would make the card vanish.
//
// The collapse trigger is POSITIONAL, not a count: a group stays open while it is
// the newest thing in its container and folds when the next element is posted
// after it. THE VERDICT IS APPLIED AT CONSTRUCTION — `ensureGroupDisclosure` reads
// the value the dispatcher pushes in through `setGroupSuperseded` and creates the
// controller already closed — and `autoCollapseGroup` is the LIVE path only, for a
// group superseded while the reader is watching it. That split is what stops a
// repaint animating: the disclosure primitive commits the OPEN height before it
// writes the change (`runTransition` forces a style flush on purpose), so "create
// it open and close it in the same task" animates exactly as loudly as closing it a
// frame later, and disposing-and-recreating a region whose open height has reached
// one frame animates too. Constructing the controller in its FINAL state is the only
// silent route. A pass that rebuilds a superseded run therefore PAINTS the fold
// rather than replaying it.
// The header shows a per-kind summary (e.g. "Read 5 files: a.ts, b.go, + 3 more")
// or a mixed breakdown for heterogeneous groups ("7 operations: 4 reads, 2 edits,
// 1 search").
//
// User-initiated clicks disable auto-collapse (the group becomes user-
// controlled) so the UI doesn't fight against the reader.
//
// FAILURE IS NOT NOISE, and that is the axis the grouping rules turn on.
// Collapsing exists to hide items that are individually uninteresting; a failed
// call is the opposite. So: a group holding a failure never auto-collapses, one
// that fails while already collapsed re-opens itself, the header's mark takes
// the SHAPE and tint of the WORST status inside it (one red member makes a red
// triangle, so the reader can act on a closed group without opening it), and the
// summary NAMES the failure rather than averaging it away — `Ran 12 commands ·
// 1 failed`.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { outcomeIcon } from "./icons.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import { kindNoun } from "./tool-kind-noun.js";
import type { ToolKind } from "./tool-schema.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";

/** CSS class names for tool-group collapse state machine. */
const CLS_COLLAPSED = "tool-group-collapsed";
const CLS_AUTO_COLLAPSED = "tool-group-auto-collapsed";
const CLS_USER_TOGGLED = "tool-group-user-toggled";
/** A shell holding fewer than two members: no header, no group chrome. A pure
 *  function of the member count with ONE writer (refreshGroupHeader), so there is
 *  no second flag or counter to keep in step. */
const CLS_BARE = "tool-group-bare";

// Per-group disclosure controllers for the .tool-group-body region. The
// collapse STATE MACHINE (user latch, auto-collapse, the CLS_* classes) stays
// vibekit's; the region-only disclosure (trigger: null) supplies the animated
// height 0↔auto plus aria-hidden + inert on the collapsed card region — which
// the old display:none class flip provided only partially.
//
// ABSENT on a BARE shell, which is what makes `ensureGroupDisclosure` create-only:
// the controller is minted by the refresh that takes the group non-bare, in the
// state the verdict below asks for, and nothing re-creates it afterwards.
const groupCtls = new WeakMap<HTMLElement, DisclosureController>();

// The newest-element verdict for a group, pushed in by the dispatcher at every card
// append (`setGroupSuperseded`). Read ONCE, by the refresh that creates the
// controller — after that the state machine owns the fold. A group with no entry
// reads as NOT superseded, the same direction `syncContainerCollapse`'s own
// absent-seat rule takes, so the two cannot disagree and be born expanded and folded
// by one pass.
const groupSuperseded = new WeakMap<HTMLElement, boolean>();

/** Record whether the run this group holds is FOLLOWED by a later element. Written
 *  per card append rather than once at creation, on purpose: a run can straddle a
 *  cold-build slice boundary, so the pass that appends the second card is not always
 *  the pass that created the group, and only the current block index has the answer. */
export function setGroupSuperseded(group: HTMLElement, superseded: boolean): void {
  groupSuperseded.set(group, superseded);
}

/** The card container inside a group shell. Cards are appended HERE, not to
 *  the group root, so the collapse region excludes the always-visible header. */
export function groupBody(group: HTMLElement): HTMLElement {
  return group.querySelector<HTMLElement>(":scope > .tool-group-body") ?? group;
}

/** Whether this shell is holding fewer than two members, so it renders as a plain
 *  card with no header. Read it rather than the `.tool-group` selector: that
 *  selector no longer means "a group of two or more". */
export function groupIsBare(group: HTMLElement): boolean {
  return group.classList.contains(CLS_BARE);
}

// --- Header update ---

/** Build a `.tool-group` shell: header (role=button, tabindex, aria-expanded)
 *  + keyboard (Enter/Space) + click collapse toggle. The caller appends the
 *  tool-call children and owns per-container grouping — the block dispatcher
 *  groups per render container (including nested subagent bodies), so there is
 *  no single global "current group" anymore. */
export function buildToolGroupShell(): HTMLDivElement {
  // Born BARE, so "bare ⇔ fewer than two members" holds at every instant
  // including the one between construction and the first append. The alternative
  // is leaving the first card's own refreshGroupHeader to add it, which is
  // invisible in production (mountToolCard builds, appends and refreshes in one
  // task) and would make the invariant true only after that task.
  const group = el("div", { className: `tool-group ${CLS_BARE}` }) as HTMLDivElement;
  const header = el(
    "div",
    {
      className: "tool-group-header",
      role: "button",
      tabindex: "0",
      "aria-expanded": "true",
      [CHROME_ATTR]: "",
    },
    // The shared disclosure chevron, replacing a `content: "▸ "` that appeared
    // ONLY when the group was collapsed — so an expanded group advertised
    // nothing and the affordance had to be discovered. It is present in both
    // states now and rotates, like every other disclosure in the app.
    chevronEl(),
    // The header's verdict slot: an ICON slot, sharing `.tool-icon` for the tint
    // classes. paintGroupOutcome writes one of `outcomeIcon("ok")` / `("fail")` /
    // `("warn")` into it as a node, so a collapsed group of twelve searches shows a
    // verdict rather than a magnifier — the KIND is named in the summary text
    // instead. It writes NO node while the group runs, because there is no verdict
    // yet; `14-tools.css` draws the hollow ring that says so, and the box is sized
    // by the slot either way so the count text does not shift when it settles.
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
  group.appendChild(el("div", { className: "tool-group-body" }));
  // NO disclosure here, and the header's `aria-expanded="true"` above is markup
  // rather than a claim: a shell is born CLS_BARE and a bare shell has nothing to
  // disclose (its header is `display: none`, so it is out of the accessibility tree
  // and out of tab order). `ensureGroupDisclosure` mints the controller when the
  // second card makes the group real, in its final open/closed state — the same
  // "withdraw the control when there is nothing to reveal" rule
  // `fundamentals/subagent-block.ts` `syncDisclosure` and `tool-card.ts`
  // `refreshToolDisclosure` already follow.
  return group;
}

/** Bring a group's header into line with its members: the bare class, the body
 *  region's existence (this is the ONE creation site — `ensureGroupDisclosure`), the
 *  summary text and the outcome verdict. Called after each card append, on every tool
 *  status flip, and on collapse toggle. */
export function refreshGroupHeader(group: HTMLElement): void {
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  // The ONE writer of the bare class, and it runs after every card append, on
  // every status flip and on every collapse toggle — so the class is a pure
  // function of the member count and reversible if a group ever drops to one.
  // It sits ABOVE the summary-span guard below because it needs only `calls`:
  // behind that guard the invariant would hold for a shell carrying a count span
  // and not for one without.
  group.classList.toggle(CLS_BARE, calls.length < 2);
  // Deliberately ABOVE the summary-span guard too, and for the same reason: it needs
  // only `calls`, and a shell carrying no count span would otherwise never get its
  // region. This is the ONE creation site.
  ensureGroupDisclosure(group, calls);
  const header = group.querySelector(".tool-group-header .tool-group-count");
  if (header === null) {
    return;
  }
  const counts = countOutcomes(calls);
  // The summary states the aggregate FACT and names every non-clean population in
  // it, so a COLLAPSED group still says what happened. It never counts cards:
  // "Read 5 files" is right, "5 tool calls" is a bug.
  const summary = summarize(calls) + namedCounts(counts);
  // No "(collapsed)" suffix: the group is one box whose chevron and
  // aria-expanded already carry the state, so the word restated the chrome.
  header.textContent = summary;
  paintGroupOutcome(group, calls, counts);
}

/** Give a non-bare group its body region, ONCE, in its FINAL open/closed state.
 *
 *  CREATE-ONLY, which is what keeps the state machine the only thing that moves a
 *  fold afterwards: a later refresh finds the controller and returns. The reverse
 *  transition (non-bare → bare, which `pruneEmptyContainers` can produce by dropping
 *  a member) therefore leaves the controller in place; noted rather than handled,
 *  because a disposal there would also have to clear `region.style.height` by hand —
 *  `DisclosureController.dispose()` pins `0px` for a closed region. */
function ensureGroupDisclosure(group: HTMLElement, calls: HTMLElement[]): void {
  if (groupIsBare(group) || groupCtls.has(group)) {
    return;
  }
  const collapsed = bornCollapsed(group, calls);
  groupCtls.set(group, createDisclosure(null, groupBody(group), { open: !collapsed }));
  if (collapsed) {
    markAutoCollapsed(group);
  }
}

/** Whether this group's region is created CLOSED. Exactly `autoCollapseGroup`'s
 *  carve-outs, read at construction: the dispatcher's verdict, and then the three
 *  refusals — a reader who has decided, a failure inside (failure is not noise), and
 *  a member still running. */
function bornCollapsed(group: HTMLElement, calls: HTMLElement[]): boolean {
  if (groupSuperseded.get(group) !== true || group.classList.contains(CLS_USER_TOGGLED)) {
    return false;
  }
  if (countOutcomes(calls).failures > 0) {
    return false;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return false;
    }
  }
  return true;
}

/** The AUTO-collapsed marking: the class the state machine reads and the header
 *  state a screen reader reads, with one writer for both. The summary refresh stays
 *  the caller's rather than joining it, because `ensureGroupDisclosure` runs INSIDE
 *  `refreshGroupHeader` — refreshing here would re-enter it, and the enclosing call
 *  finishes the job anyway. */
function markAutoCollapsed(group: HTMLElement): void {
  group.classList.add(CLS_AUTO_COLLAPSED);
  group.querySelector<HTMLElement>(".tool-group-header")?.setAttribute("aria-expanded", "false");
}

/** How many settled members of a group failed, how many refused, and how many
 *  were stopped. ONE walk, because the roll-up needs all three and the FOLD needs
 *  only `failures` — a stopped OR refused member must keep folding, since both
 *  are settles, which is the ruling the delegate card already carries.
 *
 *  `denied` is deliberately in none of them, and the asymmetry with `declined` is
 *  the reason rather than an omission: a policy refusal means the command never
 *  RAN, so it contributes nothing to what this group did, while a declined call
 *  ran and answered. */
interface GroupCounts {
  readonly failures: number;
  readonly declined: number;
  readonly aborted: number;
}

function countOutcomes(calls: HTMLElement[]): GroupCounts {
  let failures = 0;
  let declined = 0;
  let aborted = 0;
  for (const c of calls) {
    const outcome = c.dataset["outcome"];
    if (outcome === "fail") {
      failures++;
    } else if (outcome === "declined") {
      declined++;
    } else if (outcome === "warn") {
      aborted++;
    }
  }
  return { failures, declined, aborted };
}

/** Every word here is the one a tool ROW announces for that state (`tool-card.ts`
 *  `outcomeWord`), so the group and its members agree. Ordered worst-first, which
 *  is also `paintGroupOutcome`'s order, so on the ordinary group — one non-clean
 *  population — the mark and its own clause name the same thing. */
function namedCounts({ failures, declined, aborted }: GroupCounts): string {
  const parts: string[] = [];
  if (failures > 0) {
    parts.push(`${String(failures)} failed`);
  }
  if (declined > 0) {
    parts.push(`${String(declined)} declined`);
  }
  if (aborted > 0) {
    parts.push(`${String(aborted)} aborted`);
  }
  return parts.map((p) => ` \u00b7 ${p}`).join("");
}

/** Tint the group's mark to the worst status inside it, and give it the SHAPE that
 *  state carries. Reads the members' own `data-outcome`, so there is one source for
 *  the state. `denied` folds onto `ok` deliberately: the summary has no word for a
 *  policy refusal that is not this app's word for a stop.
 *
 *  ONE mark for a MIXED group, so the two amber states need an order: `declined`
 *  outranks `warn` because a refusal is something the WORK did and a stop is
 *  something the READER did, so the one they did not cause is the one to surface.
 *  The summary names both populations either way, which is what keeps the fold of
 *  the other one from being a loss.
 *
 *  The mark is `icons.ts` `outcomeIcon` rather than `applyOutcome`: this slot has no
 *  identity glyph to keep for a success. `running` writes no node — its mark is the
 *  hollow ring `14-tools.css` draws on the slot. */
function paintGroupOutcome(group: HTMLElement, calls: HTMLElement[], counts: GroupCounts): void {
  const icon = group.querySelector<HTMLElement>(".tool-group-icon");
  if (icon === null) {
    return;
  }
  const running = calls.some((c) => c.dataset["outcome"] === "running");
  const state =
    counts.failures > 0
      ? "fail"
      : running
        ? "running"
        : counts.declined > 0
          ? "declined"
          : counts.aborted > 0
            ? "warn"
            : "ok";
  group.dataset["outcome"] = state;
  icon.classList.remove("is-ok", "is-fail", "is-warn", "is-declined", "is-running");
  icon.classList.add(`is-${state}`);
  if (state === "running") {
    icon.replaceChildren();
  } else {
    icon.replaceChildren(iconEl(outcomeIcon(state)));
  }
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
  // Keyed by ToolKind rather than string: `kindNoun`'s table is total over the
  // union, so a key outside it has no noun rather than a fallback one.
  const counts = new Map<ToolKind, number>();
  for (const i of infos) {
    counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([k, c]) => `${String(c)} ${kindNoun(k, c)}`);
  return `${String(n)} operation${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

function dedup(arr: string[]): string[] {
  return [...new Set(arr)];
}

// --- Collapse / expand ---

function onHeaderClick(group: HTMLDivElement, _header: HTMLDivElement): void {
  // No bare guard here, unlike both collapse paths below, and the asymmetry is
  // not an oversight: a bare header is `display: none`, so it is out of the
  // accessibility tree and out of tab order and this handler is unreachable.
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
  // A bare group is never collapsed, so there is nothing to re-open. Its lone
  // card carries its own failure mark, which is the actionable signal a group
  // header would otherwise be standing in for.
  if (groupIsBare(group)) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];

  // A failure inside the group defeats collapse in BOTH directions: it blocks an
  // auto-collapse, and it re-opens a group that already auto-collapsed before
  // the failing member settled. Without the second half a failure inside a run
  // of twelve is invisible — the group closed while everything still looked fine.
  // The FAILURE count alone: a stopped member folds like any other settle.
  if (countOutcomes(calls).failures > 0) {
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

  // No count-based collapse here any more: a group stays OPEN while it is the
  // newest card and collapses when the next element is posted after it —
  // autoCollapseGroup, called by the dispatcher at the moment the run of
  // consecutive calls ends.
}

/** Collapse a group whose run of consecutive calls just ENDED (something else
 *  was posted after it). The positional rule that replaced "auto-collapse after
 *  ≥3 completed calls": the newest card is the open one, and being superseded
 *  is what closes it. A BARE group is exempt (there is no box to fold), a user
 *  toggle outranks it, a failure inside blocks it (failure is not noise), and a
 *  still-running member keeps it open — that member's status flip re-runs
 *  maybeCollapseGroup, which re-opens on failure.
 *
 *  THE LIVE PATH ONLY. A group the verdict already condemned when its region was
 *  created is closed by `ensureGroupDisclosure` instead, silently; this one animates,
 *  which is what a supersede the reader is watching should do. */
export function autoCollapseGroup(group: HTMLElement): void {
  // A BARE group's body region IS the lone card, and its header is hidden — so
  // closing the disclosure would make the card vanish with no affordance to bring
  // it back. Correctness, not tidiness.
  if (groupIsBare(group)) {
    return;
  }
  const ctl = groupCtls.get(group);
  if (ctl === undefined) {
    // No region to close, so no class either: the marking and the fold are one fact,
    // and a group carrying CLS_AUTO_COLLAPSED with its body wide open is the shape a
    // reader cannot act on. Unreachable through `refreshGroupHeader` (a non-bare group
    // has a controller by then), so this is agreement with `ensureGroupDisclosure`
    // rather than a guard against a known caller.
    return;
  }
  if (
    group.classList.contains(CLS_AUTO_COLLAPSED) ||
    group.classList.contains(CLS_USER_TOGGLED) ||
    group.classList.contains(CLS_COLLAPSED)
  ) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  // The FAILURE count alone, never the stopped one: a stopped member neither
  // blocks the fold nor auto-opens the body, matching the delegate card.
  if (countOutcomes(calls).failures > 0) {
    return;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return;
    }
  }
  // An AUTO collapse removes height ABOVE the reader, so it is compensated.
  // This is the one ANIMATED height change of the three §3.4 names, via
  // createDisclosure.
  preserveReadingPosition(() => {
    markAutoCollapsed(group);
    ctl.close();
    refreshGroupHeader(group);
  }, "content-growth");
}
