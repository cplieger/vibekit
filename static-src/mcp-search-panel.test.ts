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
// And three more of one class: an answer that is not what it looks like.
//
//   4. A query under the floor CLEARED the box, so one typed character read
//      exactly like an answered query with no matches. It renders a hint now.
//   5. The reply said nothing about rows it did not carry: a cut at the limit
//      and a row the install filter dropped both went unreported, so "no
//      results" could mean "matched, but nothing you can install here".
//   6. A rate-limited registry's Retry-After was dropped, so the Retry button
//      offered a click guaranteed to fail again. It waits the interval out.
//
// The subscription callback is the seam: the panel reads action lifecycle
// instances, so the test hands it synthetic ones instead of dispatching.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import type * as ActionsIndex from "./actions/index.js";
import type * as McpActions from "./actions/mcp.js";

interface Inst {
  name: string;
  status: "pending" | "success" | "error" | "cancelled";
  args: unknown;
  result?: unknown;
  error?: unknown;
}

let captured: ((inst: Inst) => void) | null = null;
const scheduled: { q: string }[] = [];
const flushed: { q: string }[] = [];
const dispatched: { q: string }[] = [];

// Both mocks spread the real module: the panel's subscription and debounce
// seams are stubbed, while `bindLoadingState`, `registryFailureOf` and the
// action definitions stay real, so the 502 classification and the Retry
// button's hold are tested as shipped.
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ActionsIndex>()),
  subscribeToActions: (cb: (inst: Inst) => void) => {
    captured = cb;
    return () => {
      captured = null;
    };
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

vi.mock("./actions/mcp.js", async (importOriginal) => ({
  ...(await importOriginal<typeof McpActions>()),
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

const { initSearchPanel, cleanupSearch, renderRegistryResult } =
  await import("./mcp-panels-search.js");

const HIT = {
  servers: [
    {
      name: "io.example/thing",
      description: "a thing",
      packages: [{ registry_type: "npm", identifier: "@example/thing" }],
    },
  ],
  truncated: false,
  filtered: 0,
};

function answered(q: string, result: unknown): void {
  captured?.({ name: "mcp.search_registry", status: "success", args: { q }, result });
}

function failed(q: string, error?: unknown): void {
  const inst: Inst = { name: "mcp.search_registry", status: "error", args: { q } };
  if (error !== undefined) {
    inst.error = error;
  }
  captured?.(inst);
}

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

afterEach(() => {
  vi.useRealTimers();
});

describe("what the panel asks the registry for", () => {
  it("ignores a one-character query and asks from two", () => {
    const { input, results } = mountPanel();

    type(input, "g");
    expect(scheduled, "a single letter matches most of the index").toEqual([]);
    expect(results.textContent).toBe("Type at least 2 characters");

    type(input, "gi");
    expect(scheduled).toEqual([{ q: "gi" }]);
  });

  // An empty box after one typed character is indistinguishable from an
  // answered query with no matches, the same laundering of a non-answer into an
  // empty success as a malformed upstream reply decoding to zero servers. The
  // floor stays (a single letter matches most of the index) and the box says so
  // in the shared vocabulary's sentence, which carries no terminal punctuation.
  it("renders the too-short hint when the query drops back below the floor", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    answered("github", HIT);
    expect(results.textContent).toContain("io.example/thing");

    type(input, "g");
    expect(results.textContent).toBe("Type at least 2 characters");
    expect(results.querySelector("button"), "a hint offers nothing to retry").toBeNull();
    expect(scheduled, "the floor still holds: only the six-letter query was asked").toEqual([
      { q: "github" },
    ]);
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

describe("what the list does not carry", () => {
  it("says when upstream held more than the list shows", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    answered("github", { ...HIT, truncated: true });
    expect(results.textContent).toContain("io.example/thing");
    expect(results.textContent).toContain(
      "More matched than shown; narrow the query to see the rest.",
    );
    expect(results.textContent).not.toContain("cannot be installed");
  });

  it("counts the rows the install filter dropped beside the ones it kept", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    answered("github", { ...HIT, filtered: 3 });
    expect(results.textContent).toContain("io.example/thing");
    expect(results.textContent).toContain("3 more matched but cannot be installed here.");
    expect(results.textContent).not.toContain("narrow the query");
  });

  it("renders neither note when the list is the whole answer", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    answered("github", HIT);
    expect(results.querySelectorAll("p.mcp-empty")).toHaveLength(0);
  });

  it("reads an all-filtered reply as matched-but-not-installable, not as no results", () => {
    const { input, results } = mountPanel();
    type(input, "pypi");
    answered("pypi", { servers: [], truncated: false, filtered: 4 });
    expect(results.textContent).toBe("4 matched, but none can be installed here.");
    expect(results.textContent).not.toContain("No results");
  });
});

describe("what a classified failure changes", () => {
  const rateLimited = {
    message: "Bad Gateway",
    status: 502,
    cause: { error: "registry unavailable", reason: "rate_limited", retry_after: 37 },
  };

  it("holds Retry for the interval the registry named", () => {
    vi.useFakeTimers();
    const { input, results } = mountPanel();
    type(input, "github");
    failed("github", rateLimited);

    expect(results.textContent).toContain("retry in 37s");
    const retry = results.querySelector("button") as HTMLButtonElement;
    expect(retry.textContent).toBe("Retry");
    expect(retry.disabled, "a click inside the interval is guaranteed to fail again").toBe(true);
    retry.click();
    expect(dispatched).toEqual([]);

    vi.advanceTimersByTime(36_999);
    expect(retry.disabled).toBe(true);
    vi.advanceTimersByTime(1);
    expect(retry.disabled).toBe(false);
    retry.click();
    expect(dispatched).toEqual([{ q: "github" }]);
  });

  it("offers Retry at once when the failure names no interval", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    failed("github", {
      message: "Bad Gateway",
      status: 502,
      cause: { error: "registry unavailable", reason: "unavailable" },
    });

    expect(results.textContent).toContain("Registry unreachable");
    const retry = results.querySelector("button") as HTMLButtonElement;
    expect(retry.disabled).toBe(false);
  });

  it("ignores a classification the server did not shape", () => {
    const { input, results } = mountPanel();
    type(input, "github");
    // A string where the wire says integer seconds, and a reason outside the
    // three the server can send: neither may hold the button.
    failed("github", {
      message: "Bad Gateway",
      status: 502,
      cause: { error: "registry unavailable", reason: "rate_limited", retry_after: "37" },
    });
    expect((results.querySelector("button") as HTMLButtonElement).disabled).toBe(false);
    failed("github", {
      message: "Bad Gateway",
      status: 502,
      cause: { error: "registry unavailable", reason: "throttled", retry_after: 37 },
    });
    expect((results.querySelector("button") as HTMLButtonElement).disabled).toBe(false);
    expect(results.textContent).not.toContain("37s");
  });

  // The real action, dispatched against a stubbed 502: the body the server
  // writes has to come back off the error's `cause`, or everything above is
  // wired to a field nothing fills.
  it("carries the 502 body onto the dispatch error", async () => {
    const real = await vi.importActual<typeof McpActions>("./actions/mcp.js");
    const body = { error: "registry unavailable", reason: "rate_limited", retry_after: 37 };
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(body), {
          status: 502,
          headers: { "content-type": "application/json" },
        }),
      ),
    );
    try {
      const outcome = await real.searchRegistry.dispatch({ q: "held" }).outcome;
      expect(outcome.status).toBe("error");
      const err = outcome.status === "error" ? outcome.error : undefined;
      expect(real.registryFailureOf(err)).toEqual(body);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  // The loading-state binding restores a button's idle `disabled` when a
  // dispatch of the bound action settles, and an abandoned prefix's dispatch
  // can settle while the held button is on screen. The hold has to survive it.
  it("keeps the hold when an abandoned prefix's dispatch settles", async () => {
    const { input, results } = mountPanel();
    type(input, "github");
    failed("github", rateLimited);
    const held = results.querySelector("button") as HTMLButtonElement;
    expect(held.disabled).toBe(true);

    const real = await vi.importActual<typeof McpActions>("./actions/mcp.js");
    let release: (r: Response) => void = () => undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockReturnValue(
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
      ),
    );
    try {
      const inFlight = real.searchRegistry.dispatch({ q: "gith" });
      expect(held.disabled, "pending: the binding disables the button anyway").toBe(true);
      release(
        new Response(JSON.stringify({ servers: [], truncated: false, filtered: 0 }), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
      await inFlight;
      expect(held.disabled, "settled: the hold outranks the binding's idle state").toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("drops a hold timer with the box it was armed for", () => {
    vi.useFakeTimers();
    const { input, results } = mountPanel();
    type(input, "github");
    failed("github", rateLimited);
    const held = results.querySelector("button") as HTMLButtonElement;

    // A new answer replaces the box before the interval elapses; the old
    // button's timer must not fire against a node that is off screen.
    answered("github", HIT);
    expect(results.contains(held)).toBe(false);
    vi.advanceTimersByTime(60_000);
    expect(held.disabled, "the replaced button keeps its state; nothing woke it").toBe(true);
  });
});

describe("what a row's install button says", () => {
  // The server surfaces npm alone today, so the label is read off the row
  // rather than hard-coded: a registry type the server starts surfacing later
  // must never render as "Use npm" over an install the npm form cannot run.
  it("names the package's own registry type", () => {
    const row = renderRegistryResult({
      name: "io.example/thing",
      packages: [{ registry_type: "oci", identifier: "ghcr.io/example/thing:1" }],
    });
    const btn = row.querySelector("button.mcp-install-btn") as HTMLButtonElement;
    expect(btn.textContent).toBe("Use oci");
    expect(btn.getAttribute("aria-label")).toBe("Use oci: ghcr.io/example/thing:1");
  });
});
