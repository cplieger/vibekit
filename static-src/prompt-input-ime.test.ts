// @vitest-environment happy-dom
// The IME guard on the composer's Enter key.
//
// Without it, the Enter that COMMITS a Japanese, Chinese or Korean candidate
// submits the prompt mid-word. The guard is a port of Crew's three-way
// predicate, and each leg is tested on its own because each covers an ordering
// the others miss — in particular `keyCode === 229` with `isComposing` already
// false, which is the case the port exists for and the reason it was not
// reinvented from `isComposing` alone.
import { describe, it, expect, beforeEach, vi, afterEach } from "vitest";

vi.mock("./platform.js", () => ({ fixIOSViewport: vi.fn() }));
vi.mock("./pill-expand.js", () => ({ collapseAll: vi.fn() }));
// The session carries ONE prior user prompt, which the ArrowUp case at the bottom
// of this file needs: against an empty session there is nothing to navigate to,
// so a handler that bailed at the top and one that ran correctly both leave the
// box empty and the assertion cannot fail.
const { PRIOR_PROMPT } = vi.hoisted(() => ({ PRIOR_PROMPT: "the prompt before this one" }));
vi.mock("./store.js", () => ({
  getActive: () => ({
    messages: [{ id: "m1", role: "user", content: PRIOR_PROMPT, ts: 1 }],
  }),
  getActiveId: () => "c1",
}));

let submits = 0;

/** Press Enter with an explicit IME state. `keyCode` is set through the init
 *  dict because happy-dom derives it from `key` otherwise. */
function pressEnter(opts: { isComposing?: boolean; keyCode?: number } = {}): KeyboardEvent {
  const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
  const e = new KeyboardEvent("keydown", {
    key: "Enter",
    bubbles: true,
    cancelable: true,
    ...opts,
  });
  input.dispatchEvent(e);
  return e;
}

function compose(type: "compositionstart" | "compositionend"): void {
  const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
  input.dispatchEvent(new CompositionEvent(type, { bubbles: true }));
}

beforeEach(async () => {
  vi.useFakeTimers();
  vi.resetModules();
  submits = 0;
  document.body.innerHTML = `
    <form id="prompt-form">
      <div id="prompt-box">
        <textarea id="prompt-input"></textarea>
      </div>
      <button id="send-btn" type="submit"></button>
    </form>`;
  document.getElementById("prompt-form")?.addEventListener("submit", (e) => {
    e.preventDefault();
    submits += 1;
  });
  const mod = await import("./prompt-input.js");
  mod.initPromptInput(
    () => undefined,
    () => undefined,
  );
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Enter with no composition in flight", () => {
  it("submits", () => {
    (document.getElementById("prompt-input") as HTMLTextAreaElement).value = "hello";
    const e = pressEnter();
    expect(submits).toBe(1);
    expect(e.defaultPrevented).toBe(true);
  });
});

describe("leg 1: the composition flag", () => {
  it("lets Enter through to the browser between compositionstart and end", () => {
    compose("compositionstart");
    const e = pressEnter();
    expect(submits).toBe(0);
    // Not prevented: the browser has to see the key to commit the candidate.
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps blocking for a 50ms tail after compositionend", () => {
    compose("compositionstart");
    compose("compositionend");
    expect(pressEnter().defaultPrevented).toBe(false);
    expect(submits).toBe(0);

    vi.advanceTimersByTime(49);
    expect(submits).toBe(0);

    vi.advanceTimersByTime(1);
    pressEnter();
    expect(submits).toBe(1);
  });

  it("does not let a stale tail timer end a composition that has restarted", () => {
    compose("compositionstart");
    compose("compositionend");
    vi.advanceTimersByTime(30);
    compose("compositionstart"); // second candidate, tail must be cancelled
    vi.advanceTimersByTime(100);
    expect(pressEnter().defaultPrevented).toBe(false);
    expect(submits).toBe(0);
  });
});

describe("leg 2: the native isComposing flag", () => {
  it("lets Enter through with no compositionstart seen at all", () => {
    const e = pressEnter({ isComposing: true });
    expect(submits).toBe(0);
    expect(e.defaultPrevented).toBe(false);
  });
});

describe("leg 3: keyCode 229", () => {
  it("blocks the commit Enter that reports 229 while isComposing is false", () => {
    // THE reason to port Crew's predicate rather than write `e.isComposing`:
    // several IMEs report the "processed by the IME" sentinel on the final Enter
    // with isComposing already false and no composition event outstanding.
    const e = pressEnter({ isComposing: false, keyCode: 229 });
    expect(e.defaultPrevented).toBe(false);
    expect(submits).toBe(0);
  });
});

describe("resetting a stuck composition", () => {
  it("clears it on blur, so a dropped compositionend cannot disable Enter", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart"); // no matching end: the Android focus-loss case
    input.dispatchEvent(new FocusEvent("blur"));
    pressEnter();
    expect(submits).toBe(1);
  });

  it("clears it on Escape, unconditionally rather than only while cycling history", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart");
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    pressEnter();
    expect(submits).toBe(1);
  });

  it("leaves a plain Escape propagating, so pills and the dock still close", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    let reachedDocument = false;
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape") {
        reachedDocument = true;
      }
    });
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(reachedDocument).toBe(true);
  });
});

describe("what the guard must NOT change", () => {
  it("keeps Shift+Enter inserting a newline", () => {
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    input.value = "hello";
    const e = new KeyboardEvent("keydown", {
      key: "Enter",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    input.dispatchEvent(e);
    expect(submits).toBe(0);
    expect(e.defaultPrevented).toBe(false);
  });

  it("keeps ArrowUp history navigation working during a composition", () => {
    // The guard sits INSIDE the Enter branch for this reason: an early return at
    // the top of the handler would break history navigation mid-candidate, which
    // is not the bug being fixed. The mocked session carries a prior prompt, so
    // the assertion separates the two: a handler that bailed leaves the box empty.
    const input = document.getElementById("prompt-input") as HTMLTextAreaElement;
    compose("compositionstart");
    const e = new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true });
    input.dispatchEvent(e);
    expect(input.value).toBe(PRIOR_PROMPT);
    // Consumed: the key moved through history rather than reaching the browser's
    // own caret handling.
    expect(e.defaultPrevented).toBe(true);
  });
});
