// The Instructions panel's two writers on one textarea, and the ordering between
// them. A save carries the box as the WHOLE document (and an empty one DELETES
// the file, per handleSteeringPut), so the box must not be typeable before the
// read lands — `loadSteeringDoc` is fired by the same tab activation that reveals
// the panel, so the window is real. The read is what unlocks the box.
import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

const H = vi.hoisted(() => ({
  apiGet: vi.fn(),
  save: vi.fn(),
  flush: vi.fn(),
  pending: vi.fn(() => false),
  showError: vi.fn(),
}));

vi.mock("./api-client.js", () => ({ apiGet: H.apiGet }));
vi.mock("./save-indicator.js", () => ({
  showSaving: vi.fn(),
  showSaved: vi.fn(),
  showError: H.showError,
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

/** A read left open, with its resolver. */
function openRead(): (d: { content: string } | null) => void {
  let land = (_: { content: string } | null): void => undefined;
  H.apiGet.mockReturnValue(
    new Promise<{ content: string } | null>((resolve) => {
      land = resolve;
    }),
  );
  return land;
}

/** Two microtask hops: the promise's own, then the `.then` body's. */
async function settle(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

describe("the steering document's read, and the box it unlocks", () => {
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

  it("locks the box at init and opens it when the document lands", async () => {
    expect(ta.readOnly, "the box is not typeable before the read").toBe(true);
    H.apiGet.mockResolvedValue({ content: "# on the server" });
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# on the server");
    });
    expect(ta.readOnly).toBe(false);
  });

  it("takes no keystroke while the read is in flight", async () => {
    // A real keystroke through the browser, because `readOnly` is a claim about
    // what the ENGINE does with one: assigning `.value` in a test would sail past
    // the very thing being pinned.
    const land = openRead();
    loadSteeringDoc();

    ta.focus();
    await userEvent.keyboard("half a sentence");

    expect(ta.value, "the keystrokes did not reach a locked box").toBe("");
    expect(H.save, "and nothing was queued to overwrite the document").not.toHaveBeenCalled();

    land({ content: "# on the server" });
    await settle();
    expect(ta.value).toBe("# on the server");
    expect(ta.readOnly).toBe(false);
  });

  it("saves what the reader types once the document is in the box", async () => {
    H.apiGet.mockResolvedValue({ content: "# on the server" });
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });

    ta.focus();
    await userEvent.keyboard("!");

    expect(ta.value).toBe("# on the server!");
    expect(H.save).toHaveBeenLastCalledWith({ content: "# on the server!" });
  });

  it("keeps the box locked when the read fails, and retries on focus", async () => {
    // apiGet collapses every failure to null. An empty box is not an empty
    // document, so opening it here would let the first keystroke delete the file.
    H.apiGet.mockResolvedValueOnce(null);
    loadSteeringDoc();
    await settle();

    expect(ta.readOnly).toBe(true);
    expect(H.showError).toHaveBeenCalledWith("steering");

    H.apiGet.mockResolvedValueOnce({ content: "# on the server" });
    ta.focus();
    await vi.waitFor(() => {
      expect(ta.value).toBe("# on the server");
    });
    expect(ta.readOnly).toBe(false);
  });

  it("issues one read while one is in flight", () => {
    openRead();
    loadSteeringDoc();
    loadSteeringDoc();
    ta.focus();
    expect(H.apiGet).toHaveBeenCalledTimes(1);
  });

  it("does not read again once the document is in", async () => {
    H.apiGet.mockResolvedValue({ content: "# on the server" });
    loadSteeringDoc();
    await vi.waitFor(() => {
      expect(ta.readOnly).toBe(false);
    });
    ta.blur();
    ta.focus();
    loadSteeringDoc();
    expect(H.apiGet).toHaveBeenCalledTimes(1);
  });
});
