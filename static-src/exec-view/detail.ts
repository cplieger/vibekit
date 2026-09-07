// The exec view's detail pane: one node, at full width.
//
// In a conversation a delegated node gets a collapsed row, because N stream into
// one column. Here there is one node on screen at a time, chosen by the reader,
// so everything the source knows about it can be stated outright.
//
// Four regions, in reading order: IDENTITY (facts as a definition list — agent,
// model, effort, signal, retries, a loop's bound, a session id), FAILURE
// (verbatim, above the output since it explains it), OUTPUT (what the node
// produced, as markdown), TRANSCRIPT (the live host, filled by the consumer's
// frames).
//
// The transcript host is handed OUT (`bodyFor`), so this pane knows nothing about
// where content comes from — `run-view.ts` supplies it to whichever step source is
// feeding a node. It is the ONLY host for a step's transcript: the transcript's run
// card used to hand out one of its own and no longer renders step content at all.

import { el } from "@cplieger/reactive";
import { buildAssistantBubble } from "../fundamentals/text-bubble.js";
import { formatElapsed } from "../strings.js";
import { elapsed, type ExecNode } from "./model.js";
import { STATE_WORD } from "./status.js";

export interface ExecDetailView {
  readonly root: HTMLElement;
  /** Show one node. `undefined` renders the empty state. */
  render(node: ExecNode | undefined): void;
  /** The element this node's live transcript renders into, created on demand.
   *
   *  Per PATH and kept for the pane's life, so a reader who selects another node
   *  and comes back finds the content still there — frames are live-only and
   *  cannot be replayed. */
  bodyFor(path: string): HTMLElement;
  /** Advance the duration of a node still running. */
  tick(): void;
}

/** What a node with no transcript host says; distinguishes three cases a blank
 *  region cannot. Injected so the CONSUMER owns the wording: a workflow run's
 *  reasons are not a subagent tab's. */
export type EmptyNote = (node: ExecNode) => string;

/** An affordance to render BESIDE that note, or null when there is none.
 *
 *  Injected for the same reason the wording is, and separate from it because a
 *  note is a claim while this is a door: a workflow run whose steps are in the
 *  launching chat can offer to open it, and a delegate's page — reached FROM the
 *  conversation already — has nowhere new to send anyone. */
export type EmptyAction = (node: ExecNode) => HTMLElement | null;

export function buildExecDetail(emptyNote: EmptyNote, emptyAction?: EmptyAction): ExecDetailView {
  const title = el("span", { className: "ev-d-title" });
  const state = el("span", { className: "ev-d-state" });
  const dur = el("span", { className: "ev-d-dur" });
  const head = el(
    "div",
    { className: "ev-d-head" },
    title,
    el("span", { className: "ev-d-meta" }, state, dur),
  );
  const facts = el("dl", { className: "ev-d-facts" });
  const failure = el("div", { className: "ev-d-fail", role: "status" });
  const output = el("div", { className: "ev-d-out" });
  const bodies = el("div", { className: "ev-d-bodies" });
  const empty = el("div", { className: "ev-d-empty" });
  const emptyAct = el("div", { className: "ev-d-empty-action", hidden: true });
  const root = el(
    "div",
    { className: "ev-detail", "aria-live": "polite" },
    head,
    facts,
    failure,
    output,
    bodies,
    empty,
    emptyAct,
  );

  const hosts = new Map<string, HTMLElement>();
  let shown: ExecNode | undefined;

  function renderOutput(node: ExecNode): void {
    const merged = new Map<string, string>();
    if (node.output !== undefined) {
      merged.set("", node.output);
    }
    for (const [k, v] of Object.entries(node.artifacts ?? {})) {
      merged.set(k, v);
    }
    if (merged.size === 0) {
      output.hidden = true;
      delete output.dataset["sig"];
      output.replaceChildren();
      return;
    }
    // A signature over the whole set: `render` runs on every refetch, dozens
    // over a live run, and re-parsing a settled report each time would reset
    // the reader's scroll.
    const sig = `${node.path}\u0000${[...merged].map(([k, v]) => `${k}\u0001${v}`).join("\u0002")}`;
    if (output.dataset["sig"] === sig) {
      return;
    }
    output.dataset["sig"] = sig;
    output.hidden = false;
    output.replaceChildren(
      ...[...merged].flatMap(([key, value]) => {
        const rows: HTMLElement[] = [];
        rows.push(el("div", { className: "ev-d-out-key" }, key === "" ? "Output" : key));
        if (value.trim() === "") {
          // An empty value is a fact, not an absence: distinguishes "finished
          // silently" from "never ran".
          rows.push(
            el(
              "div",
              { className: "ev-d-out-empty" },
              "This step finished without producing any text.",
            ),
          );
        } else {
          // Through the transcript's own markdown bubble: a captured output IS
          // an assistant message and should read as one.
          rows.push(
            el("div", { className: "ev-d-out-body" }, buildAssistantBubble(value, false).root),
          );
        }
        return rows;
      }),
    );
  }

  function render(node: ExecNode | undefined): void {
    shown = node;
    if (node === undefined) {
      root.dataset["state"] = "pending";
      title.textContent = "No step selected";
      state.textContent = "";
      dur.textContent = "";
      facts.replaceChildren();
      facts.hidden = true;
      failure.hidden = true;
      output.hidden = true;
      bodies.hidden = true;
      empty.hidden = false;
      empty.textContent = "Pick a step to see what it did.";
      emptyAct.replaceChildren();
      emptyAct.hidden = true;
      return;
    }
    root.dataset["state"] = node.state;
    title.textContent = node.label;
    state.textContent = STATE_WORD[node.state];
    const ms = elapsed(node.start, node.end);
    dur.textContent = ms > 0 ? formatElapsed(ms) : "";

    const list = node.facts ?? [];
    facts.hidden = list.length === 0;
    facts.replaceChildren(
      ...list.flatMap((f) => [
        el("dt", { className: "ev-d-k" }, f.label),
        el("dd", { className: f.mono === true ? "ev-d-v ev-mono" : "ev-d-v" }, f.value),
      ]),
    );

    failure.hidden = node.failure === undefined;
    failure.textContent = node.failure ?? "";

    renderOutput(node);

    // Only ONE node's transcript is on screen; the rest stay in the DOM since
    // their live content cannot be replayed.
    let anyShown = false;
    for (const [path, host] of hosts) {
      const isShown = path === node.path;
      host.hidden = !isShown;
      anyShown = anyShown || (isShown && host.childElementCount > 0);
    }
    bodies.hidden = false;
    // A container hosts nothing; saying so beats an empty region.
    const hostable = node.transcript === true;
    empty.hidden = anyShown;
    empty.textContent = hostable ? emptyNote(node) : "";
    if (!hostable) {
      empty.hidden = true;
    }

    const action = hostable ? (emptyAction?.(node) ?? null) : null;
    // IDENTITY-guarded, not signature-guarded: `render` runs on every store
    // invalidation, and re-seating a node BLURS it — so a reader who tabbed to the
    // link would lose focus several times a minute on a live run. The consumer
    // returns a CACHED element, so the guard holds.
    if (action !== emptyAct.firstElementChild) {
      emptyAct.replaceChildren(...(action === null ? [] : [action]));
    }
    emptyAct.hidden = action === null || empty.hidden;
  }

  return {
    root,
    render,
    bodyFor(path) {
      let host = hosts.get(path);
      if (host === undefined) {
        host = el("div", { className: "ev-d-body", "data-path": path });
        host.hidden = shown?.path !== path;
        hosts.set(path, host);
        bodies.appendChild(host);
      }
      // A first frame arriving for the node on screen retires the empty note in
      // the same pass — and its action with it, since that action's whole subject
      // is content that is NOT here.
      if (shown?.path === path) {
        empty.hidden = true;
        emptyAct.hidden = true;
      }
      return host;
    },
    tick() {
      if (shown === undefined || shown.end !== undefined) {
        return;
      }
      const ms = elapsed(shown.start, undefined);
      if (ms > 0) {
        dur.textContent = formatElapsed(ms);
      }
    },
  };
}
