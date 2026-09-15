// The shared DOM walker's own suite. find-in-chat.test.ts keeps the transcript
// overlay's cases and builds its DOM by hand; this file pins the engine on the
// shapes its two consumers really render — a diff pane with highlighter token
// spans and gutter chrome, prose with inline elements, a tool card's chrome —
// which a flat text node cannot express.
import { describe, it, expect, afterEach } from "vitest";
import { FindEngine } from "./find-engine.js";
import { lineDiff } from "./diff.js";
import { renderDiffPane } from "./diff-pane.js";

afterEach(() => {
  document.body.replaceChildren();
});

function mount(html: string): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = html;
  document.body.appendChild(host);
  return host;
}

function marks(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>("mark.find-hit")];
}

function hitOf(mark: HTMLElement): string | null {
  return mark.getAttribute("data-hit");
}

/** The editor's diff pane over a one-line change, with the option set
 *  editor-diff.ts passes (two columns, line numbers, a Go language hint), so the
 *  rows carry highlighter token spans and gutter chrome. `alpha` sits on a
 *  CONTEXT line, `old` on the deleted one, `new` on the added one. */
function goPane(): HTMLElement {
  const oldText = ["func alpha() {", '\treturn "old"', "}", ""].join("\n");
  const newText = ["func alpha() {", '\treturn "new"', "}", ""].join("\n");
  const pane = renderDiffPane(lineDiff(oldText, newText), {
    oldLabel: "before",
    newLabel: "after",
    lineNumbers: true,
    syncScroll: true,
    lang: "x.go",
    source: { oldText, newText },
  });
  const host = document.createElement("div");
  host.appendChild(pane);
  document.body.appendChild(host);
  return host;
}

describe("a match is found in a run, not in a text node", () => {
  it("matches a phrase whose words sit in different highlighter token spans", () => {
    const host = goPane();
    // The highlighter splits `func alpha() {` into a keyword span, a text node
    // and three punctuation spans; the line reads as one line.
    expect(new FindEngine(host).search("func alpha")).toBe(2);
    expect(new FindEngine(host).search("() {")).toBe(2);
    expect(new FindEngine(host).search('return "new"')).toBe(1);
  });

  it("matches a phrase crossing an inline element boundary", () => {
    const host = mount(`<p>call <code>foo bar</code> now</p>`);
    const eng = new FindEngine(host);
    expect(eng.search("call foo")).toBe(1);
    const pieces = marks(host);
    expect(pieces.map((m) => m.textContent)).toEqual(["call ", "foo"]);
    expect(pieces.map(hitOf)).toEqual(["0", "0"]);
    expect(host.textContent).toBe("call foo bar now");
  });

  it("counts hits rather than pieces, and steps between hits", () => {
    const host = mount(`<p>ab<b>c</b> abc</p>`);
    const eng = new FindEngine(host);
    expect(eng.search("abc")).toBe(2);
    expect(eng.total).toBe(2);
    const pieces = marks(host);
    expect(pieces.map(hitOf)).toEqual(["0", "0", "1"]);
    // The current hit is the first one, on every one of its pieces, and the
    // scroll target is the piece the hit starts in.
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      true,
      true,
      false,
    ]);
    expect(eng.currentMark()).toBe(pieces[0]);
    eng.next();
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      true,
    ]);
    expect(eng.currentMark()).toBe(pieces[2]);
  });

  it("drops the current hit without dropping the highlight, and takes it back on setCurrent", () => {
    // The editor's conflict mode steps one cursor through this engine's marks and
    // then the buffer's hits; while the cursor is in the buffer, a hit still styled
    // current here would be a second "you are here".
    const host = mount(`<p>ab<b>c</b> abc</p>`);
    const eng = new FindEngine(host);
    eng.search("abc");
    eng.clearCurrent();
    const pieces = marks(host);
    expect(pieces).toHaveLength(3);
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      false,
    ]);
    expect(eng.currentIndex).toBe(-1);
    expect(eng.currentMark()).toBeNull();
    eng.setCurrent(1);
    expect(pieces.map((m) => m.classList.contains("find-hit-current"))).toEqual([
      false,
      false,
      true,
    ]);
  });

  it("does not join text across a block boundary or a <br>", () => {
    expect(new FindEngine(mount(`<div>func </div><div>alpha</div>`)).search("func alpha")).toBe(0);
    expect(new FindEngine(mount(`<p>func <br>alpha</p>`)).search("func alpha")).toBe(0);
    expect(
      new FindEngine(mount(`<ul><li>func </li><li>alpha</li></ul>`)).search("func alpha"),
    ).toBe(0);
    // The control: the same words in inline siblings are one line.
    expect(new FindEngine(mount(`<span>func </span><span>alpha</span>`)).search("func alpha")).toBe(
      1,
    );
  });

  it("restores the original text and answers the same count on a second run", () => {
    const p = document.createElement("p");
    // Adjacent text nodes, the shape the streaming renderer leaves behind: the
    // per-node walker missed the split word and found it only on the run after
    // `clear()` had merged the nodes.
    p.append(document.createTextNode("al"), document.createTextNode("pha alpha"));
    const host = document.createElement("div");
    host.appendChild(p);
    document.body.appendChild(host);
    const eng = new FindEngine(host);
    expect(eng.search("alpha")).toBe(2);
    eng.clear();
    expect(host.textContent).toBe("alpha alpha");
    expect(eng.search("alpha")).toBe(2);
  });

  it("answers the same count on two consecutive runs over a real diff pane", () => {
    const host = goPane();
    const eng = new FindEngine(host);
    const first = { alpha: eng.search("alpha"), phrase: eng.search("func alpha") };
    const second = { alpha: eng.search("alpha"), phrase: eng.search("func alpha") };
    expect(first).toEqual({ alpha: 2, phrase: 2 });
    expect(second).toEqual(first);
  });
});

describe("both diff columns", () => {
  it("counts a context line rendered in both columns as two hits", () => {
    // Two hits, one per column: we find text hits, we do not filter; it must be
    // predictable.
    const host = goPane();
    expect(new FindEngine(host).search("alpha")).toBe(2);
    expect(marks(host).map((m) => m.closest(".diff-col")?.className)).toEqual([
      "diff-col diff-col-old",
      "diff-col diff-col-new",
    ]);
    expect(new FindEngine(host).search("old")).toBe(1);
    expect(new FindEngine(host).search("new")).toBe(1);
  });
});

describe("UI chrome", () => {
  it("skips a diff pane's line numbers and markers", () => {
    const host = goPane();
    expect(host.querySelector(".diff-gutter")?.textContent).toBe("1");
    expect(new FindEngine(host).search("1")).toBe(0);
    expect(new FindEngine(host).search("-")).toBe(0);
    expect(new FindEngine(host).search("+")).toBe(0);
  });

  it("marks the refused resource inside .tool-denial while skipping the card's other chrome", () => {
    const host = mount(
      `<div class="tool-call">` +
        `<div class="tool-header"><span class="tool-title">Run Command</span></div>` +
        `<div class="tool-denial" data-vk-chrome>` +
        `<div class="tool-denial-row"><span>Resource</span><code>rm -rf /config</code></div>` +
        `</div>` +
        `<button class="tool-output-reveal" data-vk-chrome>Show 3 more lines</button>` +
        `</div>`,
    );
    expect(new FindEngine(host).search("rm -rf")).toBe(1);
    expect(marks(host)[0]?.closest(".tool-denial")).not.toBeNull();
    expect(new FindEngine(host).search("more lines")).toBe(0);
  });

  it("marks the MCP server name in the badge", () => {
    const host = mount(
      `<div class="tool-header"><span class="tool-title">create issue</span>` +
        `<span class="tool-mcp-badge" data-vk-chrome>github</span></div>`,
    );
    expect(new FindEngine(host).search("github")).toBe(1);
    expect(marks(host)[0]?.className).toBe("find-hit find-hit-current");
  });
});

describe("the fold", () => {
  it("marks the right characters after U+0130, whose lowercase grows", () => {
    const host = mount(`<p>\u0130\u{1F600}needle</p>`);
    expect(new FindEngine(host).search("needle")).toBe(1);
    expect(marks(host)[0]?.textContent).toBe("needle");
    expect(host.textContent).toBe("\u0130\u{1F600}needle");
  });

  it("folds U+0130 to a plain i, as the server does", () => {
    const host = mount(`<p>\u0130stanbul</p>`);
    expect(new FindEngine(host).search("istanbul")).toBe(1);
    expect(marks(host)[0]?.textContent).toBe("\u0130stanbul");
  });
});
