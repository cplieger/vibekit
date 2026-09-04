// The Instructions panel's two writers on one textarea, and the ordering between
// them. `loadSteeringDoc` is fired by the same tab activation that makes the
// textarea typeable, so the GET is in flight while the box is already accepting
// keystrokes — and every keystroke is already in a debounced save. The read
// therefore has to lose that race.
import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

const H = vi.hoisted(() => ({
  apiGet: vi.fn(),
  save: vi.fn(),
  flush: vi.fn(),
  pending: vi.fn(() => false),
}));

vi.mock("./api-client.js", () => ({ apiGet: H.apiGet }));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: vi.fn(),
  STEERING_SAVE_KEY: "steering",
}));
vi.mock("./actions/settings.js", () => ({ saveSteering: {} }));
vi.mock("./actions/index.js", () => ({
  registerCleanup: vi.fn(),
  subscribeByName: vi.fn(() => () => undefined),
  debouncedDispatch: vi.fn(() =>
    Object.assign((...a: unknown[]) => H.save(...a), {
      isPending: H.pending,
      flush: H.flush,
    }),
  ),
}));

const { initSteeringEditor, loadSteeringDoc, _resetSteeringForTest } =
  await import("./settings-steering.js");

/** The textarea `$` resolves, mounted at the id index.html declares. */
function mount(): HTMLTextAreaElement {
  const ta = document.createElement("textarea");
  ta.id = "steering-input";
  document.body.appendChild(ta);
  return ta;
}

/** A keystroke, as the browser delivers one: the value moves, then `input` fires. */
function type(ta: HTMLTextAreaElement, text: string): void {
  ta.value = text;
  ta.dispatchEvent(new Event("input"));
}

describe("the steering document's read against the keystrokes it races", () => {
  let ta: HTMLTextAreaElement;

  beforeEach(() => {
    _resetSteeringForTest();
    vi.clearAllMocks();
    H.pending.mockReturnValue(false);
    ta = mount();
    initSteeringEditor();
  });

  afterEach(() => {
    ta.remove();
  });

  it("fills the textarea when the reader has typed nothing", async () => {
    H.apiGet.mockResolvedValue({ content: "# on the server" });
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# on the server");
    });
  });

  it("leaves what the reader typed while the read was in flight", async () => {
    // The GET is deliberately still open when the keystroke lands, which is the
    // window the panel-activation placement opens.
    let land = (_: { content: string }): void => undefined;
    H.apiGet.mockReturnValue(
      new Promise<{ content: string }>((resolve) => {
        land = resolve;
      }),
    );
    loadSteeringDoc();

    type(ta, "half a sentence the reader is still writing");
    land({ content: "# on the server" });
    // Two microtask hops: the promise's own, then the `.then` body's.
    await Promise.resolve();
    await Promise.resolve();

    expect(ta.value).toBe("half a sentence the reader is still writing");
    // And the save that is already queued carries the same text, so the editor
    // and the file agree about what the next keystroke will dispatch.
    expect(H.save).toHaveBeenLastCalledWith({
      content: "half a sentence the reader is still writing",
    });
  });

  it("leaves an emptied textarea alone once it has been typed into", async () => {
    // Selecting all and deleting is a real edit, and the queued save carries the
    // empty document. A read that refilled the box here would show content the
    // save is about to remove.
    let land = (_: { content: string }): void => undefined;
    H.apiGet.mockReturnValue(
      new Promise<{ content: string }>((resolve) => {
        land = resolve;
      }),
    );
    loadSteeringDoc();

    type(ta, "x");
    type(ta, "");
    land({ content: "# on the server" });
    await Promise.resolve();
    await Promise.resolve();

    expect(ta.value).toBe("");
  });
});
