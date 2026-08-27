// ---------------------------------------------------------------------------
// The exec view's DETAIL pane: one node, at full width.
//
// This is what the page buys that a transcript block cannot. In a conversation a
// delegated node gets a collapsed row, because N of them stream into one column and
// ten walls of text is not a transcript. Here there is one node on screen at a time,
// chosen by the reader, so everything the source knows about it can be stated
// outright: who ran it, on what, how it ended, what it produced, and what it did.
//
// Four regions, in the order a reader asks for them:
//
//   IDENTITY   the facts, as a definition list — agent, model, effort, signal,
//              retries, a loop's bound, a session id
//   FAILURE    verbatim, when there is one, and above the output because it is why
//              the output looks the way it does
//   OUTPUT     what the node produced, as the MARKDOWN it is
//   TRANSCRIPT the live host, filled by whatever streams the consumer's frames
//
// The transcript host is handed OUT rather than filled here (`bodyFor`), the same
// contract the run card's `stepBody` uses, so this pane knows nothing about where
// content comes from and a subagent tab reuses it unchanged.
// ---------------------------------------------------------------------------

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
   *  and comes back finds the content still there rather than a host that was
   *  discarded with the render. That retention is the whole reason it is a map: the
   *  frames are live-only and cannot be replayed. */
  bodyFor(path: string): HTMLElement;
  /** Advance the duration of a node still running. */
  tick(): void;
}

/** What a node with no transcript host says, and it has to distinguish three cases
 *  a blank region cannot. Injected as a function so the CONSUMER owns the wording:
 *  a workflow run's reasons (the content went to a chat, or it was never stored)
 *  are not a subagent tab's. */
export type EmptyNote = (node: ExecNode) => string;

export function buildExecDetail(emptyNote: EmptyNote): ExecDetailView {
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
  const root = el(
    "div",
    { className: "ev-detail", "aria-live": "polite" },
    head,
    facts,
    failure,
    output,
    bodies,
    empty,
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
    // A signature over the whole set, because `render` runs on every refetch —
    // dozens over a live run — and re-parsing a settled report each time would throw
    // away the reader's place in it.
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
          // An EMPTY value is a fact, not an absence: a source writes a key only for
          // a node that captured, so empty says the node finished without saying
          // anything — indistinguishable from "never ran" if the row is dropped.
          rows.push(
            el(
              "div",
              { className: "ev-d-out-empty" },
              "This step finished without producing any text.",
            ),
          );
        } else {
          // Through the transcript's own markdown bubble: a captured output IS an
          // assistant message, so a report written in markdown should read as one
          // rather than showing its own asterisks in a `<pre>`.
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

    // Only ONE node's transcript is on screen; the rest stay in the DOM so their
    // live content survives a selection change, since the frames cannot be replayed.
    let anyShown = false;
    for (const [path, host] of hosts) {
      const isShown = path === node.path;
      host.hidden = !isShown;
      anyShown = anyShown || (isShown && host.childElementCount > 0);
    }
    bodies.hidden = false;
    // A container hosts nothing, and saying so beats an empty region: the reader is
    // looking at a loop or a branch, not at a step that went quiet.
    const hostable = node.transcript === true;
    empty.hidden = anyShown;
    empty.textContent = hostable ? emptyNote(node) : "";
    if (!hostable) {
      empty.hidden = true;
    }
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
      // A first frame arriving for the node on screen retires the empty note in the
      // same pass, so a reader watching a step that has just started sees the note
      // replaced rather than sitting under real content.
      if (shown?.path === path) {
        empty.hidden = true;
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
