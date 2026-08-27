// ---------------------------------------------------------------------------
// The exec view's TREE pane: the execution's structure, as a structure.
//
// This is the half a single column cannot express and the reason the page is not a
// transcript. A delegated execution nests — a loop body, a parallel's branches, a
// watch inside a sequence — and every surface before this flattened it to its
// leaves, so a reader could see that seven steps ran and not that two of them were
// the same step twice or that three of them ran at once.
//
// A container is therefore a ROW, not an indent: it carries its own kind glyph, its
// own rolled-up state, and the one fact that explains it (a repeat's bound, a
// parallel's join policy, a watch's condition). Those facts come from `nodePlan`,
// which no client had ever decoded.
//
// SELECTION, not disclosure, is the interaction. A step's detail belongs in the
// pane beside it rather than unfolded in place: unfolding pushes every later row
// down, which on a run being watched moves the thing a reader is looking at, and a
// dedicated page has somewhere better to put it. Containers still collapse, because
// hiding a finished loop's twelve passes is a real want.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { ICON_REFRESH, ICON_GIT_BRANCH, ICON_HOURGLASS, ICON_TAB_AGENT } from "../icons.js";
import { formatElapsed } from "../strings.js";
import { elapsed, type ExecNode } from "./model.js";
import { STATE_BADGE, STATE_WORD, type ExecState } from "./status.js";

/** The kind glyph. A step gets the agent hexagon the app already uses for
 *  delegated work; a container gets a glyph that says what it DOES, because
 *  "sequence" and "parallel" are not words a reader should have to read to tell
 *  one from the other. A sequence gets none: plain top-to-bottom flow is the
 *  default and a glyph for it would be decoration on every row. */
function kindGlyph(kind: ExecNode["kind"]): Element | null {
  switch (kind) {
    case "repeat":
      return iconEl(ICON_REFRESH);
    case "parallel":
      return iconEl(ICON_GIT_BRANCH);
    case "watch":
      return iconEl(ICON_HOURGLASS);
    case "step":
      return iconEl(ICON_TAB_AGENT);
    default:
      return null;
  }
}

export interface ExecTreeView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. Rows are reconciled by PATH, so a collapsed
   *  container stays collapsed and the selected row stays selected across the
   *  refetches a live run drives several times a minute. */
  render(nodes: readonly ExecNode[], selected: string): void;
  /** Advance the durations of the rows still running. */
  tick(): void;
}

interface Row {
  root: HTMLElement;
  label: HTMLElement;
  sub: HTMLElement;
  dur: HTMLElement;
  glyph: HTMLElement;
  kindSlot: HTMLElement;
  chevron: HTMLElement;
  kids: HTMLElement | null;
  start?: string;
  end?: string;
  collapsed: boolean;
}

/** Build the tree pane. `onSelect` is injected, so this file points only downward
 *  and the page owns what selection MEANS. */
export function buildExecTree(onSelect: (path: string) => void): ExecTreeView {
  const root = el("div", {
    className: "ev-tree",
    role: "tree",
    "aria-label": "Execution steps",
  });
  const rows = new Map<string, Row>();

  function buildRow(node: ExecNode, depth: number): Row {
    const glyph = el("span", { className: "ev-state", "aria-hidden": "true" });
    const kindSlot = el("span", { className: "ev-kind", "aria-hidden": "true" });
    const label = el("span", { className: "ev-label" });
    const sub = el("span", { className: "ev-sub" });
    const dur = el("span", { className: "ev-dur" });
    // A SPAN, not a button: the row itself is the control, and a `<button>` inside
    // an element carrying `role="treeitem"` plus a click handler is axe's
    // `nested-interactive` — the finding the run card and the tab strip both had to
    // fix, and `aria-hidden` does not clear it.
    const chevron = el("span", { className: "ev-twist", "aria-hidden": "true" }, chevronEl());

    const head = el(
      "div",
      { className: "ev-row-main" },
      chevron,
      glyph,
      kindSlot,
      el("span", { className: "ev-text" }, label, sub),
      dur,
    );
    const rowRoot = el("div", {
      className: "ev-row",
      role: "treeitem",
      tabindex: "-1",
      "data-path": node.path,
    });
    rowRoot.style.setProperty("--ev-depth", String(depth));
    rowRoot.appendChild(head);

    const row: Row = {
      root: rowRoot,
      label,
      sub,
      dur,
      glyph,
      kindSlot,
      chevron,
      kids: null,
      collapsed: false,
    };

    head.addEventListener("click", (e) => {
      // The twist is a zone of the row rather than a nested control, so the row's
      // one handler tells them apart by target — which is what keeps the row a
      // single activation target for a keyboard and a screen reader.
      if (chevron.contains(e.target as Node) && row.kids !== null) {
        row.collapsed = !row.collapsed;
        applyCollapse(row);
        return;
      }
      onSelect(node.path);
    });
    head.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onSelect(node.path);
      }
    });
    return row;
  }

  function applyCollapse(row: Row): void {
    row.root.classList.toggle("ev-collapsed", row.collapsed);
    row.root.setAttribute("aria-expanded", String(!row.collapsed));
    if (row.kids !== null) {
      row.kids.hidden = row.collapsed;
    }
  }

  function paint(node: ExecNode, depth: number, selected: string, into: HTMLElement): void {
    let row = rows.get(node.path);
    if (row === undefined) {
      row = buildRow(node, depth);
      rows.set(node.path, row);
    }
    into.appendChild(row.root);

    row.label.textContent = node.label;
    row.sub.textContent = node.subtitle ?? "";
    row.sub.hidden = (node.subtitle ?? "") === "";
    row.root.dataset["state"] = node.state;
    row.root.dataset["kind"] = node.kind;
    row.glyph.textContent = STATE_BADGE[node.state];
    const kg = kindGlyph(node.kind);
    if (row.kindSlot.childElementCount === 0 && kg !== null) {
      row.kindSlot.appendChild(kg);
    }
    row.kindSlot.hidden = kg === null;
    // Assigned through locals: the fields are optional under
    // exactOptionalPropertyTypes, so `undefined` is not a value they accept.
    if (node.start === undefined) {
      delete row.start;
    } else {
      row.start = node.start;
    }
    if (node.end === undefined) {
      delete row.end;
    } else {
      row.end = node.end;
    }
    const ms = elapsed(node.start, node.end);
    row.dur.textContent = ms > 0 ? formatElapsed(ms) : "";
    row.root.classList.toggle("ev-selected", node.path === selected);
    row.root.setAttribute("aria-selected", String(node.path === selected));
    // The accessible name is read back off the RENDERED text, so it cannot
    // disagree with what is on screen.
    row.root.setAttribute(
      "aria-label",
      `${node.label}, ${STATE_WORD[node.state]}${node.subtitle === undefined ? "" : `, ${node.subtitle}`}`,
    );

    if (node.children.length === 0) {
      row.kids?.remove();
      row.kids = null;
      row.chevron.hidden = true;
      row.root.removeAttribute("aria-expanded");
      return;
    }
    row.chevron.hidden = false;
    row.kids ??= el("div", { className: "ev-kids", role: "group" });
    row.root.appendChild(row.kids);
    // Rebuilt as an ORDERING pass: every child row is a reconciled element from
    // `rows`, so appending in plan order moves the existing nodes rather than
    // replacing them, and a selected or collapsed descendant survives.
    const kids = row.kids;
    for (const child of node.children) {
      paint(child, depth + 1, selected, kids);
    }
    applyCollapse(row);
  }

  return {
    root,
    render(nodes, selected) {
      // Rows the tree no longer describes are dropped from the index, or a run
      // whose plan was appended to would keep growing a map of dead paths.
      const live = new Set<string>();
      const mark = (n: ExecNode): void => {
        live.add(n.path);
        n.children.forEach(mark);
      };
      nodes.forEach(mark);
      for (const path of [...rows.keys()]) {
        if (!live.has(path)) {
          rows.get(path)?.root.remove();
          rows.delete(path);
        }
      }
      for (const n of nodes) {
        paint(n, 0, selected, root);
      }
    },
    tick() {
      for (const row of rows.values()) {
        if (row.end !== undefined || row.start === undefined) {
          continue;
        }
        const ms = elapsed(row.start, undefined);
        if (ms > 0) {
          row.dur.textContent = formatElapsed(ms);
        }
      }
    },
  };
}

/** Find a node by path. The page's selection is a path, and the detail pane needs
 *  the node — kept here rather than in the model because it is a VIEW concern (a
 *  selection that no longer resolves falls back to the first leaf). */
export function nodeAt(nodes: readonly ExecNode[], path: string): ExecNode | undefined {
  for (const n of nodes) {
    if (n.path === path) {
      return n;
    }
    const hit = nodeAt(n.children, path);
    if (hit !== undefined) {
      return hit;
    }
  }
  return undefined;
}

/** Whether a state deserves a reader's attention, which is what the page uses to
 *  pick a default selection: the node that wants something, else the one working,
 *  else the first. */
export function attentionRank(state: ExecState): number {
  switch (state) {
    case "input":
      return 0;
    case "fail":
      return 1;
    case "running":
      return 2;
    case "waiting":
      return 3;
    case "warn":
      return 4;
    default:
      return 5;
  }
}
