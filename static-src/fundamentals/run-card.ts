// ---------------------------------------------------------------------------
// Fundamental: the WORKFLOW RUN card — a run's home in the transcript.
//
// A run used to have four disjoint surfaces in a chat and no relation between
// them: a generic `run_workflow` tool row, one anonymous box per step scattered as
// its siblings, an ephemeral toast, and a separate `/run/{id}` tab. Nothing said
// the boxes belonged to the row, nothing said the run was still going, and none of
// the twenty-odd facts KAS reports per step reached the transcript at all. This
// card is the one object: the invocation becomes its header, every step renders
// inside it, and the run tab is the same thing full-screen.
//
// Five regions, and each answers a different question:
//
//   HEAD    what is this, how is it going, how long  (always)
//   SPINE   the whole plan at a glance               (2+ leaves)
//   ALERT   what it needs from a person              (paused, or a stopped run)
//   BODY    per-step rows, each hosting the step's own live blocks
//   FOOT    the outcome, and the way to the full tree
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

/** The five node statuses folded onto the app's existing outcome vocabulary, so a
 *  step row, a tool card and a turn footer all read alike. `skipped` is its own
 *  state rather than a success: a branch that never ran did not succeed, and
 *  showing a check against it would claim work that never happened. */
type StepState = "pending" | "running" | "ok" | "fail" | "warn" | "skipped";

const STEP_BADGE: Readonly<Record<StepState, string>> = {
  pending: "",
  running: "",
  ok: "\u2713",
  fail: "\u2717",
  warn: "\u26A0",
  skipped: "\u2013",
};

/** The word the accessible name uses. Not the wire enum: "aborted" and "failed"
 *  are one thing to a listener, and "pending" reads better as "not started". */
const STEP_WORD: Readonly<Record<StepState, string>> = {
  pending: "not started",
  running: "running",
  ok: "succeeded",
  fail: "failed",
  warn: "stopped",
  skipped: "skipped",
};

function stepStateOf(status: RunNode["status"]): StepState {
  switch (status) {
    case "completed":
      return "ok";
    case "failed":
      return "fail";
    case "aborted":
      return "warn";
    case "paused":
    case "running":
      return "running";
    case "skipped":
      return "skipped";
    default:
      return "pending";
  }
}

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
   *  disclosure and the blocks inside it survive. */
  render(state: RunState | undefined): void;
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
  // `run-state`, not `run-status`: `18-pages.css` already owns a `.run-status`
  // for the /run/{id} page's own summary pill, and two different components
  // sharing a class name is how one page's tweak silently restyles another.
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

  // --- spine ----------------------------------------------------------------
  // The whole plan as one row of pips, so "how far through is this" is answered
  // without opening anything. aria-hidden: it is a visual restatement of the
  // step rows below, and the head's counter is what a listener gets.
  const spine = el("div", { className: "run-spine", "aria-hidden": "true" });

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

  root.append(head, spine, alert, body, foot);

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
  /** The last state rendered, so `setLaunch` can re-render without re-fetching. */
  let lastState: RunState | undefined;

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
  function paintRow(row: StepRow, node: RunNode): void {
    const state = stepStateOf(node.status);
    row.root.dataset["status"] = state;
    row.root.dataset["nodeType"] = node.type;
    row.glyph.textContent = STEP_BADGE[state];
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
    row.head.setAttribute("aria-label", `${row.name.textContent}, ${STEP_WORD[state]}`);

    // A captured output is a RESULT, so it is visible on the collapsed row rather
    // than hidden behind the disclosure with the step's working transcript. This
    // is the same rule the delegate card's footer follows.
    let cap = row.root.querySelector<HTMLElement>(":scope > .run-step-capture");
    const text = node.capturedOutput ?? "";
    if (text === "") {
      cap?.remove();
    } else {
      if (cap === null) {
        cap = el("div", { className: "run-step-capture" });
        row.root.insertBefore(cap, row.body);
      }
      cap.textContent = text;
    }
  }

  function renderSteps(state: RunState | undefined): void {
    const leaves = leafNodes(state?.root);
    for (const node of leaves) {
      const path = nodePathOf(state?.root, node).join("/");
      paintRow(stepRow(path), node);
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

  function renderSpine(state: RunState | undefined): void {
    const leaves = leafNodes(state?.root);
    if (leaves.length < 2) {
      spine.replaceChildren();
      spine.classList.add("hidden");
      return;
    }
    spine.classList.remove("hidden");
    spine.replaceChildren(
      ...leaves.map((n) =>
        el("span", { className: "run-pip", "data-status": stepStateOf(n.status) }),
      ),
    );
  }

  /** The alert, and the four things that can put a run in front of a person.
   *
   *  Order is by what the reader can do about it: a deliberate stop needs nothing,
   *  a pause may need an action, a transient-error park is informational, and a
   *  failure needs reading. Only one shows — a run has one reason it is not
   *  moving. */
  function renderAlert(state: RunState | undefined): void {
    const parts: string[] = [];
    let kind = "";
    if (launchError !== "") {
      // Outranks every state, because there is no state: the launch never created
      // a run, so `inspect` has nothing and only this reason exists.
      kind = "failed";
      parts.push(launchError);
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
      return;
    }
    outputs.classList.remove("hidden");
    outputs.replaceChildren(
      ...[...merged.entries()].flatMap(([k, v]) => [
        el("dt", { className: "run-output-key" }, k),
        el("dd", { className: "run-output-val" }, truncate(v, 300)),
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

  function render(state: RunState | undefined): void {
    lastState = state;
    const label = state?.runLabel ?? state?.workflowName ?? "";
    if (label !== "") {
      nameEl.textContent = label;
    }
    root.dataset["status"] = state?.status ?? "starting";
    statusEl.textContent = runWord(state?.status);

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

    renderSpine(state);
    renderAlert(state);
    renderSteps(state);
    renderOutputs(state);
    renderFoot(state);
    head.setAttribute(
      "aria-label",
      `Workflow run ${nameEl.textContent}, ${runWord(state?.status)}`,
    );
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
