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
import { CHROME_ATTR } from "./chrome-attr.js";
import { openChange, openAtLine, openCallDiff } from "./navigate.js";
import { lineDiff, windowHunks, stats as diffStats } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";
import { setUserScrolledUp, preserveReadingPosition } from "./scroll.js";
import { wireRowToggle } from "./disclosure-row.js";
import { toolCallBulk, type ToolBulk } from "./tool-bulk.js";
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
  if (opts.declined === true) {
    // The DOM is this fact's ONE source: `applyOutcome` reads it from the dataset
    // on both paths, so the stamp has to land before the call below. Only ever
    // set, never cleared — a refusal is terminal, and the wire field is
    // `omitempty`, so a later frame carrying no `declined` means unchanged.
    node.dataset["declined"] = "1";
  }
  if (info.mcp !== null) {
    node.dataset["mcpServer"] = info.mcp.server;
  }
  if (info.filePath !== "") {
    node.dataset["filename"] = info.fileBasename;
    node.dataset["filePath"] = info.filePath;
  }
  if (opts.live && isToolActive(opts.status)) {
    // `data-start-ms` MEANS this card is in flight: both fold guards in
    // `tool-group.ts` refuse to collapse a group holding one. It is dropped by
    // `applyStatusUpdate` on every settle.
    node.dataset["startMs"] = String(Date.now());
  }

  const summary = el("div", {
    className: withToggle ? "tool-summary has-disclosure" : "tool-summary",
  });
  summary.appendChild(buildHeader(opts, displayTitle, info, withToggle));
  node.appendChild(summary);
  applyOutcome(node, opts.status, displayTitle, info);

  // A claim-only kind gets no details region and no toggle. On `output` the
  // subtitle is the only place the command reaches a collapsed delegate's tail,
  // because the <pre> carrying the same string is chrome. The row sits in the
  // summary with the title, so the box is one disclosure and one hover target.
  if (depth1 === "search" || depth1 === "fetch" || depth1 === "generic" || depth1 === "output") {
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
    // The SHELL only: an empty details region plus the output slot the live
    // update path writes into. Everything with a cost in it — the denial rows,
    // the input dump, painting the output through the ANSI renderer and the path
    // linkifier — is built on first open by `detailsBody`, because a collapsed
    // card is a claim line and a transcript mounts dozens of them.
    node.insertAdjacentHTML("beforeend", detailsShell());
    // What the transcript dropped and the bulk can put back, declared once so the
    // load below and the `data-disclosable` arms cannot disagree about it.
    const deferred: DeferredCtx = { opts, depth1, info };
    // A card whose call has ALREADY failed is built OPEN, because open is that card's
    // final state: `messages-tools.ts` opens a failed card's region so the error output
    // is visible without a click, and a card mounted from the store is failed before it
    // is built. Opening it after the mount animated the reveal on every re-mount —
    // measured on the live app, 8 `Run Command` cards with `data-outcome="fail"` each
    // running a 200ms height transition ~120ms after a tab switch, which is the reported
    // symptom. `expandToolDetails` stays the path for the live status FLIP, where the
    // reveal SHOULD animate. Gated on `live` like the flip it mirrors: a replay-mode card
    // has settled and shows no expand-on-fail.
    // The READER's own state and nothing else: a card is born open only where they had
    // this region open before this render. A failed call is NOT born open — the
    // expand-on-fail courtesy belongs to the live status FLIP (`expandToolDetails`,
    // driven from `messages-tools.ts`), where the reveal is an event the reader is
    // watching and SHOULD animate. Deriving it here instead would open every failed
    // call in a reopened chat, which is a resting state rather than a reveal.
    const buildOpen = opts.detailsOpen === true;
    wireToggle(node, detailsBody(node, deferred), buildOpen);
    // One arm per thing opening this card writes, read back by `refreshToolDisclosure`.
    // The region cannot be read for them: it is empty until first open, so a card whose
    // only content is deferred looks exactly like one with none. The first three arms are
    // content this card already holds; the deferred pieces contribute their own through
    // `reveals`, which is why a diffs-only cut is openable rather than bare.
    //
    // The chat id gates the WHOLE table and is not a per-member predicate: both pieces
    // come from one bulk behind one guard, so two copies of the condition would be two
    // things to keep in step — `bulkChatID` is that one owner, shared with
    // `detailsBody`'s fetch guard. Without it a card with no chat id kept its chevron
    // and opened onto a region that can never fill — reachable from
    // `run-step-blocks.ts`, whose `toolCardOptsFor(tc, true)` carries no chat id, on a
    // run step's `hasFull` edit.
    if (
      opts.denial !== undefined ||
      (opts.live && opts.input !== undefined) ||
      (opts.output !== undefined && opts.output.trim() !== "") ||
      (bulkChatID(opts) !== null && DEFERRED_PARTS.some((part) => part.reveals(deferred)))
    ) {
      node.dataset["disclosable"] = "1";
    }
  }

  wireFileLink(node, info.filePath, depth1 === "diff");

  // An edit's diff IS its depth 1, which is why it is inserted here rather than
  // deferred with the details body. It used to be inserted for every kind, which
  // turned a merged multi-edit group into a wall of hunks by default.
  const resting = depth1 === "diff" ? restingDiff(opts, info) : null;
  if (resting !== null) {
    insertDiffPreview(node, resting.path, resting.src);
  }

  refreshToolDisclosure(node);
  return node;
}

/** The diff a card draws in its RESTING state, or null when it has none: the
 *  call's own ToolDiff, else the before/after pair its INPUT carries.
 *
 *  ONE owner, because the deferred table's diff member is its negation — a card
 *  already drawing a diff must neither fetch a second one nor count the bulk as a
 *  reason to be openable. Two conditions restated at the table would be two things
 *  to keep in step, and a preview inserted twice is the drift they produce. */
function restingDiff(
  opts: BuildToolCardOpts,
  info: ToolRenderInfo,
): { path: string; src: { oldText: string; newText: string } } | null {
  const d = opts.diffs?.[0];
  if (d !== undefined) {
    return { path: d.path, src: { oldText: d.old_text ?? "", newText: d.new_text } };
  }
  if (info.writesFile && info.diffSources !== null) {
    return { path: info.filePath, src: info.diffSources };
  }
  return null;
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
    el(
      "span",
      { className: "tool-move-arrow", "aria-hidden": "true", [CHROME_ATTR]: "" },
      "\u2192",
    ),
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
      {
        className: "tool-mcp-badge",
        title: `From the ${info.mcp.server} MCP integration`,
        [CHROME_ATTR]: "",
      },
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
        // The chip shows the BASENAME, so the full path is on no other surface
        // and rides the tooltip rather than a second native one beside it.
        //
        // THE PATH ALONE, and the action moved to the accessible name. Tooltips
        // are one size class (01-tokens.css `--tooltip-lines`), and this is the
        // only tooltip in the app whose content is long enough for that to bind:
        // a leading `Open the diff` line left the path ONE line of the two, and
        // one line holds 57 characters, which clips 19.26% of the 28,870 real
        // file-path tool inputs on the live volume. Two lines hold 114 and clip
        // 0.02%.
        // Path-first is also what this row's tooltip is FOR — the fact the chip
        // hides — where a tooltip restating the click says nothing.
        "data-tooltip": info.filePath,
        "aria-label": `Open the diff for ${info.fileBasename}`,
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

  // No status WORD, and no second mark either. The row carries ONE mark — the
  // glyph above (see applyOutcome) — and a finished card printing the literal
  // enum value `completed` was the claim "Tool call completed", which says
  // nothing the row does not already say.

  // The claim-only kinds are decided here; a card that goes bare LATER is
  // `refreshToolDisclosure`'s, which owns the reason the shape is a detach.
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

/** What a caller may state. `aborted` is a member of the tool wire enum as well as
 *  a run-level status, so one union serves both callers: the History page states a
 *  run's verdict through this same writer, and a stopped run is neither a success
 *  nor a failure of the work. */
type OutcomeStatus = ToolStatus;

/** The verdicts the vocabulary paints. Not the wire enums: `pending` and
 *  `in_progress` are one thing to a reader, and a refusal is its own state. */
type OutcomeState = "ok" | "fail" | "warn" | "declined" | "denied" | "running";

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
 *  `warn`, `declined` and `denied` REPLACE that glyph with a general road-sign
 *  silhouette from `icons.ts` (`outcomeIcon`), red for a failure and yellow for
 *  the three stops, each a distinct shape. So hue stays a channel and is never
 *  the only one, and WCAG 1.4.1 is satisfied by the shape swap rather than by the
 *  second mark this replaced (a 7px character badge composited on the glyph's
 *  corner, which said the same thing twice). The status word is still not visible text:
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
  // A DECLINED call ran correctly and refused, which is neither a success nor a
  // failure: `completed` would paint a green check over a plan update the workflow
  // rejected, and `failed` would send the reader to debug a tool that behaved.
  // Read from the DATASET alone, unlike `denied`: nothing else needs the fact, so
  // the DOM is its one source and `ToolRenderInfo` gains no field. Below `denied`
  // because a policy refusal outranks the tool's own verdict — the command never ran
  // at all, so the rule is what the reader needs.
  const declined = node.dataset["declined"] === "1";
  const state: OutcomeState = denied
    ? "denied"
    : declined
      ? "declined"
      : status === "aborted"
        ? "warn"
        : isToolDone(status)
          ? status === "failed"
            ? "fail"
            : "ok"
          : "running";
  node.dataset["outcome"] = state;
  if (icon !== null) {
    icon.classList.remove("is-ok", "is-fail", "is-warn", "is-running", "is-declined", "is-denied");
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
    case "declined":
      return "declined";
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

/** The empty details region, mounted with the card.
 *
 *  `.tool-output` is part of the SHELL rather than the deferred body, because the
 *  live update path writes streamed chunks straight into it
 *  (`messages-tools.ts` writeChunkToCard / applyOutputUpdate) and a card that is
 *  streaming has usually not been opened. An empty div costs nothing; what cost
 *  something was painting megabytes into it.
 *
 *  No "collapsed" class: the disclosure controller wired in wireToggle owns the
 *  collapse state (inline height + aria-hidden/inert on the region). */
function detailsShell(): string {
  return `<div class="tool-details"><div class="tool-output"></div></div>`;
}

/** What a deferred piece of content is decided and applied against. */
interface DeferredCtx {
  readonly opts: BuildToolCardOpts;
  readonly depth1: string;
  /** The diff member needs `filePath` for a bulk diff carrying none, and
   *  `restingDiff` for the negation its own predicate is. */
  readonly info: ToolRenderInfo;
}

/** One piece of a card's content the transcript dropped, put back from the bulk
 *  when the reader OPENS the card.
 *
 *  On open and never on mount: that is what the fetch button this replaced was
 *  defending, and the argument survives it unchanged — a card nobody opened still
 *  costs one claim line.
 *
 *  TWO predicates because they answer different questions, and for `output` they
 *  differ. The bulk is APPLIED whenever the store previewed the call, because a
 *  previewed output is sent PLAIN — TextSpan offsets are UTF-16 code units into
 *  the whole text, and remapping them onto a head-and-tail window would be a
 *  second implementation of `windowSpans` in Go against a different offset unit —
 *  so the bulk is where an ANSI-styled output regains its colour even when the
 *  text itself was not cut. Only a CUT output REVEALS anything, because otherwise
 *  the reader can already see every line of it. */
interface DeferredPart {
  /** Should the bulk be applied for this piece on open. */
  pending(ctx: DeferredCtx): boolean;
  /** Does this piece give the card a reason to be openable at all: an arm of
   *  `data-disclosable`. */
  reveals(ctx: DeferredCtx): boolean;
  apply(node: HTMLDivElement, bulk: ToolBulk, ctx: DeferredCtx): void;
}

/** A ToolDiff is a before/after pair the client runs its own line diff over, so
 *  the server sends it whole or not at all rather than a truncated pair that would
 *  render an edit nobody made — which is why a dropped diff is all-or-nothing here
 *  and the two predicates coincide. */
function diffDeferred({ opts, depth1, info }: DeferredCtx): boolean {
  return depth1 === "diff" && opts.hasFull === true && restingDiff(opts, info) === null;
}

/** The chat this card's bulk is keyed on, or `null` when it has none.
 *
 *  ONE owner of "can this card reach its bulk at all", read by the `data-disclosable`
 *  union — which must not offer a chevron onto a region that can never fill — and by
 *  `detailsBody`'s fetch guard, which must not issue a request it cannot key. Two
 *  spellings of one rule is how those two come to disagree, and the failure is the
 *  one this conjunct exists to prevent: a card that opens onto nothing. Resolving
 *  rather than answering a bool so the id is derived once and narrows for the caller. */
function bulkChatID(opts: BuildToolCardOpts): string | null {
  const id = opts.chatID ?? "";
  return id === "" ? null : id;
}

const DEFERRED_PARTS: readonly DeferredPart[] = [
  {
    pending: ({ opts }) => opts.hasFull === true,
    reveals: ({ opts }) => opts.hasFull === true && (opts.outputBytes ?? 0) > 0,
    apply: (node, bulk, { depth1 }) => {
      if (bulk.output.trim() === "") {
        return;
      }
      const out = node.querySelector(".tool-output");
      if (out === null) {
        return;
      }
      out.replaceChildren();
      appendOutput(node, bulk.output, bulk.outputSpans, depth1 === "output");
    },
  },
  {
    pending: diffDeferred,
    reveals: diffDeferred,
    apply: (node, bulk, { info }) => {
      // The presence check `messages-tools.ts`'s `applyDiffUpdate` already makes, and
      // for the same reason from the other side: this runs after an await, so a
      // `tool_call_update` carrying diffs can land between the open and the bulk and
      // insert the preview first — leaving two mini-diffs on one card. It is about the
      // SECOND insert rather than emptiness; `insertDiffPreview` returns early on a
      // zero-change diff by itself.
      if (node.querySelector(".tool-diff-preview") !== null) {
        return;
      }
      const d = bulk.diffs[0];
      if (d === undefined) {
        return;
      }
      insertDiffPreview(node, d.path === "" ? info.filePath : d.path, {
        oldText: d.old_text ?? "",
        newText: d.new_text,
      });
    },
  },
];

/** The details body's builder, run at most once, on first open.
 *
 *  Registered on the toggle BEFORE the disclosure controller's own listener so it
 *  runs first: the controller measures `scrollHeight` to animate the reveal, and
 *  a region filled after that measurement would animate to zero and then jump.
 *
 *  A previewed card (`has_full`) fetches its bulk here — ONE request for every
 *  pending piece, because `toolCallBulk` answers all of them — and repaints when
 *  it lands. The preview is painted first regardless, so the reveal shows the head
 *  and tail immediately and fills in behind; the alternative, an empty region
 *  until the network answers, is a worse reveal than the one this replaced. A
 *  `null` bulk renders nothing and is not retried from here. */
function detailsBody(node: HTMLDivElement, ctx: DeferredCtx): () => void {
  const { opts, depth1 } = ctx;
  let built = false;
  return () => {
    if (built) {
      return;
    }
    built = true;
    const details = node.querySelector<HTMLElement>(".tool-details");
    if (details === null) {
      return;
    }
    // The command this prints is ALSO in `.tool-subtitle` (:99), so a reader of
    // both — a rolling tail — has to dedupe the two.
    const inputBlock =
      opts.live && opts.input !== undefined
        ? `<pre class="tool-input">${escText(JSON.stringify(opts.input, null, 2))}</pre>`
        : "";
    const head = denialBlock(opts.denial) + inputBlock;
    if (head !== "") {
      details.insertAdjacentHTML("afterbegin", head);
    }
    if (opts.output !== undefined && opts.output.trim() !== "") {
      appendOutput(node, opts.output, opts.outputSpans ?? [], depth1 === "output");
    }
    const pending = DEFERRED_PARTS.filter((part) => part.pending(ctx));
    const chatID = bulkChatID(opts);
    if (pending.length === 0 || chatID === null) {
      return;
    }
    void toolCallBulk(chatID, opts.id).then((bulk) => {
      if (bulk === null) {
        return;
      }
      for (const part of pending) {
        part.apply(node, bulk, ctx);
      }
    });
  };
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
  return `<div class="tool-denial" ${CHROME_ATTR}>${rows.join("")}</div>`;
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

// Per-card deferred body builders. Held beside the controller because
// `expandToolDetails` opens a card WITHOUT a click, so it has to run the builder
// itself — and because the failure path reads the output back out of the DOM
// immediately afterwards to offer "Explain this error".
const detailBuilders = new WeakMap<HTMLElement, () => void>();

/** Wire a card's details region. `initialOpen` builds the body and creates the
 *  controller ALREADY OPEN, which is the only silent way to mount an open region: the
 *  primitive commits the closed height before it writes the change, so creating it
 *  closed and opening it afterwards animates even inside the same task. `open: true`
 *  needs no measurement either — `applyHeight(true, false)` writes `height: ""` — so
 *  content arriving later is fine and the card may still be detached. */
function wireToggle(el: HTMLElement, buildBody: () => void, initialOpen: boolean): void {
  const toggle = el.querySelector<HTMLElement>(".tool-disclosure");
  const details = el.querySelector<HTMLElement>(".tool-details");
  if (toggle === null || details === null) {
    return;
  }
  if (initialOpen) {
    buildBody();
  }
  // BEFORE createDisclosure registers its own click handler, so this one runs
  // first and the region is filled before the controller measures it to animate
  // the reveal. Listeners on one element in one phase fire in registration
  // order, and `wireRowToggle` forwards a summary click through `toggle.click()`,
  // so the row's whole surface reaches this too.
  toggle.addEventListener("click", buildBody);
  detailBuilders.set(el, buildBody);
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
    open: initialOpen,
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

/** Force-open a card's details (e.g. when the tool fails so the error output is visible
 *  without a click). A BARE card is refused HERE rather than at each caller, because a
 *  bare card has no chevron to close the region with again. The body is built BEFORE the
 *  open: the controller measures the region to animate it. */
export function expandToolDetails(card: HTMLElement): void {
  if (card.querySelector(".tool-disclosure") === null) {
    return;
  }
  detailBuilders.get(card)?.();
  detailCtls.get(card)?.open();
}

// Chevrons taken off bare cards. Held rather than re-queried, because the button
// is out of the DOM: re-attaching this one keeps the controller's listeners, so a
// card that regains content needs no second createDisclosure.
const detachedToggles = new WeakMap<HTMLElement, HTMLElement>();

/** Whether the details region holds anything a reader can SEE, or will once opened. The
 *  WIRE STATUS is not consulted: emptiness is a property of the region, and both call
 *  sites pass `live: true`, so `data-outcome` cannot separate filling from never-will. */
function isDisclosable(card: HTMLElement): boolean {
  if (card.dataset["disclosable"] === "1") {
    return true;
  }
  const out = card.querySelector(".tool-output");
  return out !== null && out.textContent.trim() !== "";
}

/** Give a card its disclosure, or take it away: the ONE writer of bare-ness. Idempotent
 *  both ways, so output landing later restores the chevron and a card that goes bare while
 *  open is closed rather than stranded. DETACHED rather than `display: none`d, so a bare
 *  card meets the same no-`aria-expanded` bar a claim-only one does, at no CSS cost. */
export function refreshToolDisclosure(card: HTMLElement): void {
  // A claim-only card owns none of this: no details region, no toggle, and a
  // summary that never became clickable.
  if (card.querySelector(".tool-details") === null) {
    return;
  }
  const summary = card.querySelector<HTMLElement>(".tool-summary");
  if (isDisclosable(card)) {
    const held = detachedToggles.get(card);
    if (held !== undefined) {
      card.querySelector(".tool-header")?.appendChild(held);
      detachedToggles.delete(card);
    }
    summary?.classList.add("has-disclosure");
    return;
  }
  // Closed through the CONTROLLER first, while the button is still connected: that
  // is what lands `aria-expanded="false"` on it and `aria-hidden` + `inert` on the
  // region. Removing the button first leaves the region open and exposed.
  detailCtls.get(card)?.close();
  const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
  if (toggle !== null) {
    // No FOCUSED chevron reaches this: every detach left runs inside `buildToolCard`, before
    // the card is in the document. Reintroduce an in-document one and focus falls to <body>;
    // the fallback is the card at `tabindex="-1"` or `.tool-group-header`.
    detachedToggles.set(card, toggle);
    toggle.remove();
  }
  summary?.classList.remove("has-disclosure");
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
    { type: "button", className: "tool-output-reveal", [CHROME_ATTR]: "" },
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
    {
      type: "button",
      className: "tool-diff-stats",
      "data-tooltip": "Open the diff",
      [CHROME_ATTR]: "",
    },
    el("span", { className: "diff-add-count" }, `+${String(s.adds)}`),
    el("span", { className: "diff-del-count" }, `-${String(s.dels)}`),
  );
  statBtn.addEventListener("click", (e: Event) => {
    e.stopPropagation();
    openCallDiff(filePath, src.oldText, src.newText);
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
        { className: "tool-diff-more", [CHROME_ATTR]: "" },
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
