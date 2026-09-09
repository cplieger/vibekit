// ---------------------------------------------------------------------------
// The ONE reader of `GET /api/config-template`.
//
// Two readers of that endpoint used to seed the same surfaces, so every boot and
// every transport gap cost two utility-bridge round trips. Collapsing them into
// one has exactly one way to go wrong: a seed the deleted reader carried and the
// survivor does not. Both of those seeds are pinned here.
//
// The retry POLICY is model-catalog.test.ts's subject, including the single
// in-flight slot that makes a second caller on one gap free; the real
// `refreshCatalog` is used rather than mocked so this file exercises the wiring
// that reaches it.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { ConfigTemplateResponse } from "./wire/types.gen.js";
import type { ModelInfo, Session } from "./types.js";

const reads: string[] = [];
let reply: ConfigTemplateResponse | null = null;

vi.mock("./api-client.js", () => ({
  apiGetTyped: vi.fn((path: string) => {
    reads.push(path);
    return Promise.resolve(reply);
  }),
}));

// The four sinks, recorded rather than driven: each owns its own behaviour, and
// what this file decides is which of them the one reader still reaches.
const mockSetCatalogEfforts = vi.fn();
const mockSetCatalogModes = vi.fn();
const mockSetPickerModels = vi.fn();
const mockRefreshPickerIfVisible = vi.fn();
const mockSetCatalogPhase = vi.fn();
const mockRefreshContextUI = vi.fn();
vi.mock("./effort.js", () => ({ setCatalogEfforts: mockSetCatalogEfforts }));
vi.mock("./roles.js", () => ({ setCatalogModes: mockSetCatalogModes }));
vi.mock("./picker.js", () => ({
  setPickerModels: mockSetPickerModels,
  refreshPickerIfVisible: mockRefreshPickerIfVisible,
  setCatalogPhase: mockSetCatalogPhase,
}));
vi.mock("./context-ui.js", () => ({ refreshContextUI: mockRefreshContextUI }));

// The store's context-size table is REAL: the seed's whole mechanism is that the
// table is filled from the model descriptions this same answer carried, so a mock
// of it would pin the wiring and not the seed. `{ spy: true }` calls through, so
// only "which chat is active" is controlled, in `beforeEach`.
vi.mock("./store.js", { spy: true });

const store = await import("./store.js");
const { MODEL_CONTEXT_SIZES } = store;
const { fetchCatalog } = await import("./session-catalog.js");

/** A catalog answer with the three lists the wire declares non-null. */
function catalog(over: Partial<ConfigTemplateResponse> = {}): ConfigTemplateResponse {
  return { catalog: "ready", modes: [], models: [], effort_levels: [], ...over };
}

/** One model whose description states its window, which is the only place a
 *  context size is stated on this wire. */
function model(id: string, description = "200k context"): ConfigTemplateResponse["models"][number] {
  return { id, name: id, description };
}

/** A usage block whose only interesting field is the window size. */
function usage(contextSize: number): Session["usage"] {
  return {
    context_pct: 0,
    context_size: contextSize,
    credits: 0,
    turn_count: 0,
    last_turn_ms: 0,
    has_real_data: false,
  };
}

/** An active chat, with only the fields the seed reads. */
function session(over: Partial<Session> = {}): Session {
  return {
    id: "c1",
    model: "m-big",
    usage: usage(0),
    ...over,
  } as Session;
}

beforeEach(() => {
  reads.length = 0;
  reply = null;
  vi.mocked(store.getActive).mockReturnValue(undefined);
  // The table is module state shared with the real store, so each case starts from
  // an empty one; reassigning the export is not available, hence the per-key wipe.
  for (const k of Object.keys(MODEL_CONTEXT_SIZES)) {
    // eslint-disable-next-line @typescript-eslint/no-dynamic-delete -- the keys ARE dynamic: they are model ids a case just landed.
    delete MODEL_CONTEXT_SIZES[k];
  }
});

describe("the one catalog reader", () => {
  it("reads /api/config-template and seeds all three vocabularies", async () => {
    reply = catalog({
      modes: [{ id: "vibe", name: "Default" }],
      models: [model("m-big")],
      effort_levels: [{ id: "max", name: "Max" }],
      effort_active: "max",
    });

    await fetchCatalog();

    expect(reads).toEqual(["/api/config-template"]);
    expect(mockSetCatalogEfforts).toHaveBeenCalledWith(reply.effort_levels, "max");
    expect(mockSetCatalogModes).toHaveBeenCalledWith(reply.modes);
    expect(mockSetPickerModels).toHaveBeenCalledTimes(1);
  });

  it("seeds the active chat's context size from the descriptions it just landed", async () => {
    // The first seed carried over from the deleted reader. Nothing else fills this:
    // `usage_update` has no emit site in any KAS build vibekit has run against, so
    // every chat file persists `context_size: 0` and the client derives it from the
    // model's description string.
    const active = session();
    vi.mocked(store.getActive).mockReturnValue(active);
    reply = catalog({ models: [model("m-big", "200k context")] });

    await fetchCatalog();

    expect(active.usage.context_size).toBe(200_000);
    expect(mockRefreshContextUI).toHaveBeenCalledWith(active);
  });

  it("leaves a context size that is already stated alone", async () => {
    const active = session({
      usage: usage(111),
    });
    vi.mocked(store.getActive).mockReturnValue(active);
    reply = catalog({ models: [model("m-big", "200k context")] });

    await fetchCatalog();

    expect(active.usage.context_size).toBe(111);
  });

  it("moves the picker's highlight to the ACTIVE chat's model", async () => {
    // The second seed carried over. Passing "" leaves the highlight wherever it
    // was, which is right before a session exists and wrong once one does.
    vi.mocked(store.getActive).mockReturnValue(session({ model: "m-big" }));
    reply = catalog({ models: [model("m-big"), model("m-small")] });

    await fetchCatalog();

    expect(mockRefreshPickerIfVisible).toHaveBeenCalledWith("m-big");
  });

  it("leaves the highlight alone when no chat is active", async () => {
    reply = catalog({ models: [model("m-big")] });

    await fetchCatalog();

    expect(mockRefreshPickerIfVisible).toHaveBeenCalledWith(undefined);
    expect(mockRefreshContextUI).not.toHaveBeenCalled();
  });

  it("never replaces a landed vocabulary with an EMPTY list", async () => {
    // An empty list is the absence of a vocabulary rather than a value, and each
    // list arrives empty on its own — the effort tiers ride the model, and KAS
    // resolves its model list asynchronously, so a merely cold cache reports ready
    // with nothing in it.
    reply = catalog();

    await fetchCatalog();

    expect(mockSetCatalogEfforts).not.toHaveBeenCalled();
    expect(mockSetCatalogModes).not.toHaveBeenCalled();
    expect(mockSetPickerModels).not.toHaveBeenCalled();
  });

  it("carries the model's default effort tier, which a control silently loses", async () => {
    reply = catalog({
      models: [{ id: "m-big", name: "Big", has_effort: true, default_effort_level: "high" }],
    });

    await fetchCatalog();

    const models = mockSetPickerModels.mock.calls[0]?.[0] as ModelInfo[] | undefined;
    expect(models?.[0]?.has_effort).toBe(true);
    expect(models?.[0]?.default_effort_level).toBe("high");
  });
});
