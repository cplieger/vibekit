// ---------------------------------------------------------------------------
// A row the exec view has already seated is not re-seated.
//
// `appendChild` on an already-attached node is a remove plus an insert, so it
// restarts every CSS animation in the subtree and drops the reader state that
// lives on the element (`:hover`, focus). Both panes re-render on every store
// bump and the timeline additionally on the 1s tick, so a live run paid that cost
// several times a second: measured on the live app, a running row's `vk-spin`
// ring was knocked back to its start angle 2.58 times a second against the 600ms
// it needs for one revolution, so it never completed a turn. That is what was
// reported as the spinner freezing and restarting, and as the row flickering.
//
// These cases run in real Chromium, which is what lets the first one assert on an
// actual animation clock rather than on a proxy for it. Each was red-checked
// against `place` restored to a bare `appendChild`.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { buildExecTree } from "./tree.js";
import { buildExecTimeline } from "./timeline.js";
import type { ExecNode } from "./model.js";

function step(path: string, over: Partial<ExecNode> = {}): ExecNode {
  return {
    path,
    label: path,
    kind: "step",
    state: "running",
    children: [],
    start: new Date(Date.now() - 60_000).toISOString(),
    ...over,
  };
}

let host: HTMLElement;

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
});

afterEach(() => {
  host.remove();
});

describe("the tree pane", () => {
  it("does not restart a running row's animation on re-render", async () => {
    const tree = buildExecTree(vi.fn());
    host.appendChild(tree.root);
    const nodes = [step("a"), step("b")];
    tree.render(nodes, "a");

    // A real CSS animation on the row, standing in for the `vk-spin` ring the
    // stylesheet puts on `.ev-state::before` of a running row.
    const style = document.createElement("style");
    style.textContent =
      "@keyframes reseat-spin { to { rotate: 360deg } } .ev-row { animation: reseat-spin 600ms linear infinite }";
    document.head.appendChild(style);

    const rowOf = (p: string): HTMLElement =>
      host.querySelector<HTMLElement>(`.ev-row[data-path="${p}"]`)!;

    // PAUSED AT A KNOWN POINT, so the case depends on no clock at all. Reading elapsed
    // `currentTime` needs the animation to have ADVANCED, and reading `startTime` needs it
    // to have STARTED; under a cold full-suite run Chromium throttles rAF hard enough that
    // neither happens inside any sleep worth writing — measured, a 1.6s poll for a
    // non-null `startTime` timed out. Positioning the animation ourselves removes the
    // premise: a paused animation holds its `currentTime`, and a re-seat cannot preserve
    // it because the re-seat DESTROYS this animation and starts a fresh, running one from
    // zero (measured on the live app: 7216ms to 17ms, with a new `startTime`).
    const animOf = (p: string): Animation | undefined => rowOf(p).getAnimations()[0];
    const anim = animOf("a");
    expect(anim, "the row needs an animation to probe").toBeDefined();
    anim!.pause();
    anim!.currentTime = 250;
    expect(Number(animOf("a")?.currentTime), "the probe holds a positioned animation").toBe(250);

    tree.render(nodes, "a");
    await new Promise((r) => requestAnimationFrame(r));

    const after = animOf("a");
    expect(after, "the row must still carry its animation").toBeDefined();
    expect(Number(after?.currentTime), "a re-render must not restart the row's animation").toBe(
      250,
    );
    expect(after?.playState, "nor replace it with a fresh running one").toBe("paused");
    style.remove();
  });

  it("keeps focus on a row across a re-render", () => {
    const tree = buildExecTree(vi.fn());
    host.appendChild(tree.root);
    const nodes = [step("a"), step("b"), step("c")];
    tree.render(nodes, "a");

    const row = host.querySelector<HTMLElement>('.ev-row[data-path="b"] .ev-row-main')!;
    row.tabIndex = 0;
    row.focus();
    expect(document.activeElement).toBe(row);

    tree.render(nodes, "a");
    expect(document.activeElement).toBe(row);
  });

  it("still reorders when the plan order changes, and still adds and drops rows", () => {
    const tree = buildExecTree(vi.fn());
    host.appendChild(tree.root);
    const paths = (): string[] =>
      [...host.querySelectorAll<HTMLElement>(".ev-tree > .ev-row")].map(
        (r) => r.dataset["path"] ?? "",
      );

    tree.render([step("a"), step("b"), step("c")], "a");
    expect(paths()).toEqual(["a", "b", "c"]);

    // Reversed: every row has to move, so a placement that only ever no-ops fails here.
    tree.render([step("c"), step("b"), step("a")], "a");
    expect(paths()).toEqual(["c", "b", "a"]);

    // An inserted row lands in the middle rather than at the end.
    tree.render([step("c"), step("d"), step("b"), step("a")], "a");
    expect(paths()).toEqual(["c", "d", "b", "a"]);

    // A dropped row leaves and the rest stay in order.
    tree.render([step("c"), step("a")], "a");
    expect(paths()).toEqual(["c", "a"]);
  });

  it("nests children in order under a container without re-seating the container", () => {
    const tree = buildExecTree(vi.fn());
    host.appendChild(tree.root);
    const group = (kids: ExecNode[]): ExecNode => step("g", { kind: "sequence", children: kids });
    tree.render([group([step("k1"), step("k2")])], "k1");

    const kidsBox = host.querySelector<HTMLElement>(".ev-kids")!;
    const kidPaths = (): string[] =>
      [...kidsBox.querySelectorAll<HTMLElement>(":scope > .ev-row")].map(
        (r) => r.dataset["path"] ?? "",
      );
    expect(kidPaths()).toEqual(["k1", "k2"]);

    // `.ev-kids` is index 1 of the container row; a second pass must not move it.
    const rowRoot = host.querySelector<HTMLElement>('.ev-row[data-path="g"]')!;
    expect(rowRoot.children[1]).toBe(kidsBox);
    tree.render([group([step("k2"), step("k1")])], "k1");
    expect(rowRoot.children[1]).toBe(kidsBox);
    expect(kidPaths()).toEqual(["k2", "k1"]);
  });
});

describe("the timeline pane", () => {
  const two = (): ExecNode[] => [
    step("a", { end: new Date(Date.now() - 30_000).toISOString() }),
    step("b"),
  ];

  it("keeps focus on a lane across a re-render and across a tick", () => {
    const tl = buildExecTimeline(vi.fn());
    host.appendChild(tl.root);
    tl.render(two(), "a", true);

    const lane = host.querySelector<HTMLElement>('.ev-tl-lane[data-path="b"]')!;
    lane.focus();
    expect(document.activeElement).toBe(lane);

    tl.render(two(), "a", true);
    expect(document.activeElement).toBe(lane);

    // `tick` is the 1s clock a live run drives; it rebuilt every lane too.
    tl.tick(two(), "a", true);
    expect(document.activeElement).toBe(lane);
  });

  it("reuses the same lane element while still updating its geometry and labels", () => {
    const tl = buildExecTimeline(vi.fn());
    host.appendChild(tl.root);
    tl.render(two(), "a", true);

    const lane = host.querySelector<HTMLElement>('.ev-tl-lane[data-path="b"]')!;
    const widthBefore = lane.querySelector<HTMLElement>(".ev-tl-bar")!.style.inlineSize;

    // Same paths, a longer window: the bar has to be re-placed on the SAME element.
    const grown: ExecNode[] = [
      step("a", {
        start: new Date(Date.now() - 600_000).toISOString(),
        end: new Date(Date.now() - 30_000).toISOString(),
      }),
      step("b", { label: "b renamed" }),
    ];
    tl.render(grown, "b", true);

    expect(host.querySelector('.ev-tl-lane[data-path="b"]')).toBe(lane);
    expect(lane.querySelector(".ev-tl-name")!.textContent).toBe("b renamed");
    expect(lane.classList.contains("ev-selected")).toBe(true);
    expect(lane.querySelector<HTMLElement>(".ev-tl-bar")!.style.inlineSize).not.toBe(widthBefore);
  });

  it("drops a lane the plan no longer describes", () => {
    const tl = buildExecTimeline(vi.fn());
    host.appendChild(tl.root);
    tl.render([...two(), step("c")], "a", true);
    expect(host.querySelectorAll(".ev-tl-lane").length).toBe(3);

    tl.render(two(), "a", true);
    expect(host.querySelectorAll(".ev-tl-lane").length).toBe(2);
    expect(host.querySelector('.ev-tl-lane[data-path="c"]')).toBeNull();
  });
});
