// The exec view's model: delegated work, as a tree of nodes over time.
//
// A workflow run, a subagent, and an `orchestrate_subagent` pipeline share this
// shape rather than KAS's `NodeStateSchema`, so nothing below names a workflow.
// Each consumer writes an adapter folding its own source into `ExecRun` (the
// workflow's is `run-exec-source.ts`); the panes never learn where a node came
// from.
//
// Deliberately not the wire shape of anything, and a third representation
// alongside `run-store.ts`'s verbatim KAS state: this one is derived on read,
// held by nobody, and answers only what a pane draws.

import type { ExecState } from "./status.js";

/** What KIND of node this is: glyph and reading. `group` is the catch-all for a
 *  container the source has no better word for, so an adapter for an unseen
 *  source has a legal value rather than inventing one the CSS has no rule for. */
export type ExecKind = "step" | "sequence" | "repeat" | "parallel" | "watch" | "group";

/** One labelled fact about a node, for the detail pane's identity list. A LIST
 *  rather than named fields, because the interesting facts differ per source. */
export interface ExecFact {
  label: string;
  value: string;
  /** Rendered monospace: an id, a path, a model name. */
  mono?: boolean;
}

/** A node of the execution tree. */
export interface ExecNode {
  /** Stable, instance-unique address: the tree's react key, the selection key, and
   *  the key a transcript is filed under (a repeat's second iteration must differ
   *  from its first, which a node ID alone cannot do). */
  path: string;
  /** What the row says: the step's own name, not its path. */
  label: string;
  kind: ExecKind;
  state: ExecState;
  children: ExecNode[];
  /** ISO timestamps, when the source has them. */
  start?: string;
  end?: string;
  /** The one-line summary under the label, pre-joined by the adapter. */
  subtitle?: string;
  /** The identity facts, for the detail pane. */
  facts?: ExecFact[];
  /** Why this node ended badly, verbatim. */
  failure?: string;
  /** What the node produced, as markdown. */
  output?: string;
  /** Named artifacts the node published. */
  artifacts?: Record<string, string>;
  /** Whether this node can host a live transcript. False for a container, and for
   *  a leaf whose source streams nothing — lets the detail pane say "there is no
   *  transcript here" rather than "none has arrived yet". */
  transcript?: boolean;
}

/** The whole execution, plus the facts a header states. */
export interface ExecRun {
  /** The id the route and the store key on. */
  id: string;
  label: string;
  /** The overall state, NOT derivable from the nodes: a run can be `paused` with
   *  every node settled, and only the source knows its own answer. */
  state: ExecState;
  /** The tree's roots. A list, so a source with several top-level nodes needs no
   *  synthetic parent. */
  nodes: ExecNode[];
  /** What this execution was asked to do. */
  inputs?: Record<string, string>;
  /** Run-level named results, merged from artifacts and captured outputs. */
  outputs?: Record<string, string>;
  /** One line about why the execution wants a person, pre-composed by the adapter. */
  alert?: { kind: "input" | "paused" | "stopped" | "failed"; text: string };
  /** The node the page should open on when the reader has not clicked one.
   *
   *  A run has ONE door meaning "the run", so the page follows the work. A
   *  subagent expansion has one door PER delegate, so a stage's link means "open
   *  THIS stage" and landing on a sibling would answer a question nobody asked.
   *  Honoured as a pre-registered pick: naming a node also stops the auto-follow,
   *  as a click does. Absent is the workflow's shape. */
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

/** The nodes that DO work: a container's duration is its children's span, so
 *  counting it would double-count time and inflate the step total. */
export function leaves(nodes: readonly ExecNode[]): ExecNode[] {
  return flatten(nodes).filter((n) => n.children.length === 0);
}

export interface ExecCounters {
  total: number;
  done: number;
  failed: number;
  /** The 1-based position of the running leaf, or 0 when none is. The RUNNING one
   *  rather than `done + 1`: a skipped leaf would shift the count and a parallel
   *  node has several in flight. */
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
 *  Derived from the LEAVES, not a root node's stamps: a source may not stamp its
 *  containers at all, and the leaves are what a timeline draws. */
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
  // A floor of 1ms so a run that starts and finishes in one clock tick still
  // divides by something and its bars have a width.
  return { from, to, span: Math.max(1, to - from) };
}
