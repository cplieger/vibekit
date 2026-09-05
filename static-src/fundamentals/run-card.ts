// ---------------------------------------------------------------------------
// Fundamental: the WORKFLOW RUN card — a run's home in the transcript.
//
// The invocation becomes its header, and every step renders inside it. The
// run TAB is a different surface (`exec-view/`, a tree over time with a
// detail pane): this card and that page share only a status vocabulary — a
// card among a conversation's turns is a glance, the page is the review.
//
// Four regions: HEAD (what/how/how-long, always) / ALERT (an ask, pause,
// stop or failure) / BODY (per-step rows hosting each step's live blocks) /
// FOOT (outcome + link to the full tree).
//
// OPEN BY DEFAULT — the inverse of the delegated-work card, deliberately:
// there is exactly one run card per launch and it runs for minutes, where
// the delegate card's N-at-once reasoning does not apply. Step rows ARE
// collapsed, which is where that reasoning belongs.
//
// Renders from `inspect`, never from accumulated events: `run-store.ts` owns
// the fetch, this file is a pure view. The invocation tool call is persisted
// in its launching turn, so the card refills itself on refresh; only the
// nested live blocks (belonging to a turn vibekit never finalizes) are lost.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { STATE_WORD, paintStateMark, stateOf, withAsk } from "../exec-view/status.js";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "../icons.js";
import { formatElapsed, truncate } from "../strings.js";
import type { ToolStatus } from "../types.js";
import {
  isNeedInputPause,
  leafNodes,
  nodePathOf,
  runCounters,
  runElapsedMs,
  runIsLive,
  elapsedMs,
  type RunNode,
  type RunState,
} from "../run-store.js";

/** The state vocabulary is `exec-view/status.ts`, shared with the `/run/{id}`
 *  page's tree, timeline and detail pane — one module rather than a private
 *  copy per surface. */

/** What a run is waiting on a PERSON for, passed in because the card cannot
 *  see it: KAS blocks the asking step's own turn and leaves the run
 *  `running`, so `inspect` alone cannot show it. The unanswered-asks queue
 *  lives in `decision-dock.ts`, a feature module, so this is injected rather
 *  than imported.
 *
 *  Declared here (the consumer) rather than there: the card states what it
 *  needs, and the dock answers in those terms. */
export interface RunAsks {
  /** How many of this run's asks are unanswered. */
  count: number;
  /** The node ids those asks name. Separate from `count` because the wire cannot
   *  always attribute one (the step-session registry has to have seen the
   *  sub-session), and a run with an unattributable ask is still blocked. */
  nodes: ReadonlySet<string>;
  /** The head ask as one line, for the alert. "" when there is none to name. */
  label: string;
}

const NO_ASKS: RunAsks = { count: 0, nodes: new Set<string>(), label: "" };

/** The pause reason as a sentence a reader can act on.
 *
 *  The two `need_input` literals get replaced rather than quoted. KAS writes
 *  `Step requested user input via send_message.` for a step's own question and
 *  `Step '<id>' is waiting for user input.` when a plain Resume re-parks one, and
 *  both name a TOOL and a mechanism where what the reader needs to know is that
 *  somebody owes an answer. The arm ABOVE this one already renders the question
 *  itself whenever the ask reached the dock, so this is the case where it did not —
 *  a restart lost the text, or this client has not been handed it yet. */
function pauseSentence(reason: string | undefined): string {
  if (reason === undefined || reason === "") {
    return "Waiting";
  }
  if (isNeedInputPause(reason)) {
    return "A step is waiting for your answer";
  }
  return `Waiting: ${reason}`;
}

/** A run's status as one scannable word. `paused` reads "waiting", not
 *  "paused": KAS pauses for a watch, a retry budget, and a loop policy alike,
 *  and what matters to the reader is that nothing moves until something
 *  happens. */
function runWord(status: RunState["status"]): string {
  switch (status) {
    case "running":
      return "running";
    case "paused":
      return "waiting";
    case "completed":
      return "completed";
    case "failed":
      return "failed";
    case "aborted":
      return "stopped";
    default:
      return "starting";
  }
}

/** A mounted run card plus its imperative handle. */
export interface RunCardView {
  readonly root: HTMLDivElement;
  /** Re-render every region from a fresh state. Idempotent, and safe on
   *  every invalidation: rows are reconciled in place, so a step's open
   *  disclosure and its blocks survive.
   *
   *  `asks` defaults to the last one given, so an internal re-render
   *  (`setLaunch`) does not have to restate it. */
  render(state: RunState | undefined, asks?: RunAsks): void;
  /** Advance the clocks only, on a 1s tick while the run is live. */
  tick(): void;
  /** The container a step's own blocks render into, creating the row when the
   *  state has not arrived yet.
   *
   *  Out-of-order is the normal case: a step's first frame can beat the
   *  `inspect` fetch that describes it, so a row must be creatable from a
   *  node path alone and filled in later. */
  stepBody(nodePath: string): HTMLElement;
  /** Fold in the LAUNCH tool call's own status and output — a launch that
   *  failed created no run, so `inspect` has nothing to report and the tool
   *  call is the only witness. Silent on a successful launch, since the
   *  run's own state already covers it. */
  setLaunch(status: ToolStatus, output: string | undefined): void;
}

interface StepRow {
  root: HTMLElement;
  head: HTMLElement;
  glyph: HTMLElement;
  name: HTMLElement;
  meta: HTMLElement;
  dur: HTMLElement;
  body: HTMLElement;
  /** Live while the step runs, so the clock ticks; cleared once it settles. */
  startedAt?: string;
  endedAt?: string;
}

/** Both halves of the disclosure bookkeeping this view delegates: what a row was
 *  last left at, and where a flip goes. Keyed by node path, `null` for the card
 *  itself. `wasOpen` returning undefined leaves the card's own default standing. */
export interface RunDisclosure {
  readonly wasOpen: (nodePath: string | null) => boolean | undefined;
  readonly onOpenChange: (nodePath: string | null, open: boolean) => void;
}

/** Build a run card. `name` is the best label at creation (recipe name from
 *  the invocation, or generic); later renders prefer the run's own
 *  `runLabel`. `onOpen` (the footer link) and `disclosure` are injected so this
 *  `fundamentals/` view avoids importing the feature module that owns run
 *  tabs and its open-container bookkeeping. Card mounts open; rows closed. */
export function buildRunCard(
  workflowID: string,
  name: string,
  onOpen: (id: string, label: string) => void,
  disclosure?: RunDisclosure,
): RunCardView {
  const root = el("div", {
    className: "run-card",
    "data-run": workflowID,
    "data-status": "starting",
  }) as HTMLDivElement;
  const cardOpen = disclosure?.wasOpen(null) ?? true;
  root.classList.toggle("collapsed", !cardOpen);

  // --- head -----------------------------------------------------------------
  const icon = el("span", { className: "run-icon", "aria-hidden": "true" }, iconEl(ICON_TAB_RUN));
  const nameEl = el("span", { className: "run-name" }, name);
  // `run-state`, not `run-status`: `.run-status` in 18-pages.css was the
  // `/run/{id}` page's before this card started rendering that page's own
  // content — the collision is historical and renaming would buy nothing.
  const statusEl = el("span", { className: "run-state" }, runWord(undefined));
  const countEl = el("span", { className: "run-count" });
  const clockEl = el("span", { className: "run-clock" });
  // A span, not a button: the head is `role="button"`, so a nested `<button>`
  // is axe's `nested-interactive` regardless of aria-hidden/tabindex="-1".
  const chevron = el("span", { className: "run-toggle", "aria-hidden": "true" }, chevronEl());
  const head = el(
    "div",
    { className: "run-head", role: "button", tabindex: "0" },
    icon,
    nameEl,
    el("span", { className: "run-head-meta" }, statusEl, countEl, clockEl),
    chevron,
  );

  // --- alert ------------------------------------------------------------
  // A `status` live region: a run parked waiting for a PR review must not
  // require polling to notice.
  const alert = el("div", {
    className: "run-alert hidden",
    role: "status",
    "aria-live": "polite",
  });

  // --- body -----------------------------------------------------------------
  const steps = el("div", { className: "run-steps" });
  const outputs = el("dl", { className: "run-outputs hidden" });
  const body = el("div", { className: "run-body" }, steps, outputs);

  // --- foot ---------------------------------------------------------------
  const ledger = el("span", { className: "run-ledger" });
  // A real anchor (middle-click, copy-link work) with a click handler that
  // opens the tab instead of navigating. `onOpen` is injected since this
  // `fundamentals/` view must not import `run-view.ts`'s feature module.
  const open = el(
    "a",
    { className: "run-open", href: `/run/${encodeURIComponent(workflowID)}` },
    "Open run",
    el("span", { className: "run-open-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  );
  open.addEventListener("click", (e) => {
    // A modified click is a deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    onOpen(workflowID, nameEl.textContent);
  });
  const foot = el("div", { className: "run-foot" }, ledger, open);

  root.append(head, alert, body, foot);

  createDisclosure(head, body, {
    open: cardOpen,
    onToggle: (isOpen) => {
      root.classList.toggle("collapsed", !isOpen);
      disclosure?.onOpenChange(null, isOpen);
    },
  });

  const rows = new Map<string, StepRow>();
  let liveClock = false;
  /** Set when the LAUNCH itself failed, so the alert stays on the tool call's
   *  reason instead of being overwritten by a state that will never arrive. */
  let launchError = "";
  /** The last state and the last asks rendered, so `setLaunch` can re-render
   *  without re-fetching and without restating what it does not know. */
  let lastState: RunState | undefined;
  let lastAsks: RunAsks = NO_ASKS;

  function stepRow(nodePath: string): StepRow {
    let row = rows.get(nodePath);
    if (row !== undefined) {
      return row;
    }
    const glyph = el("span", { className: "run-step-glyph", "aria-hidden": "true" });
    // The last path segment is the step; the segments above it are the loop or
    // branch containing it, and they are what tell two iterations apart.
    const segs = nodePath.split("/");
    const label = segs[segs.length - 1] ?? nodePath;
    const nameEl2 = el("span", { className: "run-step-name" }, label);
    const meta = el("span", { className: "run-step-meta" });
    const dur = el("span", { className: "run-step-dur" });
    const chev = el("span", { className: "run-step-toggle", "aria-hidden": "true" }, chevronEl());
    const rowHead = el(
      "div",
      { className: "run-step-head", role: "button", tabindex: "0" },
      glyph,
      nameEl2,
      meta,
      dur,
      chev,
    );
    const rowBody = el("div", { className: "run-step-body" });
    const rowRoot = el("div", { className: "run-step", "data-node": nodePath }, rowHead, rowBody);
    const rowOpen = disclosure?.wasOpen(nodePath) ?? false;
    rowRoot.classList.toggle("collapsed", !rowOpen);
    createDisclosure(rowHead, rowBody, {
      open: rowOpen,
      onToggle: (isOpen) => {
        rowRoot.classList.toggle("collapsed", !isOpen);
        disclosure?.onOpenChange(nodePath, isOpen);
      },
    });
    row = { root: rowRoot, head: rowHead, glyph, name: nameEl2, meta, dur, body: rowBody };
    rows.set(nodePath, row);
    steps.appendChild(rowRoot);
    return row;
  }

  /** Paint one row from its node. Split out so the reconcile below reads as an
   *  ordering pass rather than an ordering pass with a renderer inside it. */
  function paintRow(row: StepRow, node: RunNode, asks: RunAsks): void {
    const state = stateOf(node.status);
    // An ask reclassifies a step only while it is IN FLIGHT: `node_id` is a
    // node ID not a path, so a repeat's iterations share it.
    const shown = withAsk(state, asks.nodes.has(node.nodeId));
    row.root.dataset["status"] = shown;
    row.root.dataset["nodeType"] = node.type;
    paintStateMark(row.glyph, shown);
    // Via a local, not directly: `undefined` is not a valid value for an
    // optional property under exactOptionalPropertyTypes.
    if (node.startedAt === undefined) {
      delete row.startedAt;
    } else {
      row.startedAt = node.startedAt;
    }
    if (node.endedAt === undefined) {
      delete row.endedAt;
    } else {
      row.endedAt = node.endedAt;
    }

    // Who ran it, on what, what went wrong, in that order. A watch names its
    // own kind since nothing runs a watch — it polls.
    const bits: string[] = [];
    if (node.type === "watch") {
      bits.push("watch");
    } else if (node.agentName !== undefined && node.agentName !== "") {
      bits.push(node.agentName);
    }
    if (node.modelId !== undefined && node.modelId !== "" && node.modelId !== "auto") {
      bits.push(node.modelId);
    }
    if (node.iteration !== undefined) {
      // 1-based for a reader; KAS counts from zero.
      bits.push(`pass ${String(node.iteration + 1)}`);
    }
    if (node.branchId !== undefined && node.branchId !== "") {
      bits.push(node.branchId);
    }
    if (node.continuationAttempts !== undefined && node.continuationAttempts > 0) {
      bits.push(`${String(node.continuationAttempts)} retries`);
    }
    if (node.failureReason !== undefined && node.failureReason !== "") {
      bits.push(truncate(node.failureReason, 80));
    }
    row.meta.textContent = bits.join(" \u00b7 ");

    const ms = elapsedMs(node.startedAt, node.endedAt);
    row.dur.textContent = ms > 0 ? formatElapsed(ms) : "";
    row.head.setAttribute("aria-label", `${row.name.textContent}, ${STATE_WORD[shown]}`);

    // Visible on the collapsed row, like the delegate card's footer, since a
    // captured output is a RESULT rather than the working transcript. Clamps
    // to two lines in CSS; guarded on the text changing since render() runs
    // on every invalidation.
    const cap = row.root.querySelector<HTMLElement>(":scope > .run-step-capture");
    const text = node.capturedOutput ?? "";
    if (text === "") {
      cap?.remove();
    } else if (cap?.dataset["text"] !== text) {
      const next = el("div", { className: "run-step-capture", "data-text": text }, text);
      if (cap === null) {
        row.root.insertBefore(next, row.body);
      } else {
        cap.replaceWith(next);
      }
    }
  }

  function renderSteps(state: RunState | undefined, asks: RunAsks): void {
    const leaves = leafNodes(state?.root);
    for (const node of leaves) {
      const path = nodePathOf(state?.root, node).join("/");
      paintRow(stepRow(path), node, asks);
    }
    // Order to match the plan; a row created by a live frame the state
    // doesn't describe yet keeps its arrival position (dropping it would
    // delete the blocks rendered inside it).
    let anchor: Element | null = null;
    for (const node of leaves) {
      const path = nodePathOf(state?.root, node).join("/");
      const row = rows.get(path);
      if (row === undefined) {
        continue;
      }
      const want: Element | null =
        anchor === null ? steps.firstElementChild : anchor.nextElementSibling;
      if (row.root !== want) {
        if (anchor === null) {
          steps.prepend(row.root);
        } else {
          anchor.after(row.root);
        }
      }
      anchor = row.root;
    }
  }

  /** The alert: five things that can put a run in front of a person, ordered
   *  by what the reader can do about it. Only one shows. */
  function renderAlert(state: RunState | undefined, asks: RunAsks): void {
    const parts: string[] = [];
    let kind = "";
    if (launchError !== "") {
      // Outranks every state: the launch never created a run, so `inspect`
      // has nothing and only this reason exists.
      kind = "failed";
      parts.push(launchError);
    } else if (asks.count > 0) {
      // Ahead of the run's own status: it still reads `running` while a
      // step's ask blocks it.
      kind = "input";
      parts.push(
        asks.label === ""
          ? "Waiting for your answer"
          : `Waiting for your answer: ${truncate(asks.label, 120)}`,
      );
      if (asks.count > 1) {
        parts.push(`${String(asks.count)} asks waiting`);
      }
    } else if (state?.stopInitiator === "user") {
      kind = "stopped";
      parts.push(state.status === "completed" ? "Marked complete by you" : "Stopped by you");
      if (state.stopReason !== undefined && state.stopReason !== "") {
        parts.push(state.stopReason);
      }
    } else if (state?.status === "paused") {
      kind = "paused";
      parts.push(pauseSentence(state.pauseReason));
      const code = state.pauseDetail?.code;
      if (code !== undefined && code !== "") {
        parts.push(`after a transient error (${code})`);
      }
    } else if (state?.status === "failed") {
      kind = "failed";
      const failed = leafNodes(state.root).find(
        (n) => n.status === "failed" && n.failureReason !== undefined && n.failureReason !== "",
      );
      parts.push(
        failed === undefined
          ? "The run failed"
          : `${failed.nodeId} failed: ${truncate(failed.failureReason ?? "", 160)}`,
      );
    }
    if (parts.length === 0) {
      alert.classList.add("hidden");
      alert.replaceChildren();
      return;
    }
    alert.dataset["kind"] = kind;
    alert.classList.remove("hidden");
    alert.textContent = parts.join(" \u00b7 ");
  }

  /** Artifacts and captured outputs merged into one result list — KAS keeps
   *  them apart (a step declares an artifact vs `captureOutput` records a
   *  transcript) but to a reader they're the same thing. Artifacts win a
   *  key collision. */
  function renderOutputs(state: RunState | undefined): void {
    const merged = new Map<string, string>();
    for (const [k, v] of Object.entries(state?.capturedOutputs ?? {})) {
      merged.set(k, v);
    }
    for (const [k, v] of Object.entries(state?.artifacts ?? {})) {
      merged.set(k, v);
    }
    if (merged.size === 0) {
      outputs.classList.add("hidden");
      outputs.replaceChildren();
      delete outputs.dataset["sig"];
      return;
    }
    // A signature over the whole list, for paintRow's reason: this runs on every
    // invalidation, and rebuilding N markdown renders per frame would both cost
    // real work and reset the reader's scroll inside a long report.
    const sig = [...merged].map(([k, v]) => `${k}\u0000${v}`).join("\u0001");
    if (outputs.dataset["sig"] === sig) {
      return;
    }
    outputs.dataset["sig"] = sig;
    outputs.classList.remove("hidden");
    outputs.replaceChildren(
      ...[...merged.entries()].flatMap(([k, v]) => [
        el("dt", { className: "run-output-key" }, k),
        // An EMPTY value is rendered rather than skipped, and the sentence says
        // KAS writes a key only for a step that captured, so an empty value
        // means the step finished without saying anything — worth stating,
        // or it's indistinguishable from "never ran".
        el(
          "dd",
          { className: v.trim() === "" ? "run-output-val run-output-val-empty" : "run-output-val" },
          v.trim() === "" ? "This step's last message carried no text." : truncate(v, 300),
        ),
      ]),
    );
  }

  function renderFoot(state: RunState | undefined): void {
    const c = runCounters(state);
    const bits: string[] = [];
    if (c.total > 0) {
      bits.push(`${String(c.total)} ${c.total === 1 ? "step" : "steps"}`);
    }
    if (c.failed > 0) {
      bits.push(`${String(c.failed)} failed`);
    }
    bits.push(runWord(state?.status));
    const ms = runElapsedMs(state);
    if (ms > 0) {
      bits.push(formatElapsed(ms));
    }
    ledger.textContent = bits.join(" \u00b7 ");
  }

  function render(state: RunState | undefined, asks: RunAsks = lastAsks): void {
    lastState = state;
    lastAsks = asks;
    const label = state?.runLabel ?? state?.workflowName ?? "";
    if (label !== "") {
      nameEl.textContent = label;
    }
    root.dataset["status"] = state?.status ?? "starting";
    // Second axis rather than a sixth `data-status` value: the run genuinely
    // IS running while a step's ask blocks it.
    if (asks.count > 0) {
      root.dataset["asking"] = "true";
    } else {
      delete root.dataset["asking"];
    }
    statusEl.textContent = asks.count > 0 ? "needs input" : runWord(state?.status);

    const c = runCounters(state);
    countEl.textContent =
      c.total === 0
        ? ""
        : c.current > 0
          ? `step ${String(c.current)} of ${String(c.total)}`
          : `${String(c.done)} of ${String(c.total)}`;

    liveClock = runIsLive(state);
    const ms = runElapsedMs(state);
    clockEl.textContent = ms > 0 ? formatElapsed(ms) : "";

    renderAlert(state, asks);
    renderSteps(state, asks);
    renderOutputs(state);
    renderFoot(state);
    head.setAttribute("aria-label", `Workflow run ${nameEl.textContent}, ${statusEl.textContent}`);
  }

  function tick(): void {
    if (!liveClock) {
      return;
    }
    // Re-derive from the rows' own timestamps rather than re-fetch — the one
    // thing the client can advance honestly on its own.
    let first = Number.POSITIVE_INFINITY;
    for (const row of rows.values()) {
      const rowMs = elapsedMs(row.startedAt, row.endedAt);
      if (rowMs > 0) {
        row.dur.textContent = formatElapsed(rowMs);
      }
      if (row.startedAt !== undefined && row.startedAt !== "") {
        const t = Date.parse(row.startedAt);
        if (!Number.isNaN(t)) {
          first = Math.min(first, t);
        }
      }
    }
    if (Number.isFinite(first)) {
      clockEl.textContent = formatElapsed(Math.max(0, Date.now() - first));
    }
  }

  render(undefined);

  return {
    root,
    render,
    tick,
    stepBody(nodePath: string): HTMLElement {
      return stepRow(nodePath).body;
    },
    setLaunch(status: ToolStatus, output: string | undefined): void {
      if (status !== "failed") {
        return;
      }
      const text = (output ?? "").trim();
      launchError = text === "" ? "The workflow could not be started" : truncate(text, 200);
      liveClock = false;
      render(lastState);
    },
  };
}
