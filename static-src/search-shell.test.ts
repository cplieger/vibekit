// The shared search shell: what every box gets for free, and what each consumer
// still supplies.
//
// The mechanical halves are what this pins — the field's attribute set, the
// debounce, the DOUBLE supersession guard, the `Aa` toggle's forced re-run and
// its `?case=1` convention — because those are exactly what had drifted across
// three hand-authored copies. The per-consumer halves (placement, the counter
// versus the note, the cursor's prev/next) are pinned as ABSENCES: the shell must
// not decide them.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  caseParam,
  createSearchShell,
  matchCaseButton,
  searchField,
  searchIconButton,
  SEARCH_DEBOUNCE_MS,
  wireSearchKeys,
} from "./search-shell.js";
import type { SearchShellSpec } from "./search-shell.js";

beforeEach(() => {
  document.body.innerHTML = "";
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

const GLYPH =
  '<svg width="14" height="14" viewBox="0 0 24 24"><line x1="1" y1="1" x2="2" y2="2"/></svg>';

describe("caseParam", () => {
  it("is empty when insensitive, so an ABSENT parameter is what the server sees", () => {
    // Every endpoint that takes it reads absent as insensitive. Sending `case=0`
    // would work today and would be one server change away from not working.
    expect(caseParam(false)).toBe("");
    expect(caseParam(true)).toBe("1");
  });
});

describe("searchField", () => {
  it("carries the attribute set that was duplicated in three places", () => {
    const f = searchField({
      id: "x-input",
      className: "x-input",
      label: "Find things",
      placeholder: "Find\u2026",
    });
    expect(f.getAttribute("autocomplete")).toBe("off");
    expect(f.getAttribute("autocapitalize")).toBe("off");
    expect(f.getAttribute("spellcheck")).toBe("false");
    // A phone's return key has to say what it does.
    expect(f.getAttribute("enterkeyhint")).toBe("search");
    expect(f.getAttribute("aria-label")).toBe("Find things");
    expect(f.placeholder).toBe("Find\u2026");
  });

  it("defaults to type=text and takes type=search when asked", () => {
    // A `search` input draws the platform's own clear affordance, which belongs
    // on a permanent box and not on one that carries its own ×.
    expect(searchField({ id: "a", className: "c", label: "l", placeholder: "p" }).type).toBe(
      "text",
    );
    expect(
      searchField({ id: "b", className: "c", label: "l", placeholder: "p", type: "search" }).type,
    ).toBe("search");
  });
});

describe("searchIconButton", () => {
  it("holds an SVG, never a text glyph", () => {
    // THE centring contract. `align-items: center` centres a text item's LINE
    // BOX, not its ink, and `×`/`↑`/`↓` each sit differently against it in every
    // font — so the offset was platform-dependent by construction. An SVG is a
    // replaced element whose box IS its ink box.
    const btn = searchIconButton("cls", "Close find", "Close (Esc)", GLYPH, () => undefined);
    expect(btn.querySelector("svg")).not.toBeNull();
    expect(btn.textContent).toBe("");
    expect(btn.getAttribute("aria-label")).toBe("Close find");
    expect(btn.title).toBe("Close (Esc)");
    expect(btn.type).toBe("button");
  });

  it("calls its handler on click", () => {
    const onClick = vi.fn();
    const btn = searchIconButton("cls", "l", "t", GLYPH, onClick);
    btn.click();
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});

describe("matchCaseButton", () => {
  it("is a latched toggle carrying aria-pressed, and keeps Aa as TEXT", () => {
    // The one button whose glyph stays text: the letters ARE the affordance. So
    // it takes the cap-band trim in CSS rather than the SVG answer.
    const onToggle = vi.fn();
    const btn = matchCaseButton("cls case", false, onToggle);
    expect(btn.textContent).toBe("Aa");
    expect(btn.querySelector("svg")).toBeNull();
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(btn.getAttribute("aria-label")).toBe("Match case");

    btn.click();
    expect(btn.getAttribute("aria-pressed")).toBe("true");
    expect(onToggle).toHaveBeenLastCalledWith(true);
    btn.click();
    expect(btn.getAttribute("aria-pressed")).toBe("false");
    expect(onToggle).toHaveBeenLastCalledWith(false);
  });
});

describe("wireSearchKeys", () => {
  it("consumes Escape so it never reaches a modal or a global handler behind the box", () => {
    const field = document.createElement("input");
    document.body.appendChild(field);
    const onDismiss = vi.fn();
    const outer = vi.fn();
    document.body.addEventListener("keydown", outer);
    wireSearchKeys(field, { onDismiss });

    const e = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    field.dispatchEvent(e);
    expect(onDismiss).toHaveBeenCalledTimes(1);
    expect(e.defaultPrevented).toBe(true);
    expect(outer).not.toHaveBeenCalled();
  });

  it("passes Shift to onSubmit, which is a cursor's PREVIOUS", () => {
    const field = document.createElement("input");
    const onSubmit = vi.fn();
    wireSearchKeys(field, { onDismiss: () => undefined, onSubmit });
    field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
    expect(onSubmit).toHaveBeenLastCalledWith(false);
    field.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", shiftKey: true, cancelable: true }),
    );
    expect(onSubmit).toHaveBeenLastCalledWith(true);
  });

  it("leaves Enter alone when the consumer has no submit meaning", () => {
    const field = document.createElement("input");
    wireSearchKeys(field, { onDismiss: () => undefined });
    const e = new KeyboardEvent("keydown", { key: "Enter", cancelable: true });
    field.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
  });
});

/** A shell wired to a resolvable query, so a test can control when it answers. */
// The spec is named with its own result type rather than reached through
// `Parameters<typeof createSearchShell>[0]`, which resolves the generic to
// `unknown` and then poisons `query`'s return type at the spread below.
function harness(over: Partial<SearchShellSpec<string>> = {}) {
  const query = vi.fn();
  const render = vi.fn();
  let resolveWith: ((v: string | null) => void) | null = null;
  const shell = createSearchShell<string>({
    id: "t-search",
    regionClass: "t-search",
    inputClass: "t-input",
    buttonClass: "t-btn",
    label: "Test search",
    placeholder: "Type\u2026",
    compose: ({ input, caseButton, note, closeButton }) => [input, caseButton, note, closeButton],
    query: (q, ctx) => {
      query(q, ctx.caseSensitive);
      return new Promise<string | null>((res) => {
        resolveWith = res;
      });
    },
    render: (res, q) => {
      render(res, q);
    },
    ...over,
  });
  document.body.appendChild(shell.region);
  return {
    shell,
    query,
    render,
    resolve: (v: string | null): void => {
      resolveWith?.(v);
    },
  };
}

describe("createSearchShell: the region", () => {
  it("is a role=search landmark with an accessible name", () => {
    // The History box was a bare <div>, so it was not reachable by landmark
    // navigation at all while the other two were.
    const { shell } = harness();
    expect(shell.region.getAttribute("role")).toBe("search");
    expect(shell.region.getAttribute("aria-label")).toBe("Test search");
    expect(shell.region.id).toBe("t-search");
    expect(shell.input.id).toBe("t-search-input");
  });

  it("omits the toggle, the note and the × unless the consumer asks", () => {
    // FALSE is a real answer, not a default. The cross-chat endpoint is
    // case-insensitive by decision, so a toggle there would be wired to nothing.
    const { shell } = harness();
    expect(shell.caseButton).toBeNull();
    expect(shell.note).toBeNull();
    expect(shell.region.querySelector(".t-btn")).toBeNull();
  });

  it("lets the consumer ARRANGE the parts, so placement stays per-surface", () => {
    // The shell owns the elements; the consumer owns the layout. That is what
    // keeps a floating box and an in-flow panel from needing a mode flag.
    const { shell } = harness({
      matchCase: true,
      note: true,
      compose: ({ input, caseButton, note }) => {
        const row = document.createElement("div");
        row.className = "custom-row";
        row.appendChild(input);
        if (caseButton !== null) {
          row.appendChild(caseButton);
        }
        return [row, note];
      },
    });
    const row = shell.region.querySelector(".custom-row");
    expect(row).not.toBeNull();
    expect(row?.firstElementChild).toBe(shell.input);
    expect(shell.region.lastElementChild).toBe(shell.note);
  });

  it("makes the note a polite live region, because it lands after the results", () => {
    const { shell } = harness({ note: true });
    expect(shell.note?.getAttribute("role")).toBe("status");
    expect(shell.note?.getAttribute("aria-live")).toBe("polite");
    expect(shell.note?.getAttribute("aria-atomic")).toBe("true");
    shell.setNote("read 3 files");
    expect(shell.note?.textContent).toBe("read 3 files");
  });
});

describe("createSearchShell: the query lifecycle", () => {
  it("coalesces a burst of keystrokes into ONE run", async () => {
    const { shell, query } = harness();
    for (const v of ["r", "re", "red"]) {
      shell.input.value = v;
      shell.input.dispatchEvent(new Event("input"));
    }
    expect(query).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS);
    expect(query).toHaveBeenCalledTimes(1);
    expect(query).toHaveBeenCalledWith("red", false);
  });

  it("honours a per-box debounce, because one surface reads 500 files per query", async () => {
    const { shell, query } = harness({ debounceMs: 250 });
    shell.input.value = "x";
    shell.input.dispatchEvent(new Event("input"));
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS);
    expect(query).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(250 - SEARCH_DEBOUNCE_MS);
    expect(query).toHaveBeenCalledTimes(1);
  });

  it("run() fires now and cancels the pending debounce", async () => {
    const { shell, query } = harness();
    shell.input.value = "a";
    shell.input.dispatchEvent(new Event("input"));
    shell.run();
    expect(query).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS * 2);
    expect(query, "the superseded debounce must not fire a second run").toHaveBeenCalledTimes(1);
  });

  it("aborts the in-flight query when a newer one starts", () => {
    const seen: AbortSignal[] = [];
    const { shell } = harness({
      query: (_q: string, ctx: { signal: AbortSignal }) => {
        seen.push(ctx.signal);
        return new Promise<string | null>(() => undefined);
      },
    });
    shell.input.value = "a";
    shell.run();
    shell.input.value = "ab";
    shell.run();
    expect(seen).toHaveLength(2);
    expect(seen[0]?.aborted, "the first query's transport must be cancelled").toBe(true);
    expect(seen[1]?.aborted).toBe(false);
  });

  it("DROPS a stale reply that lands after newer typing, rather than repainting", async () => {
    // The second half of the double guard, and it cannot be dropped: a fetch that
    // already completed cannot be aborted, so the value comparison is what stops
    // an old answer painting over a newer query. All three hand-written copies
    // did this by hand, which is the tell that it belonged in one place.
    const { shell, render, resolve } = harness();
    shell.input.value = "old";
    shell.run();
    shell.input.value = "new";
    resolve("stale result");
    await vi.advanceTimersByTimeAsync(0);
    expect(render).not.toHaveBeenCalled();
  });

  it("renders a reply that is still current", async () => {
    const { shell, render, resolve } = harness();
    shell.input.value = "live";
    shell.run();
    resolve("fresh");
    await vi.advanceTimersByTimeAsync(0);
    expect(render).toHaveBeenCalledWith("fresh", "live");
  });

  it("cancel() drops both halves: the pending debounce AND the open request", async () => {
    // One call, so a consumer's close path cannot remember one and forget the
    // other — which is how a closed box kept repainting.
    const seen: AbortSignal[] = [];
    const { shell, query } = harness({
      query: (_q: string, ctx: { signal: AbortSignal }) => {
        query(_q);
        seen.push(ctx.signal);
        return new Promise<string | null>(() => undefined);
      },
    });
    shell.input.value = "a";
    shell.run();
    shell.input.value = "ab";
    shell.input.dispatchEvent(new Event("input"));
    shell.cancel();
    await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS * 2);
    expect(query, "no run after cancel").toHaveBeenCalledTimes(1);
    expect(seen[0]?.aborted).toBe(true);
  });

  it("swallows a rejected query instead of leaving an unhandled rejection", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const { shell } = harness({
      query: () => Promise.reject(new Error("boom")),
    });
    shell.input.value = "a";
    shell.run();
    await vi.advanceTimersByTimeAsync(0);
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("createSearchShell: the match-case toggle", () => {
  it("FORCES a re-run, because the query string did not change but the match set did", async () => {
    // Every guard in the box compares the query STRING, and flipping the toggle
    // changes neither the string nor fires an input event — so the toggle has to
    // force the run itself or nothing at all happens.
    const { shell, query } = harness({ matchCase: true });
    shell.input.value = "todo";
    shell.run();
    expect(query).toHaveBeenLastCalledWith("todo", false);
    shell.caseButton?.click();
    expect(query).toHaveBeenCalledTimes(2);
    expect(query).toHaveBeenLastCalledWith("todo", true);
  });

  it("carries the flip into every later query", () => {
    const { shell, query } = harness({ matchCase: true });
    shell.caseButton?.click();
    expect(shell.caseSensitive).toBe(true);
    shell.input.value = "z";
    shell.run();
    expect(query).toHaveBeenLastCalledWith("z", true);
  });
});

describe("createSearchShell: dismissal is the consumer's", () => {
  it("routes Escape and the × to the SAME onDismiss", () => {
    // What dismiss MEANS differs per surface (close a popup, close a panel, clear
    // a permanent box), so the shell shares the contract and not the action.
    const onDismiss = vi.fn();
    const { shell } = harness({ closeButton: true, onDismiss });
    shell.region
      .querySelector<HTMLButtonElement>('[aria-label="Close find"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
    shell.input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(onDismiss).toHaveBeenCalledTimes(2);
  });

  it("never hides or reveals the region itself", () => {
    // Reveal is placement's consequence, and the four surfaces disagree: one is a
    // popup with a trigger, one a panel whose results live outside it, two are
    // permanent page furniture. A shell that hid the box would have to know which.
    const { shell } = harness({ closeButton: true, onDismiss: () => undefined });
    shell.region
      .querySelector<HTMLButtonElement>('[aria-label="Close find"]')
      ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(shell.region.hidden).toBe(false);
    expect(shell.region.classList.contains("hidden")).toBe(false);
  });
});
