// The exec view's timeline: where the time went, and what waited on what.
//
// Every node carries `startedAt`/`endedAt`, so an execution's shape over time was
// already on the wire and nothing had drawn it — a column shows order, not
// concurrency, so two steps rendered as consecutive rows look identical whether
// they ran sequentially or at once.
//
// LEAVES ONLY: a container's span is its children's, so drawing both
// double-counts the time.
//
// A READOUT, not a chart library: no axes, no ticks, no zoom — one number for
// scale, the rest is position. Selectable with the same `onSelect` and selected
// path as the tree, so it is the same navigation surface from a different angle.

import { el } from "@cplieger/reactive";
import { formatElapsed } from "../strings.js";
import { elapsed, leaves, window as execWindow, type ExecNode } from "./model.js";
import { place } from "./place.js";
import { STATE_WORD } from "./status.js";

export interface ExecTimelineView {
  readonly root: HTMLElement;
  /** Re-render from a fresh tree. */
  render(nodes: readonly ExecNode[], selected: string, live: boolean): void;
  /** Re-place the bars of anything still running: a live run's bar grows and the
   *  window stretches with it. */
  tick(nodes: readonly ExecNode[], selected: string, live: boolean): void;
}

export function buildExecTimeline(onSelect: (path: string) => void): ExecTimelineView {
  const scale = el("span", { className: "ev-tl-scale" });
  const lanes = el("div", { className: "ev-tl-lanes" });
  const root = el(
    "div",
    { className: "ev-tl" },
    el(
      "div",
      { className: "ev-tl-head" },
      el("span", { className: "ev-tl-title" }, "Timeline"),
      scale,
    ),
    lanes,
  );

  /** One lane per node path, reused across passes. */
  interface Lane {
    readonly root: HTMLElement;
    readonly name: HTMLElement;
    readonly bar: HTMLElement;
    readonly dur: HTMLElement;
  }
  const lanesByPath = new Map<string, Lane>();

  function buildLane(path: string): Lane {
    const bar = el("span", { className: "ev-tl-bar" });
    const name = el("span", { className: "ev-tl-name" });
    const dur = el("span", { className: "ev-tl-dur" });
    const root = el(
      "div",
      {
        className: "ev-tl-lane",
        "data-path": path,
        role: "button",
        tabindex: "0",
      },
      name,
      el("span", { className: "ev-tl-track" }, bar),
      dur,
    );
    root.addEventListener("click", () => {
      onSelect(path);
    });
    root.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onSelect(path);
      }
    });
    return { root, name, bar, dur };
  }

  /** RECONCILED by path, not rebuilt, and the record that used to defend the rebuild
   *  was wrong on its own premise: it said "a bar carries no state a reader can
   *  change", and a lane carries two — `:hover`, and FOCUS, since every lane is a
   *  `tabindex="0"` `role="button"`. Both live on the element, so replacing it drops
   *  them. `tick` runs this on the 1s clock for a live run and every store bump runs it
   *  again, so a keyboard reader lost focus and a pointer reader lost the hover fill
   *  from under a stationary cursor several times a second — which is what reads as the
   *  row flickering. `detail.ts` had already recorded the identical defect for its own
   *  host seating. The geometry really is recomputed every pass, which is why the bar's
   *  offsets are written on a REUSED element instead of a fresh one. */
  function paint(nodes: readonly ExecNode[], selected: string, live: boolean): void {
    const ls = leaves(nodes).filter((n) => n.start !== undefined);
    const win = execWindow(nodes, live);
    // TWO leaves minimum: a single bar spanning its own window is always full
    // width and reports nothing the header's elapsed does not already say.
    if (win === undefined || ls.length < 2) {
      root.hidden = true;
      lanes.replaceChildren();
      lanesByPath.clear();
      return;
    }
    root.hidden = false;
    scale.textContent = formatElapsed(win.span);

    // Lanes the timeline no longer describes go first, or the map would keep growing
    // dead paths for a plan that was appended to.
    const live_ = new Set(ls.map((n) => n.path));
    for (const [path, lane] of [...lanesByPath]) {
      if (!live_.has(path)) {
        lane.root.remove();
        lanesByPath.delete(path);
      }
    }

    ls.forEach((n, i) => {
      let lane = lanesByPath.get(n.path);
      if (lane === undefined) {
        lane = buildLane(n.path);
        lanesByPath.set(n.path, lane);
      }
      // Ascending index order, so `place` leaves an already-seated lane alone.
      place(lanes, lane.root, i);

      const from = Date.parse(n.start ?? "");
      const to = n.end === undefined ? win.to : Date.parse(n.end);
      const left = ((from - win.from) / win.span) * 100;
      // Floor of 0.75%: a sub-second step in an hour-long run rounds to zero
      // width otherwise, reporting the step as absent rather than fast.
      const width = Math.max(0.75, ((Math.max(to, from) - from) / win.span) * 100);
      lane.bar.style.insetInlineStart = `${left.toFixed(3)}%`;
      // Clamped so clock skew between the server's stamps and this browser
      // cannot widen the row past the track.
      lane.bar.style.inlineSize = `${Math.min(100 - left, width).toFixed(3)}%`;

      const ms = elapsed(n.start, n.end);
      const durText = ms > 0 ? formatElapsed(ms) : "";
      lane.root.className = n.path === selected ? "ev-tl-lane ev-selected" : "ev-tl-lane";
      lane.root.dataset["state"] = n.state;
      lane.root.setAttribute(
        "aria-label",
        `${n.label}, ${STATE_WORD[n.state]}${durText === "" ? "" : `, ${durText}`}`,
      );
      lane.name.textContent = n.label;
      lane.dur.textContent = durText;
    });
  }

  return {
    root,
    render: paint,
    tick: paint,
  };
}
