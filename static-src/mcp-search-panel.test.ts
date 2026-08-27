// The registry-search panel's render states and what it asks the registry for.
//
// Three behaviours, each of which shipped wrong and each of which reads as "the
// search is broken" from the box:
//
//   1. An in-flight search painted NOTHING. The registry answers in about a
//      second when healthy and can take ten when it is not, so the box sat empty
//      for the whole wait and then printed a failure. Empty-then-error reads as a
//      dead control rather than a slow one.
//   2. Every typed PREFIX was its own query. Each is a distinct cache key and a
//      distinct dedupe key, so nothing collapsed them, and the upstream refuses
//      connections after a burst of them.
//   3. A late answer for an abandoned prefix overwrote the current one. The
//      dispatches are not scoped, so "gith" arriving after "github" put results
//      for a query the user had typed past on screen.
//
// The subscription callback is the seam: the panel reads action lifecycle
// instances, so the test hands it synthetic ones instead of dispatching.

import { describe, it, expect, vi, beforeEach } from "vitest";

interface Inst {
  name: string;
  status: "pending" | "success" | "error" | "cancelled";
  args: unknown;
  result?: unknown;
}

let captured: ((inst: Inst) => void) | null = null;
const scheduled: { q: string }[] = [];
const flushed: { q: string }[] = [];
const dispatched: { q: string }[] = [];

vi.mock("./actions/index.js", () => ({
  subscribeToActions: (cb: (inst: Inst) => void) => {
    captured = cb;
    return () => {
      captured = null;
    };
  },
  bindLoadingState: () => () => {
    /* noop */
  },
  registerCleanup: () => {
    /* noop */
  },
  debouncedDispatch: () => {
    const fn = (args: { q: string }): void => {
      scheduled.push(args);
    };
    fn.flush = (args?: { q: string }) => {
      if (args !== undefined) {
        flushed.push(args);
      }
      return undefined;
    };
    fn.cancel = () => {
      /* noop */
    };
    fn.isPending = () => false;
    return fn;
  },
}));

vi.mock("./actions/mcp.js", () => ({
  searchRegistry: {
    dispatch: (args: { q: string }) => {
      dispatched.push(args);
      return { outcome: Promise.resolve({ status: "success" }) };
    },
    cancel: () => {
      /* noop */
    },
  },
}));

const { initSearchPanel, cleanupSearch } = await import("./mcp-panels-search.js");

const HIT = {
  servers: [
    {
      name: "io.example/thing",
      description: "a thing",
      packages: [{ registry_type: "npm", identifier: "@example/thing" }],
    },
  ],
};

function mountPanel(): { input: HTMLInputElement; results: HTMLDivElement } {
  document.body.replaceChildren();
  const host = document.createElement("div");
  host.innerHTML = `
    <input id="mcp-search-input" type="search">
    <button id="mcp-search-btn" type="button"></button>
    <div id="mcp-search-results"></div>`;
  document.body.append(...Array.from(host.childNodes));
  initSearchPanel();
  return {
    input: document.getElementById("mcp-search-input") as HTMLInputElement,
    results: document.getElementById("mcp-search-results") as HTMLDivElement,
  };
}

function type(input: HTMLInputElement, value: string): void {
  input.value = value;
  input.oninput?.(new InputEvent("input"));
}

beforeEach(() => {
  scheduled.length = 0;
  flushed.length = 0;
  dispatched.length = 0;
  cleanupSearch();
});

describe("what the panel asks the registry for", () => {
  it("ignores a one-character query and asks from two", () => {
    const { input, results } = mountPanel();

    type(input, "g");
    expect(scheduled, "a single letter matches most of the index").toEqual([]);
    expect(results.children).toHaveLength(0);

    type(input, "gi");
    expect(scheduled).toEqual([{ q: "gi" }]);
  });

  it("clears the box when the query drops back below the floor", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    captured?.({
      name: "mcp.search_registry",
      status: "success",
      args: { q: "github" },
      result: HIT,
    });
    expect(results.textContent).toContain("io.example/thing");

    type(input, "g");
    expect(results.children).toHaveLength(0);
  });

  it("fires immediately on Enter and on the search button, skipping the quiet window", () => {
    const { input } = mountPanel();
    input.value = "notion";

    input.onkeydown?.(new KeyboardEvent("keydown", { key: "Enter" }));
    expect(flushed).toEqual([{ q: "notion" }]);

    (document.getElementById("mcp-search-btn") as HTMLButtonElement).onclick?.(
      new PointerEvent("click"),
    );
    expect(flushed).toEqual([{ q: "notion" }, { q: "notion" }]);
  });
});

describe("what the panel renders", () => {
  it("says it is searching while the query is in flight", () => {
    const { input, results } = mountPanel();
    type(input, "linear");

    captured?.({ name: "mcp.search_registry", status: "pending", args: { q: "linear" } });
    expect(results.textContent).toContain("Searching the registry");

    captured?.({
      name: "mcp.search_registry",
      status: "success",
      args: { q: "linear" },
      result: HIT,
    });
    expect(results.textContent).not.toContain("Searching the registry");
    expect(results.textContent).toContain("io.example/thing");
  });

  it("reads an absent result as a failure, with a retry", () => {
    const { input, results } = mountPanel();
    type(input, "linear");

    // The server used to answer a slow upstream with a bare 200 and no body,
    // which decodes to nothing here (internal/mcp/registry_proxy.go returns 502
    // now). Either way an absent result is not an empty result.
    captured?.({
      name: "mcp.search_registry",
      status: "success",
      args: { q: "linear" },
      result: undefined,
    });
    expect(results.textContent).toContain("Registry unreachable");

    const retry = results.querySelector("button");
    expect(retry?.textContent).toBe("Retry");
    retry?.click();
    expect(dispatched).toEqual([{ q: "linear" }]);
  });

  it("distinguishes no results from an unreachable registry", () => {
    const { input, results } = mountPanel();
    type(input, "zzzz");
    captured?.({
      name: "mcp.search_registry",
      status: "success",
      args: { q: "zzzz" },
      result: { servers: [] },
    });
    expect(results.textContent).toContain('No results for "zzzz"');
    expect(results.textContent).not.toContain("Registry unreachable");
  });

  it("drops an answer for a prefix the user has typed past", () => {
    const { input, results } = mountPanel();
    type(input, "githu");
    type(input, "github");
    captured?.({
      name: "mcp.search_registry",
      status: "success",
      args: { q: "github" },
      result: HIT,
    });
    expect(results.textContent).toContain("io.example/thing");

    // "githu" was abandoned; its answer must not repaint the box.
    captured?.({
      name: "mcp.search_registry",
      status: "error",
      args: { q: "githu" },
    });
    expect(results.textContent).not.toContain("Registry unreachable");
    expect(results.textContent).toContain("io.example/thing");
  });

  it("ignores every other action's lifecycle", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    captured?.({ name: "mcp.save_server", status: "error", args: { q: "github" } });
    expect(results.children).toHaveLength(0);
  });
});
