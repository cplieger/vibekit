// ---------------------------------------------------------------------------
// Fundamental: the WORKFLOW RUN card — a run's home in the transcript.
//
// A run used to have four disjoint surfaces in a chat and no relation between
// them: a generic `run_workflow` tool row, one anonymous box per step scattered as
// its siblings, an ephemeral toast, and a separate `/run/{id}` tab. Nothing said
// the boxes belonged to the row, nothing said the run was still going, and none of
// the twenty-odd facts KAS reports per step reached the transcript at all. This
// card is the one object: the invocation becomes its header, every step renders
// inside it, and the run TAB is a different surface entirely — `exec-view/`, which
// renders the execution as a tree over time with a detail pane. This card and that
// page share a status vocabulary and nothing else, because a card among a
// conversation's turns and a page of its own want opposite things: this one is a
// glance, that one is the review.
//
// Four regions, and each answers a different question:
//
//   HEAD    what is this, how is it going, how long  (always)
//   ALERT   what it needs from a person              (an ask, a pause, a stop, a failure)
//   BODY    per-step rows, each hosting the step's own live blocks
//   FOOT    the outcome, and the way to the full tree
//
// THERE IS NO SPINE, and it was DELETED rather than repadded (2026-08). A row of
// one pip per leaf used to sit between the head and the alert, restating the body
// at lower fidelity: every step it described already has a row with a live glyph of
// its own, and the head's "step N of M" answers the progress question for a
// collapsed card. Its one unique contribution was showing the steps a run has not
// reached yet while the card is closed, which does not pay for a second status
// vocabulary a reader has to learn. What is left of it is the head's own
// `margin-block-end`: the pips sat flush against the head's hover tint, and putting
// the gap on the HEAD rather than on each follower is what stops that recurring for
// whatever region comes next.
//
// OPEN BY DEFAULT, which is the exact inverse of the delegated-work card beside
// it, and deliberately. That card collapses because N delegates stream at once and
// ten walls of text is not a transcript; there is exactly ONE run card per launch,
// it is the thing the user just asked for, and it runs for minutes. The STEP rows
// inside it are collapsed, which is where the delegate card's reasoning actually
// applies.
//
// IT RENDERS FROM `inspect`, NEVER FROM ACCUMULATED EVENTS. `run-store.ts` owns
// the fetch; this file is a pure view over the value it returns. That is what
// makes the card correct after a refresh with no server change: the invocation
// tool call is persisted in its launching turn, so the card comes back and refills
// itself from the endpoint. The nested live blocks are the one thing a refresh
// loses (they belong to a turn vibekit never finalized), and the row, status,
// agent, model, duration and captured output all survive without them.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { STATE_BADGE, STATE_WORD, stateOf, withAsk } from "../exec-view/status.js";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_TAB_RUN, ICON_EXTERNAL } from "../icons.js";
import { formatElapsed, truncate } from "../strings.js";
import type { ToolStatus } from "../types.js";
import {
  leafNodes,
  nodePathOf,
  runCounters,
  runElapsedMs,
  runIsLive,
  elapsedMs,
  type RunNode,
  type RunState,
} from "../run-store.js";

/** The state vocabulary is `exec-view/status.ts`, shared with the `/run/{id}` page's
 *  tree, timeline and detail pane.
 *
 *  It used to be a private copy right here — `StepState`, `STEP_BADGE`, `STEP_WORD`
 *  and `stepStateOf`, sixty lines of it — while the page carried a third set of dot
 *  colours of its own. `vibekit-ui.md` names that exact shape as a defect (the todo
 *  block's `☐ ◐ ☑` against the task pill's `○ ⏳ ✅`: two icon sets a reader learns
 *  separately, two feeds that can disagree with nothing detecting it), and with a
 *  subagent tab coming a fourth copy was the default outcome. So the two run
 *  surfaces read one module and a third surface inherits it for free. */

/** What a run is waiting on a PERSON for, passed in because the card cannot see it.
 *
 *  An ask is not a node status. KAS blocks the asking step's own turn and leaves the
 *  run `running`, so `inspect` describes a step that looks like it is working while
 *  nothing moves until somebody clicks — which is the same masking the tab dot fixed
 *  for a background chat, one level down. The queue of unanswered asks lives in
 *  `decision-dock.ts`, a feature module, so the fact travels in the same direction
 *  `onOpen` does rather than being imported by this `fundamentals/` view.
 *
 *  Declared here rather than there because this is the CONSUMER's contract: the card
 *  states what it needs, and the dock answers in those terms. */
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

/** A run's status as one scannable word. `paused` is deliberately "waiting"
 *  rather than "paused": what a reader needs to know is that nothing will move
 *  until something happens, and KAS pauses a run for a watch, a retry budget and a
 *  loop policy alike. */
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
  /** Re-render every region from a fresh state. Idempotent, and safe to call on
   *  every invalidation: rows are reconciled in place, so a step's open
   *  disclosure and the blocks inside it survive.
   *
   *  `asks` is the second half of the truth and defaults to the last one given, so
   *  an internal re-render (`setLaunch`) does not have to restate it. The caller
   *  that owns the render effect passes both on every pass. */
  render(state: RunState | undefined, asks?: RunAsks): void;
  /** Advance the clocks only. Called on a 1s tick while the run is live, so a
   *  five-minute run does not read as frozen between server frames. */
  tick(): void;
  /** The container a step's own blocks render into, creating the row when the
   *  state has not arrived yet.
   *
   *  Out-of-order is the normal case, not an edge: a step's first frame can beat
   *  the `inspect` fetch that describes it, so a row must be creatable from a
   *  node path alone and filled in later. */
  stepBody(nodePath: string): HTMLElement;
  /** Fold in the LAUNCH tool call's own status and output.
   *
   *  A launch that failed created no run, so the endpoint has nothing to report
   *  and the card would read "starting" forever. The tool call is the only witness
   *  in that case. On a successful launch this contributes nothing the run's own
   *  state does not already say, so it is deliberately silent. */
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

/** Build a run card.
 *
 *  `name` is the best label available at creation — the recipe name from the
 *  invocation's input, or a generic one — and every later render prefers the run's
 *  own `runLabel`. `onOpen` is what the footer's link calls; injected so this
 *  `fundamentals/` view does not import the feature module that owns run tabs. */
export function buildRunCard(
  workflowID: string,
  name: string,
  onOpen: (id: string, label: string) => void,
): RunCardView {
  const root = el("div", {
    className: "run-card",
    "data-run": workflowID,
    "data-status": "starting",
  }) as HTMLDivElement;

  // --- head -----------------------------------------------------------------
  const icon = el("span", { className: "run-icon", "aria-hidden": "true" }, iconEl(ICON_TAB_RUN));
  const nameEl = el("span", { className: "run-name" }, name);
  // `run-state`, not `run-status`: the /run/{id} page owned a `.run-status` in
  // `18-pages.css` first, and two different components sharing a class name is
  // how one page's tweak silently restyles another. That page renders THIS card
  // now and its own vocabulary is deleted, so the collision is historical — the
  // name stays because renaming it back would buy nothing and cost a CSS sweep.
  const statusEl = el("span", { className: "run-state" }, runWord(undefined));
  const countEl = el("span", { className: "run-count" });
  const clockEl = el("span", { className: "run-clock" });
  // A SPAN, and that is forced by the head being `role="button"`. A `<button>`
  // inside it is `nested-interactive` (axe, serious) whatever the ARIA says:
  // measured 18 nodes on a four-card page, and `aria-hidden` plus
  // `tabindex="-1"` does NOT clear it, because a `tabindex="-1"` element is
  // still focusable by click and by script. The head carries the activation and
  // `aria-expanded`, so nothing is lost — this glyph never had a handler.
  const chevron = el("span", { className: "run-toggle", "aria-hidden": "true" }, chevronEl());
  const head = el(
    "div",
    { className: "run-head", role: "button", tabindex: "0" },
    icon,
    nameEl,
    el("span", { className: "run-head-meta" }, statusEl, countEl, clockEl),
    chevron,
  );

  // --- alert ----------------------------------------------------------------
  // The only region that speaks about a person's involvement, so it is a `status`
  // live region: a run that parks waiting for a PR review is exactly the state a
  // reader must not have to poll for.
  const alert = el("div", {
    className: "run-alert hidden",
    role: "status",
    "aria-live": "polite",
  });

  // --- body -----------------------------------------------------------------
  const steps = el("div", { className: "run-steps" });
  const outputs = el("dl", { className: "run-outputs hidden" });
  const body = el("div", { className: "run-body" }, steps, outputs);

  // --- foot -----------------------------------------------------------------
  const ledger = el("span", { className: "run-ledger" });
  // A real anchor, so middle-click and copy-link work, with a click handler over
  // it so an ordinary click OPENS THE TAB rather than navigating.
  //
  // The distinction matters because of the offer guard: the automatic sub-tab is
  // offered once per run per client, so a reader who closed it gets it back only by
  // asking, and this link is the asking. `onOpen` is injected rather than imported
  // because this file is a `fundamentals/` view: `run-view.ts` owns the opener and
  // importing it here would point a primitive at a feature module.
  const open = el(
    "a",
    { className: "run-open", href: `/run/${encodeURIComponent(workflowID)}` },
    "Open run",
    el("span", { className: "run-open-icon", "aria-hidden": "true" }, iconEl(ICON_EXTERNAL)),
  );
  open.addEventListener("click", (e) => {
    // Let a modified click do what the browser would: a new tab or window is a
    // deliberate escape from the app's own routing.
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || (e as MouseEvent).button !== 0) {
      return;
    }
    e.preventDefault();
    onOpen(workflowID, nameEl.textContent);
  });
  const foot = el("div", { className: "run-foot" }, ledger, open);

  root.append(head, alert, body, foot);

  createDisclosure(head, body, {
    open: true,
    onToggle: (isOpen) => {
      root.classList.toggle("collapsed", !isOpen);
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
    // A span for the same reason as the card's own chevron above.
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
    const rowRoot = el(
      "div",
      { className: "run-step collapsed", "data-node": nodePath },
      rowHead,
      rowBody,
    );
    createDisclosure(rowHead, rowBody, {
      open: false,
      onToggle: (isOpen) => {
        rowRoot.classList.toggle("collapsed", !isOpen);
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
    // An ask reclassifies a step only while that step is otherwise IN FLIGHT, and
    // that guard is what resolves the one ambiguity in the join: `node_id` on the
    // wire is a node ID, not a node PATH, so a repeat's iterations share it and a
    // finished pass would light up beside the live one.
    const shown = withAsk(state, asks.nodes.has(node.nodeId));
    row.root.dataset["status"] = shown;
    row.root.dataset["nodeType"] = node.type;
    row.glyph.textContent = STATE_BADGE[shown];
    // Assigned through a local rather than directly, because the fields are
    // optional under exactOptionalPropertyTypes: `undefined` is not a value an
    // optional property accepts, and a pending step legitimately has neither.
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

    // The facts a reader can act on, in the order they answer questions: who ran
    // it, on what, and what went wrong. A watch node names its own kind instead
    // of an agent, because nothing runs a watch — it polls.
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

    // A captured output is a RESULT, so it is visible on the collapsed row rather
    // than hidden behind the disclosure with the step's working transcript. This
    // is the same rule the delegate card's footer follows.
    //
    // TWO LINES, then it clamps (27-run-card.css): this is a trace of what the step
    // produced, and the place to read one in full is the run's own tab, where the
    // exec view's detail pane renders it as the markdown it is. Guarded on the text
    // having CHANGED because `render` runs on every invalidation, dozens of times
    // over a live run.
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
    // Order the rows to match the plan. A row created by a live frame the state
    // does not describe yet keeps its arrival position at the end rather than
    // being dropped: dropping it would delete the blocks rendered inside it.
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

  /** The alert, and the five things that can put a run in front of a person.
   *
   *  Order is by what the reader can do about it: an unanswered ask is the one
   *  state a click resolves right now, a deliberate stop needs nothing, a pause may
   *  need an action, a transient-error park is informational, and a failure needs
   *  reading. Only one shows — a run has one reason it is not moving. */
  function renderAlert(state: RunState | undefined, asks: RunAsks): void {
    const parts: string[] = [];
    let kind = "";
    if (launchError !== "") {
      // Outranks every state, because there is no state: the launch never created
      // a run, so `inspect` has nothing and only this reason exists.
      kind = "failed";
      parts.push(launchError);
    } else if (asks.count > 0) {
      // Ahead of the run's own status deliberately: the run still reads `running`
      // while a step's ask blocks it, so the status would say nothing is wrong.
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
      parts.push(
        state.pauseReason === undefined || state.pauseReason === ""
          ? "Waiting"
          : `Waiting: ${state.pauseReason}`,
      );
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

  /** Artifacts and captured outputs, merged into one list of named results.
   *
   *  KAS keeps them apart because they are produced differently (a step declares
   *  an artifact, `captureOutput` records a transcript), but to a reader they are
   *  the same thing: what the run produced. Artifacts win a key collision, being
   *  the value a step chose to publish. */
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
      // Cleared with the children, or a later render whose content matches this
      // signature would take the early return below and leave the list hidden.
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
        // what it means. KAS writes a key only for a step that captured, so an
        // empty value is the step reporting that it finished without saying
        // anything — the most diagnostic fact a finished run holds, and
        // indistinguishable from "never ran" if the row is dropped.
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
    // A second axis rather than a sixth `data-status` value: the run genuinely IS
    // running while a step's ask blocks it, and overwriting the status would lose
    // that. The rail and the status word read this instead.
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
    // Reads the rendered word rather than re-deriving it, so the accessible name
    // cannot disagree with the one on screen.
    head.setAttribute("aria-label", `Workflow run ${nameEl.textContent}, ${statusEl.textContent}`);
  }

  function tick(): void {
    if (!liveClock) {
      return;
    }
    // Re-derive from the rows' own timestamps rather than re-fetching: a clock is
    // the one thing the client can advance honestly on its own, and a tick that
    // hit the network once a second per card would be a poll nobody asked for.
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
      // Re-render from the store's current value rather than from nothing, so a
      // late failure on a run that DID produce state keeps the steps it produced.
      render(lastState);
    },
  };
}
