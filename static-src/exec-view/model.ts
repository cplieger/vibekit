// ---------------------------------------------------------------------------
// The EXEC VIEW's model: delegated work, as a tree of nodes over time.
//
// This family renders a full-page view of work THIS agent did not do itself. A
// workflow run is the first consumer; a subagent or an `orchestrate_subagent`
// pipeline in its own tab is the next, and the two have the same shape, so the
// views take this model rather than KAS's `NodeStateSchema`:
//
//   a workflow run   sequence / repeat / parallel / watch / step, per-step
//                    sessions, per-step transcripts, per-node timings
//   a pipeline       one driver, N stages, each stage its own transcript —
//                    the same tree one level deep
//   a subagent       one node with a transcript, which is that tree with one leaf
//
// So NOTHING below names a workflow. Each consumer writes an ADAPTER that folds
// its own source into `ExecRun` (the workflow's is `run-exec-source.ts`) and the
// panes never learn where a node came from. That boundary is what makes the
// second consumer a new adapter rather than a second page.
//
// It is deliberately NOT the wire shape of anything. `run-store.ts` keeps KAS's
// `state` verbatim because a second representation of a foreign structure is a
// thing to keep in sync; this is a THIRD kind of type — a presentation model
// derived on read, held by nobody, and answering only what a pane draws. Adding a
// field here costs an adapter a line and costs the wire nothing.
// ---------------------------------------------------------------------------

import type { ExecState } from "./status.js";

/** What KIND of node this is, which decides its glyph and how it reads.
 *
 *  `group` is the catch-all for a container the source has no better word for,
 *  and it exists so an adapter for a source this file has never seen still has a
 *  legal value — the alternative is an adapter inventing a kind the CSS has no
 *  rule for, which renders as nothing. */
export type ExecKind = "step" | "sequence" | "repeat" | "parallel" | "watch" | "group";

/** One labelled fact about a node, for the detail pane's identity list.
 *
 *  A LIST rather than named fields, because the interesting facts are the
 *  source's own and differ per source: a workflow step has an agent, a model, an
 *  effort tier and a completion signal, while a pipeline stage has a stage name
 *  and a tool-call id. Naming them here would mean this file grows a field per
 *  source, which is the god-object shape one convenience at a time. */
export interface ExecFact {
  label: string;
  value: string;
  /** Rendered monospace: an id, a path, a model name. Prose stays proportional. */
  mono?: boolean;
}

/** A node of the execution tree. */
export interface ExecNode {
  /** Stable, instance-unique address. The tree's react key, the selection key,
   *  and the key a transcript is filed under — so a repeat's second iteration
   *  must differ from its first, which a node ID alone cannot do. */
  path: string;
  /** What the row says. The step's own name, not its path. */
  label: string;
  kind: ExecKind;
  state: ExecState;
  children: ExecNode[];
  /** ISO timestamps, when the source has them. Both absent on a node that has
   *  not started; `end` absent on one still going. */
  start?: string;
  end?: string;
  /** The one-line summary under the label: who ran it, on what. Pre-joined by the
   *  adapter, because what belongs there is the source's judgement. */
  subtitle?: string;
  /** The identity facts, for the detail pane. */
  facts?: ExecFact[];
  /** Why this node ended badly, verbatim. Drives the row's emphasis and the
   *  detail pane's callout. */
  failure?: string;
  /** What the node produced, as markdown. A workflow step's `capturedOutput`; a
   *  stage's final message. */
  output?: string;
  /** Named artifacts the node published. */
  artifacts?: Record<string, string>;
  /** Whether this node can host a live transcript. False for a container, and for
   *  a leaf whose source streams nothing — which is what lets the detail pane say
   *  "there is no transcript here" rather than "none has arrived yet". */
  transcript?: boolean;
}

/** The whole execution, plus the facts a header states. */
export interface ExecRun {
  /** The id the route and the store key on. */
  id: string;
  /** What to call this execution. */
  label: string;
  /** The overall state, which is NOT derivable from the nodes: a run can be
   *  `paused` with every node settled, and a source knows its own answer. */
  state: ExecState;
  /** The tree's roots. A list rather than one root, so a source with several
   *  top-level nodes needs no synthetic parent. */
  nodes: ExecNode[];
  /** What this execution was ASKED to do. Rendered in the header, because for a
   *  workflow run it is the launcher's inputs and nothing in the app has ever
   *  shown them. */
  inputs?: Record<string, string>;
  /** Run-level named results, merged from artifacts and captured outputs. */
  outputs?: Record<string, string>;
  /** One line about why the execution wants a person: an unanswered ask, a pause
   *  reason, a deliberate stop, a failure. Pre-composed by the adapter, since the
   *  precedence between those is the source's business. */
  alert?: { kind: "input" | "paused" | "stopped" | "failed"; text: string };
  /** The node the page should OPEN on, when the reader has not clicked one.
   *
   *  A second consumer's requirement rather than the workflow's, and the asymmetry
   *  is in the doors. A run has ONE door meaning "the run", so the page follows the
   *  work and opens on whatever wants attention. A subagent expansion has one door
   *  PER delegate: a stage's link in the transcript means "open THIS stage", and
   *  landing on a sibling because that sibling happened to be running would answer a
   *  question nobody asked.
   *
   *  Honoured as a pre-registered PICK, not as a preference — a door that names a
   *  node is a choice, so it also stops the auto-follow, exactly as a click does.
   *  Absent is the workflow's shape and changes nothing. */
  focus?: string;
  /** Whether anything is still moving, so the page knows to run its clock. */
  live: boolean;
}

/** Every node, depth-first, in plan order. */
export function flatten(nodes: readonly ExecNode[]): ExecNode[] {
  const out: ExecNode[] = [];
  const walk = (n: ExecNode): void => {
    out.push(n);
    for (const k of n.children) {
      walk(k);
    }
  };
  for (const n of nodes) {
    walk(n);
  }
  return out;
}

/** The nodes that DO work, which is what a progress count and a timeline are
 *  about. A container's duration is its children's span, so counting it would
 *  double-count the time and inflate the step total. */
export function leaves(nodes: readonly ExecNode[]): ExecNode[] {
  return flatten(nodes).filter((n) => n.children.length === 0);
}

export interface ExecCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the running leaf, or 0 when none is. The RUNNING one
   *  rather than `done + 1`, because a skipped leaf would shift the count and a
   *  parallel node has several in flight. */
  current: number;
}

export function counters(nodes: readonly ExecNode[]): ExecCounters {
  const ls = leaves(nodes);
  let done = 0;
  let failed = 0;
  let current = 0;
  ls.forEach((n, i) => {
    if (n.state === "ok" || n.state === "skipped") {
      done++;
    }
    if (n.state === "fail") {
      failed++;
    }
    if (current === 0 && (n.state === "running" || n.state === "input")) {
      current = i + 1;
    }
  });
  return { total: ls.length, done, failed, current };
}

/** Elapsed ms between two ISO stamps, counting to NOW when the end is absent and
 *  the start is not. Zero for anything unusable, so a caller can treat 0 as "no
 *  duration to show" rather than testing three cases. */
export function elapsed(start: string | undefined, end: string | undefined): number {
  if (start === undefined || start === "") {
    return 0;
  }
  const from = Date.parse(start);
  if (Number.isNaN(from)) {
    return 0;
  }
  const to = end === undefined || end === "" ? Date.now() : Date.parse(end);
  if (Number.isNaN(to)) {
    return 0;
  }
  return Math.max(0, to - from);
}

/** The execution's own window: earliest start to latest end, or to now while
 *  anything is still going.
 *
 *  Derived from the LEAVES rather than from a root node's stamps, because a source
 *  may not stamp its containers at all — and the leaves are what a timeline draws,
 *  so the window has to be the one they are placed in or the bars leave the box. */
export interface ExecWindow {
  from: number;
  to: number;
  span: number;
}

export function window(nodes: readonly ExecNode[], live: boolean): ExecWindow | undefined {
  let from = Number.POSITIVE_INFINITY;
  let to = 0;
  for (const n of leaves(nodes)) {
    if (n.start === undefined) {
      continue;
    }
    const s = Date.parse(n.start);
    if (Number.isNaN(s)) {
      continue;
    }
    from = Math.min(from, s);
    const e = n.end === undefined ? Date.now() : Date.parse(n.end);
    to = Math.max(to, Number.isNaN(e) ? s : e);
  }
  if (!Number.isFinite(from)) {
    return undefined;
  }
  if (live) {
    to = Math.max(to, Date.now());
  }
  // A floor of 1ms, so a run that started and finished inside one clock tick
  // divides by something and its bars have a width rather than collapsing.
  return { from, to, span: Math.max(1, to - from) };
}
