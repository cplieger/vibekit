// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";

vi.mock("./dom.js", () => ({
  $: new Proxy({}, { get: () => document.createElement("div") }),
}));
vi.mock("./modals.js", () => ({ showConfirm: () => {} }));
vi.mock("./icons.js", () => ({
  ICON_PLUS: "+", ICON_MINUS: "-", ICON_TRASH: "x",
  ICON_GIT_UP_ARROW: "↑", ICON_GIT_DOWN_ARROW: "↓",
  iconEl: (icon: string) => { const s = document.createElement("span"); s.textContent = icon; return s; },
}));
vi.mock("./editor-openers.js", () => ({ openFileGitDiff: () => {} }));
vi.mock("./git.js", () => ({
  gitPost: async () => {}, selectedRepoKey: () => "r1",
  getStatusData: () => null, refreshGitStatus: async () => {},
}));

import { arrowButtonNodes } from "./git-render.js";

describe("arrowButtonNodes", () => {
  const cases: Array<{ count: number; expectPill: boolean }> = [
    { count: 0, expectPill: false },
    { count: 1, expectPill: true },
    { count: 99, expectPill: true },
  ];

  for (const { count, expectPill } of cases) {
    it(`count=${count} → pill=${expectPill}`, () => {
      const nodes = arrowButtonNodes("↑", count);
      if (!expectPill) {
        expect(nodes).toHaveLength(1);
      } else {
        expect(nodes).toHaveLength(2);
        const pill = nodes[1] as HTMLSpanElement;
        expect(pill.className).toBe("pill-count");
        expect(pill.textContent).toBe(String(count));
      }
    });
  }
});
